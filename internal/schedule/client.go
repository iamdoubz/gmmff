package schedule

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Client handles Schedule HTTP interactions and AES-256-GCM encryption for
// CLI use. The same crypto logic runs in the browser via crypto.subtle; both
// sides must produce identical ciphertext layouts for interoperability.
type Client struct {
	BaseURL    string       // https://host (no trailing slash)
	Password   string       // upload password, empty if not required
	HTTPClient *http.Client // nil → http.DefaultClient
}

// NewClient constructs a Client from a server URL in any supported format:
//
//	wss://host/ws  →  https://host
//	ws://host/ws   →  http://host
//	https://host   →  https://host  (pass-through)
//	http://host    →  http://host   (pass-through)
func NewClient(rawURL string) (*Client, error) {
	base, err := NormaliseServerURL(rawURL)
	if err != nil {
		return nil, err
	}
	return &Client{BaseURL: base, HTTPClient: &http.Client{Timeout: 0}}, nil
}

// NormaliseServerURL converts any gmmff server URL variant to a plain HTTPS base URL.
func NormaliseServerURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)

	// Convert WebSocket schemes to HTTP.
	raw = strings.NewReplacer(
		"wss://", "https://",
		"ws://", "http://",
	).Replace(raw)

	// Strip trailing /ws path component.
	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("invalid server URL %q: %w", raw, err)
	}
	u.Path = strings.TrimSuffix(u.Path, "/ws")
	u.Path = strings.TrimSuffix(u.Path, "/")

	if u.Scheme != "https" && u.Scheme != "http" {
		return "", fmt.Errorf("unsupported scheme %q — use https://, http://, wss://, or ws://", u.Scheme)
	}
	return u.String(), nil
}

// ─────────────────────────────────────────────────────────────────────────────
// Auth
// ─────────────────────────────────────────────────────────────────────────────

// AuthStatus describes the server's upload access requirements for the caller.
type AuthStatus struct {
	Allowed       bool // IP is in the upload allowlist
	NeedsPassword bool // password is required to proceed
}

// CheckAuth calls POST /api/schedule/auth and returns the server's verdict.
func (c *Client) CheckAuth(ctx context.Context) (AuthStatus, error) {
	var out AuthStatus
	resp, err := c.post(ctx, "/api/schedule/auth", nil, "")
	if err != nil {
		return out, err
	}
	defer resp.Body.Close()
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return out, fmt.Errorf("schedule auth: decode response: %w", err)
	}
	return out, nil
}

// ─────────────────────────────────────────────────────────────────────────────
// Upload
// ─────────────────────────────────────────────────────────────────────────────

// UploadOptions controls the upload behaviour.
type UploadOptions struct {
	TTL          time.Duration // expiry duration
	MaxDownloads int           // 0 = unlimited
	ChunkSize    int           // 0 = auto-select based on file size
	// Progress is called after each chunk upload with bytes uploaded so far.
	Progress func(uploaded, total int64, speed float64)
}

// UploadResult is returned by a successful upload.
type UploadResult struct {
	FileID    string
	KeyHex    string
	DeleteKey string
	ExpiresAt time.Time
	ShareURL  string // base URL without fragment
	FullURL   string // ShareURL + #key=KeyHex
	DeleteURL string
}

