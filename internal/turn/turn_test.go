package turn

import (
	"crypto/hmac"
	"crypto/sha1" //nolint:gosec
	"encoding/base64"
	"strconv"
	"strings"
	"testing"
	"time"
)

// ─────────────────────────────────────────────────────────────────────────────
// ParseOne — valid inputs
// ─────────────────────────────────────────────────────────────────────────────

func TestParseOne_ValidInputs(t *testing.T) {
	cases := []struct {
		name     string
		raw      string
		wantURL  string
		wantUser string // "" means check that it is non-empty (ephemeral)
		wantPass string // "" means check that it is non-empty (ephemeral)
		static   bool  // true = credentials are static (exact match expected)
	}{
		{
			name:     "static_credentials_udp",
			raw:      "turn:relay.example.com:3478?user=alice&pass=secret123",
			wantURL:  "turn:relay.example.com:3478",
			wantUser: "alice",
			wantPass: "secret123",
			static:   true,
		},
		{
			name:     "static_credentials_with_transport_tcp",
			raw:      "turn:relay.example.com:3478?transport=tcp&user=bob&pass=hunter2",
			wantURL:  "turn:relay.example.com:3478?transport=tcp",
			wantUser: "bob",
			wantPass: "hunter2",
			static:   true,
		},
		{
			name:    "ephemeral_credentials_udp",
			raw:     "turn:relay.example.com:3478?transport=udp&secret=mysecret",
			wantURL: "turn:relay.example.com:3478?transport=udp",
			static:  false,
		},
		{
			name:    "ephemeral_credentials_no_transport",
			raw:     "turn:relay.example.com:3478?secret=mysecret",
			wantURL: "turn:relay.example.com:3478",
			static:  false,
		},
		{
			name:     "turns_scheme_tcp",
			raw:      "turns:relay.example.com:5349?transport=tcp&user=u&pass=p",
			wantURL:  "turns:relay.example.com:5349?transport=tcp",
			wantUser: "u",
			wantPass: "p",
			static:   true,
		},
		{
			name:     "no_transport_specified",
			raw:      "turn:relay.example.com:3478?user=u&pass=p",
			wantURL:  "turn:relay.example.com:3478",
			wantUser: "u",
			wantPass: "p",
			static:   true,
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			srv, err := ParseOne(tc.raw)
			if err != nil {
				t.Fatalf("ParseOne(%q): unexpected error: %v", tc.raw, err)
			}
			if srv.URL != tc.wantURL {
				t.Errorf("URL: got %q, want %q", srv.URL, tc.wantURL)
			}
			if tc.static {
				if srv.Username != tc.wantUser {
					t.Errorf("Username: got %q, want %q", srv.Username, tc.wantUser)
				}
				if srv.Password != tc.wantPass {
					t.Errorf("Password: got %q, want %q", srv.Password, tc.wantPass)
				}
			} else {
				// Ephemeral: credentials are derived — verify they are non-empty.
				if srv.Username == "" {
					t.Error("Username should not be empty for ephemeral credentials")
				}
				if srv.Password == "" {
					t.Error("Password should not be empty for ephemeral credentials")
				}
				// Ephemeral username must contain a colon separating expiry and user.
				if !strings.Contains(srv.Username, ":") {
					t.Errorf("Ephemeral username %q should contain ':'", srv.Username)
				}
			}
		})
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// ParseOne — invalid inputs
// ─────────────────────────────────────────────────────────────────────────────

func TestParseOne_InvalidInputs(t *testing.T) {
	cases := []struct {
		name string
		raw  string
	}{
		{"empty_string", ""},
		{"whitespace_only", "   "},
		{"wrong_scheme_http", "http://relay.example.com:3478"},
		{"wrong_scheme_stun", "stun:relay.example.com:3478"},
		{"no_credentials", "turn:relay.example.com:3478"},
		{"user_without_pass", "turn:relay.example.com:3478?user=alice"},
		{"pass_without_user", "turn:relay.example.com:3478?pass=secret"},
		{"both_static_and_ephemeral", "turn:relay.example.com:3478?user=u&pass=p&secret=s"},
		{"invalid_transport", "turn:relay.example.com:3478?transport=sctp&user=u&pass=p"},
		{"turns_with_udp_transport", "turns:relay.example.com:5349?transport=udp&user=u&pass=p"},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseOne(tc.raw)
			if err == nil {
				t.Errorf("ParseOne(%q): expected error, got nil", tc.raw)
			}
		})
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// ParseOne — whitespace tolerance
// ─────────────────────────────────────────────────────────────────────────────

func TestParseOne_WhitespaceTrimmed(t *testing.T) {
	raw := "  turn:relay.example.com:3478?user=u&pass=p  "
	srv, err := ParseOne(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if srv.URL != "turn:relay.example.com:3478" {
		t.Errorf("URL: got %q, want %q", srv.URL, "turn:relay.example.com:3478")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// ParseAll — count limits
// ─────────────────────────────────────────────────────────────────────────────

func TestParseAll_MaxServers(t *testing.T) {
	valid := "turn:relay.example.com:3478?user=u&pass=p"

	t.Run("at_limit_succeeds", func(t *testing.T) {
		raw := make([]string, MaxServers)
		for i := range raw {
			raw[i] = valid
		}
		_, err := ParseAll(raw)
		if err != nil {
			t.Fatalf("unexpected error at limit: %v", err)
		}
	})

	t.Run("over_limit_returns_error", func(t *testing.T) {
		raw := make([]string, MaxServers+1)
		for i := range raw {
			raw[i] = valid
		}
		_, err := ParseAll(raw)
		if err == nil {
			t.Error("expected error for too many servers, got nil")
		}
	})

	t.Run("nil_input_returns_nil", func(t *testing.T) {
		servers, err := ParseAll(nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if servers != nil {
			t.Errorf("expected nil for nil input, got %v", servers)
		}
	})

	t.Run("empty_slice_returns_nil", func(t *testing.T) {
		servers, err := ParseAll([]string{})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if servers != nil {
			t.Errorf("expected nil for empty input, got %v", servers)
		}
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// ParseAllWithTTL — shorter TTL is honoured
// ─────────────────────────────────────────────────────────────────────────────

func TestParseAllWithTTL(t *testing.T) {
	raw := []string{"turn:relay.example.com:3478?secret=mysecret"}
	ttl := 30 * time.Minute

	servers, err := ParseAllWithTTL(raw, ttl)
	if err != nil {
		t.Fatalf("ParseAllWithTTL: unexpected error: %v", err)
	}
	if len(servers) != 1 {
		t.Fatalf("got %d servers, want 1", len(servers))
	}

	// The ephemeral username encodes the expiry timestamp as the first field.
	// Parse it and verify it's approximately now + ttl (within a 10-second window).
	parts := strings.SplitN(servers[0].Username, ":", 2)
	if len(parts) != 2 {
		t.Fatalf("username %q: expected 'expiry:user' format", servers[0].Username)
	}
	expiry, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		t.Fatalf("username %q: cannot parse expiry: %v", servers[0].Username, err)
	}
	now := time.Now().Unix()
	expected := now + int64(ttl.Seconds())
	if expiry < expected-10 || expiry > expected+10 {
		t.Errorf("expiry %d is not within 10s of expected %d (now=%d, ttl=%v)",
			expiry, expected, now, ttl)
	}

	// Verify the default (24h) TTL produces a later expiry.
	longServers, _ := ParseAll(raw)
	longParts := strings.SplitN(longServers[0].Username, ":", 2)
	longExpiry, _ := strconv.ParseInt(longParts[0], 10, 64)

	if longExpiry <= expiry {
		t.Errorf("24h TTL expiry (%d) should be greater than 30m TTL expiry (%d)",
			longExpiry, expiry)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// ephemeralCredentials — RFC 8489 §9.2 compliance
// ─────────────────────────────────────────────────────────────────────────────

func TestEphemeralCredentials_RFC8489(t *testing.T) {
	// Fix time so the test is deterministic.
	ttl := time.Hour
	secret := "test-secret"
	user := "gmmff"

	username, password := ephemeralCredentials(secret, user, ttl)

	// ── Username format: "<expiry>:<user>" ────────────────────────────────────
	parts := strings.SplitN(username, ":", 2)
	if len(parts) != 2 {
		t.Fatalf("username %q: expected 'expiry:user' format", username)
	}
	if parts[1] != user {
		t.Errorf("username user part: got %q, want %q", parts[1], user)
	}
	expiry, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		t.Fatalf("cannot parse expiry from username %q: %v", username, err)
	}

	// Expiry should be approximately now + ttl.
	now := time.Now().Unix()
	if expiry < now || expiry > now+int64(ttl.Seconds())+5 {
		t.Errorf("expiry %d is not in expected range [%d, %d]",
			expiry, now, now+int64(ttl.Seconds())+5)
	}

	// ── Password: base64(HMAC-SHA1(secret, username)) ─────────────────────────
	mac := hmac.New(sha1.New, []byte(secret)) //nolint:gosec
	mac.Write([]byte(username))
	expectedPassword := base64.StdEncoding.EncodeToString(mac.Sum(nil))

	if password != expectedPassword {
		t.Errorf("password: got %q, want %q", password, expectedPassword)
	}
}

func TestEphemeralCredentials_DifferentSecretsProduceDifferentPasswords(t *testing.T) {
	_, pass1 := ephemeralCredentials("secret-a", "user", time.Hour)
	_, pass2 := ephemeralCredentials("secret-b", "user", time.Hour)
	if pass1 == pass2 {
		t.Error("different secrets should produce different passwords")
	}
}

func TestEphemeralCredentials_NonEmpty(t *testing.T) {
	u, p := ephemeralCredentials("s", "u", time.Hour)
	if u == "" {
		t.Error("username should not be empty")
	}
	if p == "" {
		t.Error("password should not be empty")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// ICEServers — Pion conversion
// ─────────────────────────────────────────────────────────────────────────────

func TestICEServers(t *testing.T) {
	t.Run("empty_returns_empty", func(t *testing.T) {
		ice := ICEServers(nil)
		if len(ice) != 0 {
			t.Errorf("expected empty slice, got %v", ice)
		}
	})

	t.Run("fields_mapped_correctly", func(t *testing.T) {
		servers := []Server{
			{URL: "turn:relay.example.com:3478", Username: "user1", Password: "pass1"},
			{URL: "turns:relay.example.com:5349?transport=tcp", Username: "user2", Password: "pass2"},
		}
		ice := ICEServers(servers)
		if len(ice) != 2 {
			t.Fatalf("got %d ICE servers, want 2", len(ice))
		}
		if ice[0].URLs[0] != servers[0].URL {
			t.Errorf("ICE[0].URLs[0]: got %q, want %q", ice[0].URLs[0], servers[0].URL)
		}
		if ice[0].Username != servers[0].Username {
			t.Errorf("ICE[0].Username: got %q, want %q", ice[0].Username, servers[0].Username)
		}
		if ice[0].Credential != servers[0].Password {
			t.Errorf("ICE[0].Credential: got %q, want %q", ice[0].Credential, servers[0].Password)
		}
	})
}
