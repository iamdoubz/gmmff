package pake

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"io"
	"strings"
	"testing"

	"golang.org/x/crypto/hkdf"
)

// ─────────────────────────────────────────────────────────────────────────────
// Helpers
// ─────────────────────────────────────────────────────────────────────────────

// randomKey generates n random bytes for use as a fake shared secret in tests.
func randomKey(t *testing.T, n int) []byte {
	t.Helper()
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		t.Fatalf("rand.Read: %v", err)
	}
	return b
}

// newTestSession creates a Session from a random 32-byte shared key.
func newTestSession(t *testing.T) (*Session, []byte) {
	t.Helper()
	key := randomKey(t, 32)
	sess, err := NewSession(key)
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	return sess, key
}

// ─────────────────────────────────────────────────────────────────────────────
// NewSession
// ─────────────────────────────────────────────────────────────────────────────

func TestNewSession_Success(t *testing.T) {
	_, err := NewSession(randomKey(t, 32))
	if err != nil {
		t.Errorf("NewSession(32-byte key): unexpected error: %v", err)
	}
}

func TestNewSession_EmptyKey(t *testing.T) {
	// Empty key is technically usable by HKDF but should not panic.
	_, err := NewSession([]byte{})
	if err != nil {
		// Acceptable to fail — just must not panic.
		t.Logf("NewSession(empty key) returned error (acceptable): %v", err)
	}
}

func TestNewSession_DifferentKeysProduceDifferentSubkeys(t *testing.T) {
	key1 := randomKey(t, 32)
	key2 := randomKey(t, 32)

	sess1, _ := NewSession(key1)
	sess2, _ := NewSession(key2)

	// Sign the same data with both sessions; MACs must differ.
	sdp := []byte(`{"type":"offer","sdp":"v=0..."}`)
	mac1 := sess1.SignOffer(sdp)
	mac2 := sess2.SignOffer(sdp)

	if mac1 == mac2 {
		t.Error("different shared keys produced the same offer MAC")
	}
}

