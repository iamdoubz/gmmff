package broker

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/iamdoubz/gmmff/v2/internal/slot"
	"github.com/iamdoubz/gmmff/v2/internal/store"
)

// newServerSuite creates a Server backed by a MemStore with the broker hub
// running, ready for httptest requests against all HTTP routes.
func newServerSuite(t *testing.T) (*Server, *store.MemStore) {
	t.Helper()
	mem := store.NewMemStore()
	b := New(mem)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go b.Run(ctx)

	srv := NewServer(b, mem, "", false, DefaultUIConfig())
	return srv, mem
}

func doRequest(t *testing.T, handler http.Handler, method, path string, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, nil)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	return rr
}

// ─────────────────────────────────────────────────────────────────────────────
// /healthz
// ─────────────────────────────────────────────────────────────────────────────

func TestHealthz_Returns200(t *testing.T) {
	srv, _ := newServerSuite(t)
	rr := doRequest(t, srv.Handler(), "GET", "/healthz", nil)
	if rr.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rr.Code)
	}
	body := strings.TrimSpace(rr.Body.String())
	if body != "ok" {
		t.Errorf("body = %q, want %q", body, "ok")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// /readyz
// ─────────────────────────────────────────────────────────────────────────────

func TestReadyz_Returns200_WhenStoreHealthy(t *testing.T) {
	srv, _ := newServerSuite(t)
	rr := doRequest(t, srv.Handler(), "GET", "/readyz", nil)
	if rr.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rr.Code)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// /metrics
// ─────────────────────────────────────────────────────────────────────────────

func TestMetrics_ReturnsJSON(t *testing.T) {
	srv, _ := newServerSuite(t)
	rr := doRequest(t, srv.Handler(), "GET", "/metrics", nil)
	if rr.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rr.Code)
	}
	ct := rr.Header().Get("Content-Type")
	if ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
	var m map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &m); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	for _, key := range []string{"uptime_seconds", "connections_total", "goroutines", "heap_alloc_bytes"} {
		if _, ok := m[key]; !ok {
			t.Errorf("missing key %q in metrics", key)
		}
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// /config.json
// ─────────────────────────────────────────────────────────────────────────────

func TestConfigJSON_ReturnsUIConfig(t *testing.T) {
	srv, _ := newServerSuite(t)
	rr := doRequest(t, srv.Handler(), "GET", "/config.json", nil)
	if rr.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rr.Code)
	}
	if cc := rr.Header().Get("Cache-Control"); cc != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", cc)
	}
	var cfg UIConfig
	if err := json.Unmarshal(rr.Body.Bytes(), &cfg); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if !cfg.ShowFiles {
		t.Error("ShowFiles should default to true")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Landing page (no webDir)
// ─────────────────────────────────────────────────────────────────────────────

func TestIndex_ReturnsHTML(t *testing.T) {
	srv, _ := newServerSuite(t)
	rr := doRequest(t, srv.Handler(), "GET", "/", nil)
	if rr.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rr.Code)
	}
	ct := rr.Header().Get("Content-Type")
	if !strings.Contains(ct, "text/html") {
		t.Errorf("Content-Type = %q, want text/html", ct)
	}
	if !strings.Contains(rr.Body.String(), "gmmff") {
		t.Error("landing page should mention gmmff")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Security headers
// ─────────────────────────────────────────────────────────────────────────────

func TestSecurityHeaders_Present(t *testing.T) {
	srv, _ := newServerSuite(t)
	rr := doRequest(t, srv.Handler(), "GET", "/healthz", nil)
	h := rr.Header()
	checks := map[string]string{
		"X-Content-Type-Options":       "nosniff",
		"X-Frame-Options":              "DENY",
		"Referrer-Policy":              "no-referrer",
		"Cross-Origin-Opener-Policy":   "same-origin",
		"Cross-Origin-Embedder-Policy": "require-corp",
	}
	for header, want := range checks {
		got := h.Get(header)
		if got != want {
			t.Errorf("%s = %q, want %q", header, got, want)
		}
	}
}

func TestCSP_EnforcingByDefault(t *testing.T) {
	srv, _ := newServerSuite(t)
	rr := doRequest(t, srv.Handler(), "GET", "/healthz", nil)
	csp := rr.Header().Get("Content-Security-Policy")
	if csp == "" {
		t.Error("CSP header missing")
	}
	if rr.Header().Get("Content-Security-Policy-Report-Only") != "" {
		t.Error("CSP-Report-Only should not be set when cspReportOnly=false")
	}
}

func TestCSP_ReportOnlyWhenSet(t *testing.T) {
	mem := store.NewMemStore()
	b := New(mem)
	srv := NewServer(b, mem, "", true, DefaultUIConfig())
	rr := doRequest(t, srv.Handler(), "GET", "/healthz", nil)
	if rr.Header().Get("Content-Security-Policy-Report-Only") == "" {
		t.Error("CSP-Report-Only header should be set")
	}
}

func TestCSP_LocalMode(t *testing.T) {
	mem := store.NewMemStore()
	b := New(mem)
	srv := NewServerWithFS(b, mem, nil, false)
	rr := doRequest(t, srv.Handler(), "GET", "/healthz", nil)
	csp := rr.Header().Get("Content-Security-Policy-Report-Only")
	if csp == "" {
		t.Fatal("local mode should use CSP-Report-Only")
	}
	if strings.Contains(csp, "cdnjs.cloudflare.com") {
		t.Error("local mode CSP should not reference external CDNs")
	}
	if !strings.Contains(csp, "ws:") {
		t.Error("local mode CSP should allow ws: for WebSocket")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// /api/ice — gating (security-critical)
// ─────────────────────────────────────────────────────────────────────────────

func newICEServer(t *testing.T, pushSTUN, pushTURN bool) (*Server, *store.MemStore) {
	t.Helper()
	mem := store.NewMemStore()
	b := New(mem)
	cfg := DefaultUIConfig()
	cfg.PushSTUN = pushSTUN
	cfg.PushTURN = pushTURN
	srv := NewServer(b, mem, "", false, cfg)
	if pushSTUN {
		srv.SetICEConfig([]string{"stun:stun.example.com:3478"}, nil, 0)
	}
	return srv, mem
}

func seedSlot(t *testing.T, mem *store.MemStore, code string, state slot.State) {
	t.Helper()
	sl := slot.New("test-slot-id", code, "init-conn", "files", 2)
	sl.State = state
	if err := mem.Create(context.Background(), sl); err != nil {
		t.Fatalf("seed slot: %v", err)
	}
}

func TestICE_NoBearerToken_Returns401(t *testing.T) {
	srv, _ := newICEServer(t, true, false)
	rr := doRequest(t, srv.Handler(), "GET", "/api/ice", nil)
	if rr.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rr.Code)
	}
}

func TestICE_InvalidCode_Returns401(t *testing.T) {
	srv, _ := newICEServer(t, true, false)
	rr := doRequest(t, srv.Handler(), "GET", "/api/ice", map[string]string{
		"Authorization": "Bearer bad-code-here",
	})
	if rr.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rr.Code)
	}
}

