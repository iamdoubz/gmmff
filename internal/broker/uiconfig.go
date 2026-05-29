package broker

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// UIConfig holds all server-side feature flags that are served to the browser
// via GET /config.json. Every field has a safe default so the UI works
// correctly without any configuration.
type UIConfig struct {
	// Tab visibility
	ShowFiles bool `json:"show_files"`
	ShowChat  bool `json:"show_chat"`

	// ICE settings panel
	ShowICESettings bool `json:"show_ice_settings"`
	AllowSTUN       bool `json:"allow_stun"`
	AllowTURN       bool `json:"allow_turn"`

	// Sharing
	ShowShareLink bool `json:"show_share_link"`
	ShowQRCode    bool `json:"show_qr_code"`

	// Server connectivity
	AllowCustomServer bool `json:"allow_custom_server"`

	// Multi-peer
	ShowPeersLimit bool `json:"show_peers_limit"`
	MaxPeersLimit  int  `json:"max_peers_limit"`

	// Transfer tuning (server-enforced, not shown as UI sliders)
	MaxWindow    int `json:"max_window"`
	MaxChunkSize int `json:"max_chunk_size"`

	// Languages — filtered list sent to the browser.
	// When only one language is present, the picker is hidden.
	AllowedLangs []string `json:"allowed_langs"` // nil = all

	// Message of the day — empty string means no message shown.
	MOTD string `json:"motd"`

	// Schedule tab visibility — mirrors GMMFF_SHOW_SCHEDULE.
	ShowSchedule bool `json:"show_schedule"`

	// TabOrder defines the left-to-right display order of tabs.
	// Valid values: "files", "chat", "schedule".
	// Default: ["files", "chat", "schedule"].
	// Tabs not present in the slice appear after those that are.
	// Tabs disabled via ShowFiles/ShowChat/ShowSchedule are hidden regardless.
	TabOrder []string `json:"tab_order"`

	// TabDefault is the tab shown on page load.
	// If empty, the first tab in TabOrder is used.
	// Valid values: "files", "chat", "schedule".
	TabDefault string `json:"tab_default"`

	// PushSTUN — when true the server pushes its STUN config to the browser
	// via /api/ice, replacing user-defined STUN servers.
	PushSTUN bool `json:"push_stun"`

	// PushTURN — when true the server pushes its TURN config to the browser
	// via /api/ice, replacing user-defined TURN servers.
	// If TURN uses ephemeral credentials (secret=) a short-lived credential is
	// generated server-side using PushTURNTTL. Static user/pass credentials are
	// forwarded as-is — the admin accepts that all peers will receive them.
	PushTURN bool `json:"push_turn"`

	// PushTURNTTL is the lifetime of ephemeral TURN credentials generated
	// server-side when PushTURN=true and the TURN server uses a shared secret.
	// Parsed from GMMFF_PUSH_TTL. Default: 30 minutes.
	// Ignored when using static user/pass credentials.
	// Not sent to the browser — tagged json:"-".
	PushTURNTTL time.Duration `json:"-"`
}

// knownTabs is the canonical set of valid tab names and their default order.
var knownTabs = []string{"files", "chat", "schedule"}

// DefaultUIConfig returns the safest, most fully-featured defaults.
func DefaultUIConfig() UIConfig {
	return UIConfig{
		ShowFiles:         true,
		ShowChat:          true,
		ShowICESettings:   true,
		AllowSTUN:         true,
		AllowTURN:         true,
		ShowShareLink:     true,
		ShowQRCode:        true,
		AllowCustomServer: false, // default false — lock clients to this server
		ShowPeersLimit:    true,
		MaxPeersLimit:     10,
		MaxWindow:         2,
		MaxChunkSize:      65526,
		AllowedLangs:      nil, // nil = all languages
		MOTD:              "",
		TabOrder:          []string{"files", "chat", "schedule"},
	}
}

