package schedule

import (
	"encoding/json"
	"testing"
)

// TestAuthStatus_DecodesServerJSON pins the wire contract between the server's
// authResponse and the client's AuthStatus. Without JSON tags on AuthStatus the
// snake_case "needs_password" key silently fails to decode (Go's tagless field
// matching is case-insensitive but does not bridge the underscore), which broke
// the CLI's password-required upload path — the client saw NeedsPassword=false
// and wrongly reported "upload not permitted from your IP address".
func TestAuthStatus_DecodesServerJSON(t *testing.T) {
	// Exact shape the server emits (see handler.go authResponse).
	const body = `{"allowed":false,"needs_password":true}`
	var got AuthStatus
	if err := json.Unmarshal([]byte(body), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Allowed {
		t.Error("Allowed: got true, want false")
	}
	if !got.NeedsPassword {
		t.Error("NeedsPassword: got false, want true — \"needs_password\" failed to decode")
	}
}

// TestNormaliseServerURL pins that a base URL is produced with no query or
// fragment, even when callers pass a full share/delete URL as the server arg.
// Keeping the query/fragment corrupted every subsequent /api/... request (the
// real path resolved to "/", which the SPA answered with HTML), breaking CLI
// download and delete.
func TestNormaliseServerURL(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"plain http base", "http://host:8099", "http://host:8099"},
		{"trailing slash trimmed", "http://host:8099/", "http://host:8099"},
		{"ws scheme + /ws path", "ws://host:8099/ws", "http://host:8099"},
		{"wss scheme + /ws path", "wss://host/ws", "https://host"},
		{"full share URL drops query+fragment",
			"http://host:8099/?type=schedule&id=abc#key=deadbeef", "http://host:8099"},
		{"delete URL drops query",
			"https://host/?type=schedule&id=abc&action=delete&dk=ff", "https://host"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := NormaliseServerURL(tc.in)
			if err != nil {
				t.Fatalf("NormaliseServerURL(%q): %v", tc.in, err)
			}
			if got != tc.want {
				t.Errorf("NormaliseServerURL(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestAuthStatus_RoundTripsWithServerType guards that the server's authResponse
// and the client's AuthStatus agree on JSON keys, so a value marshalled by one
// decodes correctly into the other.
func TestAuthStatus_RoundTripsWithServerType(t *testing.T) {
	srv := authResponse{Allowed: true, NeedsPassword: true}
	b, err := json.Marshal(srv)
	if err != nil {
		t.Fatalf("marshal authResponse: %v", err)
	}
	var cli AuthStatus
	if err := json.Unmarshal(b, &cli); err != nil {
		t.Fatalf("unmarshal into AuthStatus: %v", err)
	}
	if cli.Allowed != srv.Allowed || cli.NeedsPassword != srv.NeedsPassword {
		t.Errorf("round-trip mismatch: server %+v → client %+v", srv, cli)
	}
}
