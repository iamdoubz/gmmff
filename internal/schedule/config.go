package schedule

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Config holds all schedule feature configuration.
type Config struct {
	// Enabled mirrors GMMFF_SHOW_SCHEDULE. When false the entire feature is off.
	Enabled bool

	// Dir is the root storage directory. Subdirs pending/ and complete/ are
	// created automatically. Default: ./data/schedule
	Dir string

	// PendingDir and CompleteDir are derived from Dir.
	PendingDir  string
	CompleteDir string

	// MaxSize is the maximum upload size in bytes. Default: 1 GiB.
	MaxSize int64

	// MaxDownloads is the server-wide cap on per-file download limits.
	// 0 = unlimited. Default: 1.
	MaxDownloads int

	// UploadIPs is the list of allowed upload CIDRs. Empty = no IP restriction.
	UploadIPs []*net.IPNet

	// DownloadIPs is the list of allowed download CIDRs. Empty = allow all.
	DownloadIPs []*net.IPNet

	// UploadPassword is the required upload password. Empty = no password.
	UploadPassword string

	// CleanupInterval is a crontab-format string, e.g. "*/5 * * * *".
	// Empty string disables the background cleanup goroutine.
	CleanupInterval string

	// TTLOptions is the ordered list of valid TTL choices presented in the UI.
	TTLOptions []TTLOption
}

// TTLOption is a single TTL entry shown in the dropdown.
type TTLOption struct {
	Label    string        // e.g. "1 hour"
	Duration time.Duration // actual duration
}

// DefaultTTLOptions returns the built-in TTL choices used when
// GMMFF_TTL_SETTINGS is not set.
func DefaultTTLOptions() []TTLOption {
	return []TTLOption{
		{"1 hour", 1 * time.Hour},
		{"8 hours", 8 * time.Hour},
		{"1 day", 24 * time.Hour},
		{"3 days", 72 * time.Hour},
		{"7 days", 7 * 24 * time.Hour},
		{"30 days", 30 * 24 * time.Hour},
	}
}

// ConfigFromEnv reads all GMMFF_SCHEDULE_* environment variables.
func ConfigFromEnv() (Config, error) {
	cfg := Config{
		Enabled:         parseBoolEnv("GMMFF_SHOW_SCHEDULE", false),
		Dir:             envOr("GMMFF_SCHEDULE_DIR", filepath.Join(".", "data", "schedule")),
		MaxSize:         parseByteSize(os.Getenv("GMMFF_SCHEDULE_MAX_SIZE"), 1<<30), // 1 GiB
		MaxDownloads:    parseIntEnv("GMMFF_SCHEDULE_MAX_DOWNLOADS", 1),
		UploadPassword:  os.Getenv("GMMFF_SCHEDULE_PASSWORD"),
		CleanupInterval: os.Getenv("GMMFF_SCHEDULE_CLEANUP_INTERVAL"),
	}

	cfg.PendingDir  = filepath.Join(cfg.Dir, "pending")
	cfg.CompleteDir = filepath.Join(cfg.Dir, "complete")

	// Parse upload IP allowlist.
	if raw := os.Getenv("GMMFF_SCHEDULE_UPLOAD_IP"); raw != "" {
		nets, err := parseCIDRList(raw)
		if err != nil {
			return cfg, fmt.Errorf("schedule: GMMFF_SCHEDULE_UPLOAD_IP: %w", err)
		}
		cfg.UploadIPs = nets
	}

	// Parse download IP allowlist.
	if raw := os.Getenv("GMMFF_SCHEDULE_DOWNLOAD_IP"); raw != "" && raw != "0.0.0.0" {
		nets, err := parseCIDRList(raw)
		if err != nil {
			return cfg, fmt.Errorf("schedule: GMMFF_SCHEDULE_DOWNLOAD_IP: %w", err)
		}
		cfg.DownloadIPs = nets
	}

	// Parse TTL options.
	if raw := os.Getenv("GMMFF_TTL_SETTINGS"); raw != "" {
		opts, err := parseTTLSettings(raw)
		if err != nil {
			return cfg, fmt.Errorf("schedule: GMMFF_TTL_SETTINGS: %w", err)
		}
		cfg.TTLOptions = opts
	} else {
		cfg.TTLOptions = DefaultTTLOptions()
	}

	return cfg, nil
}

// EnsureDirs creates the pending and complete directories if they don't exist.
func (c *Config) EnsureDirs() error {
	for _, d := range []string{c.PendingDir, c.CompleteDir} {
		if err := os.MkdirAll(d, 0o750); err != nil {
			return fmt.Errorf("schedule: create dir %s: %w", d, err)
		}
	}
	return nil
}

// IPAllowedToUpload reports whether ip is allowed to upload.
// If UploadIPs is empty, all IPs are allowed (subject to password).
func (c *Config) IPAllowedToUpload(ip net.IP) bool {
	if len(c.UploadIPs) == 0 {
		return true
	}
	for _, cidr := range c.UploadIPs {
		if cidr.Contains(ip) {
			return true
		}
	}
	return false
}