// UIConfigFromEnv reads all GMMFF_* feature-flag environment variables and
// returns a UIConfig. Unset variables fall back to DefaultUIConfig values.
func UIConfigFromEnv() UIConfig {
	cfg := DefaultUIConfig()

	cfg.ShowFiles = boolEnv("GMMFF_SHOW_FILES", cfg.ShowFiles)
	cfg.ShowChat = boolEnv("GMMFF_SHOW_CHAT", cfg.ShowChat)

	cfg.ShowICESettings = boolEnv("GMMFF_SHOW_ICE_SETTINGS", cfg.ShowICESettings)
	cfg.AllowSTUN = boolEnv("GMMFF_ALLOW_STUN", cfg.AllowSTUN)
	cfg.AllowTURN = boolEnv("GMMFF_ALLOW_TURN", cfg.AllowTURN)

	cfg.ShowShareLink = boolEnv("GMMFF_SHOW_SHARE_LINK", cfg.ShowShareLink)
	cfg.ShowQRCode = boolEnv("GMMFF_SHOW_QR_CODE", cfg.ShowQRCode)

	cfg.AllowCustomServer = boolEnv("GMMFF_ALLOW_CUSTOM_SERVER", cfg.AllowCustomServer)

	cfg.ShowPeersLimit = boolEnv("GMMFF_SHOW_PEERS_LIMIT", cfg.ShowPeersLimit)
	cfg.MaxPeersLimit = clampInt(intEnv("GMMFF_MAX_PEERS_LIMIT", cfg.MaxPeersLimit), 2, 10)

	cfg.MaxWindow = clampInt(intEnv("GMMFF_MAX_WINDOW", cfg.MaxWindow), 1, 16)
	cfg.MaxChunkSize = clampInt(intEnv("GMMFF_MAX_CHUNK_SIZE", cfg.MaxChunkSize), 1024, 65526)

	cfg.MOTD = strings.TrimSpace(os.Getenv("GMMFF_MOTD"))

	if raw := strings.TrimSpace(os.Getenv("GMMFF_ALLOWED_LANGS")); raw != "" && raw != "all" {
		parts := strings.Split(raw, ",")
		langs := make([]string, 0, len(parts))
		for _, p := range parts {
			p = strings.TrimSpace(p)
			if p != "" {
				langs = append(langs, p)
			}
		}
		if len(langs) > 0 {
			cfg.AllowedLangs = langs
		}
	}

	if raw := strings.TrimSpace(os.Getenv("GMMFF_TAB_ORDER")); raw != "" {
		cfg.TabOrder = parseTabOrder(raw)
	}

	if raw := strings.ToLower(strings.TrimSpace(os.Getenv("GMMFF_TAB_DEFAULT"))); raw != "" {
		valid := map[string]bool{"files": true, "chat": true, "schedule": true}
		if valid[raw] {
			cfg.TabDefault = raw
		}
	}

	cfg.PushSTUN    = boolEnv("GMMFF_PUSH_STUN", false)
	cfg.PushTURN    = boolEnv("GMMFF_PUSH_TURN", false)
	cfg.PushTURNTTL = 30 * time.Minute // default
	if raw := strings.TrimSpace(os.Getenv("GMMFF_PUSH_TTL")); raw != "" {
		if d, err := time.ParseDuration(raw); err == nil && d > 0 {
			cfg.PushTURNTTL = d
		}
	}

	return cfg
}

// parseTabOrder parses a comma-separated tab order string.
// Unknown tab names are ignored with a note; any recognised tabs missing from
// the list are appended at the end in default order so they still show up.
func parseTabOrder(raw string) []string {
	valid := map[string]bool{"files": true, "chat": true, "schedule": true}
	seen  := map[string]bool{}
	order := []string{}

	for _, part := range strings.Split(raw, ",") {
		name := strings.ToLower(strings.TrimSpace(part))
		if name == "" {
			continue
		}
		if !valid[name] {
			continue // unknown — silently skip; ValidateEnv will warn about it
		}
		if seen[name] {
			continue // duplicate — skip
		}
		seen[name] = true
		order = append(order, name)
	}

	// Append any known tabs not explicitly listed, preserving default order.
	for _, tab := range knownTabs {
		if !seen[tab] {
			order = append(order, tab)
		}
	}

	return order
}

// ── Env validation ────────────────────────────────────────────────────────────

// EnvWarning describes a single misconfigured environment variable.
type EnvWarning struct {
	Key     string
	Value   string
	Message string
}

