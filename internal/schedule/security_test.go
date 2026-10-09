package schedule

import (
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// Spoofed forwarding headers from a peer that is not a trusted proxy must be
// ignored — otherwise anyone reaching the backend directly can claim an
// allowlisted IP and skip the upload password.
func TestClientIP_SpoofedHeaderFromUntrustedPeerIgnored(t *testing.T) {
	_, handler := newTestHandler(t, func(cfg *Config) {
		cfg.UploadIPs, _ = parseCIDRList("10.0.0.1")
		cfg.UploadPassword = "secret"
		cfg.TrustedProxies, _ = parseCIDRList("127.0.0.0/8")
	})
	req := httptest.NewRequest(http.MethodPost, "/api/schedule/auth", nil)
	req.RemoteAddr = "203.0.113.9:4444" // direct internet client
	req.Header.Set("X-Real-IP", "10.0.0.1")
	req.Header.Set("X-Forwarded-For", "10.0.0.1")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	var resp authResponse
	decodeJSON(t, w, &resp)
	if resp.Allowed || !resp.NeedsPassword {
		t.Fatalf("spoofed header bypassed allowlist: %+v", resp)
	}
}

func TestClientIP_RightmostForwardedHopFromTrustedProxy(t *testing.T) {
	cfg := &Config{}
	cfg.TrustedProxies, _ = parseCIDRList("127.0.0.0/8")
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "127.0.0.1:1"
	req.Header.Set("X-Forwarded-For", "10.0.0.1, 198.51.100.7") // left part is client-controlled
	if got := cfg.ClientIP(req).String(); got != "198.51.100.7" {
		t.Errorf("ClientIP = %s, want 198.51.100.7", got)
	}
}

func TestParseTrustedProxies(t *testing.T) {
	def, _ := parseTrustedProxies("")
	if len(def) == 0 {
		t.Error("unset should yield default private ranges")
	}
	if none, _ := parseTrustedProxies("none"); none != nil {
		t.Error("\"none\" should trust no proxy")
	}
	all, _ := parseTrustedProxies("0.0.0.0")
	if !containsIP(all, net.ParseIP("203.0.113.9")) || !containsIP(all, net.ParseIP("2001:db8::1")) {
		t.Error("0.0.0.0 should trust every peer (v4 and v6)")
	}
	if _, err := parseTrustedProxies("not-an-ip"); err == nil {
		t.Error("invalid CIDR should error")
	}
}

func TestStore_RejectsTraversalIDs(t *testing.T) {
	h, handler := newTestHandler(t, nil)
	fm := doFullUpload(t, h.store, 1, 256)

	for _, id := range []string{"../complete/" + fm.FileID, "..%2fcomplete%2f" + fm.FileID, fm.FileID[:31], fm.FileID + "0"} {
		w := do(t, handler, http.MethodPost,
			"/api/schedule/upload/chunk?upload_id="+id+"&chunk_index=0", nil, nil)
		assertStatus(t, w, http.StatusBadRequest)
	}
	if _, err := h.store.ReadFileMeta("../pending/" + fm.FileID); err == nil {
		t.Error("ReadFileMeta accepted a traversal ID")
	}
	if err := h.store.Delete("../complete/"+fm.FileID, fm.DeleteKey); err == nil {
		t.Error("Delete accepted a traversal ID")
	}
}

func TestUploadInit_ClampsTTLAndNegativeDownloads(t *testing.T) {
	h, handler := newTestHandler(t, nil) // MaxDownloads: 3, default TTL options (max 30 days)
	w := do(t, handler, http.MethodPost, "/api/schedule/upload/init",
		jsonBody(t, map[string]any{
			"total_size":    256,
			"chunks_total":  1,
			"ttl_seconds":   int64(1) << 62, // would overflow time.Duration
			"max_downloads": -1,
		}), nil)
	assertStatus(t, w, http.StatusOK)

	var resp uploadInitResponse
	decodeJSON(t, w, &resp)
	if limit := time.Now().Add(30*24*time.Hour + time.Minute); resp.ExpiresAt.After(limit) || resp.ExpiresAt.Before(time.Now()) {
		t.Errorf("ExpiresAt %v not clamped to max TTL", resp.ExpiresAt)
	}
	meta, err := h.store.ReadPendingMeta(resp.UploadID)
	if err != nil {
		t.Fatal(err)
	}
	if meta.MaxDownloads != 3 {
		t.Errorf("MaxDownloads = %d, want server cap 3", meta.MaxDownloads)
	}
}
