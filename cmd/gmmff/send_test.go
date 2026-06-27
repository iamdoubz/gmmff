package main

import (
	"os"
	"path/filepath"
	"testing"
)

// TestExpandArgs covers glob expansion, literal passthrough, recursive
// matching, and the no-match / bad-pattern error paths.
func TestExpandArgs(t *testing.T) {
	dir := t.TempDir()
	mk := func(rel string) {
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	mk("a.txt")
	mk("b.txt")
	mk("c.md")
	mk("sub/deep.txt")

	// Non-recursive glob: only top-level .txt files.
	got, err := expandArgs([]string{filepath.Join(dir, "*.txt")}, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("non-recursive *.txt: got %d, want 2: %v", len(got), got)
	}

	// Recursive glob: includes sub/deep.txt.
	got, err = expandArgs([]string{filepath.Join(dir, "*.txt")}, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("recursive *.txt: got %d, want 3: %v", len(got), got)
	}

	// Literal existing path passes through untouched.
	lit := filepath.Join(dir, "c.md")
	got, err = expandArgs([]string{lit}, false)
	if err != nil || len(got) != 1 || got[0] != lit {
		t.Fatalf("literal passthrough: got %v err %v", got, err)
	}

	// No match is an error (avoids silently sending nothing).
	if _, err := expandArgs([]string{filepath.Join(dir, "*.zzz")}, false); err == nil {
		t.Fatal("expected error for no-match pattern")
	}

	// Bad pattern surfaces ErrBadPattern.
	if _, err := expandArgs([]string{filepath.Join(dir, "[")}, false); err == nil {
		t.Fatal("expected error for bad pattern")
	}
}