// Upload encrypts r (plaintext, size bytes, named filename) and uploads it to
// the server in chunks.  The key is generated internally and never sent to the
// server — it is returned in UploadResult for the caller to display.
func (c *Client) Upload(ctx context.Context, r io.Reader, filename string, size int64, opts UploadOptions) (*UploadResult, error) {
	// ── 1. Select chunk size ──────────────────────────────────────────────────
	chunkSize := opts.ChunkSize
	if chunkSize <= 0 {
		chunkSize = selectChunkSize(size)
	}

	// ── 2. Generate AES-256-GCM key ───────────────────────────────────────────
	rawKey := make([]byte, 32)
	if _, err := rand.Read(rawKey); err != nil {
		return nil, fmt.Errorf("schedule upload: generate key: %w", err)
	}
	keyHex := hex.EncodeToString(rawKey)
	block, err := aes.NewCipher(rawKey)
	if err != nil {
		return nil, fmt.Errorf("schedule upload: create cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("schedule upload: create GCM: %w", err)
	}

	// ── 3. Encrypt filename ───────────────────────────────────────────────────
	fnNonce := make([]byte, NonceSize)
	if _, err := rand.Read(fnNonce); err != nil {
		return nil, fmt.Errorf("schedule upload: filename nonce: %w", err)
	}
	fnEnc := gcm.Seal(nil, fnNonce, []byte(filename), nil)

	// ── 4. Chunk count ────────────────────────────────────────────────────────
	chunksTotal := int((size + int64(chunkSize) - 1) / int64(chunkSize))

	// ── 5. Init upload on server ──────────────────────────────────────────────
	ttlSecs := int64(opts.TTL.Seconds())
	if ttlSecs <= 0 {
		ttlSecs = int64((24 * time.Hour).Seconds())
	}
	initBody, _ := json.Marshal(map[string]any{
		"password":      c.Password,
		"chunks_total":  chunksTotal,
		"total_size":    size,
		"ttl_seconds":   ttlSecs,
		"max_downloads": opts.MaxDownloads,
		"chunk_size":    chunkSize,
	})
	initResp, err := c.post(ctx, "/api/schedule/upload/init", bytes.NewReader(initBody), "application/json")
	if err != nil {
		return nil, fmt.Errorf("schedule upload: init: %w", err)
	}
	defer initResp.Body.Close()
	if initResp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("schedule upload: init: %s", readErrorBody(initResp))
	}
	var initOut struct {
		UploadID  string    `json:"upload_id"`
		ExpiresAt time.Time `json:"expires_at"`
		ChunkSize int       `json:"chunk_size"`
	}
	if err := json.NewDecoder(initResp.Body).Decode(&initOut); err != nil {
		return nil, fmt.Errorf("schedule upload: init decode: %w", err)
	}
	// Use server-returned chunk size — it may have been clamped.
	if initOut.ChunkSize > 0 {
		chunkSize = initOut.ChunkSize
	}

	// ── 6. Encrypt and upload chunks (streaming, low memory) ──────────────────
	// nonce prefix: 8 random bytes, fixed for this upload.
	noncePrefix := make([]byte, 8)
	if _, err := rand.Read(noncePrefix); err != nil {
		return nil, fmt.Errorf("schedule upload: nonce prefix: %w", err)
	}

	sha256h := sha256.New()
	plainBuf := make([]byte, chunkSize)
	var uploadedBytes int64

	for i := 0; ; i++ {
		n, readErr := io.ReadFull(r, plainBuf)
		if n == 0 && readErr == io.EOF {
			break
		}
		if readErr != nil && readErr != io.ErrUnexpectedEOF && readErr != io.EOF {
			return nil, fmt.Errorf("schedule upload: read chunk %d: %w", i, readErr)
		}
		plain := plainBuf[:n]

		// Build nonce: [4-byte big-endian index][8-byte prefix]
		nonce := make([]byte, NonceSize)
		binary.BigEndian.PutUint32(nonce[:4], uint32(i))
		copy(nonce[4:], noncePrefix)

		ciphertext := gcm.Seal(nil, nonce, plain, nil)

		// on-wire: nonce || ciphertext+tag
		wire := make([]byte, NonceSize+len(ciphertext))
		copy(wire, nonce)
		copy(wire[NonceSize:], ciphertext)

		// Accumulate SHA-256.
		sha256h.Write(wire)

		// Upload chunk.
		q := url.Values{
			"upload_id":   {initOut.UploadID},
			"chunk_index": {fmt.Sprintf("%d", i)},
		}
		chunkResp, err := c.postRaw(ctx, "/api/schedule/upload/chunk?"+q.Encode(),
			bytes.NewReader(wire), "application/octet-stream")
		if err != nil {
			return nil, fmt.Errorf("schedule upload: chunk %d: %w", i, err)
		}
		chunkResp.Body.Close()
		if chunkResp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("schedule upload: chunk %d rejected: %s", i, readErrorBody(chunkResp))
		}

		uploadedBytes += int64(n)
		if opts.Progress != nil {
			elapsed := 1.0 // placeholder — caller tracks wall time
			opts.Progress(uploadedBytes, size, float64(n)/elapsed)
		}

		if readErr == io.ErrUnexpectedEOF || readErr == io.EOF {
			break
		}
	}

	// ── 7. Finalize ───────────────────────────────────────────────────────────
	finBody, _ := json.Marshal(map[string]string{
		"upload_id":      initOut.UploadID,
		"filename_enc":   hex.EncodeToString(fnEnc),
		"filename_nonce": hex.EncodeToString(fnNonce),
		"sha256_cipher":  hex.EncodeToString(sha256h.Sum(nil)),
	})
	finResp, err := c.post(ctx, "/api/schedule/upload/complete",
		bytes.NewReader(finBody), "application/json")
	if err != nil {
		return nil, fmt.Errorf("schedule upload: finalize: %w", err)
	}
	defer finResp.Body.Close()
	if finResp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("schedule upload: finalize: %s", readErrorBody(finResp))
	}
	var finOut struct {
		FileID    string    `json:"file_id"`
		DeleteKey string    `json:"delete_key"`
		ExpiresAt time.Time `json:"expires_at"`
	}
	if err := json.NewDecoder(finResp.Body).Decode(&finOut); err != nil {
		return nil, fmt.Errorf("schedule upload: finalize decode: %w", err)
	}

	// ── 8. Build result ───────────────────────────────────────────────────────
	shareURL := fmt.Sprintf("%s/?type=schedule&id=%s", c.BaseURL, finOut.FileID)
	fullURL := fmt.Sprintf("%s#key=%s", shareURL, keyHex)
	deleteURL := fmt.Sprintf("%s/?type=schedule&id=%s&action=delete&dk=%s",
		c.BaseURL, finOut.FileID, finOut.DeleteKey)

	return &UploadResult{
		FileID:    finOut.FileID,
		KeyHex:    keyHex,
		DeleteKey: finOut.DeleteKey,
		ExpiresAt: finOut.ExpiresAt,
		ShareURL:  shareURL,
		FullURL:   fullURL,
		DeleteURL: deleteURL,
	}, nil
}

