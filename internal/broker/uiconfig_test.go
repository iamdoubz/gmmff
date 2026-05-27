package broker

import (
	"os"
	"testing"
)

// ─────────────────────────────────────────────────────────────────────────────
// parseTabOrder
// ─────────────────────────────────────────────────────────────────────────────

func TestParseTabOrder(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  []string
	}{
		{
			name:  "default_order_explicit",
			input: "files,chat,schedule",
			want:  []string{"files", "chat", "schedule"},
		},
		{
			name:  "schedule_first",
			input: "schedule,files,chat",
			want:  []string{"schedule", "files", "chat"},
		},
		{
			name:  "partial_order_appends_missing",
			input: "schedule",
			want:  []string{"schedule", "files", "chat"},
		},
		{
			name:  "two_tabs_specified",
			input: "chat,files",
			want:  []string{"chat", "files", "schedule"},
		},
		{
			name:  "unknown_name_silently_skipped",
			input: "files,unknown,chat,schedule",
			want:  []string{"files", "chat", "schedule"},
		},
		{
			name:  "duplicate_silently_skipped",
			input: "files,files,chat,schedule",
			want:  []string{"files", "chat", "schedule"},
		},
		{
			name:  "case_insensitive",
			input: "FILES,CHAT,SCHEDULE",
			want:  []string{"files", "chat", "schedule"},
		},
		{
			name:  "whitespace_trimmed",
			input: " files , chat , schedule ",
			want:  []string{"files", "chat", "schedule"},
		},
		{
			name:  "empty_entries_skipped",
			input: "files,,chat,,schedule",
			want:  []string{"files", "chat", "schedule"},
		},
		{
			name:  "all_unknown_falls_back_to_default",
			input: "foo,bar,baz",
			want:  []string{"files", "chat", "schedule"},
		},
		{
			name:  "empty_string_produces_full_default",
			input: "",
			want:  []string{"files", "chat", "schedule"},
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			got := parseTabOrder(tc.input)
			if len(got) != len(tc.want) {
				t.Fatalf("parseTabOrder(%q): got %v (len %d), want %v (len %d)",
					tc.input, got, len(got), tc.want, len(tc.want))
			}
			for i := range tc.want {
				if got[i] != tc.want[i] {
					t.Errorf("parseTabOrder(%q)[%d]: got %q, want %q",
						tc.input, i, got[i], tc.want[i])
				}
			}
		})
	}
}

