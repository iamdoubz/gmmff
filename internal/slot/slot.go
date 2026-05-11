// Package slot defines the domain model for a gmmff rendezvous slot.
//
// Lifecycle:
//
//	Created ──► Waiting  (initiator connected, code issued)
//	         ──► Ready   (responder joined, both peers present)
//	         ──► Closed  (bye received, or TTL expired)
//
// The slot state is stored in Redis; this package defines the struct
// and the transition rules only — no I/O.
package slot

import (
	"errors"
	"time"
)

// State represents the lifecycle stage of a slot.
type State string

const (
	StateWaiting State = "waiting" // initiator connected, awaiting peer
	StateReady   State = "ready"   // both peers present
	StateClosed  State = "closed"  // expired or explicitly closed
)

// DefaultTTL is how long a slot lives in the waiting state before expiry.
const DefaultTTL = 10 * time.Minute

// MaxMessageSize is the maximum bytes allowed in a single WebSocket frame
// from a client.  Protects against memory exhaustion.
const MaxMessageSize = 64 * 1024 // 64 KiB — more than enough for SDP/ICE

// ErrSlotFull is returned when a third peer attempts to join.
var ErrSlotFull = errors.New("slot is full")

// ErrSlotNotFound is returned when the requested code does not map to a slot.
var ErrSlotNotFound = errors.New("slot not found")

// ErrSlotClosed is returned when a message arrives for a closed slot.
var ErrSlotClosed = errors.New("slot is closed")

// Slot is the persisted representation of a rendezvous pair.
// Stored as a Redis hash under key "slot:<slot_id>".
type Slot struct {
	// ID is the canonical UUID (not shown to users).
	ID string `json:"id"`

	// Code is the human-readable passphrase (shown to initiator, shared OOB).
	Code string `json:"code"`

	// State is the current lifecycle stage.
	State State `json:"state"`

	// InitiatorID is the connection ID of the peer that created the slot.
	InitiatorID string `json:"initiator_id"`

	// ResponderID is the connection ID of the peer that joined.
	// Empty until the responder connects.
	ResponderID string `json:"responder_id,omitempty"`

	// CreatedAt is when the slot was allocated.
	CreatedAt time.Time `json:"created_at"`

	// ExpiresAt is when the slot will be reaped if still in Waiting state.
	ExpiresAt time.Time `json:"expires_at"`

	// SessionType identifies what kind of session this slot holds.
	// Echoed from the slot.create payload so the joiner knows what to expect.
	SessionType string `json:"session_type,omitempty"`
}

// New constructs a new Slot in the Waiting state.
func New(id, code, initiatorID, sessionType string) *Slot {
	now := time.Now().UTC()
	return &Slot{
		ID:          id,
		Code:        code,
		State:       StateWaiting,
		InitiatorID: initiatorID,
		SessionType: sessionType,
		CreatedAt:   now,
		ExpiresAt:   now.Add(DefaultTTL),
	}
}

// Join transitions the slot from Waiting → Ready by recording the responder.
// Returns ErrSlotFull if the slot is already ready, ErrSlotClosed if closed.
func (s *Slot) Join(responderID string) error {
	switch s.State {
	case StateReady:
		return ErrSlotFull
	case StateClosed:
		return ErrSlotClosed
	}
	s.ResponderID = responderID
	s.State = StateReady
	return nil
}

// Close marks the slot as closed.
func (s *Slot) Close() {
	s.State = StateClosed
}

// IsExpired reports whether the slot has passed its TTL.
func (s *Slot) IsExpired() bool {
	return time.Now().UTC().After(s.ExpiresAt)
}

// PeerOf returns the connection ID of the other peer given one connection ID.
// Returns ("", false) if connID is not a member of this slot.
func (s *Slot) PeerOf(connID string) (string, bool) {
	switch connID {
	case s.InitiatorID:
		if s.ResponderID != "" {
			return s.ResponderID, true
		}
		return "", false
	case s.ResponderID:
		return s.InitiatorID, true
	default:
		return "", false
	}
}

// RoleOf returns "initiator" or "responder" for the given connection ID.
func (s *Slot) RoleOf(connID string) string {
	if connID == s.InitiatorID {
		return "initiator"
	}
	return "responder"
}
