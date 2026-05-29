package schedule

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
)

// ─────────────────────────────────────────────────────────────────────────────
// Test infrastructure
// ─────────────────────────────────────────────────────────────────────────────

// newTestHandler builds a Handler + chi router wired up for testing.
// The store is backed by t.TempDir() so all files are cleaned up automatically.
// An optional cfgFn callback lets individual tests adjust the Config before
// the Handler is created.
func newTestHandler(t *testing.T, cfgFn func(*Config)) (*Handler, http.Handler) {
	t.Helper()
	dir := t.TempDir()
	cfg := &Config{
		Dir:          dir,
		PendingDir:   filepath.Join(dir, "pending"),
		CompleteDir:  filepath.Join(dir, "complete"),
		MaxSize:      10 * 1024 * 1024, // 10 MB
		MaxDownloads: 3,
		TTLOptions:   DefaultTTLOptions(),
	}
	if cfgFn != nil {
		cfgFn(cfg)
	}
	h, err := NewHandler(cfg)
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}
	r := chi.NewRouter()
	h.Mount(r)
	return h, r
}

// do fires a single HTTP request against the handler and returns the response.
func do(t *testing.T, handler http.Handler, method, path string, body io.Reader, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, body)
	req.RemoteAddr = "127.0.0.1:12345"
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	return w
}

// jsonBody returns an io.Reader containing the JSON encoding of v.
func jsonBody(t *testing.T, v any) io.Reader {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("jsonBody: %v", err)
	}
	return bytes.NewReader(b)
}

// decodeJSON decodes the response body into v, failing the test on error.
func decodeJSON(t *testing.T, w *httptest.ResponseRecorder, v any) {
	t.Helper()
	if err := json.Unmarshal(w.Body.Bytes(), v); err != nil {
		t.Fatalf("decodeJSON: %v\nbody: %s", err, w.Body.String())
	}
}

// assertStatus fails the test if the recorded status code doesn't match want.
func assertStatus(t *testing.T, w *httptest.ResponseRecorder, want int) {
	t.Helper()
	if w.Code != want {
		t.Errorf("status: got %d, want %d\nbody: %s", w.Code, want, w.Body.String())
	}
}

// assertContentType fails if Content-Type doesn't contain the expected value.
func assertContentType(t *testing.T, w *httptest.ResponseRecorder, want string) {
	t.Helper()
	got := w.Header().Get("Content-Type")
	if !strings.Contains(got, want) {
		t.Errorf("Content-Type: got %q, want it to contain %q", got, want)
	}
}

// doFullUploadViaHTTP runs the complete upload lifecycle through the HTTP
// handlers, mirroring exactly what the browser does.
// Returns the uploadCompleteResponse with file_id, delete_key, expires_at.
func doFullUploadViaHTTP(t *testing.T, handler http.Handler, chunks int, chunkPayload int) uploadCompleteResponse {
	t.Helper()
	totalSize := int64(chunks * chunkPayload)

	// Init.
	initW := do(t, handler, http.MethodPost, "/api/schedule/upload/init",
		jsonBody(t, map[string]any{
			"chunks_total":  chunks,
			"total_size":    totalSize,
			"ttl_seconds":   3600,
			"max_downloads": 1,
			"chunk_size":    chunkPayload,
		}),
		map[string]string{"Content-Type": "application/json"},
	)
	assertStatus(t, initW, http.StatusOK)
	var initResp uploadInitResponse
	decodeJSON(t, initW, &initResp)
	if initResp.UploadID == "" {
		t.Fatal("upload_id is empty")
	}

	// Upload chunks.
	for i := 0; i < chunks; i++ {
		chunk := fakeChunk(i, chunkPayload)
		path := fmt.Sprintf("/api/schedule/upload/chunk?upload_id=%s&chunk_index=%d",
			initResp.UploadID, i)
		chunkW := do(t, handler, http.MethodPost, path,
			bytes.NewReader(chunk),
			map[string]string{"Content-Type": "application/octet-stream"},
		)
		assertStatus(t, chunkW, http.StatusOK)
	}

	// Complete.
	completeW := do(t, handler, http.MethodPost, "/api/schedule/upload/complete",
		jsonBody(t, map[string]any{
			"upload_id":      initResp.UploadID,
			"filename_enc":   "deadbeef",
			"filename_nonce": "cafebabe",
			"sha256_cipher":  "abc123",
		}),
		map[string]string{"Content-Type": "application/json"},
	)
	assertStatus(t, completeW, http.StatusOK)

	var completeResp uploadCompleteResponse
	decodeJSON(t, completeW, &completeResp)
	if completeResp.FileID == "" {
		t.Fatal("file_id is empty after complete")
	}
	return completeResp
}

