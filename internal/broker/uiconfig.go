package broker

import (
	"os"
	"strconv"
	"strings"
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
}

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

	return cfg
}

// ── helpers ───────────────────────────────────────────────────────────────────

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