func TestNewSession_SameKeyProducesSameSubkeys(t *testing.T) {
	key := randomKey(t, 32)
	sess1, _ := NewSession(key)
	sess2, _ := NewSession(key)

	sdp := []byte(`{"type":"offer","sdp":"v=0..."}`)
	if sess1.SignOffer(sdp) != sess2.SignOffer(sdp) {
		t.Error("same shared key should produce identical offer MACs")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// HKDF subkey derivation — verify the key derivation matches spec
// ─────────────────────────────────────────────────────────────────────────────

func TestSubkeyDerivation_MatchesHKDF(t *testing.T) {
	// Independently derive what the subkeys should be, then verify that
	// signing with the Session produces a MAC consistent with those keys.
	sharedKey := randomKey(t, 32)

	deriveExpected := func(info string) []byte {
		r := hkdf.New(sha256.New, sharedKey, []byte("gmmff-v1"), []byte(info))
		key := make([]byte, 32)
		if _, err := io.ReadFull(r, key); err != nil {
			t.Fatalf("hkdf derive: %v", err)
		}
		return key
	}

	expectedOfferKey := deriveExpected("sdp-offer-mac")
	expectedAnswerKey := deriveExpected("sdp-answer-mac")

	sess, _ := NewSession(sharedKey)
	sdp := []byte(`{"type":"offer","sdp":"test-sdp-payload"}`)

	// Manually compute expected offer MAC.
	mac := hmac.New(sha256.New, expectedOfferKey)
	mac.Write(sdp)
	expectedOfferMAC := base64.StdEncoding.EncodeToString(mac.Sum(nil))

	if got := sess.SignOffer(sdp); got != expectedOfferMAC {
		t.Errorf("SignOffer MAC = %q, want %q", got, expectedOfferMAC)
	}

	// Manually compute expected answer MAC.
	mac2 := hmac.New(sha256.New, expectedAnswerKey)
	mac2.Write(sdp)
	expectedAnswerMAC := base64.StdEncoding.EncodeToString(mac2.Sum(nil))

	if got := sess.SignAnswer(sdp); got != expectedAnswerMAC {
		t.Errorf("SignAnswer MAC = %q, want %q", got, expectedAnswerMAC)
	}
}

func TestSubkeyDerivation_OfferAndAnswerKeysAreDifferent(t *testing.T) {
	// The two subkeys must be distinct — if they were identical, a responder
	// could replay the initiator's MAC as their own, defeating the binding.
	sharedKey := randomKey(t, 32)
	sess, _ := NewSession(sharedKey)

	sdp := []byte(`{"type":"offer","sdp":"test"}`)
	offerMAC := sess.SignOffer(sdp)
	answerMAC := sess.SignAnswer(sdp)

	if offerMAC == answerMAC {
		t.Error("offer and answer MACs must differ — subkeys must be distinct")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// SignOffer / VerifyOffer
// ─────────────────────────────────────────────────────────────────────────────

func TestSignAndVerifyOffer_RoundTrip(t *testing.T) {
	sess, _ := newTestSession(t)
	sdp := []byte(`{"type":"offer","sdp":"v=0\r\no=..."}`)

	mac := sess.SignOffer(sdp)
	if err := sess.VerifyOffer(sdp, mac); err != nil {
		t.Errorf("VerifyOffer with correct MAC: %v", err)
	}
}

func TestVerifyOffer_WrongData(t *testing.T) {
	sess, _ := newTestSession(t)
	sdp := []byte(`{"type":"offer","sdp":"original"}`)
	sdp2 := []byte(`{"type":"offer","sdp":"tampered"}`)
	mac := sess.SignOffer(sdp)

	if err := sess.VerifyOffer(sdp2, mac); err == nil {
		t.Error("VerifyOffer should fail when SDP has been tampered with")
	}
}

func TestVerifyOffer_WrongMAC(t *testing.T) {
	sess, _ := newTestSession(t)
	sdp := []byte(`{"type":"offer","sdp":"v=0"}`)
	if err := sess.VerifyOffer(sdp, "bm90YXZhbGlkbWFj"); err == nil {
		t.Error("VerifyOffer with wrong MAC should fail")
	}
}

func TestVerifyOffer_MalformedBase64(t *testing.T) {
	sess, _ := newTestSession(t)
	sdp := []byte(`{"type":"offer","sdp":"v=0"}`)
	if err := sess.VerifyOffer(sdp, "!!!not-base64!!!"); err == nil {
		t.Error("VerifyOffer with malformed base64 MAC should fail")
	}
}

func TestVerifyOffer_EmptyMAC(t *testing.T) {
	sess, _ := newTestSession(t)
	sdp := []byte(`{"type":"offer","sdp":"v=0"}`)
	if err := sess.VerifyOffer(sdp, ""); err == nil {
		t.Error("VerifyOffer with empty MAC should fail")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// SignAnswer / VerifyAnswer
// ─────────────────────────────────────────────────────────────────────────────

func TestSignAndVerifyAnswer_RoundTrip(t *testing.T) {
	sess, _ := newTestSession(t)
	sdp := []byte(`{"type":"answer","sdp":"v=0\r\na=..."}`)

	mac := sess.SignAnswer(sdp)
	if err := sess.VerifyAnswer(sdp, mac); err != nil {
		t.Errorf("VerifyAnswer with correct MAC: %v", err)
	}
}

func TestVerifyAnswer_WrongData(t *testing.T) {
	sess, _ := newTestSession(t)
	sdp := []byte(`{"type":"answer","sdp":"original"}`)
	sdp2 := []byte(`{"type":"answer","sdp":"tampered"}`)
	mac := sess.SignAnswer(sdp)

	if err := sess.VerifyAnswer(sdp2, mac); err == nil {
		t.Error("VerifyAnswer should fail when SDP has been tampered with")
	}
}

func TestVerifyAnswer_WrongMAC(t *testing.T) {
	sess, _ := newTestSession(t)
	sdp := []byte(`{"type":"answer","sdp":"v=0"}`)
	if err := sess.VerifyAnswer(sdp, "d3JvbmdrZXk="); err == nil {
		t.Error("VerifyAnswer with wrong MAC should fail")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Cross-key verification — the core security property
// ─────────────────────────────────────────────────────────────────────────────

func TestCrossKeyVerification_FailsWithDifferentSharedKey(t *testing.T) {
	// Simulate a MITM attacker who has a different shared key.
	// They must not be able to produce a MAC that verifies under the real key.
	realKey := randomKey(t, 32)
	attackerKey := randomKey(t, 32)

	realSess, _ := NewSession(realKey)
	attackerSess, _ := NewSession(attackerKey)

	sdp := []byte(`{"type":"offer","sdp":"attacker-sdp"}`)
	attackerMAC := attackerSess.SignOffer(sdp)

	// Real session must reject the attacker's MAC.
	if err := realSess.VerifyOffer(sdp, attackerMAC); err == nil {
		t.Error("VerifyOffer accepted a MAC from a different shared key — security failure")
	}
}

func TestOfferMACCannotBeUsedAsAnswerMAC(t *testing.T) {
	// The offer and answer subkeys are different. A responder must not be able
	// to replay the initiator's offer MAC as their own answer MAC.
	sess, _ := newTestSession(t)
	sdp := []byte(`{"type":"offer","sdp":"v=0"}`)

	offerMAC := sess.SignOffer(sdp)
	// Try to use the offer MAC to satisfy an answer verification.
	if err := sess.VerifyAnswer(sdp, offerMAC); err == nil {
		t.Error("offer MAC must not satisfy answer verification")
	}
}

func TestAnswerMACCannotBeUsedAsOfferMAC(t *testing.T) {
	sess, _ := newTestSession(t)
	sdp := []byte(`{"type":"answer","sdp":"v=0"}`)

	answerMAC := sess.SignAnswer(sdp)
	if err := sess.VerifyOffer(sdp, answerMAC); err == nil {
		t.Error("answer MAC must not satisfy offer verification")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// MAC output properties
// ─────────────────────────────────────────────────────────────────────────────

func TestSignOffer_OutputIsValidBase64(t *testing.T) {
	sess, _ := newTestSession(t)
	mac := sess.SignOffer([]byte("test-sdp"))
	if _, err := base64.StdEncoding.DecodeString(mac); err != nil {
		t.Errorf("SignOffer produced invalid base64: %v", err)
	}
}

func TestSignOffer_OutputIs32BytesDecoded(t *testing.T) {
	// HMAC-SHA256 always produces 32 bytes.
	sess, _ := newTestSession(t)
	mac := sess.SignOffer([]byte("test-sdp"))
	decoded, _ := base64.StdEncoding.DecodeString(mac)
	if len(decoded) != 32 {
		t.Errorf("decoded MAC length = %d, want 32", len(decoded))
	}
}

func TestSignOffer_DifferentSDPProducesDifferentMAC(t *testing.T) {
	sess, _ := newTestSession(t)
	mac1 := sess.SignOffer([]byte("sdp-version-1"))
	mac2 := sess.SignOffer([]byte("sdp-version-2"))
	if mac1 == mac2 {
		t.Error("different SDP payloads should produce different MACs")
	}
}

func TestSignOffer_EmptySDP(t *testing.T) {
	// Empty SDP is unusual but must not panic — HMAC of empty input is defined.
	sess, _ := newTestSession(t)
	mac := sess.SignOffer([]byte{})
	if mac == "" {
		t.Error("SignOffer with empty SDP should still produce a non-empty MAC")
	}
	if err := sess.VerifyOffer([]byte{}, mac); err != nil {
		t.Errorf("VerifyOffer with empty SDP and matching MAC should succeed: %v", err)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Error message quality
// ─────────────────────────────────────────────────────────────────────────────

func TestVerifyOffer_ErrorMentionsMITM(t *testing.T) {
	// The error message should hint at the security implication so developers
	// don't silently swallow it.
	sess, _ := newTestSession(t)
	sdp := []byte("test")
	err := sess.VerifyOffer(sdp, "bm90YXZhbGlkbWFj")
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "man-in-the-middle") && !strings.Contains(err.Error(), "wrong code") {
		t.Errorf("error message %q should mention MITM or wrong code", err.Error())
	}
}