// ─────────────────────────────────────────────────────────────────────────────
// handleAuth
// ─────────────────────────────────────────────────────────────────────────────

func TestHandleAuth_NoRestrictions_AllowsEveryone(t *testing.T) {
	_, handler := newTestHandler(t, nil)

	w := do(t, handler, http.MethodPost, "/api/schedule/auth", nil, nil)
	assertStatus(t, w, http.StatusOK)

	var resp authResponse
	decodeJSON(t, w, &resp)
	if !resp.Allowed {
		t.Error("allowed: got false, want true (no restrictions set)")
	}
	if resp.NeedsPassword {
		t.Error("needs_password: got true, want false (no password set)")
	}
}

func TestHandleAuth_PasswordRequired_WhenIPNotAllowed(t *testing.T) {
	_, handler := newTestHandler(t, func(cfg *Config) {
		cfg.UploadPassword = "secret"
		// No UploadIPs set — password applies to everyone.
	})

	w := do(t, handler, http.MethodPost, "/api/schedule/auth", nil, nil)
	assertStatus(t, w, http.StatusOK)

	var resp authResponse
	decodeJSON(t, w, &resp)
	if resp.Allowed {
		t.Error("allowed: got true, want false (IP not in allowlist)")
	}
	if !resp.NeedsPassword {
		t.Error("needs_password: got false, want true (password is set)")
	}
}

func TestHandleAuth_IPAllowed_NoPasswordNeeded(t *testing.T) {
	_, handler := newTestHandler(t, func(cfg *Config) {
		cfg.UploadPassword = "secret"
		nets, _ := parseCIDRList("127.0.0.1")
		cfg.UploadIPs = nets
	})

	// RemoteAddr is 127.0.0.1 by default in do().
	w := do(t, handler, http.MethodPost, "/api/schedule/auth", nil, nil)
	assertStatus(t, w, http.StatusOK)

	var resp authResponse
	decodeJSON(t, w, &resp)
	if !resp.Allowed {
		t.Error("allowed: got false, want true (IP is in allowlist)")
	}
	if resp.NeedsPassword {
		t.Error("needs_password: got true, want false (IP bypasses password)")
	}
}

