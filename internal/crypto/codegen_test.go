package crypto

import (
	"strings"
	"testing"
)

// ─────────────────────────────────────────────────────────────────────────────
// GenerateCode
// ─────────────────────────────────────────────────────────────────────────────

func TestGenerateCode_Format(t *testing.T) {
	for i := 0; i < 50; i++ {
		code, err := GenerateCode()
		if err != nil {
			t.Fatalf("GenerateCode() error: %v", err)
		}
		parts := strings.Split(code, "-")
		if len(parts) != 3 {
			t.Errorf("GenerateCode() = %q: want exactly 3 words separated by '-', got %d parts",
				code, len(parts))
		}
		for j, p := range parts {
			if len(p) < 2 {
				t.Errorf("GenerateCode() = %q: word[%d] %q is shorter than 2 chars", code, j, p)
			}
			if len(p) > 12 {
				t.Errorf("GenerateCode() = %q: word[%d] %q is longer than 12 chars", code, j, p)
			}
		}
	}
}

func TestGenerateCode_WordsFromWordlist(t *testing.T) {
	// Build a set for O(1) lookup.
	known := make(map[string]bool, len(wordlist))
	for _, w := range wordlist {
		known[w] = true
	}

	for i := 0; i < 100; i++ {
		code, err := GenerateCode()
		if err != nil {
			t.Fatalf("GenerateCode() error: %v", err)
		}
		for _, word := range strings.Split(code, "-") {
			if !known[word] {
				t.Errorf("GenerateCode(): word %q not in wordlist", word)
			}
		}
	}
}

func TestGenerateCode_NoDuplicateCodesEveryRun(t *testing.T) {
	// 100 codes should all be unique with overwhelming probability.
	// The space is ~4 billion; a collision in 100 tries is vanishingly unlikely.
	seen := make(map[string]bool, 100)
	for i := 0; i < 100; i++ {
		code, err := GenerateCode()
		if err != nil {
			t.Fatalf("GenerateCode() error: %v", err)
		}
		if seen[code] {
			t.Errorf("GenerateCode() produced duplicate code %q after %d iterations", code, i)
		}
		seen[code] = true
	}
}

func TestGenerateCode_NoError(t *testing.T) {
	// Calling GenerateCode many times should never return an error.
	for i := 0; i < 200; i++ {
		_, err := GenerateCode()
		if err != nil {
			t.Fatalf("GenerateCode() iteration %d: unexpected error: %v", i, err)
		}
	}
}

func TestGenerateCode_ValidateAcceptsEveryGeneratedCode(t *testing.T) {
	// Every code GenerateCode produces must pass ValidateCode.
	for i := 0; i < 100; i++ {
		code, err := GenerateCode()
		if err != nil {
			t.Fatalf("GenerateCode(): %v", err)
		}
		if !ValidateCode(code) {
			t.Errorf("ValidateCode(%q) = false for a generated code", code)
		}
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// ValidateCode
// ─────────────────────────────────────────────────────────────────────────────

func TestValidateCode_ValidInputs(t *testing.T) {
	cases := []string{
		"bear-cozy-cone",
		"acid-aged-also",
		"ab-cd-ef",                               // minimum word length (2 chars each)
		"abcdefghijkl-abcdefghijkl-abcdefghijkl", // maximum word length (12 chars each)
	}
	for _, c := range cases {
		if !ValidateCode(c) {
			t.Errorf("ValidateCode(%q) = false, want true", c)
		}
	}
}

func TestValidateCode_InvalidInputs(t *testing.T) {
	cases := []struct {
		code string
		name string
	}{
		{"", "empty string"},
		{"single", "single word"},
		{"two-words", "two words"},
		{"a-b-c-d", "four words"},
		{"a-b-c", "words too short (single char)"},
		{"abcdefghijklm-word-word", "first word too long (13 chars)"},
		{"word-word-", "trailing dash"},
		{"-word-word", "leading dash"},
		{"word--word-word", "double dash"},
		{"bear cozy cone", "spaces instead of dashes"},
		{"bear_cozy_cone", "underscores instead of dashes"},
	}
	for _, tc := range cases {
		if ValidateCode(tc.code) {
			t.Errorf("ValidateCode(%q) = true, want false (%s)", tc.code, tc.name)
		}
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Wordlist integrity
// ─────────────────────────────────────────────────────────────────────────────

func TestWordlist_Length(t *testing.T) {
	if len(wordlist) != 2048 {
		t.Errorf("wordlist length = %d, want 2048", len(wordlist))
	}
}

func TestWordlist_NoDuplicates(t *testing.T) {
	seen := make(map[string]int, len(wordlist))
	for i, w := range wordlist {
		if prev, ok := seen[w]; ok {
			t.Errorf("wordlist[%d] = %q is a duplicate of wordlist[%d]", i, w, prev)
		}
		seen[w] = i
	}
}

func TestWordlist_MinWordLength(t *testing.T) {
	for i, w := range wordlist {
		if len(w) < 2 {
			t.Errorf("wordlist[%d] = %q: shorter than 2 chars", i, w)
		}
	}
}

func TestWordlist_MaxWordLength(t *testing.T) {
	for i, w := range wordlist {
		if len(w) > 12 {
			t.Errorf("wordlist[%d] = %q: longer than 12 chars", i, w)
		}
	}
}
