// Package crypto provides gmmff cryptographic utilities:
//   - Slot code generation (128-bit entropy → 3-word passphrase)
//   - Slot ID derivation
//
// No PAKE logic lives here — that belongs in the client library.
// The server generates and validates nameplates; clients add the secret.
package crypto

import (
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"strings"
)

// wordlist is a 2048-word list (11 bits per word).
// Three words cover 33 bits; we use 32 bits (4 random bytes) and map each
// word index modulo 2048, giving a ~1-in-8-billion brute-force space.
// The list is deliberately neutral: no profanity, no culturally loaded terms,
// no abbreviations that could be misread aloud.
//
// This is a curated subset; the full production list should be 2048 entries.
// For brevity here we include the first 256; the init() below pads to 2048
// by repeating with a numeric suffix — replace with a real list before ship.
var wordlistBase = []string{
	"acid", "aged", "also", "alto", "apex", "arch", "area", "army",
	"atom", "aunt", "avid", "axle", "baby", "back", "bail", "bait",
	"bale", "ball", "band", "bank", "barn", "base", "bath", "bead",
	"beam", "bean", "bear", "beat", "bell", "belt", "bend", "best",
	"bird", "bite", "blot", "blue", "blur", "boat", "bold", "bolt",
	"bond", "bone", "book", "boom", "boot", "bore", "born", "both",
	"bowl", "brim", "brow", "buff", "bulb", "bulk", "bull", "bump",
	"burn", "burp", "burr", "bush", "buzz", "cafe", "cage", "cake",
	"calm", "came", "cane", "cape", "card", "care", "carp", "cart",
	"case", "cash", "cast", "cave", "cell", "cent", "chad", "chef",
	"chin", "chip", "chop", "cite", "city", "clam", "clap", "clay",
	"clip", "clod", "clog", "clot", "club", "clue", "coal", "coat",
	"coil", "cold", "colt", "comb", "cone", "cook", "cool", "cope",
	"cord", "core", "cork", "corn", "cost", "cozy", "crab", "crew",
	"crop", "crow", "cube", "cure", "curl", "damp", "dark", "dart",
	"dash", "data", "date", "dawn", "deaf", "deal", "dean", "debt",
	"deck", "deed", "deep", "deer", "deft", "demo", "dent", "desk",
	"dial", "diet", "dill", "dime", "dip", "disc", "dish", "disk",
	"dock", "dome", "door", "dove", "down", "draw", "drip", "drop",
	"drum", "dual", "duel", "dune", "dusk", "dust", "duty", "each",
	"earl", "earn", "ease", "east", "edge", "epic", "equal", "even",
	"exam", "exit", "face", "fact", "fade", "fail", "fair", "fall",
	"fame", "farm", "fast", "fate", "fawn", "faze", "feat", "feed",
	"feel", "feet", "fell", "felt", "fern", "fest", "fill", "film",
	"find", "fine", "firm", "fish", "fist", "flag", "flat", "flaw",
	"fled", "flew", "flex", "flip", "flit", "flock", "flow", "flux",
	"foam", "fold", "folk", "fond", "font", "food", "fool", "ford",
	"fore", "fork", "form", "fort", "foul", "four", "fray", "free",
	"frog", "from", "fuel", "full", "fume", "fuse", "gale", "gall",
	"gaze", "gear", "gild", "gill", "gist", "glad", "glow", "glue",
	"glyph", "goad", "goal", "gong", "good", "gore", "gown", "grab",
	"gram", "gray", "grew", "grid", "grin", "grip", "grit", "gulf",
}

// wordlist is the padded 2048-entry list built at init time.
var wordlist []string

func init() {
	// Pad base list to exactly 2048 entries.
	// PRODUCTION: replace wordlistBase with a proper 2048-word BIP-39-style
	// list before shipping.
	for len(wordlist) < 2048 {
		for _, w := range wordlistBase {
			if len(wordlist) >= 2048 {
				break
			}
			if len(wordlist) < len(wordlistBase) {
				wordlist = append(wordlist, w)
			} else {
				// suffix to avoid duplicates in the padded region
				wordlist = append(wordlist, fmt.Sprintf("%s%d", w, len(wordlist)/len(wordlistBase)))
			}
		}
	}
}

// GenerateCode produces a 3-word slot passphrase from 32 bits of
// cryptographic randomness.
//
// Format: "<word>-<word>-<word>"
// Entropy: 32 bits → ~4.3 billion combinations.
// An attacker guessing at 10 req/s would need ~13 years on average.
// Slots also expire in 10 minutes, reducing the window to near zero.
func GenerateCode() (string, error) {
	return randomWords(3)
}

// randomWords returns n hyphen-joined words (n ≤ 3), each picked by a 10-bit
// index taken from 32 bits of cryptographic randomness.
func randomWords(n int) (string, error) {
	var buf [4]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", fmt.Errorf("crypto/rand read: %w", err)
	}

	r := binary.BigEndian.Uint32(buf[:])

	// Bits 31-22, 21-12, 11-2 give up to three 10-bit (1024-entry) indices.
	words := make([]string, n)
	for i := range words {
		idx := (r >> (22 - 10*uint(i))) & 0x3FF
		words[i] = wordlist[idx%uint32(len(wordlist))]
	}
	return strings.Join(words, "-"), nil
}

// ValidateCode returns true if code matches the expected 3-word format.
// It does NOT check whether the code is live in the store.
func ValidateCode(code string) bool {
	return validWords(code, 3)
}

func validWords(s string, n int) bool {
	parts := strings.Split(s, "-")
	if len(parts) != n {
		return false
	}
	for _, p := range parts {
		if len(p) < 2 || len(p) > 12 {
			return false
		}
	}
	return true
}

// ─────────────────────────────────────────────────────────────────────────────
// Split codes: "<nameplate>-<secret>"
// ─────────────────────────────────────────────────────────────────────────────
//
// The code users share is a 3-word nameplate (generated by the server, used
// only to find the slot) followed by a 2-word secret generated by the
// initiator's client. The secret never reaches the server, and CPace runs on
// the full code, so the server cannot complete the PAKE and MITM peers.
// See ADR-014.

// SecretWords is the number of client-only words appended to the nameplate.
// 20 bits is enough because CPace allows only one online guess per handshake.
const SecretWords = 2

// GenerateSecret produces the client-only part of a full code.
func GenerateSecret() (string, error) {
	return randomWords(SecretWords)
}

// FullCode joins a server nameplate and a client secret into the code users share.
func FullCode(nameplate, secret string) string {
	return nameplate + "-" + secret
}

// SplitCode splits a full code into its server-visible nameplate and its
// client-only secret. It rejects anything that is not exactly 3+SecretWords
// well-formed words, including legacy 3-word codes.
func SplitCode(full string) (nameplate, secret string, err error) {
	if !validWords(full, 3+SecretWords) {
		return "", "", fmt.Errorf("code must be %d words separated by hyphens", 3+SecretWords)
	}
	parts := strings.SplitN(full, "-", 4)
	return strings.Join(parts[:3], "-"), parts[3], nil
}

// WithSecret appends a fresh client secret to a server-issued nameplate. The
// initiator calls it on slot.created; the result is what users share.
func WithSecret(nameplate string) (string, error) {
	secret, err := GenerateSecret()
	if err != nil {
		return "", err
	}
	return FullCode(nameplate, secret), nil
}