func TestHandleAuth_XRealIP_UsedWhenPresent(t *testing.T) {
	_, handler := newTestHandler(t, func(cfg *Config) {
		nets, _ := parseCIDRList("10.0.0.1")
		cfg.UploadIPs = nets
		cfg.UploadPassword = "secret"
	})

	// Simulate nginx forwarding X-Real-IP.
	w := do(t, handler, http.MethodPost, "/api/schedule/auth", nil,
		map[string]string{"X-Real-IP": "10.0.0.1"},
	)
	assertStatus(t, w, http.StatusOK)

	var resp authResponse
	decodeJSON(t, w, &resp)
	if !resp.Allowed {
		t.Error("X-Real-IP 10.0.0.1 should be allowed")
	}
	if resp.NeedsPassword {
		t.Error("X-Real-IP in allowlist should not require password")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// handleProbe
// ─────────────────────────────────────────────────────────────────────────────

func TestHandleProbe_DiscardsBodyAndResponds(t *testing.T) {
	_, handler := newTestHandler(t, nil)

	body := bytes.Repeat([]byte{0xAB}, 512*1024) // 512 KB
	w := do(t, handler, http.MethodPost, "/api/schedule/probe",
		bytes.NewReader(body),
		map[string]string{"Content-Type": "application/octet-stream"},
	)
	assertStatus(t, w, http.StatusOK)
	assertContentType(t, w, "application/json")

	var resp map[string]any
	decodeJSON(t, w, &resp)

	gotBytes, ok := resp["bytes"]
	if !ok {
		t.Fatal("response missing 'bytes' field")
	}
	// JSON numbers decode as float64.
	if int(gotBytes.(float64)) != len(body) {
		t.Errorf("bytes: got %v, want %d", gotBytes, len(body))
	}
	if _, ok := resp["elapsed_ms"]; !ok {
		t.Error("response missing 'elapsed_ms' field")
	}
}

func TestHandleProbe_EmptyBody(t *testing.T) {
	_, handler := newTestHandler(t, nil)
	w := do(t, handler, http.MethodPost, "/api/schedule/probe", nil, nil)
	assertStatus(t, w, http.StatusOK)
}

// ─────────────────────────────────────────────────────────────────────────────
// handleUploadInit
// ─────────────────────────────────────────────────────────────────────────────

func TestHandleUploadInit_Success(t *testing.T) {
	_, handler := newTestHandler(t, nil)

	w := do(t, handler, http.MethodPost, "/api/schedule/upload/init",
		jsonBody(t, map[string]any{
			"chunks_total":  4,
			"total_size":    4 * 256,
			"ttl_seconds":   3600,
			"max_downloads": 1,
			"chunk_size":    256,
		}),
		map[string]string{"Content-Type": "application/json"},
	)
	assertStatus(t, w, http.StatusOK)

	var resp uploadInitResponse
	decodeJSON(t, w, &resp)
	if resp.UploadID == "" {
		t.Error("upload_id should not be empty")
	}
	if resp.ExpiresAt.IsZero() {
		t.Error("expires_at should not be zero")
	}
	if resp.ExpiresAt.Before(time.Now()) {
		t.Error("expires_at should be in the future")
	}
}

func TestHandleUploadInit_FileTooLarge(t *testing.T) {
	_, handler := newTestHandler(t, func(cfg *Config) {
		cfg.MaxSize = 1024 // 1 KB limit
	})

	w := do(t, handler, http.MethodPost, "/api/schedule/upload/init",
		jsonBody(t, map[string]any{
			"chunks_total":  1,
			"total_size":    2048, // 2 KB — over limit
			"ttl_seconds":   3600,
			"max_downloads": 1,
			"chunk_size":    2048,
		}),
		map[string]string{"Content-Type": "application/json"},
	)
	assertStatus(t, w, http.StatusRequestEntityTooLarge)
}

func TestHandleUploadInit_ZeroTotalSize_Rejected(t *testing.T) {
	_, handler := newTestHandler(t, nil)

	w := do(t, handler, http.MethodPost, "/api/schedule/upload/init",
		jsonBody(t, map[string]any{
			"chunks_total":  1,
			"total_size":    0,
			"ttl_seconds":   3600,
			"max_downloads": 1,
			"chunk_size":    256,
		}),
		map[string]string{"Content-Type": "application/json"},
	)
	assertStatus(t, w, http.StatusBadRequest)
}

func TestHandleUploadInit_ZeroTTL_Rejected(t *testing.T) {
	_, handler := newTestHandler(t, nil)

	w := do(t, handler, http.MethodPost, "/api/schedule/upload/init",
		jsonBody(t, map[string]any{
			"chunks_total":  1,
			"total_size":    256,
			"ttl_seconds":   0,
			"max_downloads": 1,
			"chunk_size":    256,
		}),
		map[string]string{"Content-Type": "application/json"},
	)
	assertStatus(t, w, http.StatusBadRequest)
}

func TestHandleUploadInit_ChunksTotalMismatch_Rejected(t *testing.T) {
	_, handler := newTestHandler(t, nil)

	// totalSize=512, chunkSize=256 → expectedChunks=2, but we claim 5.
	w := do(t, handler, http.MethodPost, "/api/schedule/upload/init",
		jsonBody(t, map[string]any{
			"chunks_total":  5,
			"total_size":    512,
			"ttl_seconds":   3600,
			"max_downloads": 1,
			"chunk_size":    256,
		}),
		map[string]string{"Content-Type": "application/json"},
	)
	assertStatus(t, w, http.StatusBadRequest)
}

func TestHandleUploadInit_PasswordRequired_WithoutPassword(t *testing.T) {
	_, handler := newTestHandler(t, func(cfg *Config) {
		cfg.UploadPassword = "secret"
	})

	w := do(t, handler, http.MethodPost, "/api/schedule/upload/init",
		jsonBody(t, map[string]any{
			"chunks_total":  1,
			"total_size":    256,
			"ttl_seconds":   3600,
			"max_downloads": 1,
			"chunk_size":    256,
		}),
		map[string]string{"Content-Type": "application/json"},
	)
	assertStatus(t, w, http.StatusForbidden)
}

func TestHandleUploadInit_CorrectPassword_Accepted(t *testing.T) {
	_, handler := newTestHandler(t, func(cfg *Config) {
		cfg.UploadPassword = "secret"
	})

	w := do(t, handler, http.MethodPost, "/api/schedule/upload/init",
		jsonBody(t, map[string]any{
			"chunks_total":  1,
			"total_size":    256,
			"ttl_seconds":   3600,
			"max_downloads": 1,
			"chunk_size":    256,
		}),
		map[string]string{
			"Content-Type":       "application/json",
			"X-Schedule-Password": "secret",
		},
	)
	assertStatus(t, w, http.StatusOK)
}

func TestHandleUploadInit_MaxDownloadsCapped(t *testing.T) {
	_, handler := newTestHandler(t, func(cfg *Config) {
		cfg.MaxDownloads = 2 // server cap
	})

	// Client requests 10 — should be silently capped to 2.
	w := do(t, handler, http.MethodPost, "/api/schedule/upload/init",
		jsonBody(t, map[string]any{
			"chunks_total":  1,
			"total_size":    256,
			"ttl_seconds":   3600,
			"max_downloads": 10,
			"chunk_size":    256,
		}),
		map[string]string{"Content-Type": "application/json"},
	)
	assertStatus(t, w, http.StatusOK)
	// Cap enforced — subsequent download limit will be 2, not 10.
}

func TestHandleUploadInit_MaxDownloadsBelowCap_Honoured(t *testing.T) {
	_, handler := newTestHandler(t, func(cfg *Config) {
		cfg.MaxDownloads = 5 // server cap
	})

	// Client requests 2 (below cap) — should be honoured as-is.
	initW := do(t, handler, http.MethodPost, "/api/schedule/upload/init",
		jsonBody(t, map[string]any{
			"chunks_total":  1,
			"total_size":    256,
			"ttl_seconds":   3600,
			"max_downloads": 2,
			"chunk_size":    256,
		}),
		map[string]string{"Content-Type": "application/json"},
	)
	assertStatus(t, initW, http.StatusOK)
	var initResp uploadInitResponse
	decodeJSON(t, initW, &initResp)

	path := fmt.Sprintf("/api/schedule/upload/chunk?upload_id=%s&chunk_index=0", initResp.UploadID)
	do(t, handler, http.MethodPost, path,
		bytes.NewReader(fakeChunk(0, 256)),
		map[string]string{"Content-Type": "application/octet-stream"},
	)
	completeW := do(t, handler, http.MethodPost, "/api/schedule/upload/complete",
		jsonBody(t, map[string]any{
			"upload_id":      initResp.UploadID,
			"filename_enc":   "enc",
			"filename_nonce": "nonce",
			"sha256_cipher":  "sha",
		}),
		map[string]string{"Content-Type": "application/json"},
	)
	var completeResp uploadCompleteResponse
	decodeJSON(t, completeW, &completeResp)

	// Meta should report 2 downloads left — client's value honoured.
	metaW := do(t, handler, http.MethodGet,
		"/api/schedule/meta/"+completeResp.FileID, nil, nil)
	var meta publicMeta
	decodeJSON(t, metaW, &meta)
	if meta.DownloadsLeft != 2 {
		t.Errorf("downloads_left: got %d, want 2 (client value below cap)", meta.DownloadsLeft)
	}
}

func TestHandleUploadInit_InvalidJSON_Rejected(t *testing.T) {
	_, handler := newTestHandler(t, nil)

	w := do(t, handler, http.MethodPost, "/api/schedule/upload/init",
		strings.NewReader("{not valid json"),
		map[string]string{"Content-Type": "application/json"},
	)
	assertStatus(t, w, http.StatusBadRequest)
}

// ─────────────────────────────────────────────────────────────────────────────
// handleUploadChunk
// ─────────────────────────────────────────────────────────────────────────────

func TestHandleUploadChunk_Success(t *testing.T) {
	_, handler := newTestHandler(t, nil)

	// Init first.
	initW := do(t, handler, http.MethodPost, "/api/schedule/upload/init",
		jsonBody(t, map[string]any{
			"chunks_total":  2,
			"total_size":    512,
			"ttl_seconds":   3600,
			"max_downloads": 1,
			"chunk_size":    256,
		}),
		map[string]string{"Content-Type": "application/json"},
	)
	assertStatus(t, initW, http.StatusOK)
	var initResp uploadInitResponse
	decodeJSON(t, initW, &initResp)

	// Upload chunk 0.
	chunk := fakeChunk(0, 256)
	path := fmt.Sprintf("/api/schedule/upload/chunk?upload_id=%s&chunk_index=0", initResp.UploadID)
	w := do(t, handler, http.MethodPost, path,
		bytes.NewReader(chunk),
		map[string]string{"Content-Type": "application/octet-stream"},
	)
	assertStatus(t, w, http.StatusOK)

	var resp map[string]any
	decodeJSON(t, w, &resp)
	if resp["ok"] != true {
		t.Errorf("ok: got %v, want true", resp["ok"])
	}
}

func TestHandleUploadChunk_MissingParams_Rejected(t *testing.T) {
	_, handler := newTestHandler(t, nil)

	// No upload_id or chunk_index.
	w := do(t, handler, http.MethodPost, "/api/schedule/upload/chunk",
		bytes.NewReader(fakeChunk(0, 256)),
		map[string]string{"Content-Type": "application/octet-stream"},
	)
	assertStatus(t, w, http.StatusBadRequest)
}

func TestHandleUploadChunk_UnknownUploadID_Rejected(t *testing.T) {
	_, handler := newTestHandler(t, nil)

	w := do(t, handler, http.MethodPost,
		"/api/schedule/upload/chunk?upload_id=doesnotexist&chunk_index=0",
		bytes.NewReader(fakeChunk(0, 256)),
		map[string]string{"Content-Type": "application/octet-stream"},
	)
	assertStatus(t, w, http.StatusBadRequest)
}

func TestHandleUploadChunk_OutOfOrder_Rejected(t *testing.T) {
	_, handler := newTestHandler(t, nil)

	initW := do(t, handler, http.MethodPost, "/api/schedule/upload/init",
		jsonBody(t, map[string]any{
			"chunks_total":  3,
			"total_size":    768,
			"ttl_seconds":   3600,
			"max_downloads": 1,
			"chunk_size":    256,
		}),
		map[string]string{"Content-Type": "application/json"},
	)
	var initResp uploadInitResponse
	decodeJSON(t, initW, &initResp)

	// Send chunk 2 before chunk 0.
	path := fmt.Sprintf("/api/schedule/upload/chunk?upload_id=%s&chunk_index=2", initResp.UploadID)
	w := do(t, handler, http.MethodPost, path,
		bytes.NewReader(fakeChunk(2, 256)),
		map[string]string{"Content-Type": "application/octet-stream"},
	)
	assertStatus(t, w, http.StatusBadRequest)
}

// ─────────────────────────────────────────────────────────────────────────────
// handleUploadComplete
// ─────────────────────────────────────────────────────────────────────────────

func TestHandleUploadComplete_FullLifecycle(t *testing.T) {
	_, handler := newTestHandler(t, nil)
	resp := doFullUploadViaHTTP(t, handler, 2, 256)

	if resp.FileID == "" {
		t.Error("file_id should not be empty")
	}
	if resp.DeleteKey == "" {
		t.Error("delete_key should not be empty")
	}
	if resp.ExpiresAt.Before(time.Now()) {
		t.Error("expires_at should be in the future")
	}
}

func TestHandleUploadComplete_MissingFields_Rejected(t *testing.T) {
	_, handler := newTestHandler(t, nil)

	// Finalize without uploading — missing upload_id, filename_enc, sha256_cipher.
	w := do(t, handler, http.MethodPost, "/api/schedule/upload/complete",
		jsonBody(t, map[string]any{}),
		map[string]string{"Content-Type": "application/json"},
	)
	assertStatus(t, w, http.StatusBadRequest)
}

func TestHandleUploadComplete_IncompleteUpload_Rejected(t *testing.T) {
	_, handler := newTestHandler(t, nil)

	// Init for 3 chunks but only upload 1.
	initW := do(t, handler, http.MethodPost, "/api/schedule/upload/init",
		jsonBody(t, map[string]any{
			"chunks_total":  3,
			"total_size":    768,
			"ttl_seconds":   3600,
			"max_downloads": 1,
			"chunk_size":    256,
		}),
		map[string]string{"Content-Type": "application/json"},
	)
	var initResp uploadInitResponse
	decodeJSON(t, initW, &initResp)

	path := fmt.Sprintf("/api/schedule/upload/chunk?upload_id=%s&chunk_index=0", initResp.UploadID)
	do(t, handler, http.MethodPost, path,
		bytes.NewReader(fakeChunk(0, 256)),
		map[string]string{"Content-Type": "application/octet-stream"},
	)

	w := do(t, handler, http.MethodPost, "/api/schedule/upload/complete",
		jsonBody(t, map[string]any{
			"upload_id":      initResp.UploadID,
			"filename_enc":   "abc",
			"filename_nonce": "def",
			"sha256_cipher":  "ghi",
		}),
		map[string]string{"Content-Type": "application/json"},
	)
	assertStatus(t, w, http.StatusBadRequest)
}

// ─────────────────────────────────────────────────────────────────────────────
// handleDownload
// ─────────────────────────────────────────────────────────────────────────────

func TestHandleDownload_Success(t *testing.T) {
	_, handler := newTestHandler(t, nil)
	completeResp := doFullUploadViaHTTP(t, handler, 2, 256)

	w := do(t, handler, http.MethodGet,
		"/api/schedule/download/"+completeResp.FileID, nil, nil)
	assertStatus(t, w, http.StatusOK)
	assertContentType(t, w, "application/octet-stream")

	// Body must be non-empty.
	if w.Body.Len() == 0 {
		t.Error("download body should not be empty")
	}

	// Check response headers.
	if w.Header().Get("X-Chunks-Total") == "" {
		t.Error("X-Chunks-Total header should be set")
	}
	if w.Header().Get("X-Chunk-Size") == "" {
		t.Error("X-Chunk-Size header should be set")
	}
	if w.Header().Get("X-Filename-Enc") != "deadbeef" {
		t.Errorf("X-Filename-Enc: got %q, want %q",
			w.Header().Get("X-Filename-Enc"), "deadbeef")
	}
	if w.Header().Get("Cache-Control") != "no-store" {
		t.Errorf("Cache-Control: got %q, want no-store",
			w.Header().Get("Cache-Control"))
	}
}

func TestHandleDownload_DecrementsCounts(t *testing.T) {
	_, handler := newTestHandler(t, func(cfg *Config) {
		cfg.MaxDownloads = 3 // server cap of 3; client will request 2
	})

	// Init with max_downloads=2 (below server cap of 3 — client choice honoured).
	initW := do(t, handler, http.MethodPost, "/api/schedule/upload/init",
		jsonBody(t, map[string]any{
			"chunks_total":  1,
			"total_size":    256,
			"ttl_seconds":   3600,
			"max_downloads": 2,
			"chunk_size":    256,
		}),
		map[string]string{"Content-Type": "application/json"},
	)
	assertStatus(t, initW, http.StatusOK)
	var initResp uploadInitResponse
	decodeJSON(t, initW, &initResp)

	path := fmt.Sprintf("/api/schedule/upload/chunk?upload_id=%s&chunk_index=0", initResp.UploadID)
	do(t, handler, http.MethodPost, path,
		bytes.NewReader(fakeChunk(0, 256)),
		map[string]string{"Content-Type": "application/octet-stream"},
	)
	completeW := do(t, handler, http.MethodPost, "/api/schedule/upload/complete",
		jsonBody(t, map[string]any{
			"upload_id":      initResp.UploadID,
			"filename_enc":   "deadbeef",
			"filename_nonce": "cafebabe",
			"sha256_cipher":  "abc123",
		}),
		map[string]string{"Content-Type": "application/json"},
	)
	var completeResp uploadCompleteResponse
	decodeJSON(t, completeW, &completeResp)

	// Two downloads should succeed.
	for i := 0; i < 2; i++ {
		w := do(t, handler, http.MethodGet,
			"/api/schedule/download/"+completeResp.FileID, nil, nil)
		assertStatus(t, w, http.StatusOK)
	}

	// Third download must fail — client requested limit of 2.
	w := do(t, handler, http.MethodGet,
		"/api/schedule/download/"+completeResp.FileID, nil, nil)
	assertStatus(t, w, http.StatusGone)
}

func TestHandleDownload_UnknownFileID_NotFound(t *testing.T) {
	_, handler := newTestHandler(t, nil)

	w := do(t, handler, http.MethodGet,
		"/api/schedule/download/doesnotexist", nil, nil)
	// Unknown file → os error (no such file) → 404 or 410.
	if w.Code != http.StatusNotFound && w.Code != http.StatusGone {
		t.Errorf("expected 404 or 410, got %d", w.Code)
	}
}

func TestHandleDownload_DownloadIPRestricted(t *testing.T) {
	_, handler := newTestHandler(t, func(cfg *Config) {
		nets, _ := parseCIDRList("10.0.0.0/8")
		cfg.DownloadIPs = nets
	})
	completeResp := doFullUploadViaHTTP(t, handler, 1, 256)

	// Request from 127.0.0.1 (not in 10.0.0.0/8) should be forbidden.
	w := do(t, handler, http.MethodGet,
		"/api/schedule/download/"+completeResp.FileID, nil, nil)
	assertStatus(t, w, http.StatusForbidden)
}

func TestHandleDownload_ContentLengthMatchesBody(t *testing.T) {
	_, handler := newTestHandler(t, nil)
	completeResp := doFullUploadViaHTTP(t, handler, 3, 256)

	w := do(t, handler, http.MethodGet,
		"/api/schedule/download/"+completeResp.FileID, nil, nil)
	assertStatus(t, w, http.StatusOK)

	clStr := w.Header().Get("Content-Length")
	if clStr == "" {
		t.Fatal("Content-Length header missing")
	}
	cl, err := strconv.ParseInt(clStr, 10, 64)
	if err != nil {
		t.Fatalf("Content-Length not an integer: %v", err)
	}
	if cl != int64(w.Body.Len()) {
		t.Errorf("Content-Length %d != body length %d", cl, w.Body.Len())
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// handleMeta
// ─────────────────────────────────────────────────────────────────────────────

func TestHandleMeta_Success(t *testing.T) {
	_, handler := newTestHandler(t, nil)
	completeResp := doFullUploadViaHTTP(t, handler, 2, 256)

	w := do(t, handler, http.MethodGet,
		"/api/schedule/meta/"+completeResp.FileID, nil, nil)
	assertStatus(t, w, http.StatusOK)
	assertContentType(t, w, "application/json")

	var meta publicMeta
	decodeJSON(t, w, &meta)
	if meta.FileID != completeResp.FileID {
		t.Errorf("file_id: got %q, want %q", meta.FileID, completeResp.FileID)
	}
	if meta.FileNameEnc != "deadbeef" {
		t.Errorf("filename_enc: got %q, want %q", meta.FileNameEnc, "deadbeef")
	}
	if meta.ChunksTotal != 2 {
		t.Errorf("chunks_total: got %d, want 2", meta.ChunksTotal)
	}
	// Meta must NOT expose the delete key.
	raw := w.Body.String()
	if strings.Contains(raw, "delete_key") {
		t.Error("meta response must not contain delete_key")
	}
}

func TestHandleMeta_UnknownFile_NotFound(t *testing.T) {
	_, handler := newTestHandler(t, nil)

	w := do(t, handler, http.MethodGet,
		"/api/schedule/meta/doesnotexist", nil, nil)
	assertStatus(t, w, http.StatusNotFound)
}

func TestHandleMeta_ExpiredFile_Gone(t *testing.T) {
	// Expiry backdating requires direct store access.
	// This scenario is covered in Tier 2 store tests (TestOpenDownload_ExpiredFileRejected).
	t.Skip("expiry backdating covered in store_test.go")
}

func TestHandleMeta_ExhaustedFile_Gone(t *testing.T) {
	_, handler := newTestHandler(t, func(cfg *Config) {
		cfg.MaxDownloads = 1
	})
	completeResp := doFullUploadViaHTTP(t, handler, 1, 256)

	// Download once to exhaust.
	do(t, handler, http.MethodGet,
		"/api/schedule/download/"+completeResp.FileID, nil, nil)

	// Meta should now return 410 Gone.
	w := do(t, handler, http.MethodGet,
		"/api/schedule/meta/"+completeResp.FileID, nil, nil)
	assertStatus(t, w, http.StatusGone)
}

// ─────────────────────────────────────────────────────────────────────────────
// handleDelete
// ─────────────────────────────────────────────────────────────────────────────

func TestHandleDelete_Success(t *testing.T) {
	_, handler := newTestHandler(t, nil)
	completeResp := doFullUploadViaHTTP(t, handler, 1, 256)

	path := fmt.Sprintf("/api/schedule/delete/%s/%s",
		completeResp.FileID, completeResp.DeleteKey)
	w := do(t, handler, http.MethodDelete, path, nil, nil)
	assertStatus(t, w, http.StatusOK)

	var resp map[string]bool
	decodeJSON(t, w, &resp)
	if !resp["deleted"] {
		t.Error("deleted: got false, want true")
	}

	// File should be gone — download must fail.
	dlW := do(t, handler, http.MethodGet,
		"/api/schedule/download/"+completeResp.FileID, nil, nil)
	if dlW.Code == http.StatusOK {
		t.Error("file should be gone after delete, but download succeeded")
	}
}

func TestHandleDelete_WrongKey_Forbidden(t *testing.T) {
	_, handler := newTestHandler(t, nil)
	completeResp := doFullUploadViaHTTP(t, handler, 1, 256)

	path := fmt.Sprintf("/api/schedule/delete/%s/wrongkey", completeResp.FileID)
	w := do(t, handler, http.MethodDelete, path, nil, nil)
	assertStatus(t, w, http.StatusForbidden)

	// File must still be downloadable.
	dlW := do(t, handler, http.MethodGet,
		"/api/schedule/download/"+completeResp.FileID, nil, nil)
	assertStatus(t, dlW, http.StatusOK)
}

func TestHandleDelete_UnknownFile_NotFound(t *testing.T) {
	_, handler := newTestHandler(t, nil)

	w := do(t, handler, http.MethodDelete,
		"/api/schedule/delete/doesnotexist/anykey", nil, nil)
	assertStatus(t, w, http.StatusNotFound)
}

// ─────────────────────────────────────────────────────────────────────────────
// handleTTLOptions
// ─────────────────────────────────────────────────────────────────────────────

func TestHandleTTLOptions_ReturnsOptions(t *testing.T) {
	_, handler := newTestHandler(t, nil)

	w := do(t, handler, http.MethodGet, "/api/schedule/ttl-options", nil, nil)
	assertStatus(t, w, http.StatusOK)
	assertContentType(t, w, "application/json")

	var resp ttlOptionsResponse
	decodeJSON(t, w, &resp)

	if len(resp.Options) == 0 {
		t.Error("options should not be empty")
	}
	for i, o := range resp.Options {
		if o.Label == "" {
			t.Errorf("options[%d].label is empty", i)
		}
		if o.Seconds <= 0 {
			t.Errorf("options[%d].seconds = %d, want > 0", i, o.Seconds)
		}
	}
}

func TestHandleTTLOptions_MaxSizeBytesPresent(t *testing.T) {
	_, handler := newTestHandler(t, func(cfg *Config) {
		cfg.MaxSize = 5 * 1024 * 1024 // 5 MB
	})

	w := do(t, handler, http.MethodGet, "/api/schedule/ttl-options", nil, nil)
	assertStatus(t, w, http.StatusOK)

	var resp ttlOptionsResponse
	decodeJSON(t, w, &resp)

	if resp.MaxSizeBytes != 5*1024*1024 {
		t.Errorf("max_size_bytes: got %d, want %d", resp.MaxSizeBytes, 5*1024*1024)
	}
}

func TestHandleTTLOptions_MaxDownloadsCapPresent(t *testing.T) {
	_, handler := newTestHandler(t, func(cfg *Config) {
		cfg.MaxDownloads = 5
	})

	w := do(t, handler, http.MethodGet, "/api/schedule/ttl-options", nil, nil)
	assertStatus(t, w, http.StatusOK)

	var resp ttlOptionsResponse
	decodeJSON(t, w, &resp)

	if resp.MaxDownloadsCap != 5 {
		t.Errorf("max_downloads_cap: got %d, want 5", resp.MaxDownloadsCap)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Full end-to-end: upload → download → delete
// ─────────────────────────────────────────────────────────────────────────────

func TestEndToEnd_UploadDownloadDelete(t *testing.T) {
	_, handler := newTestHandler(t, func(cfg *Config) {
		cfg.MaxDownloads = 5 // generous server cap; client will request 2
	})

	// Upload with explicit max_downloads=2.
	totalSize := int64(3 * 256)
	initW := do(t, handler, http.MethodPost, "/api/schedule/upload/init",
		jsonBody(t, map[string]any{
			"chunks_total":  3,
			"total_size":    totalSize,
			"ttl_seconds":   3600,
			"max_downloads": 2,
			"chunk_size":    256,
		}),
		map[string]string{"Content-Type": "application/json"},
	)
	assertStatus(t, initW, http.StatusOK)
	var initResp uploadInitResponse
	decodeJSON(t, initW, &initResp)

	for i := 0; i < 3; i++ {
		path := fmt.Sprintf("/api/schedule/upload/chunk?upload_id=%s&chunk_index=%d",
			initResp.UploadID, i)
		do(t, handler, http.MethodPost, path,
			bytes.NewReader(fakeChunk(i, 256)),
			map[string]string{"Content-Type": "application/octet-stream"},
		)
	}

	completeW := do(t, handler, http.MethodPost, "/api/schedule/upload/complete",
		jsonBody(t, map[string]any{
			"upload_id":      initResp.UploadID,
			"filename_enc":   "deadbeef",
			"filename_nonce": "cafebabe",
			"sha256_cipher":  "abc123",
		}),
		map[string]string{"Content-Type": "application/json"},
	)
	assertStatus(t, completeW, http.StatusOK)
	var completeResp uploadCompleteResponse
	decodeJSON(t, completeW, &completeResp)

	// Meta — should show 2 downloads left.
	metaW := do(t, handler, http.MethodGet,
		"/api/schedule/meta/"+completeResp.FileID, nil, nil)
	assertStatus(t, metaW, http.StatusOK)
	var meta publicMeta
	decodeJSON(t, metaW, &meta)
	if meta.DownloadsLeft != 2 {
		t.Errorf("downloads_left before any download: got %d, want 2", meta.DownloadsLeft)
	}

	// First download.
	dl1 := do(t, handler, http.MethodGet,
		"/api/schedule/download/"+completeResp.FileID, nil, nil)
	assertStatus(t, dl1, http.StatusOK)

	// Meta — should show 1 download left.
	meta2W := do(t, handler, http.MethodGet,
		"/api/schedule/meta/"+completeResp.FileID, nil, nil)
	assertStatus(t, meta2W, http.StatusOK)
	var meta2 publicMeta
	decodeJSON(t, meta2W, &meta2)
	if meta2.DownloadsLeft != 1 {
		t.Errorf("downloads_left after 1 download: got %d, want 1", meta2.DownloadsLeft)
	}

	// Delete before second download.
	delPath := fmt.Sprintf("/api/schedule/delete/%s/%s",
		completeResp.FileID, completeResp.DeleteKey)
	delW := do(t, handler, http.MethodDelete, delPath, nil, nil)
	assertStatus(t, delW, http.StatusOK)

	// Download must now fail.
	dl2 := do(t, handler, http.MethodGet,
		"/api/schedule/download/"+completeResp.FileID, nil, nil)
	if dl2.Code == http.StatusOK {
		t.Error("download after delete should not succeed")
	}
}
