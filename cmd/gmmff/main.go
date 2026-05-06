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
	"syscall"
	"time"

	"github.com/iamdoubz/gmmff/internal/broker"
	applog "github.com/iamdoubz/gmmff/internal/log"
	"github.com/iamdoubz/gmmff/internal/store"
	"github.com/redis/go-redis/v9"
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
	addr        string
	redisURL    string
	memoryStore bool
	logLevel    string
	logPretty   bool
	slotTTL     time.Duration
	webDir      string
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
}

func runServe(_ *cobra.Command, _ []string) error {
	// ── Logging ──────────────────────────────────────────────────────────────
	applog.Init(serveCfg.logPretty, serveCfg.logLevel)
	l := applog.Component("main")
	l().Info().
		Str("version", version).
		Str("commit", commit).
		Str("built_at", date).
		Msg("gmmff signaling server starting")

	// ── Store ─────────────────────────────────────────────────────────────────
	var st store.SlotStore
	if serveCfg.memoryStore {
		l().Warn().Msg("using in-memory store — data will be lost on restart; NOT for production")
		st = store.NewMemStore()
	} else {
		opts, err := redis.ParseURL(serveCfg.redisURL)
		if err != nil {
			return fmt.Errorf("invalid --redis-url: %w", err)
		}
		rdb := redis.NewClient(opts)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := rdb.Ping(ctx).Err(); err != nil {
			return fmt.Errorf("cannot reach Redis at %q: %w\n"+
				"  Tip: start Redis with `redis-server` or set --memory for dev mode",
				serveCfg.redisURL, err)
		}
		l().Info().Str("redis_url", redactURL(serveCfg.redisURL)).Msg("Redis connected")
		st = store.New(rdb, serveCfg.slotTTL)
	}

	// ── Broker ────────────────────────────────────────────────────────────────
	b := broker.New(st)

	// ── HTTP server ───────────────────────────────────────────────────────────
	srv := broker.NewServer(b, st, serveCfg.webDir)
	httpServer := &http.Server{
		Addr:         serveCfg.addr,
		Handler:      srv.Handler(),
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 15 * time.Second,
		IdleTimeout:  120 * time.Second,
	}

	// ── Lifecycle ─────────────────────────────────────────────────────────────
	ctx, stop := signal.NotifyContext(context.Background(),
		os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Run hub in background.
	go b.Run(ctx)

	// Start HTTP listener.
	errCh := make(chan error, 1)
	go func() {
		l().Info().Str("addr", serveCfg.addr).Msg("listening")
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

	// Wait for signal or listener error.
	select {
	case <-ctx.Done():
		l().Info().Msg("shutdown signal received")
	case err := <-errCh:
		return fmt.Errorf("http server: %w", err)
	}

	// Graceful shutdown: give in-flight requests 15 seconds to finish.
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer shutdownCancel()
	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		l().Error().Err(err).Msg("graceful shutdown failed")
	}
	l().Info().Msg("server stopped cleanly")
	return nil
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

// envOr returns the environment variable value or the fallback.
func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// redactURL strips credentials from a Redis URL before logging.
// Handles both tcp (redis://) and unix socket (unix://) URLs.
func redactURL(raw string) string {
	opts, err := redis.ParseURL(raw)
	if err != nil {
		return "<invalid url>"
	}
	// Unix socket connections have no Addr — use the Network path instead.
	if opts.Network == "unix" {
		return fmt.Sprintf("unix://%s", opts.Addr)
	}
	return fmt.Sprintf("redis://%s/%d", opts.Addr, opts.DB)
}
