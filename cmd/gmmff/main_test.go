package main

import "testing"

// TestNormalizeCacheURL verifies that Valkey URL schemes are rewritten to the
// Redis schemes redis.ParseURL accepts, while every other URL passes through
// untouched. Valkey is wire-compatible, so this is purely a cosmetic alias.
func TestNormalizeCacheURL(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"valkey scheme rewritten", "valkey://localhost:6379/0", "redis://localhost:6379/0"},
		{"valkeys TLS scheme rewritten", "valkeys://cache:6380/1", "rediss://cache:6380/1"},
		{"valkey with credentials", "valkey://user:pass@host:6379/0", "redis://user:pass@host:6379/0"},
		{"redis passes through", "redis://localhost:6379/0", "redis://localhost:6379/0"},
		{"rediss passes through", "rediss://localhost:6379/0", "rediss://localhost:6379/0"},
		{"unix socket passes through", "unix:///var/run/redis.sock", "unix:///var/run/redis.sock"},
		{"empty passes through", "", ""},
		{"substring not at prefix untouched", "redis://valkey-host:6379/0", "redis://valkey-host:6379/0"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := normalizeCacheURL(tc.in); got != tc.want {
				t.Errorf("normalizeCacheURL(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}
