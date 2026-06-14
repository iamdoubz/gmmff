// Command gmmff is the signaling server for the gmmff peer-to-peer file
// transfer system.
//
// Usage:
//
//	gmmff serve [flags]
//
// All configuration is via flags or environment variables (GMMFF_ prefix).
// See gmmff serve --help for the full flag list.
package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/iamdoubz/gmmff/v2/internal/broker"
	applog "github.com/iamdoubz/gmmff/v2/internal/log"
	"github.com/iamdoubz/gmmff/v2/internal/peer"
	"github.com/iamdoubz/gmmff/v2/internal/schedule"
	"github.com/iamdoubz/gmmff/v2/internal/store"
	"github.com/iamdoubz/gmmff/v2/internal/turn"
	"github.com/redis/go-redis/v9"
	"github.com/rs/zerolog"
	"github.com/spf13/cobra"
)

// build-time variables injected by goreleaser / ldflags.
var (
	version = "dev"
	commit  = "unknown"
	date    = "unknown"
)

func main() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Root command
// ─────────────────────────────────────────────────────────────────────────────

var rootCmd = &cobra.Command{
	Use:   "gmmff",
	Short: "gmmff — pronounced 'gimph' — peer-to-peer file transfer signaling server",
	Long: `gmmff (gimph) is the signaling server component of the gmmff
peer-to-peer file transfer system.

The server never sees file contents.  It acts as a dumb rendezvous relay:
  1. Peer A connects and receives a one-time code.
  2. Peer B connects with that code — the server links them.
  3. PAKE, SDP, and ICE frames are forwarded opaquely.
  4. The WebRTC data channel takes over; the server's job is done.

All logs are privacy-safe: no file names, sizes, IPs, or user data.`,
}

func init() {
	rootCmd.AddCommand(serveCmd)
	rootCmd.AddCommand(versionCmd)
}

// ─────────────────────────────────────────────────────────────────────────────
// Serve command
// ─────────────────────────────────────────────────────────────────────────────

type serveConfig struct {
	addr          string
	redisURL      string
	memoryStore   bool
	logLevel      string
	logPretty     bool
	slotTTL       time.Duration
	webDir        string
	cspReportOnly bool
	// TLS (optional — use a reverse proxy like Caddy/nginx in production)
	tlsCert string
	tlsKey  string
}

var serveCfg serveConfig

var serveCmd = &cobra.Command{
	Use:   "serve",
	Short: "Start the gmmff signaling server",
	RunE:  runServe,
}

func init() {
	f := serveCmd.Flags()

	f.StringVar(&serveCfg.addr, "addr", envOr("GMMFF_ADDR", ":8080"),
		"TCP address to listen on (GMMFF_ADDR)")

	f.StringVar(&serveCfg.redisURL, "redis-url", envOr("GMMFF_REDIS_URL", "redis://localhost:6379/0"),
		"Redis connection URL (GMMFF_REDIS_URL)")

	f.BoolVar(&serveCfg.memoryStore, "memory", false,
		"Use in-memory store instead of Redis (development only — NOT for production)")

	f.StringVar(&serveCfg.logLevel, "log-level", envOr("GMMFF_LOG_LEVEL", "info"),
		"Log level: trace|debug|info|warn|error (GMMFF_LOG_LEVEL)")

	f.BoolVar(&serveCfg.logPretty, "log-pretty", false,
		"Human-readable log output (disable in production)")

	f.DurationVar(&serveCfg.slotTTL, "slot-ttl", 10*time.Minute,
		"How long a waiting slot is kept alive before expiry")

	f.StringVar(&serveCfg.tlsCert, "tls-cert", envOr("GMMFF_TLS_CERT", ""),
		"Path to TLS certificate (optional; prefer terminating TLS at the proxy)")

	f.StringVar(&serveCfg.tlsKey, "tls-key", envOr("GMMFF_TLS_KEY", ""),
		"Path to TLS private key (optional)")

	f.StringVar(&serveCfg.webDir, "web", envOr("GMMFF_WEB_DIR", ""),
		"Path to web/static directory to serve the browser UI (GMMFF_WEB_DIR); omit to show plain landing page")

	f.BoolVar(&serveCfg.cspReportOnly, "csp-report-only", false,
		"Use Content-Security-Policy-Report-Only instead of enforcing CSP — NOT for production")
}

