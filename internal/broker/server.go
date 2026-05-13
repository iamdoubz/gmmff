// Package broker (server.go) wires together the HTTP routes for the
// gmmff signaling server.
//
// Routes:
//   GET  /ws                 — WebSocket upgrade (handled by Broker)
//   GET  /healthz            — liveness probe (returns 200 + "ok")
//   GET  /readyz             — readiness probe (checks Redis reachability)
//   GET  /metrics            — plaintext operational counters (no user data)
//   GET  /*                  — browser UI file server (when --web is set)
//   GET  /                   — plain landing page (when --web is not set)
package broker

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"mime"
	"net/http"
	"runtime"
	"sync/atomic"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/iamdoubz/gmmff/internal/store"
)

// Metrics holds privacy-safe operational counters.
// All fields are updated atomically.  No per-user or per-slot data is stored.
type Metrics struct {
	ConnectionsTotal  atomic.Int64
	ConnectionsActive atomic.Int64
	SlotsCreated      atomic.Int64
	SlotsCompleted    atomic.Int64
	RelayedMessages   atomic.Int64
	Errors            atomic.Int64
}

// global metrics instance (package-level for simplicity; inject if needed).
var metrics Metrics

// Server combines the HTTP server, broker, and store.
type Server struct {
	broker *Broker
	store  store.SlotStore
	router *chi.Mux
	start  time.Time
	webDir        string   // path to web/static on disk; empty = plain landing
	staticFS      fs.FS    // embedded fs.FS; non-nil overrides webDir
	cspReportOnly bool     // use CSP-Report-Only instead of enforcing
	localMode     bool     // offline-safe CSP, no external origins
}

// NewServer constructs a Server and registers all routes.
// webDir is the path to the web/static directory.  When non-empty the
// server mounts a file server at / that serves the browser UI.  When
// empty a plain HTML landing page is shown instead.
func NewServer(b *Broker, st store.SlotStore, webDir string, cspReportOnly bool) *Server {
	// Register the .wasm MIME type in case the OS doesn't have it.
	_ = mime.AddExtensionType(".wasm", "application/wasm")
	s := &Server{
		broker: b,
		store:  st,
		router: chi.NewRouter(),
		start:  time.Now(),
		webDir:        webDir,
		cspReportOnly: cspReportOnly,
	}
	s.routes()
	return s
}

// NewServerWithFS constructs a Server that serves the browser UI from an
// embedded fs.FS instead of a disk directory.  Used by gmmff local.
// Sets localMode=true for an offline-safe CSP and cspReportOnly=true so
// CSP violations are logged but never block the UI in local mode.
func NewServerWithFS(b *Broker, st store.SlotStore, staticFS fs.FS, cspReportOnly bool) *Server {
	_ = mime.AddExtensionType(".wasm", "application/wasm")
	s := &Server{
		broker:        b,
		store:         st,
		router:        chi.NewRouter(),
		start:         time.Now(),
		staticFS:      staticFS,
		cspReportOnly: true, // always report-only in local mode — never block
		localMode:     true,
	}
	s.routes()
	return s
}

// Handler returns the root http.Handler.
func (s *Server) Handler() http.Handler { return s.router }

