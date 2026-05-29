package schedule

import (
	"net"
	"testing"
	"time"
)

// ─────────────────────────────────────────────────────────────────────────────
// ParseFuzzyDuration
// ─────────────────────────────────────────────────────────────────────────────

func TestParseFuzzyDuration(t *testing.T) {
	cases := []struct {
		input     string
		wantSecs  int64 // expected duration in seconds; ignored when wantErr true
		wantLabel string
		wantErr   bool
	}{
		// ── Standard Go duration strings ─────────────────────────────────────
		{"1h", 3600, "1 hour", false},
		{"8h", 28800, "8 hours", false},
		{"24h", 86400, "1 day", false},
		{"168h", 7 * 86400, "1 week", false},

		// ── "N hour(s)" long-form ─────────────────────────────────────────────
		{"1 hour", 3600, "1 hour", false},
		{"2 hours", 7200, "2 hours", false},
		{"8 hours", 28800, "8 hours", false},

		// ── Days — short and long form ────────────────────────────────────────
		{"1d", 86400, "1 day", false},
		{"2d", 172800, "2 days", false},
		{"3d", 259200, "3 days", false},
		{"1 day", 86400, "1 day", false},
		{"3 days", 259200, "3 days", false},
		{"30 days", 30 * 86400, "30 days", false},

		// ── Weeks — short and long form ───────────────────────────────────────
		{"1w", 7 * 86400, "1 week", false},
		{"2w", 14 * 86400, "2 weeks", false},
		{"1 week", 7 * 86400, "1 week", false},
		{"2 weeks", 14 * 86400, "2 weeks", false},

		// ── Case insensitive ─────────────────────────────────────────────────
		{"1H", 3600, "1 hour", false},
		{"1D", 86400, "1 day", false},
		{"1W", 7 * 86400, "1 week", false},
		{"1 HOUR", 3600, "1 hour", false},
		{"1 Day", 86400, "1 day", false},

		// ── Edge: zero duration ───────────────────────────────────────────────
		// "0d" and "0h" are valid (zero duration) — label is Go's default "0s".
		{"0d", 0, "0s", false},
		{"0h", 0, "0s", false},

		// ── Errors ────────────────────────────────────────────────────────────
		{"", 0, "", true},
		{"banana", 0, "", true},
		{"d", 0, "", true},   // suffix without number
		{"w", 0, "", true},   // suffix without number
		{"1.5d", 0, "", true}, // fractional days not supported
	}

	for _, tc := range cases {
		tc := tc // capture for parallel sub-tests
		t.Run(tc.input, func(t *testing.T) {
			got, label, err := ParseFuzzyDuration(tc.input)

			if tc.wantErr {
				if err == nil {
					t.Errorf("ParseFuzzyDuration(%q): expected error, got duration %v", tc.input, got)
				}
				return
			}

			if err != nil {
				t.Errorf("ParseFuzzyDuration(%q): unexpected error: %v", tc.input, err)
				return
			}

			if int64(got.Seconds()) != tc.wantSecs {
				t.Errorf("ParseFuzzyDuration(%q): got %v (%d s), want %d s",
					tc.input, got, int64(got.Seconds()), tc.wantSecs)
			}

			if label != tc.wantLabel {
				t.Errorf("ParseFuzzyDuration(%q): label = %q, want %q",
					tc.input, label, tc.wantLabel)
			}
		})
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// formatDurationLabel
// ─────────────────────────────────────────────────────────────────────────────

func TestFormatDurationLabel(t *testing.T) {
	cases := []struct {
		d    time.Duration
		want string
	}{
		{time.Hour, "1 hour"},
		{8 * time.Hour, "8 hours"},
		{24 * time.Hour, "1 day"},
		{3 * 24 * time.Hour, "3 days"},
		{7 * 24 * time.Hour, "1 week"},
		{14 * 24 * time.Hour, "2 weeks"},
		{30 * 24 * time.Hour, "30 days"}, // not a whole number of weeks
		// Sub-hour duration falls back to Go's default string representation.
		{90 * time.Minute, "1h30m0s"},
	}
	for _, tc := range cases {
		t.Run(tc.want, func(t *testing.T) {
			got := formatDurationLabel(tc.d)
			if got != tc.want {
				t.Errorf("formatDurationLabel(%v): got %q, want %q", tc.d, got, tc.want)
			}
		})
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// parseTTLSettings
// ─────────────────────────────────────────────────────────────────────────────

func TestParseTTLSettings(t *testing.T) {
	t.Run("default_options_round_trip", func(t *testing.T) {
		// The default TTL string used in .env.example should parse cleanly.
		raw := "1h,8h,1 day,3 days,7 days,30 days"
		opts, err := parseTTLSettings(raw)
		if err != nil {
			t.Fatalf("parseTTLSettings(%q): unexpected error: %v", raw, err)
		}
		if len(opts) != 6 {
			t.Fatalf("parseTTLSettings(%q): got %d options, want 6", raw, len(opts))
		}
		wantDurations := []time.Duration{
			time.Hour, 8 * time.Hour, 24 * time.Hour,
			72 * time.Hour, 7 * 24 * time.Hour, 30 * 24 * time.Hour,
		}
		for i, want := range wantDurations {
			if opts[i].Duration != want {
				t.Errorf("option[%d]: got duration %v, want %v", i, opts[i].Duration, want)
			}
		}
	})

	t.Run("short_form_values", func(t *testing.T) {
		raw := "1d,3d,7d"
		opts, err := parseTTLSettings(raw)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(opts) != 3 {
			t.Fatalf("got %d options, want 3", len(opts))
		}
		if opts[0].Duration != 24*time.Hour {
			t.Errorf("opts[0]: got %v, want 24h", opts[0].Duration)
		}
	})

	t.Run("empty_entries_are_skipped", func(t *testing.T) {
		// Leading/trailing/double commas should not cause errors.
		raw := ",1h,,8h,"
		opts, err := parseTTLSettings(raw)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(opts) != 2 {
			t.Fatalf("got %d options, want 2", len(opts))
		}
	})

	t.Run("all_invalid", func(t *testing.T) {
		_, err := parseTTLSettings("banana,potato")
		if err == nil {
			t.Error("expected error for all-invalid TTL string, got nil")
		}
	})

	t.Run("empty_string", func(t *testing.T) {
		_, err := parseTTLSettings("")
		if err == nil {
			t.Error("expected error for empty TTL string, got nil")
		}
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// parseByteSize
// ─────────────────────────────────────────────────────────────────────────────

func TestParseByteSize(t *testing.T) {
	const def = int64(1 << 30) // 1 GiB default used in production

	cases := []struct {
		input string
		want  int64
	}{
		// ── Suffix forms ─────────────────────────────────────────────────────
		{"1gb", 1 << 30},
		{"2gb", 2 << 30},
		{"512mb", 512 << 20},
		{"1mb", 1 << 20},
		{"256kb", 256 << 10},
		{"1024b", 1024},

		// ── Case insensitive ─────────────────────────────────────────────────
		{"1GB", 1 << 30},
		{"512MB", 512 << 20},

		// ── Plain integer (bytes) ─────────────────────────────────────────────
		{"1073741824", 1 << 30},
		{"0", 0},

		// ── Empty / invalid → default ─────────────────────────────────────────
		{"", def},
		{"banana", def},
		{"1tb", def}, // unknown suffix → default
		{"-1gb", def}, // negative
	}

	for _, tc := range cases {
		t.Run(tc.input, func(t *testing.T) {
			got := parseByteSize(tc.input, def)
			if got != tc.want {
				t.Errorf("parseByteSize(%q): got %d, want %d", tc.input, got, tc.want)
			}
		})
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// parseCIDRList
// ─────────────────────────────────────────────────────────────────────────────

func TestParseCIDRList(t *testing.T) {
	t.Run("single_ipv4_cidr", func(t *testing.T) {
		nets, err := parseCIDRList("192.168.0.0/24")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(nets) != 1 {
			t.Fatalf("got %d networks, want 1", len(nets))
		}
	})

	t.Run("multiple_cidrs", func(t *testing.T) {
		nets, err := parseCIDRList("192.168.0.0/24,10.0.0.0/8")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(nets) != 2 {
			t.Fatalf("got %d networks, want 2", len(nets))
		}
	})

	t.Run("plain_ipv4_becomes_slash32", func(t *testing.T) {
		nets, err := parseCIDRList("10.0.0.5")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(nets) != 1 {
			t.Fatalf("got %d networks, want 1", len(nets))
		}
		// /32 means only that exact IP is in the network.
		ip := net.ParseIP("10.0.0.5")
		if !nets[0].Contains(ip) {
			t.Errorf("network %v does not contain its own IP %v", nets[0], ip)
		}
		outsideIP := net.ParseIP("10.0.0.6")
		if nets[0].Contains(outsideIP) {
			t.Errorf("/32 network %v should not contain %v", nets[0], outsideIP)
		}
	})

	t.Run("plain_ipv6_becomes_slash128", func(t *testing.T) {
		nets, err := parseCIDRList("::1")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(nets) != 1 {
			t.Fatalf("got %d networks, want 1", len(nets))
		}
		if !nets[0].Contains(net.ParseIP("::1")) {
			t.Errorf("network %v does not contain ::1", nets[0])
		}
	})

	t.Run("empty_entries_skipped", func(t *testing.T) {
		nets, err := parseCIDRList(",192.168.1.0/24,,")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(nets) != 1 {
			t.Fatalf("got %d networks, want 1", len(nets))
		}
	})

	t.Run("empty_string_returns_nil", func(t *testing.T) {
		nets, err := parseCIDRList("")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if nets != nil {
			t.Errorf("expected nil for empty input, got %v", nets)
		}
	})

	t.Run("invalid_ip_returns_error", func(t *testing.T) {
		_, err := parseCIDRList("not.an.ip")
		if err == nil {
			t.Error("expected error for invalid IP, got nil")
		}
	})

	t.Run("invalid_cidr_returns_error", func(t *testing.T) {
		_, err := parseCIDRList("192.168.0.0/99")
		if err == nil {
			t.Error("expected error for invalid CIDR prefix length, got nil")
		}
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// IPAllowedToUpload / IPAllowedToDownload
// ─────────────────────────────────────────────────────────────────────────────

func TestIPAllowedToUpload(t *testing.T) {
	makeConfig := func(cidrs ...string) Config {
		var c Config
		if len(cidrs) > 0 {
			nets, err := parseCIDRList(cidrs[0])
			if err != nil {
				t.Fatalf("parseCIDRList: %v", err)
			}
			c.UploadIPs = nets
		}
		return c
	}

	t.Run("empty_list_allows_everyone", func(t *testing.T) {
		c := makeConfig()
		if !c.IPAllowedToUpload(net.ParseIP("1.2.3.4")) {
			t.Error("empty allowlist should permit all IPs")
		}
	})

	t.Run("ip_in_cidr_allowed", func(t *testing.T) {
		c := makeConfig("192.168.1.0/24")
		if !c.IPAllowedToUpload(net.ParseIP("192.168.1.42")) {
			t.Error("IP inside CIDR should be allowed")
		}
	})

	t.Run("ip_outside_cidr_denied", func(t *testing.T) {
		c := makeConfig("192.168.1.0/24")
		if c.IPAllowedToUpload(net.ParseIP("192.168.2.1")) {
			t.Error("IP outside CIDR should be denied")
		}
	})

	t.Run("exact_ip_match", func(t *testing.T) {
		c := makeConfig("10.0.0.5")
		if !c.IPAllowedToUpload(net.ParseIP("10.0.0.5")) {
			t.Error("exact IP should be allowed")
		}
		if c.IPAllowedToUpload(net.ParseIP("10.0.0.6")) {
			t.Error("different IP should be denied")
		}
	})

	t.Run("multiple_cidrs_any_match_allowed", func(t *testing.T) {
		nets, _ := parseCIDRList("10.0.0.0/8,172.16.0.0/12")
		c := Config{UploadIPs: nets}
		if !c.IPAllowedToUpload(net.ParseIP("10.1.2.3")) {
			t.Error("IP in first CIDR should be allowed")
		}
		if !c.IPAllowedToUpload(net.ParseIP("172.20.0.1")) {
			t.Error("IP in second CIDR should be allowed")
		}
		if c.IPAllowedToUpload(net.ParseIP("8.8.8.8")) {
			t.Error("IP in neither CIDR should be denied")
		}
	})
}

func TestIPAllowedToDownload(t *testing.T) {
	t.Run("empty_list_allows_everyone", func(t *testing.T) {
		c := Config{}
		if !c.IPAllowedToDownload(net.ParseIP("8.8.8.8")) {
			t.Error("empty download list should permit all IPs")
		}
	})

	t.Run("restricted_allows_only_match", func(t *testing.T) {
		nets, _ := parseCIDRList("192.168.0.0/16")
		c := Config{DownloadIPs: nets}
		if !c.IPAllowedToDownload(net.ParseIP("192.168.1.1")) {
			t.Error("IP inside CIDR should be allowed to download")
		}
		if c.IPAllowedToDownload(net.ParseIP("10.0.0.1")) {
			t.Error("IP outside CIDR should be denied download")
		}
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// DefaultTTLOptions
// ─────────────────────────────────────────────────────────────────────────────

func TestDefaultTTLOptions(t *testing.T) {
	opts := DefaultTTLOptions()

	if len(opts) == 0 {
		t.Fatal("DefaultTTLOptions returned empty slice")
	}

	// Verify options are in ascending order of duration.
	for i := 1; i < len(opts); i++ {
		if opts[i].Duration <= opts[i-1].Duration {
			t.Errorf("DefaultTTLOptions: option[%d] (%v) is not greater than option[%d] (%v)",
				i, opts[i].Duration, i-1, opts[i-1].Duration)
		}
	}

	// Verify every option has a non-empty label.
	for i, o := range opts {
		if o.Label == "" {
			t.Errorf("DefaultTTLOptions: option[%d] has empty label", i)
		}
		if o.Duration <= 0 {
			t.Errorf("DefaultTTLOptions: option[%d] has non-positive duration %v", i, o.Duration)
		}
	}
}