func runServe(_ *cobra.Command, _ []string) error {
	applog.Init(serveCfg.logPretty, serveCfg.logLevel)
	l := applog.Component("main")

	for _, w := range broker.ValidateEnv() {
		l().Warn().
			Str("env_var", w.Key).
			Str("value", w.Value).
			Str("reason", w.Message).
			Msg("invalid environment variable — using default")
	}
	l().Info().
		Str("version", version).
		Str("commit", commit).
		Str("built_at", date).
		Msg("gmmff signaling server starting")

	st, err := setupStore(l)
	if err != nil {
		return err
	}

	b := broker.New(st)
	uiCfg := broker.UIConfigFromEnv()

	schedCfg, srv, err := setupSchedule(st, b, uiCfg, l)
	if err != nil {
		return err
	}

	setupICEPush(srv, uiCfg, l)

	httpServer := &http.Server{
		Addr:         serveCfg.addr,
		Handler:      srv.Handler(),
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 0,
		IdleTimeout:  120 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	go b.Run(ctx)

	if schedCfg.Enabled && schedCfg.CleanupInterval != "" {
		startScheduleCleanup(ctx, &schedCfg, l)
	}

	if err := startHTTPServer(ctx, httpServer, l); err != nil {
		return err
	}

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer shutdownCancel()
	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		l().Error().Err(err).Msg("graceful shutdown failed")
	}
	l().Info().Msg("server stopped cleanly")
	return nil
}

// loggerFn is the type returned by applog.Component.
type loggerFn = func() *zerolog.Logger

func setupStore(l loggerFn) (store.SlotStore, error) {
	if serveCfg.memoryStore {
		l().Warn().Msg("using in-memory store — data will be lost on restart; NOT for production")
		return store.NewMemStore(), nil
	}
	opts, err := redis.ParseURL(normalizeCacheURL(serveCfg.redisURL))
	if err != nil {
		return nil, fmt.Errorf("invalid --redis-url: %w", err)
	}
	rdb := redis.NewClient(opts)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := rdb.Ping(ctx).Err(); err != nil {
		return nil, fmt.Errorf("cannot reach Redis/Valkey at %q: %w\n"+
			"  Tip: start a server with `redis-server` or `valkey-server`, or set --memory for dev mode",
			serveCfg.redisURL, err)
	}
	l().Info().Str("redis_url", redactURL(serveCfg.redisURL)).Msg("Redis/Valkey connected")
	return store.New(rdb, serveCfg.slotTTL), nil
}

func setupSchedule(st store.SlotStore, b *broker.Broker, uiCfg broker.UIConfig, l loggerFn) (schedule.Config, *broker.Server, error) {
	uiCfgCopy := uiCfg
	schedCfg, err := schedule.ConfigFromEnv()
	if err != nil {
		return schedCfg, nil, fmt.Errorf("schedule config: %w", err)
	}
	uiCfgCopy.ShowSchedule = schedCfg.Enabled

	if serveCfg.cspReportOnly {
		l().Warn().Msg("⚠  CSP report-only mode enabled — Content-Security-Policy is NOT enforced; do NOT use in production")
	}
	srv := broker.NewServer(b, st, serveCfg.webDir, serveCfg.cspReportOnly, uiCfgCopy)

	if schedCfg.Enabled {
		sh, err := schedule.NewHandler(&schedCfg)
		if err != nil {
			return schedCfg, nil, fmt.Errorf("schedule handler: %w", err)
		}
		srv.SetScheduleHandler(sh)
		l().Info().Str("dir", schedCfg.Dir).Msg("schedule feature enabled")
	}
	return schedCfg, srv, nil
}

func setupICEPush(srv *broker.Server, uiCfg broker.UIConfig, l loggerFn) {
	if !uiCfg.PushSTUN && !uiCfg.PushTURN {
		return
	}
	var pushedSTUN []string
	var rawTURN []string

	if uiCfg.PushSTUN {
		pushedSTUN = stunServersDefault()
		l().Info().Strs("servers", pushedSTUN).Msg("STUN push enabled")
	}
	if uiCfg.PushTURN {
		rawTURN = turnServersDefault()
		if _, err := turn.ParseAllWithTTL(rawTURN, uiCfg.PushTURNTTL); err != nil {
			l().Warn().Err(err).Msg("TURN push: failed to parse TURN servers — push disabled")
			rawTURN = nil
		} else if len(rawTURN) > 0 {
			l().Warn().
				Dur("credential_ttl", uiCfg.PushTURNTTL).
				Msg("⚠  TURN push enabled — TURN credentials will be sent to all peers (per-session, slot-gated)")
		}
	}
	srv.SetICEConfig(pushedSTUN, rawTURN, uiCfg.PushTURNTTL)
}