// IPAllowedToDownload reports whether ip is allowed to download.
// If DownloadIPs is empty, all IPs are allowed.
func (c *Config) IPAllowedToDownload(ip net.IP) bool {
	if len(c.DownloadIPs) == 0 {
		return true
	}
	for _, cidr := range c.DownloadIPs {
		if cidr.Contains(ip) {
			return true
		}
	}
	return false
}

// ─────────────────────────────────────────────────────────────────────────────
// TTL parsing
// ─────────────────────────────────────────────────────────────────────────────

// parseTTLSettings parses a comma-separated list of duration strings into
// TTLOption slice. Accepts flexible formats: "1h", "1 hour", "2 days", "2d", etc.
func parseTTLSettings(raw string) ([]TTLOption, error) {
	parts := strings.Split(raw, ",")
	opts := make([]TTLOption, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		d, label, err := parseFuzzyDuration(p)
		if err != nil {
			return nil, fmt.Errorf("cannot parse TTL %q: %w", p, err)
		}
		opts = append(opts, TTLOption{Label: label, Duration: d})
	}
	if len(opts) == 0 {
		return nil, fmt.Errorf("no valid TTL options found")
	}
	return opts, nil
}

// parseFuzzyDuration accepts many human-friendly duration formats.
func parseFuzzyDuration(s string) (time.Duration, string, error) {
	s = strings.ToLower(strings.TrimSpace(s))

	// Try standard Go duration first (e.g. "1h", "24h", "168h").
	if d, err := time.ParseDuration(s); err == nil {
		return d, formatDurationLabel(d), nil
	}

	// Try "N unit" or "Nu" patterns.
	s = strings.NewReplacer(
		"hours", "h", "hour", "h",
		"days", "d", "day", "d",
		"weeks", "w", "week", "w",
		"minutes", "m", "minute", "m",
		" ", "",
	).Replace(s)

	// Now handle d and w which Go doesn't support natively.
	if strings.HasSuffix(s, "d") {
		n, err := strconv.Atoi(strings.TrimSuffix(s, "d"))
		if err != nil {
			return 0, "", fmt.Errorf("invalid days value")
		}
		d := time.Duration(n) * 24 * time.Hour
		return d, formatDurationLabel(d), nil
	}
	if strings.HasSuffix(s, "w") {
		n, err := strconv.Atoi(strings.TrimSuffix(s, "w"))
		if err != nil {
			return 0, "", fmt.Errorf("invalid weeks value")
		}
		d := time.Duration(n) * 7 * 24 * time.Hour
		return d, formatDurationLabel(d), nil
	}

	// Try after replacing d/w.
	if d, err := time.ParseDuration(s); err == nil {
		return d, formatDurationLabel(d), nil
	}

	return 0, "", fmt.Errorf("unrecognised duration format")
}

func formatDurationLabel(d time.Duration) string {
	switch {
	case d%(7*24*time.Hour) == 0:
		n := int(d / (7 * 24 * time.Hour))
		if n == 1 {
			return "1 week"
		}
		return fmt.Sprintf("%d weeks", n)
	case d%(24*time.Hour) == 0:
		n := int(d / (24 * time.Hour))
		if n == 1 {
			return "1 day"
		}
		return fmt.Sprintf("%d days", n)
	case d%time.Hour == 0:
		n := int(d / time.Hour)
		if n == 1 {
			return "1 hour"
		}
		return fmt.Sprintf("%d hours", n)
	default:
		return d.String()
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Helpers
// ─────────────────────────────────────────────────────────────────────────────

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func parseBoolEnv(key string, def bool) bool {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return def
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return def
	}
	return b
}

func parseIntEnv(key string, def int) int {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return def
	}
	return n
}

// parseByteSize parses a size string like "1gb", "512mb", "1073741824".
func parseByteSize(s string, def int64) int64 {
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "" {
		return def
	}
	multipliers := map[string]int64{
		"gb": 1 << 30,
		"mb": 1 << 20,
		"kb": 1 << 10,
		"b":  1,
	}
	for suffix, mult := range multipliers {
		if strings.HasSuffix(s, suffix) {
			n, err := strconv.ParseInt(strings.TrimSuffix(s, suffix), 10, 64)
			if err != nil {
				return def
			}
			return n * mult
		}
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return def
	}
	return n
}

func parseCIDRList(raw string) ([]*net.IPNet, error) {
	var nets []*net.IPNet
	for _, entry := range strings.Split(raw, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		// Plain IP — convert to /32 or /128.
		if !strings.Contains(entry, "/") {
			ip := net.ParseIP(entry)
			if ip == nil {
				return nil, fmt.Errorf("invalid IP %q", entry)
			}
			bits := 32
			if ip.To4() == nil {
				bits = 128
			}
			entry = fmt.Sprintf("%s/%d", entry, bits)
		}
		_, cidr, err := net.ParseCIDR(entry)
		if err != nil {
			return nil, fmt.Errorf("invalid CIDR %q: %w", entry, err)
		}
		nets = append(nets, cidr)
	}
	return nets, nil
}
