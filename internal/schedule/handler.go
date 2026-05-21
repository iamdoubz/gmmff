package schedule

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
)

// Handler holds all schedule HTTP handlers.
type Handler struct {
	cfg   *Config
	store *Store
}

// NewHandler creates a Handler. Returns an error if storage dirs cannot be created.
func NewHandler(cfg *Config) (*Handler, error) {
	st, err := NewStore(cfg)
	if err != nil {
		return nil, err
	}
	return &Handler{cfg: cfg, store: st}, nil
}

// Mount registers all schedule routes on the given chi router.
// All routes are under /api/schedule/.
func (h *Handler) Mount(r chi.Router) {
	r.Route("/api/schedule", func(r chi.Router) {
		r.Post("/auth",            h.handleAuth)
		r.Post("/upload/init",     h.handleUploadInit)
		r.Post("/upload/chunk",    h.handleUploadChunk)
		r.Post("/upload/complete", h.handleUploadComplete)
		r.Get("/download/{fileID}", h.handleDownload)
		r.Get("/meta/{fileID}",     h.handleMeta)
		r.Delete("/delete/{fileID}/{deleteKey}", h.handleDelete)
		r.Get("/ttl-options",      h.handleTTLOptions)
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// Auth check
// ─────────────────────────────────────────────────────────────────────────────

type authResponse struct {
	Allowed      bool `json:"allowed"`       // IP is in upload allowlist
	NeedsPassword bool `json:"needs_password"` // password required to proceed
}

// handleAuth returns the caller's upload auth status.
// The UI calls this when the user clicks "Create" to decide what to show.
func (h *Handler) handleAuth(w http.ResponseWriter, r *http.Request) {
	ip := remoteIP(r)

	ipAllowed := h.cfg.IPAllowedToUpload(ip)
	needsPw   := !ipAllowed && h.cfg.UploadPassword != ""

	// If neither IP allowlist nor password — allow everyone.
	if len(h.cfg.UploadIPs) == 0 && h.cfg.UploadPassword == "" {
		ipAllowed = true
		needsPw   = false
	}

	writeJSON(w, http.StatusOK, authResponse{
		Allowed:      ipAllowed,
		NeedsPassword: needsPw,
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// Upload — init
// ─────────────────────────────────────────────────────────────────────────────

type uploadInitRequest struct {
	Password     string `json:"password"`
	ChunksTotal  int    `json:"chunks_total"`
	TotalSize    int64  `json:"total_size"`
	TTLSeconds   int64  `json:"ttl_seconds"`
	MaxDownloads int    `json:"max_downloads"`
}

type uploadInitResponse struct {
	UploadID  string    `json:"upload_id"`
	ExpiresAt time.Time `json:"expires_at"`
	ChunkSize int       `json:"chunk_size"`
}

func (h *Handler) handleUploadInit(w http.ResponseWriter, r *http.Request) {
	ip := remoteIP(r)

	if !h.authorizeUpload(w, r, ip) {
		return
	}

	var req uploadInitRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	// Validate size.
	if req.TotalSize <= 0 {
		writeError(w, http.StatusBadRequest, "total_size must be > 0")
		return
	}
	if req.TotalSize > h.cfg.MaxSize {
		writeError(w, http.StatusRequestEntityTooLarge,
			fmt.Sprintf("file too large: max %d bytes", h.cfg.MaxSize))
		return
	}

	// Validate TTL.
	if req.TTLSeconds <= 0 {
		writeError(w, http.StatusBadRequest, "ttl_seconds must be > 0")
		return
	}

	// Validate download limit.
	maxDl := req.MaxDownloads
	if h.cfg.MaxDownloads > 0 && (maxDl == 0 || maxDl > h.cfg.MaxDownloads) {
		maxDl = h.cfg.MaxDownloads
	}

	// Calculate expected chunk count.
	expectedChunks := int((req.TotalSize + ChunkSize - 1) / ChunkSize)
	if req.ChunksTotal != expectedChunks {
		writeError(w, http.StatusBadRequest,
			fmt.Sprintf("chunks_total mismatch: expected %d for %d bytes", expectedChunks, req.TotalSize))
		return
	}

	expires := time.Now().Add(time.Duration(req.TTLSeconds) * time.Second)
	meta, err := h.store.InitUpload(req.ChunksTotal, req.TotalSize, expires, maxDl)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to init upload")
		return
	}

	writeJSON(w, http.StatusOK, uploadInitResponse{
		UploadID:  meta.UploadID,
		ExpiresAt: meta.ExpiresAt,
		ChunkSize: ChunkSize,
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// Upload — chunk
// ─────────────────────────────────────────────────────────────────────────────

func (h *Handler) handleUploadChunk(w http.ResponseWriter, r *http.Request) {
	uploadID   := r.FormValue("upload_id")
	chunkIndexS := r.FormValue("chunk_index")

	if uploadID == "" || chunkIndexS == "" {
		writeError(w, http.StatusBadRequest, "missing upload_id or chunk_index")
		return
	}
	chunkIndex, err := strconv.Atoi(chunkIndexS)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid chunk_index")
		return
	}

	// Read the raw chunk bytes from the request body.
	// Max = nonce(12) + ciphertext(ChunkSize) + tag(16)
	maxRead := int64(NonceSize + ChunkSize + TagSize)
	data, err := io.ReadAll(io.LimitReader(r.Body, maxRead+1))
	if err != nil {
		writeError(w, http.StatusBadRequest, "failed to read chunk")
		return
	}
	if int64(len(data)) > maxRead {
		writeError(w, http.StatusRequestEntityTooLarge, "chunk too large")
		return
	}

	if err := h.store.AppendChunk(uploadID, chunkIndex, data); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"ok":          true,
		"chunk_index": chunkIndex,
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// Upload — complete
// ─────────────────────────────────────────────────────────────────────────────

type uploadCompleteRequest struct {
	UploadID      string `json:"upload_id"`
	FileNameEnc   string `json:"filename_enc"`   // hex-encoded encrypted filename
	FileNameNonce string `json:"filename_nonce"` // hex-encoded 12-byte nonce
	SHA256Cipher  string `json:"sha256_cipher"`  // hex SHA-256 of full ciphertext
}

type uploadCompleteResponse struct {
	FileID    string    `json:"file_id"`
	DeleteKey string    `json:"delete_key"`
	ExpiresAt time.Time `json:"expires_at"`
}

func (h *Handler) handleUploadComplete(w http.ResponseWriter, r *http.Request) {
	var req uploadCompleteRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.UploadID == "" || req.FileNameEnc == "" || req.SHA256Cipher == "" {
		writeError(w, http.StatusBadRequest, "missing required fields")
		return
	}

	fm, err := h.store.FinalizeUpload(req.UploadID, req.FileNameEnc, req.FileNameNonce, req.SHA256Cipher)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, uploadCompleteResponse{
		FileID:    fm.FileID,
		DeleteKey: fm.DeleteKey,
		ExpiresAt: fm.ExpiresAt,
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// Download
// ─────────────────────────────────────────────────────────────────────────────

func (h *Handler) handleDownload(w http.ResponseWriter, r *http.Request) {
	fileID := chi.URLParam(r, "fileID")
	ip     := remoteIP(r)

	if !h.cfg.IPAllowedToDownload(ip) {
		writeError(w, http.StatusForbidden, "download not permitted from your IP")
		return
	}

	meta, f, err := h.store.OpenDownload(fileID)
	if err != nil {
		if strings.Contains(err.Error(), "no such file") {
			writeError(w, http.StatusNotFound, "file not found")
		} else {
			writeError(w, http.StatusGone, err.Error())
		}
		return
	}
	defer f.Close()

	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Length", strconv.FormatInt(meta.EncryptedSize, 10))
	w.Header().Set("X-Chunks-Total", strconv.Itoa(meta.ChunksTotal))
	w.Header().Set("X-Chunk-Size",   strconv.Itoa(meta.ChunkSize))
	w.Header().Set("X-Filename-Enc", meta.FileNameEnc)
	w.Header().Set("X-Filename-Nonce", meta.FileNameNonce)
	w.Header().Set("Cache-Control", "no-store")
	io.Copy(w, f) //nolint:errcheck
}

// ─────────────────────────────────────────────────────────────────────────────
// Meta (for Join flow — returns public metadata without delete key)
// ─────────────────────────────────────────────────────────────────────────────

type publicMeta struct {
	FileID        string    `json:"file_id"`
	FileNameEnc   string    `json:"filename_enc"`
	FileNameNonce string    `json:"filename_nonce"`
	TotalSize     int64     `json:"total_size"`
	ChunksTotal   int       `json:"chunks_total"`
	ChunkSize     int       `json:"chunk_size"`
	ExpiresAt     time.Time `json:"expires_at"`
	DownloadsLeft int       `json:"downloads_left"`
}

func (h *Handler) handleMeta(w http.ResponseWriter, r *http.Request) {
	fileID := chi.URLParam(r, "fileID")
	ip     := remoteIP(r)

	if !h.cfg.IPAllowedToDownload(ip) {
		writeError(w, http.StatusForbidden, "not permitted")
		return
	}

	meta, err := h.store.ReadFileMeta(fileID)
	if err != nil {
		writeError(w, http.StatusNotFound, "file not found")
		return
	}
	if time.Now().After(meta.ExpiresAt) {
		writeError(w, http.StatusGone, "file expired")
		return
	}
	if meta.DownloadsLeft == 0 {
		writeError(w, http.StatusGone, "download limit reached")
		return
	}

	writeJSON(w, http.StatusOK, publicMeta{
		FileID:        meta.FileID,
		FileNameEnc:   meta.FileNameEnc,
		FileNameNonce: meta.FileNameNonce,
		TotalSize:     meta.TotalSize,
		ChunksTotal:   meta.ChunksTotal,
		ChunkSize:     meta.ChunkSize,
		ExpiresAt:     meta.ExpiresAt,
		DownloadsLeft: meta.DownloadsLeft,
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// Delete
// ─────────────────────────────────────────────────────────────────────────────

func (h *Handler) handleDelete(w http.ResponseWriter, r *http.Request) {
	fileID    := chi.URLParam(r, "fileID")
	deleteKey := chi.URLParam(r, "deleteKey")

	if err := h.store.Delete(fileID, deleteKey); err != nil {
		if strings.Contains(err.Error(), "invalid delete key") {
			writeError(w, http.StatusForbidden, "invalid delete key")
		} else {
			writeError(w, http.StatusNotFound, "file not found")
		}
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"deleted": true})
}

// ─────────────────────────────────────────────────────────────────────────────
// TTL options (served to the UI)
// ─────────────────────────────────────────────────────────────────────────────

type ttlOptionJSON struct {
	Label   string `json:"label"`
	Seconds int64  `json:"seconds"`
}

func (h *Handler) handleTTLOptions(w http.ResponseWriter, r *http.Request) {
	opts := make([]ttlOptionJSON, len(h.cfg.TTLOptions))
	for i, o := range h.cfg.TTLOptions {
		opts[i] = ttlOptionJSON{
			Label:   o.Label,
			Seconds: int64(o.Duration.Seconds()),
		}
	}
	writeJSON(w, http.StatusOK, opts)
}

// ─────────────────────────────────────────────────────────────────────────────
// Auth helper
// ─────────────────────────────────────────────────────────────────────────────

// authorizeUpload checks IP allowlist and password from the request.
// Writes an error response and returns false if not authorized.
func (h *Handler) authorizeUpload(w http.ResponseWriter, r *http.Request, ip net.IP) bool {
	// IP in allowlist → no password needed.
	if h.cfg.IPAllowedToUpload(ip) {
		return true
	}
	// No restrictions at all → allow everyone.
	if len(h.cfg.UploadIPs) == 0 && h.cfg.UploadPassword == "" {
		return true
	}
	// Password required.
	if h.cfg.UploadPassword != "" {
		// Accept password from header or JSON body field.
		pw := r.Header.Get("X-Schedule-Password")
		if pw == "" {
			// Try to peek from already-decoded JSON — callers pass it explicitly.
			pw = r.FormValue("password")
		}
		if pw == h.cfg.UploadPassword {
			return true
		}
		writeError(w, http.StatusForbidden, "invalid upload password")
		return false
	}
	writeError(w, http.StatusForbidden, "upload not permitted from your IP")
	return false
}

// ─────────────────────────────────────────────────────────────────────────────
// Helpers
// ─────────────────────────────────────────────────────────────────────────────

func remoteIP(r *http.Request) net.IP {
	// Respect X-Forwarded-For / X-Real-IP when behind a proxy.
	for _, h := range []string{"X-Real-IP", "X-Forwarded-For"} {
		if v := r.Header.Get(h); v != "" {
			// X-Forwarded-For may be comma-separated; take the first.
			raw := strings.SplitN(v, ",", 2)[0]
			if ip := net.ParseIP(strings.TrimSpace(raw)); ip != nil {
				return ip
			}
		}
	}
	host, _, _ := net.SplitHostPort(r.RemoteAddr)
	return net.ParseIP(host)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v) //nolint:errcheck
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
