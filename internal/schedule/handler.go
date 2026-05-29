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
		r.Post("/probe",           h.handleProbe)
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

// handleProbe accepts an upload of arbitrary size, discards it immediately,
// and returns the server-side receipt time in milliseconds.  The client uses
// two probe sizes (1 MB and 512 KB) to estimate upload bandwidth before
// choosing a chunk size for the actual upload.  No data is written to disk.
func (h *Handler) handleProbe(w http.ResponseWriter, r *http.Request) {
	start := time.Now()

	// Drain the body (up to 2 MiB — largest probe size).
	maxRead := int64(2 * 1024 * 1024)
	n, _ := io.Copy(io.Discard, io.LimitReader(r.Body, maxRead))

	elapsed := time.Since(start).Milliseconds()

	writeJSON(w, http.StatusOK, map[string]any{
		"bytes":      n,
		"elapsed_ms": elapsed,
	})
}

// handleAuth returns the caller's upload auth status.
// The UI calls this when the user clicks "Create" to decide what to show.
func (h *Handler) handleAuth(w http.ResponseWriter, r *http.Request) {
	ip := remoteIP(r)

	// If neither IP allowlist nor password configured — allow everyone.
	if len(h.cfg.UploadIPs) == 0 && h.cfg.UploadPassword == "" {
		writeJSON(w, http.StatusOK, authResponse{Allowed: true, NeedsPassword: false})
		return
	}

	// If IP is in the allowlist it always wins — no password needed.
	if h.cfg.IPAllowedToUpload(ip) && len(h.cfg.UploadIPs) > 0 {
		writeJSON(w, http.StatusOK, authResponse{Allowed: true, NeedsPassword: false})
		return
	}

	// IP not in allowlist (or no allowlist) — check whether a password gates access.
	if h.cfg.UploadPassword != "" {
		writeJSON(w, http.StatusOK, authResponse{Allowed: false, NeedsPassword: true})
		return
	}

	// IP allowlist set, no password, IP not in list — blocked entirely.
	writeJSON(w, http.StatusOK, authResponse{Allowed: false, NeedsPassword: false})
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
	ChunkSize    int    `json:"chunk_size"` // 0 = use server default
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

	// Use client-supplied chunk size, clamped to valid range.
	// Falls back to the server default (ChunkSize constant) if not provided.
	// Maximum is 2 MiB — the top tier of the client's adaptive chunk size table.
	const maxChunkSize = 2 * 1024 * 1024 // 2 MiB
	cs := req.ChunkSize
	if cs <= 0 {
		cs = ChunkSize
	}
	if cs > maxChunkSize {
		cs = maxChunkSize
	}

	// Calculate expected chunk count using the negotiated chunk size.
	expectedChunks := int((req.TotalSize + int64(cs) - 1) / int64(cs))
	if req.ChunksTotal != expectedChunks {
		writeError(w, http.StatusBadRequest,
			fmt.Sprintf("chunks_total mismatch: expected %d for %d bytes with chunk_size %d", expectedChunks, req.TotalSize, cs))
		return
	}

	expires := time.Now().Add(time.Duration(req.TTLSeconds) * time.Second)
	meta, err := h.store.InitUpload(req.ChunksTotal, req.TotalSize, expires, maxDl, cs)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to init upload")
		return
	}

	writeJSON(w, http.StatusOK, uploadInitResponse{
		UploadID:  meta.UploadID,
		ExpiresAt: meta.ExpiresAt,
		ChunkSize: cs,
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
	// Use the chunk size recorded in the pending meta (negotiated at init time).
	// This is important when the client uploads smaller chunks than the server default.
	pendingMeta, err := h.store.ReadPendingMeta(uploadID)
	if err != nil {
		writeError(w, http.StatusBadRequest, "upload not found")
		return
	}
	negChunkSize := pendingMeta.ChunkSize
	if negChunkSize <= 0 {
		negChunkSize = ChunkSize
	}
	// Max = nonce(12) + ciphertext(negotiated chunk size) + tag(16)
	maxRead := int64(NonceSize + negChunkSize + TagSize)
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

type ttlOptionsResponse struct {
	Options         []ttlOptionJSON `json:"options"`
	MaxDownloadsCap int             `json:"max_downloads_cap"` // 0 = unlimited
	MaxSizeBytes    int64           `json:"max_size_bytes"`    // 0 = unlimited
}

func (h *Handler) handleTTLOptions(w http.ResponseWriter, r *http.Request) {
	opts := make([]ttlOptionJSON, len(h.cfg.TTLOptions))
	for i, o := range h.cfg.TTLOptions {
		opts[i] = ttlOptionJSON{
			Label:   o.Label,
			Seconds: int64(o.Duration.Seconds()),
		}
	}
	writeJSON(w, http.StatusOK, ttlOptionsResponse{
		Options:         opts,
		MaxDownloadsCap: h.cfg.MaxDownloads,
		MaxSizeBytes:    h.cfg.MaxSize,
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// Auth helper
// ─────────────────────────────────────────────────────────────────────────────

// authorizeUpload checks IP allowlist and password from the request.
// Writes an error response and returns false if not authorized.
func (h *Handler) authorizeUpload(w http.ResponseWriter, r *http.Request, ip net.IP) bool {
	// No restrictions at all — allow everyone.
	if len(h.cfg.UploadIPs) == 0 && h.cfg.UploadPassword == "" {
		return true
	}

	// IP in allowlist always wins — no password needed.
	if len(h.cfg.UploadIPs) > 0 && h.cfg.IPAllowedToUpload(ip) {
		return true
	}

	// Password required — check header then form value.
	if h.cfg.UploadPassword != "" {
		pw := r.Header.Get("X-Schedule-Password")
		if pw == "" {
			pw = r.FormValue("password")
		}
		if pw == h.cfg.UploadPassword {
			return true
		}
		writeError(w, http.StatusForbidden, "invalid upload password")
		return false
	}

	// IP allowlist set, no password, IP not in list — blocked.
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