// ValidateEnv checks all known GMMFF_* environment variables for invalid
// values and returns a slice of warnings. It never modifies any state and
// never panics — it is purely informational. Call it at startup and log each
// warning. The server can always run; these are warnings, not fatal errors.
func ValidateEnv() []EnvWarning {
	var warns []EnvWarning

	add := func(key, val, msg string) {
		warns = append(warns, EnvWarning{Key: key, Value: val, Message: msg})
	}

	// ── Bool vars ────────────────────────────────────────────────────────────
	boolVars := []string{
		"GMMFF_SHOW_FILES", "GMMFF_SHOW_CHAT", "GMMFF_SHOW_SCHEDULE",
		"GMMFF_SHOW_ICE_SETTINGS", "GMMFF_ALLOW_STUN", "GMMFF_ALLOW_TURN",
		"GMMFF_SHOW_SHARE_LINK", "GMMFF_SHOW_QR_CODE",
		"GMMFF_ALLOW_CUSTOM_SERVER", "GMMFF_SHOW_PEERS_LIMIT",
		"GMMFF_PUSH_STUN", "GMMFF_PUSH_TURN",
	}
	for _, key := range boolVars {
		v := strings.TrimSpace(os.Getenv(key))
		if v == "" {
			continue
		}
		if _, err := strconv.ParseBool(v); err != nil {
			add(key, v, fmt.Sprintf("must be a boolean (true/false/1/0); got %q", v))
		}
	}

	// ── Int vars with ranges ─────────────────────────────────────────────────
	intRangeVars := []struct {
		key      string
		min, max int
		hint     string
	}{
		{"GMMFF_MAX_PEERS_LIMIT", 2, 10, "must be an integer between 2 and 10"},
		{"GMMFF_MAX_WINDOW", 1, 16, "must be an integer between 1 and 16"},
		{"GMMFF_MAX_CHUNK_SIZE", 1024, 65526, "must be an integer between 1024 and 65526"},
		{"GMMFF_SCHEDULE_MAX_DOWNLOADS", 0, 1<<31 - 1, "must be a non-negative integer (0 = unlimited)"},
	}
	for _, iv := range intRangeVars {
		v := strings.TrimSpace(os.Getenv(iv.key))
		if v == "" {
			continue
		}
		n, err := strconv.Atoi(v)
		if err != nil {
			add(iv.key, v, fmt.Sprintf("%s; got %q", iv.hint, v))
		} else if n < iv.min || n > iv.max {
			add(iv.key, v, fmt.Sprintf("%s; got %d", iv.hint, n))
		}
	}

	// ── Tab order ─────────────────────────────────────────────────────────────
	if raw := strings.TrimSpace(os.Getenv("GMMFF_TAB_ORDER")); raw != "" {
		valid := map[string]bool{"files": true, "chat": true, "schedule": true}
		seen  := map[string]bool{}
		for _, part := range strings.Split(raw, ",") {
			name := strings.ToLower(strings.TrimSpace(part))
			if name == "" {
				continue
			}
			if !valid[name] {
				add("GMMFF_TAB_ORDER", raw,
					fmt.Sprintf("unknown tab name %q — valid names are: files, chat, schedule", name))
			} else if seen[name] {
				add("GMMFF_TAB_ORDER", raw,
					fmt.Sprintf("duplicate tab name %q — each tab should appear at most once", name))
			}
			seen[name] = true
		}
	}

	// ── GMMFF_PUSH_STUN / GMMFF_PUSH_TURN already covered in bool vars above.

	// ── GMMFF_PUSH_TTL ────────────────────────────────────────────────────────
	if raw := strings.TrimSpace(os.Getenv("GMMFF_PUSH_TTL")); raw != "" {
		d, err := time.ParseDuration(raw)
		if err != nil {
			add("GMMFF_PUSH_TTL", raw,
				fmt.Sprintf("must be a Go duration (e.g. 30m, 1h, 2h30m); got %q", raw))
		} else if d <= 0 {
			add("GMMFF_PUSH_TTL", raw, "must be a positive duration")
		} else if d > 24*time.Hour {
			add("GMMFF_PUSH_TTL", raw,
				fmt.Sprintf("TTL of %v is unusually long — TURN credentials are typically short-lived (≤24h)", d))
		}
	}

	// ── GMMFF_TAB_DEFAULT ─────────────────────────────────────────────────────
	if raw := strings.TrimSpace(os.Getenv("GMMFF_TAB_DEFAULT")); raw != "" {
		valid := map[string]bool{"files": true, "chat": true, "schedule": true}
		if !valid[strings.ToLower(raw)] {
			add("GMMFF_TAB_DEFAULT", raw,
				fmt.Sprintf("unknown tab name %q — valid names are: files, chat, schedule", raw))
		}
	}

	// ── GMMFF_SCHEDULE_MAX_SIZE ───────────────────────────────────────────────
	if raw := strings.TrimSpace(os.Getenv("GMMFF_SCHEDULE_MAX_SIZE")); raw != "" {
		if !validByteSize(raw) {
			add("GMMFF_SCHEDULE_MAX_SIZE", raw,
				`must be a size with optional suffix (e.g. "1gb", "512mb", "1073741824"); got `+fmt.Sprintf("%q", raw))
		}
	}

	// ── GMMFF_SCHEDULE_CLEANUP_INTERVAL ──────────────────────────────────────
	if raw := strings.TrimSpace(os.Getenv("GMMFF_SCHEDULE_CLEANUP_INTERVAL")); raw != "" {
		if !validCronExpr(raw) {
			add("GMMFF_SCHEDULE_CLEANUP_INTERVAL", raw,
				`must be a 5-field crontab expression (e.g. "*/30 * * * *"); got `+fmt.Sprintf("%q", raw))
		}
	}

	// ── GMMFF_SCHEDULE_UPLOAD_IP / GMMFF_SCHEDULE_DOWNLOAD_IP ────────────────
	for _, key := range []string{"GMMFF_SCHEDULE_UPLOAD_IP", "GMMFF_SCHEDULE_DOWNLOAD_IP"} {
		raw := strings.TrimSpace(os.Getenv(key))
		if raw == "" || raw == "0.0.0.0" {
			continue
		}
		for _, entry := range strings.Split(raw, ",") {
			entry = strings.TrimSpace(entry)
			if entry == "" {
				continue
			}
			if msg := validateIPOrCIDR(entry); msg != "" {
				add(key, raw, fmt.Sprintf("invalid entry %q: %s", entry, msg))
			}
		}
	}

	// ── GMMFF_TTL_SETTINGS ────────────────────────────────────────────────────
	if raw := strings.TrimSpace(os.Getenv("GMMFF_TTL_SETTINGS")); raw != "" {
		for _, part := range strings.Split(raw, ",") {
			part = strings.TrimSpace(part)
			if part == "" {
				continue
			}
			if !validDurationString(part) {
				add("GMMFF_TTL_SETTINGS", raw,
					fmt.Sprintf("cannot parse duration %q — use formats like: 1h, 8h, 1 day, 3 days, 7d, 1w", part))
			}
		}
	}

	// ── GMMFF_ALLOWED_LANGS ───────────────────────────────────────────────────
	if raw := strings.TrimSpace(os.Getenv("GMMFF_ALLOWED_LANGS")); raw != "" && raw != "all" {
		// We can't validate against the runtime language list here (no access),
		// but we can warn about obviously malformed entries (empty after trim).
		for _, part := range strings.Split(raw, ",") {
			if strings.TrimSpace(part) == "" {
				add("GMMFF_ALLOWED_LANGS", raw,
					"contains an empty entry (check for trailing commas or double commas)")
				break
			}
		}
	}

	// ── GMMFF_LOG_LEVEL ───────────────────────────────────────────────────────
	if raw := strings.TrimSpace(os.Getenv("GMMFF_LOG_LEVEL")); raw != "" {
		valid := map[string]bool{
			"trace": true, "debug": true, "info": true,
			"warn": true, "error": true, "fatal": true, "panic": true,
		}
		if !valid[strings.ToLower(raw)] {
			add("GMMFF_LOG_LEVEL", raw,
				`must be one of: trace, debug, info, warn, error, fatal, panic; got `+fmt.Sprintf("%q", raw))
		}
	}

	return warns
}

