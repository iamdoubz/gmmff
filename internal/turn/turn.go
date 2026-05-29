// Package turn handles TURN server configuration for gmmff.
//
// # URL format
//
// Each TURN server is specified as a single string with auth embedded as
// query parameters:
//
//	turn:host:port[?param=value&...]
//	turns:host:port[?param=value&...]
//
// # Supported query parameters
//
//	transport=udp|tcp   restrict ICE to a specific transport (optional)
//	user=<username>     long-term credential username
//	pass=<password>     long-term credential password
//	secret=<value>      ephemeral credential static-auth-secret
//
// Long-term and ephemeral auth are mutually exclusive per server entry.
// Mixing auth types across servers is fully supported.
//
// # Ephemeral credentials
//
// When secret= is provided, HMAC-SHA1 credentials are derived per RFC 8489 §9.2:
//
//	username = "<unix_expiry>:<user>"   (user defaults to "gmmff")
//	password = base64(HMAC-SHA1(secret, username))
//	ttl      = 24 hours
//
// # Examples
//
//	turn:local.host:3478?transport=udp&secret=abc
//	turns:paid.host:5349?transport=tcp&user=alice&pass=xyz
//	turn:host:3478?user=bob&pass=hunter2
package turn

import (
	"crypto/hmac"
	"crypto/sha1" //nolint:gosec // RFC 8489 mandates HMAC-SHA1 for TURN ephemeral creds
	"encoding/base64"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/pion/webrtc/v4"
)

// MaxServers is the maximum number of TURN servers allowed.
const MaxServers = 3

// EphemeralTTL is how long ephemeral credentials remain valid.
const EphemeralTTL = 24 * time.Hour

// Server is a fully parsed and validated TURN server entry.
type Server struct {
	// URL is the base TURN URL including transport parameter if specified.
	// e.g. "turn:host:3478" or "turn:host:3478?transport=udp"
	URL string

	// Username and Password are the ICE credentials.
	// For long-term auth these come directly from the config.
	// For ephemeral auth these are derived from the static secret.
	Username string
	Password string
}

// ParseAll parses a slice of raw TURN strings into Server entries.
// Returns an error if any entry is malformed or the count exceeds MaxServers.
func ParseAll(raw []string) ([]Server, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	if len(raw) > MaxServers {
		return nil, fmt.Errorf("turn: too many servers — maximum is %d, got %d", MaxServers, len(raw))
	}
	servers := make([]Server, 0, len(raw))
	for _, r := range raw {
		s, err := parse(r)
		if err != nil {
			return nil, err
		}
		servers = append(servers, s)
	}
	return servers, nil
}

// ParseAllWithTTL is like ParseAll but uses a custom TTL for ephemeral credentials.
// Use this for server-push scenarios where shorter-lived credentials are preferred.
func ParseAllWithTTL(raw []string, ttl time.Duration) ([]Server, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	if len(raw) > MaxServers {
		return nil, fmt.Errorf("turn: too many servers — maximum is %d, got %d", MaxServers, len(raw))
	}
	servers := make([]Server, 0, len(raw))
	for _, r := range raw {
		s, err := parseWithTTL(r, ttl)
		if err != nil {
			return nil, err
		}
		servers = append(servers, s)
	}
	return servers, nil
}
// one per TURN server (each needs its own credentials).
func ICEServers(servers []Server) []webrtc.ICEServer {
	ice := make([]webrtc.ICEServer, 0, len(servers))
	for _, s := range servers {
		ice = append(ice, webrtc.ICEServer{
			URLs:       []string{s.URL},
			Username:   s.Username,
			Credential: s.Password,
		})
	}
	return ice
}

// ─────────────────────────────────────────────────────────────────────────────
// Parsing
// ─────────────────────────────────────────────────────────────────────────────

// ParseOne parses and validates a single raw TURN URL string.
// The format is described in the package documentation.
func ParseOne(raw string) (Server, error) {
	return parseWithTTL(raw, EphemeralTTL)
}

// parse is the internal implementation using the default TTL.
func parse(raw string) (Server, error) {
	return parseWithTTL(raw, EphemeralTTL)
}

// parseWithTTL is the full implementation with configurable TTL.
func parseWithTTL(raw string, ttl time.Duration) (Server, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return Server{}, fmt.Errorf("turn: empty server string")
	}

	// Split on first '?' to separate the base URL from our custom params.
	baseURL, paramStr, _ := strings.Cut(raw, "?")
	baseURL = strings.ToLower(strings.TrimSpace(baseURL))

	if !strings.HasPrefix(baseURL, "turn:") && !strings.HasPrefix(baseURL, "turns:") {
		return Server{}, fmt.Errorf("turn: URL must begin with turn: or turns: — got %q", baseURL)
	}

	// Parse query parameters.
	var params url.Values
	if paramStr != "" {
		var err error
		params, err = url.ParseQuery(paramStr)
		if err != nil {
			return Server{}, fmt.Errorf("turn: parse params for %q: %w", baseURL, err)
		}
	}

	transport := strings.ToLower(params.Get("transport"))
	user      := params.Get("user")
	pass      := params.Get("pass")
	secret    := params.Get("secret")

	// Validate transport value.
	if transport != "" && transport != "udp" && transport != "tcp" {
		return Server{}, fmt.Errorf("turn: invalid transport %q for %q — must be udp or tcp", transport, baseURL)
	}

	// turns: is always TLS/TCP — warn if transport=udp is requested.
	if strings.HasPrefix(baseURL, "turns:") && transport == "udp" {
		return Server{}, fmt.Errorf("turn: turns: scheme requires TCP/TLS — transport=udp is not valid for %q", baseURL)
	}

	// Validate auth — must have exactly one of long-term or ephemeral.
	hasLongTerm := user != "" || pass != ""
	hasEphemeral := secret != ""

	if !hasLongTerm && !hasEphemeral {
		return Server{}, fmt.Errorf("turn: %q has no credentials — provide user+pass or secret", baseURL)
	}
	if hasLongTerm && hasEphemeral {
		return Server{}, fmt.Errorf("turn: %q has both user/pass and secret — choose one auth type", baseURL)
	}
	if hasLongTerm && (user == "" || pass == "") {
		return Server{}, fmt.Errorf("turn: %q has user or pass but not both", baseURL)
	}

	// Build the final URL — only transport belongs in it, not auth params.
	finalURL := baseURL
	if transport != "" {
		finalURL = baseURL + "?transport=" + transport
	}

	// Resolve credentials.
	var username, password string
	if hasEphemeral {
		username, password = ephemeralCredentials(secret, "gmmff", ttl)
	} else {
		username = user
		password = pass
	}

	return Server{URL: finalURL, Username: username, Password: password}, nil
}

// ─────────────────────────────────────────────────────────────────────────────
// Ephemeral credential derivation — RFC 8489 §9.2
// ─────────────────────────────────────────────────────────────────────────────

// ephemeralCredentials derives TURN credentials from a static-auth-secret.
// The algorithm is specified in RFC 8489 §9.2 and implemented identically
// by coturn's REST API mode:
//
//	username = "<unix_expiry>:<user>"
//	password = base64(HMAC-SHA1(secret, username))
func ephemeralCredentials(secret, user string, ttl time.Duration) (username, password string) {
	expiry := time.Now().Add(ttl).Unix()
	username = strconv.FormatInt(expiry, 10) + ":" + user

	mac := hmac.New(sha1.New, []byte(secret)) //nolint:gosec
	mac.Write([]byte(username))
	password = base64.StdEncoding.EncodeToString(mac.Sum(nil))
	return
}
