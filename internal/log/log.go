// Package log provides the gmmff privacy-safe structured logger.
//
// Design contract
//   - Log lines NEVER contain: file names, file sizes, IP addresses, user
//     agents, slot codes, or any data that could identify a transfer or
//     a user.
//   - Every event carries only: timestamp, level, component, error code
//     (if applicable), and a session/slot UUID (opaque to outsiders).
//   - This makes logs safe to ship to a shared ops dashboard without a
//     data-processing agreement.
package log

import (
	"io"
	"os"
	"time"

	"github.com/rs/zerolog"
)

// Logger is the package-level logger.  Initialise once at startup via Init.
var Logger zerolog.Logger

// Init configures the global logger.
//
//   - pretty: if true, output is human-readable (dev mode).
//   - level:  one of "trace", "debug", "info", "warn", "error", "fatal".
//
// Call Init before any other package in main().
func Init(pretty bool, level string) {
	lvl, err := zerolog.ParseLevel(level)
	if err != nil {
		lvl = zerolog.InfoLevel
	}

	zerolog.TimeFieldFormat = time.RFC3339

	var w io.Writer = os.Stdout
	if pretty {
		w = zerolog.ConsoleWriter{Out: os.Stdout, TimeFormat: time.RFC3339}
	}

	Logger = zerolog.New(w).
		Level(lvl).
		With().
		Timestamp().
		Str("app", "gmmff").
		Logger()
}

// Component returns a function that produces a child logger tagged with the
// given component name, reading from the live Logger at call time.
//
// Usage in each package:
//
//	var logger = applog.Component("broker")
//
// Then call logger() to log:
//
//	logger().Info().Msg("hub started")
//
// This ensures the logger always reflects the level and writer configured
// by Init(), regardless of package initialisation order.
func Component(name string) func() zerolog.Logger {
	return func() zerolog.Logger {
		return Logger.With().Str("component", name).Logger()
	}
}