// ── helpers ───────────────────────────────────────────────────────────────────

func validByteSize(s string) bool {
	s = strings.ToLower(strings.TrimSpace(s))
	for _, suffix := range []string{"gb", "mb", "kb", "b"} {
		if strings.HasSuffix(s, suffix) {
			n, err := strconv.ParseInt(strings.TrimSuffix(s, suffix), 10, 64)
			return err == nil && n > 0
		}
	}
	n, err := strconv.ParseInt(s, 10, 64)
	return err == nil && n > 0
}

func validCronExpr(s string) bool {
	fields := strings.Fields(s)
	return len(fields) == 5
}

func validateIPOrCIDR(s string) string {
	// Simple validation: contains only valid IP/CIDR characters.
	// Full parsing is done in schedule.parseCIDRList at runtime.
	for _, c := range s {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') ||
			(c >= 'A' && c <= 'F') || c == '.' || c == ':' || c == '/') {
			return fmt.Sprintf("unexpected character %q in IP/CIDR", c)
		}
	}
	if strings.Count(s, "/") > 1 {
		return "too many slashes for a CIDR notation"
	}
	return ""
}

func validDurationString(s string) bool {
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.NewReplacer(
		"hours", "h", "hour", "h",
		"days", "d", "day", "d",
		"weeks", "w", "week", "w",
		"minutes", "m", "minute", "m",
		" ", "",
	).Replace(s)
	if strings.HasSuffix(s, "d") || strings.HasSuffix(s, "w") {
		_, err := strconv.Atoi(s[:len(s)-1])
		return err == nil
	}
	// Try standard Go duration parse.
	for _, suffix := range []string{"h", "m", "s", "ms"} {
		if strings.HasSuffix(s, suffix) {
			_, err := strconv.ParseFloat(s[:len(s)-len(suffix)], 64)
			return err == nil
		}
	}
	return false
}

func boolEnv(key string, def bool) bool {
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

func intEnv(key string, def int) int {
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

func clampInt(v, min, max int) int {
	if v < min {
		return min
	}
	if v > max {
		return max
	}
	return v
}