func (s *Server) routes() {
	r := s.router

	// ── Middleware ────────────────────────────────────────────────────────────
	r.Use(middleware.RealIP)
	r.Use(privacyLogger)        // logs only method + path + status — no IPs
	r.Use(middleware.Recoverer)
	r.Use(s.securityHeaders)

	// ── Routes ───────────────────────────────────────────────────────────────
	r.Get("/ws", s.broker.ServeHTTP)
	r.Get("/healthz", s.handleLiveness)
	r.Get("/readyz", s.handleReadiness)
	r.Get("/metrics", s.handleMetrics)

	if s.staticFS != nil {
		// Serve from embedded fs.FS (local mode).
		r.Get("/*", http.FileServer(http.FS(s.staticFS)).ServeHTTP)
	} else if s.webDir != "" {
		// Serve the browser UI from the given directory.
		fs := http.FileServer(http.Dir(s.webDir))
		r.Get("/*", fs.ServeHTTP)
	} else {
		r.Get("/", s.handleIndex)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Handlers
// ─────────────────────────────────────────────────────────────────────────────

func (s *Server) handleLiveness(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain")
	w.WriteHeader(http.StatusOK)
	_, _ = fmt.Fprintln(w, "ok")
}

func (s *Server) handleReadiness(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()

	if err := s.store.Ping(ctx); err != nil {
		logger().Error().Str("error_code", "ERR_REDIS_UNAVAILABLE").Msg("readiness check failed")
		http.Error(w, "store unavailable", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "text/plain")
	w.WriteHeader(http.StatusOK)
	_, _ = fmt.Fprintln(w, "ok")
}

func (s *Server) handleMetrics(w http.ResponseWriter, _ *http.Request) {
	// Privacy-safe metrics: aggregate counts only, no per-slot detail.
	var m runtime.MemStats
	runtime.ReadMemStats(&m)

	payload := map[string]any{
		"uptime_seconds":      time.Since(s.start).Seconds(),
		"connections_total":   metrics.ConnectionsTotal.Load(),
		"connections_active":  metrics.ConnectionsActive.Load(),
		"slots_created":       metrics.SlotsCreated.Load(),
		"slots_completed":     metrics.SlotsCompleted.Load(),
		"relayed_messages":    metrics.RelayedMessages.Load(),
		"errors":              metrics.Errors.Load(),
		"goroutines":          runtime.NumGoroutine(),
		"heap_alloc_bytes":    m.HeapAlloc,
		"heap_sys_bytes":      m.HeapSys,
	}

	w.Header().Set("Content-Type", "application/json")
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(payload)
}

func (s *Server) handleIndex(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = fmt.Fprint(w, indexHTML)
}

// ─────────────────────────────────────────────────────────────────────────────
// Middleware
// ─────────────────────────────────────────────────────────────────────────────

// privacyLogger logs method + path + status.  Deliberately omits: remote IP,
// User-Agent, referer, query strings, and request duration (which could be
// used for timing attacks on slot enumeration).
func privacyLogger(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
		next.ServeHTTP(ww, r)
		logger().Info().
			Str("method", r.Method).
			Str("path", r.URL.Path).
			Int("status", ww.Status()).
			Msg("request")
	})
}

// securityHeaders returns middleware that sets conservative HTTP security headers.
// When localMode is true a relaxed CSP is applied that does not reference any
// external origins — safe for offline / local-network use.
func (s *Server) securityHeaders(next http.Handler) http.Handler {
	cspHeader := "Content-Security-Policy"
	if s.cspReportOnly {
		cspHeader = "Content-Security-Policy-Report-Only"
	}

	var cspValue string
	if s.localMode {
		// Local mode: no external origins allowed or needed.
		// Allow ws: and wss: for WebSocket (we don't know the scheme at
		// middleware construction time so allow both).
		cspValue = "default-src 'none'; " +
			"connect-src 'self' ws: wss:; " +
			"script-src 'self' 'wasm-unsafe-eval' 'unsafe-eval'; " +
			"style-src 'self' 'unsafe-inline'; " +
			"font-src 'self'; " +
			"img-src 'self' data:"
	} else {
		cspValue = "default-src 'none'; " +
			"connect-src 'self' wss: https:; " +
			"script-src 'self' 'wasm-unsafe-eval' 'unsafe-eval' https://cdnjs.cloudflare.com; " +
			"style-src 'self' https://fonts.googleapis.com; " +
			"font-src https://fonts.gstatic.com; " +
			"img-src 'self' data:"
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set(cspHeader, cspValue)
		// Required for SharedArrayBuffer (used by Go Wasm runtime).
		h.Set("Cross-Origin-Opener-Policy", "same-origin")
		h.Set("Cross-Origin-Embedder-Policy", "require-corp")
		next.ServeHTTP(w, r)
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// Landing page
// ─────────────────────────────────────────────────────────────────────────────

const indexHTML = `<!DOCTYPE html>
<html lang="en">
<head>
  <meta charset="UTF-8">
  <meta name="viewport" content="width=device-width, initial-scale=1.0">
  <title>gmmff signaling server</title>
  <style>
    body { font-family: system-ui, sans-serif; max-width: 640px; margin: 4rem auto; padding: 0 1rem; color: #1a1a1a; }
    h1   { font-size: 1.6rem; font-weight: 600; }
    code { background: #f4f4f4; padding: 2px 6px; border-radius: 4px; font-size: 0.9em; }
    a    { color: #0066cc; }
    .status { display: inline-block; width: 10px; height: 10px; border-radius: 50%; background: #22c55e; margin-right: 6px; }
  </style>
</head>
<body>
  <h1>gmmff</h1>
  <p><span class="status"></span>Signaling server is running.</p>
  <p>
    Connect a client to <code>wss://&lt;host&gt;/ws</code> to begin a file transfer.
    See <a href="https://github.com/iamdoubz/gmmff">github.com/iamdoubz/gmmff</a> for documentation.
  </p>
  <ul>
    <li><a href="/healthz">Liveness</a></li>
    <li><a href="/readyz">Readiness</a></li>
    <li><a href="/metrics">Metrics</a></li>
  </ul>
</body>
</html>`