func TestParseTabOrder_AlwaysContainsAllTabs(t *testing.T) {
	// No matter what input is given, all three tabs must appear exactly once.
	inputs := []string{
		"",
		"files",
		"schedule,files",
		"unknown",
		"files,files,files",
		"schedule,files,chat",
	}
	required := []string{"files", "chat", "schedule"}

	for _, input := range inputs {
		got := parseTabOrder(input)
		seen := map[string]int{}
		for _, tab := range got {
			seen[tab]++
		}
		for _, req := range required {
			if seen[req] == 0 {
				t.Errorf("parseTabOrder(%q): missing tab %q in result %v", input, req, got)
			}
			if seen[req] > 1 {
				t.Errorf("parseTabOrder(%q): tab %q appears %d times in result %v",
					input, req, seen[req], got)
			}
		}
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// ValidateEnv
// ─────────────────────────────────────────────────────────────────────────────

// setEnv is a test helper that sets an env var and restores the original
// value when the test ends. Using t.Cleanup avoids defer-in-loop issues.
func setEnv(t *testing.T, key, value string) {
	t.Helper()
	original, had := os.LookupEnv(key)
	os.Setenv(key, value)
	t.Cleanup(func() {
		if had {
			os.Setenv(key, original)
		} else {
			os.Unsetenv(key)
		}
	})
}

func TestValidateEnv_CleanEnvironment(t *testing.T) {
	// With no GMMFF_* vars set, ValidateEnv should return zero warnings.
	// Unset all known vars so this test is isolated from the real environment.
	knownVars := []string{
		"GMMFF_SHOW_FILES", "GMMFF_SHOW_CHAT", "GMMFF_SHOW_SCHEDULE",
		"GMMFF_SHOW_ICE_SETTINGS", "GMMFF_ALLOW_STUN", "GMMFF_ALLOW_TURN",
		"GMMFF_SHOW_SHARE_LINK", "GMMFF_SHOW_QR_CODE",
		"GMMFF_ALLOW_CUSTOM_SERVER", "GMMFF_SHOW_PEERS_LIMIT",
		"GMMFF_PUSH_STUN", "GMMFF_PUSH_TURN",
		"GMMFF_MAX_PEERS_LIMIT", "GMMFF_MAX_WINDOW", "GMMFF_MAX_CHUNK_SIZE",
		"GMMFF_SCHEDULE_MAX_DOWNLOADS",
		"GMMFF_TAB_ORDER", "GMMFF_TAB_DEFAULT",
		"GMMFF_SCHEDULE_MAX_SIZE", "GMMFF_SCHEDULE_CLEANUP_INTERVAL",
		"GMMFF_SCHEDULE_UPLOAD_IP", "GMMFF_SCHEDULE_DOWNLOAD_IP",
		"GMMFF_TTL_SETTINGS", "GMMFF_ALLOWED_LANGS", "GMMFF_LOG_LEVEL",
	}
	for _, key := range knownVars {
		original, had := os.LookupEnv(key)
		os.Unsetenv(key)
		if had {
			defer os.Setenv(key, original)
		}
	}

	warns := ValidateEnv()
	if len(warns) != 0 {
		t.Errorf("clean environment: expected 0 warnings, got %d: %v", len(warns), warns)
	}
}

func TestValidateEnv_InvalidBoolValues(t *testing.T) {
	boolVars := []string{
		"GMMFF_SHOW_FILES",
		"GMMFF_SHOW_CHAT",
		"GMMFF_SHOW_SCHEDULE",
		"GMMFF_SHOW_ICE_SETTINGS",
		"GMMFF_ALLOW_STUN",
		"GMMFF_ALLOW_TURN",
		"GMMFF_PUSH_STUN",
		"GMMFF_PUSH_TURN",
	}
	for _, key := range boolVars {
		key := key
		t.Run(key, func(t *testing.T) {
			setEnv(t, key, "notabool")
			warns := ValidateEnv()
			found := false
			for _, w := range warns {
				if w.Key == key {
					found = true
					break
				}
			}
			if !found {
				t.Errorf("expected warning for %s=notabool, got warnings: %v", key, warns)
			}
		})
	}
}

func TestValidateEnv_ValidBoolValues(t *testing.T) {
	validBools := []string{"true", "false", "1", "0", "TRUE", "FALSE"}
	for _, val := range validBools {
		val := val
		t.Run(val, func(t *testing.T) {
			setEnv(t, "GMMFF_SHOW_FILES", val)
			warns := ValidateEnv()
			for _, w := range warns {
				if w.Key == "GMMFF_SHOW_FILES" {
					t.Errorf("valid bool %q should not produce warning, got: %s", val, w.Message)
				}
			}
		})
	}
}

func TestValidateEnv_IntOutOfRange(t *testing.T) {
	cases := []struct {
		key   string
		value string
	}{
		{"GMMFF_MAX_PEERS_LIMIT", "0"},   // below min (2)
		{"GMMFF_MAX_PEERS_LIMIT", "11"},  // above max (10)
		{"GMMFF_MAX_PEERS_LIMIT", "abc"}, // not an integer
		{"GMMFF_MAX_WINDOW", "0"},        // below min (1)
		{"GMMFF_MAX_WINDOW", "17"},       // above max (16)
		{"GMMFF_MAX_CHUNK_SIZE", "512"},  // below min (1024)
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.key+"="+tc.value, func(t *testing.T) {
			setEnv(t, tc.key, tc.value)
			warns := ValidateEnv()
			found := false
			for _, w := range warns {
				if w.Key == tc.key {
					found = true
					break
				}
			}
			if !found {
				t.Errorf("expected warning for %s=%s, got: %v", tc.key, tc.value, warns)
			}
		})
	}
}

func TestValidateEnv_ValidIntValues(t *testing.T) {
	setEnv(t, "GMMFF_MAX_PEERS_LIMIT", "5")
	setEnv(t, "GMMFF_MAX_WINDOW", "4")
	warns := ValidateEnv()
	for _, w := range warns {
		if w.Key == "GMMFF_MAX_PEERS_LIMIT" || w.Key == "GMMFF_MAX_WINDOW" {
			t.Errorf("valid int value should not produce warning: %v", w)
		}
	}
}

func TestValidateEnv_InvalidTabOrder(t *testing.T) {
	t.Run("unknown_tab_name", func(t *testing.T) {
		setEnv(t, "GMMFF_TAB_ORDER", "files,unknown,chat")
		warns := ValidateEnv()
		found := false
		for _, w := range warns {
			if w.Key == "GMMFF_TAB_ORDER" {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("expected warning for GMMFF_TAB_ORDER with unknown name, got: %v", warns)
		}
	})

	t.Run("duplicate_tab_name", func(t *testing.T) {
		setEnv(t, "GMMFF_TAB_ORDER", "files,files,chat")
		warns := ValidateEnv()
		found := false
		for _, w := range warns {
			if w.Key == "GMMFF_TAB_ORDER" {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("expected warning for duplicate tab name, got: %v", warns)
		}
	})

	t.Run("valid_order_no_warning", func(t *testing.T) {
		setEnv(t, "GMMFF_TAB_ORDER", "schedule,files,chat")
		warns := ValidateEnv()
		for _, w := range warns {
			if w.Key == "GMMFF_TAB_ORDER" {
				t.Errorf("valid tab order should not produce warning: %v", w)
			}
		}
	})
}

func TestValidateEnv_InvalidTabDefault(t *testing.T) {
	setEnv(t, "GMMFF_TAB_DEFAULT", "notAtab")
	warns := ValidateEnv()
	found := false
	for _, w := range warns {
		if w.Key == "GMMFF_TAB_DEFAULT" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected warning for invalid GMMFF_TAB_DEFAULT, got: %v", warns)
	}
}

func TestValidateEnv_ValidTabDefault(t *testing.T) {
	for _, val := range []string{"files", "chat", "schedule"} {
		val := val
		t.Run(val, func(t *testing.T) {
			setEnv(t, "GMMFF_TAB_DEFAULT", val)
			warns := ValidateEnv()
			for _, w := range warns {
				if w.Key == "GMMFF_TAB_DEFAULT" {
					t.Errorf("valid tab default %q should not produce warning: %v", val, w)
				}
			}
		})
	}
}

func TestValidateEnv_InvalidLogLevel(t *testing.T) {
	setEnv(t, "GMMFF_LOG_LEVEL", "verbose")
	warns := ValidateEnv()
	found := false
	for _, w := range warns {
		if w.Key == "GMMFF_LOG_LEVEL" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected warning for invalid log level, got: %v", warns)
	}
}

func TestValidateEnv_ValidLogLevels(t *testing.T) {
	for _, level := range []string{"trace", "debug", "info", "warn", "error", "fatal", "panic"} {
		level := level
		t.Run(level, func(t *testing.T) {
			setEnv(t, "GMMFF_LOG_LEVEL", level)
			warns := ValidateEnv()
			for _, w := range warns {
				if w.Key == "GMMFF_LOG_LEVEL" {
					t.Errorf("valid log level %q should not produce warning: %v", level, w)
				}
			}
		})
	}
}

func TestValidateEnv_InvalidScheduleMaxSize(t *testing.T) {
	setEnv(t, "GMMFF_SCHEDULE_MAX_SIZE", "banana")
	warns := ValidateEnv()
	found := false
	for _, w := range warns {
		if w.Key == "GMMFF_SCHEDULE_MAX_SIZE" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected warning for invalid GMMFF_SCHEDULE_MAX_SIZE, got: %v", warns)
	}
}

func TestValidateEnv_ValidScheduleMaxSize(t *testing.T) {
	for _, val := range []string{"1gb", "512mb", "1073741824"} {
		val := val
		t.Run(val, func(t *testing.T) {
			setEnv(t, "GMMFF_SCHEDULE_MAX_SIZE", val)
			warns := ValidateEnv()
			for _, w := range warns {
				if w.Key == "GMMFF_SCHEDULE_MAX_SIZE" {
					t.Errorf("valid size %q should not produce warning: %v", val, w)
				}
			}
		})
	}
}

func TestValidateEnv_InvalidCronInterval(t *testing.T) {
	setEnv(t, "GMMFF_SCHEDULE_CLEANUP_INTERVAL", "every 5 minutes")
	warns := ValidateEnv()
	found := false
	for _, w := range warns {
		if w.Key == "GMMFF_SCHEDULE_CLEANUP_INTERVAL" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected warning for invalid cron expression, got: %v", warns)
	}
}

func TestValidateEnv_ValidCronExpressions(t *testing.T) {
	for _, expr := range []string{"*/5 * * * *", "0 */6 * * *", "0 0 * * 0"} {
		expr := expr
		t.Run(expr, func(t *testing.T) {
			setEnv(t, "GMMFF_SCHEDULE_CLEANUP_INTERVAL", expr)
			warns := ValidateEnv()
			for _, w := range warns {
				if w.Key == "GMMFF_SCHEDULE_CLEANUP_INTERVAL" {
					t.Errorf("valid cron %q should not produce warning: %v", expr, w)
				}
			}
		})
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// clampInt
// ─────────────────────────────────────────────────────────────────────────────

func TestClampInt(t *testing.T) {
	cases := []struct {
		v, min, max, want int
	}{
		{5, 1, 10, 5},   // within range — unchanged
		{0, 1, 10, 1},   // below min — clamped to min
		{11, 1, 10, 10}, // above max — clamped to max
		{1, 1, 10, 1},   // exactly min
		{10, 1, 10, 10}, // exactly max
	}
	for _, tc := range cases {
		got := clampInt(tc.v, tc.min, tc.max)
		if got != tc.want {
			t.Errorf("clampInt(%d, %d, %d): got %d, want %d",
				tc.v, tc.min, tc.max, got, tc.want)
		}
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// DefaultUIConfig
// ─────────────────────────────────────────────────────────────────────────────

func TestDefaultUIConfig(t *testing.T) {
	cfg := DefaultUIConfig()

	// Verify the most critical defaults.
	if !cfg.ShowFiles {
		t.Error("ShowFiles should default to true")
	}
	if !cfg.ShowChat {
		t.Error("ShowChat should default to true")
	}
	if cfg.ShowSchedule {
		t.Error("ShowSchedule should default to false")
	}
	if !cfg.ShowICESettings {
		t.Error("ShowICESettings should default to true")
	}
	if !cfg.AllowSTUN {
		t.Error("AllowSTUN should default to true")
	}
	if !cfg.AllowTURN {
		t.Error("AllowTURN should default to true")
	}
	if cfg.AllowCustomServer {
		t.Error("AllowCustomServer should default to false")
	}
	if cfg.MaxPeersLimit < 2 || cfg.MaxPeersLimit > 10 {
		t.Errorf("MaxPeersLimit %d is outside valid range [2,10]", cfg.MaxPeersLimit)
	}
	if len(cfg.TabOrder) != 3 {
		t.Errorf("TabOrder should have 3 entries, got %d: %v", len(cfg.TabOrder), cfg.TabOrder)
	}
}