func TestICE_ClosedSlot_Returns401(t *testing.T) {
	srv, mem := newICEServer(t, true, false)
	seedSlot(t, mem, "bear-cozy-cone", slot.StateClosed)
	rr := doRequest(t, srv.Handler(), "GET", "/api/ice", map[string]string{
		"Authorization": "Bearer bear-cozy-cone",
	})
	if rr.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401 for closed slot", rr.Code)
	}
}

func TestICE_ValidSlot_Returns200_WithSTUN(t *testing.T) {
	srv, mem := newICEServer(t, true, false)
	seedSlot(t, mem, "bear-cozy-cone", slot.StateWaiting)
	rr := doRequest(t, srv.Handler(), "GET", "/api/ice", map[string]string{
		"Authorization": "Bearer bear-cozy-cone",
	})
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	if cc := rr.Header().Get("Cache-Control"); cc != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", cc)
	}
	var resp iceResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if len(resp.STUN) == 0 {
		t.Error("STUN should be populated")
	}
}

func TestICE_ActiveSlot_Returns200(t *testing.T) {
	srv, mem := newICEServer(t, true, false)
	seedSlot(t, mem, "bear-cozy-cone", slot.StateActive)
	rr := doRequest(t, srv.Handler(), "GET", "/api/ice", map[string]string{
		"Authorization": "Bearer bear-cozy-cone",
	})
	if rr.Code != http.StatusOK {
		t.Errorf("status = %d, want 200 for active slot", rr.Code)
	}
}