// ─────────────────────────────────────────────────────────────────────────────
// Download
// ─────────────────────────────────────────────────────────────────────────────

// FileMeta is the public metadata returned by GET /api/schedule/meta/{fileID}.
type PublicFileMeta struct {
	FileID        string    `json:"file_id"`
	FileNameEnc   string    `json:"filename_enc"`
	FileNameNonce string    `json:"filename_nonce"`
	TotalSize     int64     `json:"total_size"`
	ChunksTotal   int       `json:"chunks_total"`
	ChunkSize     int       `json:"chunk_size"`
	ExpiresAt     time.Time `json:"expires_at"`
	DownloadsLeft int       `json:"downloads_left"`
}

// FetchMeta fetches public file metadata without consuming a download.
func (c *Client) FetchMeta(ctx context.Context, fileID string) (*PublicFileMeta, error) {
	resp, err := c.get(ctx, "/api/schedule/meta/"+fileID)
	if err != nil {
		return nil, fmt.Errorf("schedule meta: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusGone {
		return nil, fmt.Errorf("file not found or expired")
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("schedule meta: %s", readErrorBody(resp))
	}
	var m PublicFileMeta
	if err := json.NewDecoder(resp.Body).Decode(&m); err != nil {
		return nil, fmt.Errorf("schedule meta: decode: %w", err)
	}
	return &m, nil
}

// DownloadOptions controls the download behaviour.
type DownloadOptions struct {
	// Progress is called after each decrypted chunk with bytes written so far.
	Progress func(written, total int64, speed float64)
}

// DownloadResult describes a completed download.
type DownloadResult struct {
	Filename  string
	BytesRead int64
}

// Download fetches, decrypts, and streams the file to w.
// keyHex is the hex-encoded AES-256-GCM key from the URL fragment.
// meta is obtained from FetchMeta; passing it avoids a second /meta round trip.
func (c *Client) Download(ctx context.Context, fileID, keyHex string, meta *PublicFileMeta, w io.Writer, opts DownloadOptions) (*DownloadResult, error) {
	// ── 1. Import key ─────────────────────────────────────────────────────────
	rawKey, err := hex.DecodeString(keyHex)
	if err != nil || len(rawKey) != 32 {
		return nil, fmt.Errorf("schedule download: invalid key (expected 32-byte hex)")
	}
	block, err := aes.NewCipher(rawKey)
	if err != nil {
		return nil, fmt.Errorf("schedule download: create cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("schedule download: create GCM: %w", err)
	}

	// ── 2. Start download stream ──────────────────────────────────────────────
	resp, err := c.get(ctx, "/api/schedule/download/"+fileID)
	if err != nil {
		return nil, fmt.Errorf("schedule download: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusGone {
		return nil, fmt.Errorf("file expired or download limit reached")
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("schedule download: %s", readErrorBody(resp))
	}

	// Use server-authoritative chunk size from response headers.
	chunkSize := meta.ChunkSize
	if h := resp.Header.Get("X-Chunk-Size"); h != "" {
		if n, err := fmt.Sscanf(h, "%d", &chunkSize); n != 1 || err != nil {
			chunkSize = meta.ChunkSize
		}
	}
	chunksTotal := meta.ChunksTotal
	if h := resp.Header.Get("X-Chunks-Total"); h != "" {
		if n, err := fmt.Sscanf(h, "%d", &chunksTotal); n != 1 || err != nil {
			chunksTotal = meta.ChunksTotal
		}
	}
	fnEncHex := resp.Header.Get("X-Filename-Enc")
	fnNonceHex := resp.Header.Get("X-Filename-Nonce")

	// ── 3. Decrypt filename ───────────────────────────────────────────────────
	filename := "download"
	if fnEncHex != "" && fnNonceHex != "" {
		fnEnc, e1 := hex.DecodeString(fnEncHex)
		fnNonce, e2 := hex.DecodeString(fnNonceHex)
		if e1 == nil && e2 == nil {
			if plain, err := gcm.Open(nil, fnNonce, fnEnc, nil); err == nil {
				filename = string(plain)
			}
		}
	}

	// ── 4. Stream-decrypt chunks → w ──────────────────────────────────────────
	encChunkSize := NonceSize + chunkSize + TagSize
	wireBuf := make([]byte, encChunkSize)
	var written int64

	for i := 0; i < chunksTotal; i++ {
		// Read exactly one encrypted chunk from the HTTP body.
		// Last chunk may be smaller if file size isn't a multiple of chunkSize.
		n, err := io.ReadFull(resp.Body, wireBuf)
		if err != nil && err != io.ErrUnexpectedEOF {
			return nil, fmt.Errorf("schedule download: read chunk %d: %w", i, err)
		}
		wire := wireBuf[:n]
		if len(wire) < NonceSize {
			return nil, fmt.Errorf("schedule download: chunk %d too short (%d bytes)", i, n)
		}

		nonce := wire[:NonceSize]
		ciphertext := wire[NonceSize:]

		plain, err := gcm.Open(nil, nonce, ciphertext, nil)
		if err != nil {
			return nil, fmt.Errorf("schedule download: decrypt chunk %d: authentication failed — wrong key or corrupted data", i)
		}

		if _, err := w.Write(plain); err != nil {
			return nil, fmt.Errorf("schedule download: write chunk %d: %w", i, err)
		}
		written += int64(len(plain))
		if opts.Progress != nil {
			opts.Progress(written, meta.TotalSize, 0) // caller tracks wall time for speed
		}
	}

	return &DownloadResult{Filename: filename, BytesRead: written}, nil
}

// Delete removes a file from the server using its delete key.
func (c *Client) Delete(ctx context.Context, fileID, deleteKey string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete,
		c.BaseURL+"/api/schedule/delete/"+fileID+"/"+deleteKey, nil)
	if err != nil {
		return err
	}
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return fmt.Errorf("schedule delete: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusForbidden {
		return fmt.Errorf("invalid delete key")
	}
	if resp.StatusCode == http.StatusNotFound {
		return fmt.Errorf("file not found")
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("schedule delete: %s", readErrorBody(resp))
	}
	return nil
}

// ParseDeleteURL extracts fileID and deleteKey from a delete URL.
func ParseDeleteURL(raw string) (fileID, deleteKey string, err error) {
	// Strip fragment if present.
	if i := strings.Index(raw, "#"); i != -1 {
		raw = raw[:i]
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", "", fmt.Errorf("invalid URL: %w", err)
	}
	fileID = u.Query().Get("id")
	deleteKey = u.Query().Get("dk")
	if fileID == "" {
		return "", "", fmt.Errorf("missing file ID in delete URL")
	}
	if deleteKey == "" {
		return "", "", fmt.Errorf("missing delete key (dk=) in delete URL")
	}
	return fileID, deleteKey, nil
}

// Handles the #key= fragment correctly whether or not the shell stripped it.
func ParseShareURL(raw string) (fileID, keyHex string, err error) {
	raw = strings.TrimSpace(raw)

	// Split on # manually — url.Parse treats fragment differently.
	hashIdx := strings.Index(raw, "#")
	fragment := ""
	urlPart := raw
	if hashIdx != -1 {
		fragment = raw[hashIdx+1:]
		urlPart = raw[:hashIdx]
	}

	// Extract key from fragment.
	if strings.HasPrefix(fragment, "key=") {
		keyHex = fragment[4:]
	}
	if keyHex == "" {
		return "", "", fmt.Errorf("missing decryption key — the URL must include #key=... (make sure you copied the full URL including the # part)")
	}

	// Parse base URL for file_id.
	u, err := url.Parse(urlPart)
	if err != nil {
		return "", "", fmt.Errorf("invalid URL: %w", err)
	}
	fileID = u.Query().Get("id")
	if fileID == "" {
		return "", "", fmt.Errorf("missing file ID — the URL must include ?id=... (share URL appears incomplete)")
	}

	// Validate key is hex.
	if _, err := hex.DecodeString(keyHex); err != nil {
		return "", "", fmt.Errorf("invalid decryption key format — expected hex string, got %q", keyHex)
	}

	return fileID, keyHex, nil
}

// ─────────────────────────────────────────────────────────────────────────────
// Helpers
// ─────────────────────────────────────────────────────────────────────────────

func (c *Client) httpClient() *http.Client {
	if c.HTTPClient != nil {
		return c.HTTPClient
	}
	return http.DefaultClient
}

func (c *Client) post(ctx context.Context, path string, body io.Reader, contentType string) (*http.Response, error) {
	if body == nil {
		body = http.NoBody
	}
	if contentType == "" {
		contentType = "application/json"
	}
	return c.postRaw(ctx, path, body, contentType)
}

func (c *Client) postRaw(ctx context.Context, path string, body io.Reader, contentType string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+path, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", contentType)
	if c.Password != "" {
		req.Header.Set("X-Schedule-Password", c.Password)
	}
	return c.httpClient().Do(req)
}

func (c *Client) get(ctx context.Context, path string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+path, nil)
	if err != nil {
		return nil, err
	}
	return c.httpClient().Do(req)
}

func readErrorBody(resp *http.Response) string {
	var e struct {
		Error string `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&e); err == nil && e.Error != "" {
		return e.Error
	}
	return resp.Status
}

// selectChunkSize returns an appropriate chunk size based on file size.
// Smaller files use smaller chunks to reduce per-chunk overhead;
// larger files use larger chunks for throughput efficiency.
func selectChunkSize(fileSize int64) int {
	switch {
	case fileSize < 10*1024*1024: // < 10 MB
		return 256 * 1024 // 256 KB
	case fileSize < 100*1024*1024: // < 100 MB
		return 512 * 1024 // 512 KB
	default: // ≥ 100 MB
		return 1024 * 1024 // 1 MB
	}
}
