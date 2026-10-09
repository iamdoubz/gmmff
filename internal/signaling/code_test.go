package signaling

import "testing"

// Security-load-bearing: slot.join must carry only the nameplate. If the
// secret leaked here, the server would know the full PAKE password again.
func TestJoinPayload_SendsOnlyNameplate(t *testing.T) {
	p, err := joinPayload("bear-cozy-cone-maple-river")
	if err != nil {
		t.Fatal(err)
	}
	if p.Code != "bear-cozy-cone" {
		t.Errorf("slot.join code = %q, want nameplate only", p.Code)
	}
}

func TestJoinPayload_RejectsLegacyCode(t *testing.T) {
	if _, err := joinPayload("bear-cozy-cone"); err == nil {
		t.Error("3-word legacy code accepted; it carries no client secret")
	}
}
