package schedule

import (
	"bytes"
	"context"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// ─────────────────────────────────────────────────────────────────────────────
// Group 1: Pure URL parsers
// ─────────────────────────────────────────────────────────────────────────────

func TestParseDeleteURL(t *testing.T) {
	tests := []struct {
		name      string
		input     string
		wantID    string
		wantDK    string
		wantError bool
	}{
		{"valid", "https://host/?type=schedule&id=abc123&action=delete&dk=deadbeef", "abc123", "deadbeef", false},
		{"with fragment", "https://host/?id=abc&dk=ff#key=ignored", "abc", "ff", false},
		{"missing id", "https://host/?dk=ff", "", "", true},
		{"missing dk", "https://host/?id=abc", "", "", true},
		{"empty string", "", "", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			id, dk, err := ParseDeleteURL(tt.input)
			if tt.wantError {
				if err == nil {
					t.Fatal("expected error")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if id != tt.wantID || dk != tt.wantDK {
				t.Errorf("got (%q, %q), want (%q, %q)", id, dk, tt.wantID, tt.wantDK)
			}
		})
	}
}

func TestParseShareURL(t *testing.T) {
	validKey := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	tests := []struct {
		name      string
		input     string
		wantID    string
		wantKey   string
		wantError bool
	}{
		{"valid", "https://host/?type=schedule&id=abc123#key=" + validKey, "abc123", validKey, false},
		{"missing key", "https://host/?id=abc123", "", "", true},
		{"missing id", "https://host/#key=" + validKey, "", "", true},
		{"invalid hex key", "https://host/?id=abc#key=not-hex!", "", "", true},
		{"empty string", "", "", "", true},
		{"no hash", "https://host/?id=abc", "", "", true},
		{"extra query", "https://host/?id=abc&other=val#key=" + validKey, "abc", validKey, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			id, key, err := ParseShareURL(tt.input)
			if tt.wantError {
				if err == nil {
					t.Fatal("expected error")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if id != tt.wantID || key != tt.wantKey {
				t.Errorf("got (%q, %q), want (%q, %q)", id, key, tt.wantID, tt.wantKey)
			}
		})
	}
}

func TestSelectChunkSize(t *testing.T) {
	tests := []struct {
		fileSize int64
		want     int
	}{
		{1024, 256 * 1024},
		{5 * 1024 * 1024, 256 * 1024},
		{10*1024*1024 - 1, 256 * 1024},
		{10 * 1024 * 1024, 512 * 1024},
		{50 * 1024 * 1024, 512 * 1024},
		{100*1024*1024 - 1, 512 * 1024},
		{100 * 1024 * 1024, 1024 * 1024},
		{500 * 1024 * 1024, 1024 * 1024},
	}
	for _, tt := range tests {
		got := selectChunkSize(tt.fileSize)
		if got != tt.want {
			t.Errorf("selectChunkSize(%d) = %d, want %d", tt.fileSize, got, tt.want)
		}
	}
}

func TestNewClient(t *testing.T) {
	c, err := NewClient("wss://example.com/ws")
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if c.BaseURL != "https://example.com" {
		t.Errorf("BaseURL = %q, want https://example.com", c.BaseURL)
	}
}

func TestNewClient_BadScheme(t *testing.T) {
	_, err := NewClient("ftp://example.com")
	if err == nil {
		t.Fatal("expected error for unsupported scheme")
	}
}

func TestNormaliseServerURL_Empty(t *testing.T) {
	_, err := NormaliseServerURL("")
	if err == nil {
		t.Fatal("expected error for empty URL")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Group 2: Client ↔ Handler round-trip
// ─────────────────────────────────────────────────────────────────────────────

// newRoundtripSuite starts an httptest.Server with the real schedule handler
// and returns a Client pointed at it.
func newRoundtripSuite(t *testing.T, cfgFn func(*Config)) (*Client, *httptest.Server) {
	t.Helper()
	_, handler := newTestHandler(t, cfgFn)
	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)

	c := &Client{
		BaseURL:    ts.URL,
		HTTPClient: ts.Client(),
	}
	return c, ts
}

func TestRoundtrip_CheckAuth_NoRestrictions(t *testing.T) {
	c, _ := newRoundtripSuite(t, nil)
	status, err := c.CheckAuth(context.Background())
	if err != nil {
		t.Fatalf("CheckAuth: %v", err)
	}
	if !status.Allowed {
		t.Error("should be allowed with no IP or password restrictions")
	}
	if status.NeedsPassword {
		t.Error("should not need password when none configured")
	}
}

func TestRoundtrip_CheckAuth_PasswordRequired(t *testing.T) {
	c, _ := newRoundtripSuite(t, func(cfg *Config) {
		cfg.UploadPassword = "secret123"
	})
	status, err := c.CheckAuth(context.Background())
	if err != nil {
		t.Fatalf("CheckAuth: %v", err)
	}
	if status.Allowed {
		t.Error("should not be allowed without password")
	}
	if !status.NeedsPassword {
		t.Error("should report password needed")
	}
}

func TestRoundtrip_UploadDownloadDelete(t *testing.T) {
	c, _ := newRoundtripSuite(t, nil)
	ctx := context.Background()
	plaintext := []byte("Hello, this is a secret file for testing the full round-trip!")
	filename := "test-secret.txt"

	// ── Upload ──────────────────────────────────────────────────────────────
	var progressCalls int
	result, err := c.Upload(ctx, bytes.NewReader(plaintext), filename, int64(len(plaintext)), UploadOptions{
		TTL:          1 * time.Hour,
		MaxDownloads: 5,
		ChunkSize:    256 * 1024,
		Progress:     func(_, _ int64, _ float64) { progressCalls++ },
	})
	if err != nil {
		t.Fatalf("Upload: %v", err)
	}
	if result.FileID == "" {
		t.Fatal("FileID is empty")
	}
	if result.KeyHex == "" {
		t.Fatal("KeyHex is empty")
	}
	if result.DeleteKey == "" {
		t.Fatal("DeleteKey is empty")
	}
	if !strings.Contains(result.FullURL, "#key=") {
		t.Errorf("FullURL missing key fragment: %s", result.FullURL)
	}
	if progressCalls == 0 {
		t.Error("progress callback never called")
	}

	// ── FetchMeta ───────────────────────────────────────────────────────────
	meta, err := c.FetchMeta(ctx, result.FileID)
	if err != nil {
		t.Fatalf("FetchMeta: %v", err)
	}
	if meta.FileID != result.FileID {
		t.Errorf("meta.FileID = %q, want %q", meta.FileID, result.FileID)
	}
	if meta.TotalSize != int64(len(plaintext)) {
		t.Errorf("meta.TotalSize = %d, want %d", meta.TotalSize, len(plaintext))
	}

	// ── Download ────────────────────────────────────────────────────────────
	var buf bytes.Buffer
	dlResult, err := c.Download(ctx, result.FileID, result.KeyHex, meta, &buf, DownloadOptions{})
	if err != nil {
		t.Fatalf("Download: %v", err)
	}
	if dlResult.Filename != filename {
		t.Errorf("Filename = %q, want %q", dlResult.Filename, filename)
	}
	if !bytes.Equal(buf.Bytes(), plaintext) {
		t.Errorf("downloaded content mismatch: got %d bytes, want %d", buf.Len(), len(plaintext))
	}

	// ── Delete ──────────────────────────────────────────────────────────────
	if err := c.Delete(ctx, result.FileID, result.DeleteKey); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	// Verify gone.
	_, err = c.FetchMeta(ctx, result.FileID)
	if err == nil {
		t.Error("FetchMeta should fail after delete")
	}
}

func TestRoundtrip_FetchMeta_NotFound(t *testing.T) {
	c, _ := newRoundtripSuite(t, nil)
	_, err := c.FetchMeta(context.Background(), "nonexistent-id")
	if err == nil {
		t.Fatal("expected error for nonexistent file")
	}
}

func TestRoundtrip_Delete_InvalidKey(t *testing.T) {
	c, _ := newRoundtripSuite(t, nil)
	ctx := context.Background()

	// Upload a file first.
	result, err := c.Upload(ctx, bytes.NewReader([]byte("x")), "x.txt", 1, UploadOptions{
		TTL:       1 * time.Hour,
		ChunkSize: 256 * 1024,
	})
	if err != nil {
		t.Fatalf("Upload: %v", err)
	}

	// Try deleting with wrong key.
	err = c.Delete(ctx, result.FileID, "wrong-key")
	if err == nil {
		t.Fatal("expected error for invalid delete key")
	}
}

func TestRoundtrip_Delete_NotFound(t *testing.T) {
	c, _ := newRoundtripSuite(t, nil)
	err := c.Delete(context.Background(), "nonexistent", "somekey")
	if err == nil {
		t.Fatal("expected error for nonexistent file")
	}
}

func TestRoundtrip_Download_WrongKey(t *testing.T) {
	c, _ := newRoundtripSuite(t, nil)
	ctx := context.Background()

	result, err := c.Upload(ctx, bytes.NewReader([]byte("secret")), "s.txt", 6, UploadOptions{
		TTL:       1 * time.Hour,
		ChunkSize: 256 * 1024,
	})
	if err != nil {
		t.Fatalf("Upload: %v", err)
	}

	meta, err := c.FetchMeta(ctx, result.FileID)
	if err != nil {
		t.Fatalf("FetchMeta: %v", err)
	}

	// Use a valid-length but wrong key.
	wrongKey := strings.Repeat("ab", 32)
	var buf bytes.Buffer
	_, err = c.Download(ctx, result.FileID, wrongKey, meta, &buf, DownloadOptions{})
	if err == nil {
		t.Fatal("expected decryption error with wrong key")
	}
}

func TestRoundtrip_Upload_WithPassword(t *testing.T) {
	c, _ := newRoundtripSuite(t, func(cfg *Config) {
		cfg.UploadPassword = "correcthorsebatterystaple"
	})
	ctx := context.Background()

	// Without password — should fail.
	_, err := c.Upload(ctx, bytes.NewReader([]byte("x")), "x.txt", 1, UploadOptions{
		TTL:       1 * time.Hour,
		ChunkSize: 256 * 1024,
	})
	if err == nil {
		t.Fatal("expected error uploading without password")
	}

	// With correct password — should succeed.
	c.Password = "correcthorsebatterystaple"
	result, err := c.Upload(ctx, bytes.NewReader([]byte("x")), "x.txt", 1, UploadOptions{
		TTL:       1 * time.Hour,
		ChunkSize: 256 * 1024,
	})
	if err != nil {
		t.Fatalf("Upload with password: %v", err)
	}
	if result.FileID == "" {
		t.Error("FileID empty after authenticated upload")
	}
}
