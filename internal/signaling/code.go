package signaling

import (
	"github.com/iamdoubz/gmmff/v2/internal/crypto"
	"github.com/iamdoubz/gmmff/v2/pkg/protocol"
)

// joinPayload builds the slot.join payload for a full "<nameplate>-<secret>"
// code. Only the nameplate is sent: the secret is the part of the PAKE
// password the server must never see (ADR-014). Both the native and js
// JoinSlot go through here so the stripping cannot be forgotten in one build.
func joinPayload(fullCode string) (protocol.SlotJoinPayload, error) {
	nameplate, _, err := crypto.SplitCode(fullCode)
	if err != nil {
		return protocol.SlotJoinPayload{}, err
	}
	return protocol.SlotJoinPayload{Code: nameplate, ProtocolVersion: protocol.Version}, nil
}
