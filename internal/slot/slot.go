// Package slot defines the domain model for a gmmff rendezvous slot.
//
// Lifecycle:
//
//	Created ──► Waiting   (initiator connected, code issued, accepting joins)
//	         ──► Active   (at least one peer joined, still accepting if not full)
//	         ──► Full     (max peers reached, no longer accepting joins)
//	         ──► Closed   (bye received, initiator left, or TTL expired)
//
// With multi-peer support the slot remains open for new joins until either
// MaxPeers is reached or the slot expires. Once ever full (EverFull=true)
// it stays closed even if peers leave.
package slot

import (
	"errors"
	"time"
)

// State represents the lifecycle stage of a slot.
type State string

const (
	StateWaiting State = "waiting" // initiator connected, awaiting peers
	StateActive  State = "active"  // at least one peer joined, still open
	StateFull    State = "full"    // max peers reached
	StateClosed  State = "closed"  // expired or explicitly closed
)

// DefaultTTL is how long a slot lives before expiry.
const DefaultTTL = 10 * time.Minute

// MaxAllowedPeers is the hard upper limit on peers per session (initiator + N).
const MaxAllowedPeers = 10

// DefaultMaxPeers is the default when none is specified.
const DefaultMaxPeers = 2

// MaxMessageSize is the maximum bytes allowed in a single WebSocket frame.
const MaxMessageSize = 64 * 1024

var ErrSlotFull = errors.New("slot is full")
var ErrSlotNotFound = errors.New("slot not found")
var ErrSlotClosed = errors.New("slot is closed")

// Slot is the persisted representation of a rendezvous session.
type Slot struct {
	ID          string    `json:"id"`
	Code        string    `json:"code"`
	State       State     `json:"state"`
	SessionType string    `json:"session_type,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
	ExpiresAt   time.Time `json:"expires_at"`

	// InitiatorID is the connection ID of the peer that created the slot.
	InitiatorID string `json:"initiator_id"`

	// PeerIDs holds the connection IDs of all joined peers (not including initiator).
	PeerIDs []string `json:"peer_ids,omitempty"`

	// MaxPeers is the total number of participants allowed (initiator counts as 1).
	MaxPeers int `json:"max_peers"`

	// EverFull is set to true the first time the slot reaches MaxPeers.
	// Once true, the slot will not reopen for new joins after a peer leaves.
	EverFull bool `json:"ever_full,omitempty"`
}

// New constructs a new Slot in the Waiting state.
func New(id, code, initiatorID, sessionType string, maxPeers int) *Slot {
	if maxPeers < 2 {
		maxPeers = DefaultMaxPeers
	}
	if maxPeers > MaxAllowedPeers {
		maxPeers = MaxAllowedPeers
	}
	now := time.Now().UTC()
	return &Slot{
		ID:          id,
		Code:        code,
		State:       StateWaiting,
		InitiatorID: initiatorID,
		SessionType: sessionType,
		MaxPeers:    maxPeers,
		PeerIDs:     []string{},
		CreatedAt:   now,
		ExpiresAt:   now.Add(DefaultTTL),
	}
}

// ConnectedCount returns the total number of connected participants
// (initiator + joined peers).
func (s *Slot) ConnectedCount() int {
	return 1 + len(s.PeerIDs) // 1 = initiator
}

// CanJoin reports whether the slot will accept another peer.
func (s *Slot) CanJoin() bool {
	if s.State == StateClosed || s.State == StateFull {
		return false
	}
	if s.EverFull {
		return false
	}
	return s.ConnectedCount() < s.MaxPeers
}

// Join adds a new peer to the slot.
// Returns ErrSlotFull if no room, ErrSlotClosed if closed.
func (s *Slot) Join(peerID string) error {
	if !s.CanJoin() {
		if s.State == StateClosed {
			return ErrSlotClosed
		}
		return ErrSlotFull
	}
	s.PeerIDs = append(s.PeerIDs, peerID)
	if s.ConnectedCount() >= s.MaxPeers {
		s.State = StateFull
		s.EverFull = true
	} else {
		s.State = StateActive
	}
	return nil
}

// RemovePeer removes a peer by connection ID.
// If the removed peer was the last non-initiator and EverFull is false,
// the slot transitions back to Waiting.
func (s *Slot) RemovePeer(connID string) {
	filtered := s.PeerIDs[:0]
	for _, id := range s.PeerIDs {
		if id != connID {
			filtered = append(filtered, id)
		}
	}
	s.PeerIDs = filtered
	// Update state
	if !s.EverFull && s.ConnectedCount() < s.MaxPeers {
		if len(s.PeerIDs) == 0 {
			s.State = StateWaiting
		} else {
			s.State = StateActive
		}
	}
}

// Close marks the slot as closed.
func (s *Slot) Close() { s.State = StateClosed }

// IsExpired reports whether the slot has passed its TTL.
func (s *Slot) IsExpired() bool { return time.Now().UTC().After(s.ExpiresAt) }

// IsInitiator reports whether connID is the slot initiator.
func (s *Slot) IsInitiator(connID string) bool { return connID == s.InitiatorID }

// HasPeer reports whether connID is a joined (non-initiator) peer.
func (s *Slot) HasPeer(connID string) bool {
	for _, id := range s.PeerIDs {
		if id == connID {
			return true
		}
	}
	return false
}

// IsMember reports whether connID is any participant (initiator or peer).
func (s *Slot) IsMember(connID string) bool {
	return s.IsInitiator(connID) || s.HasPeer(connID)
}

// AllPeerIDs returns all non-initiator peer IDs.
func (s *Slot) AllPeerIDs() []string { return append([]string{}, s.PeerIDs...) }

// OtherMembers returns connection IDs of all members except the given connID.
func (s *Slot) OtherMembers(connID string) []string {
	var others []string
	if s.InitiatorID != connID {
		others = append(others, s.InitiatorID)
	}
	for _, id := range s.PeerIDs {
		if id != connID {
			others = append(others, id)
		}
	}
	return others
}

// RoleOf returns "initiator" or "responder" for the given connection ID.
func (s *Slot) RoleOf(connID string) string {
	if connID == s.InitiatorID {
		return "initiator"
	}
	return "responder"
}
