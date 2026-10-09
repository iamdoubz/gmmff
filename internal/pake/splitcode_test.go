package pake

import (
	"testing"

	"filippo.io/cpace"

	"github.com/iamdoubz/gmmff/v2/internal/crypto"
)

// handshake runs CPace exactly as internal/peer does and returns both sides'
// sessions.
func handshake(t *testing.T, initiatorPW, responderPW string) (*Session, *Session) {
	t.Helper()
	ci := cpace.NewContextInfo("gmmff-initiator", "gmmff-responder", nil)
	msgA, state, err := cpace.Start(initiatorPW, ci)
	if err != nil {
		t.Fatal(err)
	}
	msgB, keyB, err := cpace.Exchange(responderPW, ci, msgA)
	if err != nil {
		t.Fatal(err)
	}
	keyA, err := state.Finish(msgB)
	if err != nil {
		t.Fatal(err)
	}
	a, err := NewSession(keyA)
	if err != nil {
		t.Fatal(err)
	}
	b, err := NewSession(keyB)
	if err != nil {
		t.Fatal(err)
	}
	return a, b
}

// Security-load-bearing (ADR-014): the server learns only the nameplate. A
// server that impersonates the responder with it must fail SDP verification.
func TestSplitCode_ServerWithNameplateCannotMITM(t *testing.T) {
	nameplate, _ := crypto.GenerateCode()
	full, err := crypto.WithSecret(nameplate)
	if err != nil {
		t.Fatal(err)
	}
	sdp := []byte(`{"type":"offer","sdp":"v=0"}`)

	honestA, honestB := handshake(t, full, full)
	if err := honestB.VerifyOffer(sdp, honestA.SignOffer(sdp)); err != nil {
		t.Fatalf("peers sharing the full code failed to verify: %v", err)
	}

	victim, server := handshake(t, full, nameplate)
	if err := server.VerifyOffer(sdp, victim.SignOffer(sdp)); err == nil {
		t.Fatal("server knowing only the nameplate verified the initiator's offer")
	}
	if err := victim.VerifyAnswer(sdp, server.SignAnswer(sdp)); err == nil {
		t.Fatal("initiator accepted an answer signed by a nameplate-only server")
	}
}
