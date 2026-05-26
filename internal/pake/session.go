// Package pake provides the session key material derived from the CPace
// handshake and uses it to cryptographically bind the WebRTC SDP exchange
// to the shared secret.
//
// # Zero-trust guarantee
//
// After CPace completes, both peers hold the same sharedKey.  That key is
// used to HMAC-SHA256-sign the SDP JSON before it is sent over the signaling
// relay.  The receiving peer verifies the MAC before passing the SDP to
// WebRTC.
//
// This means a compromised signaling server — or any network attacker — cannot
// substitute their own SDP fingerprints, because they do not know sharedKey
// and therefore cannot produce a valid MAC.
//
// # Key derivation
//
// Two subkeys are derived from sharedKey using HKDF-SHA256 with distinct
// info labels so neither peer can replay the other's MAC:
//
//	offerKey  = HKDF(sharedKey, salt="gmmff-v1", info="sdp-offer-mac")
//	answerKey = HKDF(sharedKey, salt="gmmff-v1", info="sdp-answer-mac")
//
// The initiator signs the offer with offerKey and verifies the answer with
// answerKey.  The responder does the reverse.
package pake

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"io"

	"golang.org/x/crypto/hkdf"
)

const (
	hkdfSalt   = "gmmff-v1"
	infoOffer  = "sdp-offer-mac"
	infoAnswer = "sdp-answer-mac"
	subkeyLen  = 32 // 256-bit subkeys
)

// Session holds the two subkeys derived from the CPace shared secret.
// Create one with NewSession immediately after the PAKE handshake completes.
type Session struct {
	offerKey  []byte // signs/verifies the SDP offer
	answerKey []byte // signs/verifies the SDP answer
}

// NewSession derives offerKey and answerKey from the CPace shared secret.
// sharedKey must be the raw bytes returned by cpace.State.Finish or
// cpace.Exchange — do not hash or truncate it beforehand.
func NewSession(sharedKey []byte) (*Session, error) {
	offerKey, err := deriveSubkey(sharedKey, infoOffer)
	if err != nil {
		return nil, fmt.Errorf("pake: derive offer key: %w", err)
	}
	answerKey, err := deriveSubkey(sharedKey, infoAnswer)
	if err != nil {
		return nil, fmt.Errorf("pake: derive answer key: %w", err)
	}
	return &Session{offerKey: offerKey, answerKey: answerKey}, nil
}

// ─────────────────────────────────────────────────────────────────────────────
// Signing and verification
// ─────────────────────────────────────────────────────────────────────────────

// SignOffer returns the base64-encoded HMAC-SHA256 of sdpJSON using offerKey.
func (s *Session) SignOffer(sdpJSON []byte) string {
	return sign(s.offerKey, sdpJSON)
}

// VerifyOffer checks that mac is the correct HMAC of sdpJSON under offerKey.
func (s *Session) VerifyOffer(sdpJSON []byte, mac string) error {
	return verify(s.offerKey, sdpJSON, mac, "offer")
}

// SignAnswer returns the base64-encoded HMAC-SHA256 of sdpJSON using answerKey.
func (s *Session) SignAnswer(sdpJSON []byte) string {
	return sign(s.answerKey, sdpJSON)
}

// VerifyAnswer checks that mac is the correct HMAC of sdpJSON under answerKey.
func (s *Session) VerifyAnswer(sdpJSON []byte, mac string) error {
	return verify(s.answerKey, sdpJSON, mac, "answer")
}

// ─────────────────────────────────────────────────────────────────────────────
// Wire payload
// ─────────────────────────────────────────────────────────────────────────────

// SignedSDP is the JSON payload sent inside MsgSDPOffer / MsgSDPAnswer
// envelopes.  Both fields are standard base64 (RFC 4648 §4).
type SignedSDP struct {
	// SDP is the base64-encoded WebRTC SessionDescription JSON.
	SDP string `json:"sdp"`

	// MAC is the base64-encoded HMAC-SHA256 over the raw SDP bytes,
	// computed with the appropriate subkey.
	MAC string `json:"mac"`
}

// ─────────────────────────────────────────────────────────────────────────────
// Helpers
// ─────────────────────────────────────────────────────────────────────────────

func deriveSubkey(secret []byte, info string) ([]byte, error) {
	r := hkdf.New(sha256.New, secret, []byte(hkdfSalt), []byte(info))
	key := make([]byte, subkeyLen)
	if _, err := io.ReadFull(r, key); err != nil {
		return nil, err
	}
	return key, nil
}

func sign(key, data []byte) string {
	mac := hmac.New(sha256.New, key)
	mac.Write(data)
	return base64.StdEncoding.EncodeToString(mac.Sum(nil))
}

func verify(key, data []byte, gotB64, label string) error {
	want := sign(key, data)
	// Constant-time comparison via hmac.Equal on the decoded bytes.
	gotBytes, err := base64.StdEncoding.DecodeString(gotB64)
	if err != nil {
		return fmt.Errorf("pake: %s MAC decode: %w", label, err)
	}
	wantBytes, _ := base64.StdEncoding.DecodeString(want)
	if !hmac.Equal(gotBytes, wantBytes) {
		return fmt.Errorf(
			"pake: %s MAC verification failed — possible man-in-the-middle attack or wrong code",
			label,
		)
	}
	return nil
}