func startScheduleCleanup(ctx context.Context, schedCfg *schedule.Config, l loggerFn) {
	cleanStore, _ := schedule.NewStore(schedCfg)
	if err := schedule.StartCleanup(ctx, cleanStore, schedCfg.CleanupInterval); err != nil {
		l().Warn().Err(err).Msg("schedule cleanup: invalid cron expression, background cleanup disabled")
	} else {
		l().Info().Str("interval", schedCfg.CleanupInterval).Msg("schedule cleanup goroutine started")
	}
}

func startHTTPServer(ctx context.Context, httpServer *http.Server, l loggerFn) error {
	errCh := make(chan error, 1)
	go func() {
		l().Info().Str("addr", httpServer.Addr).Msg("listening")
		var err error
		if serveCfg.tlsCert != "" && serveCfg.tlsKey != "" {
			l().Info().Msg("TLS enabled")
			err = httpServer.ListenAndServeTLS(serveCfg.tlsCert, serveCfg.tlsKey)
		} else {
			err = httpServer.ListenAndServe()
		}
		if !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()
	select {
	case <-ctx.Done():
		l().Info().Msg("shutdown signal received")
		return nil
	case err := <-errCh:
		return fmt.Errorf("http server: %w", err)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Version command
// ─────────────────────────────────────────────────────────────────────────────

var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "Print version information",
	Run: func(_ *cobra.Command, _ []string) {
		fmt.Printf("gmmff %s (commit %s, built %s)\n", version, commit, date)
	},
}

// ─────────────────────────────────────────────────────────────────────────────
// Helpers
// ─────────────────────────────────────────────────────────────────────────────

// stunServersDefault returns the default STUN server list.
// GMMFF_STUN may be a comma-separated list of stun:/stuns: URLs.
// If unset, returns []string{peer.DefaultSTUN}.
func stunServersDefault() []string {
	v := os.Getenv("GMMFF_STUN")
	if v == "" {
		return []string{peer.DefaultSTUN}
	}
	parts := strings.Split(v, ",")
	result := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			result = append(result, p)
		}
	}
	if len(result) == 0 {
		return []string{peer.DefaultSTUN}
	}
	return result
}

// envOr returns the environment variable value or the fallback.
func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// turnServersDefault parses GMMFF_TURN env var (comma-separated) into raw strings.
// Returns nil when the env var is unset — callers treat nil as "no TURN".
func turnServersDefault() []string {
	v := os.Getenv("GMMFF_TURN")
	if v == "" {
		return nil
	}
	parts := strings.Split(v, ",")
	result := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			result = append(result, p)
		}
	}
	return result
}

// parseTURNServers validates and parses raw TURN strings into turn.Server entries.
func parseTURNServers(raw []string) ([]turn.Server, error) {
	return turn.ParseAll(raw)
}

// normalizeCacheURL accepts Valkey-style URL schemes and rewrites them to the
// Redis schemes that redis.ParseURL understands. Valkey is wire-compatible with
// Redis, so go-redis talks to either server unchanged; this only lets operators
// write a natural valkey:// (or valkeys:// for TLS) URL in GMMFF_REDIS_URL.
func normalizeCacheURL(raw string) string {
	switch {
	case strings.HasPrefix(raw, "valkeys://"):
		return "rediss://" + strings.TrimPrefix(raw, "valkeys://")
	case strings.HasPrefix(raw, "valkey://"):
		return "redis://" + strings.TrimPrefix(raw, "valkey://")
	default:
		return raw
	}
}

// redactURL strips credentials from a Redis/Valkey URL before logging.
// Handles both tcp (redis://) and unix socket (unix://) URLs.
func redactURL(raw string) string {
	opts, err := redis.ParseURL(normalizeCacheURL(raw))
	if err != nil {
		return "<invalid url>"
	}
	// Unix socket connections have no Addr — use the Network path instead.
	if opts.Network == "unix" {
		return fmt.Sprintf("unix://%s", opts.Addr)
	}
	return fmt.Sprintf("redis://%s/%d", opts.Addr, opts.DB)
}