func TestICE_FullSlot_Returns200(t *testing.T) {
	srv, mem := newICEServer(t, true, false)
	seedSlot(t, mem, "bear-cozy-cone", slot.StateFull)
	rr := doRequest(t, srv.Handler(), "GET", "/api/ice", map[string]string{
		"Authorization": "Bearer bear-cozy-cone",
	})
	if rr.Code != http.StatusOK {
		t.Errorf("status = %d, want 200 for full slot", rr.Code)
	}
}

func TestICE_NoPushConfigured_SkipsAuth(t *testing.T) {
	srv, _ := newICEServer(t, false, false)
	rr := doRequest(t, srv.Handler(), "GET", "/api/ice", nil)
	if rr.Code != http.StatusOK {
		t.Errorf("status = %d, want 200 when push is disabled (no auth needed)", rr.Code)
	}
	var resp iceResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if resp.STUN != nil || resp.TURN != nil {
		t.Errorf("STUN/TURN should be nil when push is disabled, got stun=%v turn=%v", resp.STUN, resp.TURN)
	}
}

func TestICE_WithStaticTURN_Returns200(t *testing.T) {
	mem := store.NewMemStore()
	b := New(mem)
	cfg := DefaultUIConfig()
	cfg.PushSTUN = true
	cfg.PushTURN = true
	srv := NewServer(b, mem, "", false, cfg)
	srv.SetICEConfig(
		[]string{"stun:stun.example.com:3478"},
		[]string{"turn:turn.example.com:3478?user=testuser&pass=testpass"},
		30*time.Minute,
	)

	seedSlot(t, mem, "bear-cozy-cone", slot.StateWaiting)
	rr := doRequest(t, srv.Handler(), "GET", "/api/ice", map[string]string{
		"Authorization": "Bearer bear-cozy-cone",
	})
	if rr.Code != http.StatusOK {
		body, _ := io.ReadAll(rr.Body)
		t.Fatalf("status = %d, want 200; body: %s", rr.Code, body)
	}
	var resp iceResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if len(resp.STUN) == 0 {
		t.Error("STUN should be populated")
	}
	if len(resp.TURN) == 0 {
		t.Error("TURN should be populated")
	}
	if resp.TURN[0].Username == "" {
		t.Error("TURN username should not be empty")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// bearerToken helper
// ─────────────────────────────────────────────────────────────────────────────

func TestBearerToken(t *testing.T) {
	tests := []struct {
		header string
		want   string
	}{
		{"Bearer abc-123", "abc-123"},
		{"Bearer bear-cozy-cone", "bear-cozy-cone"},
		{"bearer abc", ""},
		{"Basic dXNlcjpwYXNz", ""},
		{"", ""},
		{"Bearer ", ""},
	}
	for _, tt := range tests {
		req := httptest.NewRequest("GET", "/", nil)
		if tt.header != "" {
			req.Header.Set("Authorization", tt.header)
		}
		got := bearerToken(req)
		if got != tt.want {
			t.Errorf("bearerToken(%q) = %q, want %q", tt.header, got, tt.want)
		}
	}
}
