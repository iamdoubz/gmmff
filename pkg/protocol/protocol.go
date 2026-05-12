// Package protocol defines the wire message types exchanged over the
// WebSocket signaling channel between gmmff peers and the signaling server.
// All messages are JSON-encoded. The server is a dumb relay and never
// inspects message payloads beyond the Type field.
package protocol

import "encoding/json"

// Version is the signaling protocol version.  Bumped on breaking changes.
const Version = "1"

// ─────────────────────────────────────────────────────────────────────────────
// Top-level envelope
// ─────────────────────────────────────────────────────────────────────────────

// Envelope is the outermost JSON wrapper for every WebSocket message.
// Payload is kept as raw JSON so callers can unmarshal only what they need.
type Envelope struct {
	// Type identifies the message kind (see Msg* constants below).
	Type string `json:"type"`

	// Payload holds the type-specific body, encoded as raw JSON.
	Payload json.RawMessage `json:"payload,omitempty"`
}

// ─────────────────────────────────────────────────────────────────────────────
// Message type constants
// ─────────────────────────────────────────────────────────────────────────────

const (
	// Client → server: request a new slot.
	MsgSlotCreate = "slot.create"

	// Server → client: slot successfully created; carries the slot ID and
	// human-readable code.
	MsgSlotCreated = "slot.created"

	// Client → server: join an existing slot using a code.
	MsgSlotJoin = "slot.join"

	// Server → client: peer has joined the slot.
	MsgSlotReady = "slot.ready"

	// Server → client: the slot does not exist or has expired.
	MsgSlotNotFound = "slot.not_found"

	// Server → client: the slot already has two peers; no more may join.
	MsgSlotFull = "slot.full"

	// Client → server: PAKE message A (initiator → responder).
	MsgPakeA = "pake.a"

	// Client → server: PAKE message B (responder → initiator).
	MsgPakeB = "pake.b"

	// Client → server: WebRTC SDP offer.
	MsgSDPOffer = "sdp.offer"

	// Client → server: WebRTC SDP answer.
	MsgSDPAnswer = "sdp.answer"

	// Client → server: ICE candidate trickle.
	MsgICECandidate = "ice.candidate"

	// Client → server / server → client: graceful hangup.
	MsgBye = "bye"

	// Server → all: a new peer has joined the slot.
	MsgPeerJoined = "peer.joined"

	// Server → all: a peer has left the slot.
	MsgPeerLeft = "peer.left"

	// Server → client: targeted relay between specific peers.
	// Used for multi-peer PAKE/SDP routing.
	MsgTargeted = "targeted"

	// Server → client: unrecoverable server error.  Payload carries an
	// ErrorPayload.  Clients SHOULD display Code to the user and report it
	// when filing issues — it contains no sensitive information.
	MsgError = "error"

	// Server → client: keep-alive ping.  Clients SHOULD respond with pong.
	MsgPing = "ping"

	// Client → server: keep-alive pong.
	MsgPong = "pong"
)

// ─────────────────────────────────────────────────────────────────────────────
// Payload structs
// ─────────────────────────────────────────────────────────────────────────────

// SlotCreatePayload is sent by the initiating peer to request a new slot.
type SlotCreatePayload struct {
	// ProtocolVersion lets the server reject incompatible clients early.
	ProtocolVersion string `json:"protocol_version"`
	// SessionType identifies what kind of session this is ("files" or "chat").
	SessionType string `json:"session_type,omitempty"`
	// MaxPeers is the maximum number of participants (initiator + N). Default 2, max 10.
	MaxPeers int `json:"max_peers,omitempty"`
}

// SlotCreatedPayload is the server's response to a successful SlotCreate.
type SlotCreatedPayload struct {
	// SlotID is the canonical UUID of the slot (opaque to users).
	SlotID string `json:"slot_id"`

	// Code is the human-readable passphrase the initiator shares out-of-band.
	// Format: "<word>-<word>-<word>" derived from 128 bits of entropy.
	Code string `json:"code"`

	// TTLSeconds is how long the slot will be held open before expiry.
	TTLSeconds int `json:"ttl_seconds"`
	// SessionType is echoed back from the create request.
	SessionType string `json:"session_type,omitempty"`
	// MaxPeers is echoed from the create request.
	MaxPeers int `json:"max_peers,omitempty"`
}

// SlotJoinPayload is sent by the responding peer to join a slot.
type SlotJoinPayload struct {
	// Code is the passphrase received out-of-band from the initiator.
	Code string `json:"code"`

	// ProtocolVersion lets the server reject incompatible clients early.
	ProtocolVersion string `json:"protocol_version"`
}

// SlotReadyPayload is sent to both peers once the rendezvous is complete.
type SlotReadyPayload struct {
	// Role tells each peer whether it is the "initiator" or "responder".
	// The initiator sends the WebRTC offer; the responder sends the answer.
	Role string `json:"role"` // "initiator" | "responder"

	// SessionType echoes the type from slot.create so the joiner knows
	// what kind of session they are entering ("files" or "chat").
	SessionType string `json:"session_type,omitempty"`
	// MaxPeers is the total allowed participants for this session.
	MaxPeers int `json:"max_peers,omitempty"`
	// PeerCount is the current number of connected participants.
	PeerCount int `json:"peer_count,omitempty"`
}

// OpaquePayload wraps a raw base64-encoded byte slice.  Used for PAKE
// messages and SDP blobs — the server never decodes these.
type OpaquePayload struct {
	Data string `json:"data"` // standard base64 (RFC 4648 §4)
}

// ICECandidatePayload wraps a single trickled ICE candidate.
type ICECandidatePayload struct {
	Candidate     string `json:"candidate"`
	SDPMid        string `json:"sdpMid"`
	SDPMLineIndex uint16 `json:"sdpMLineIndex"`
}

// ErrorPayload is attached to MsgError frames.
// Designed for privacy: no filenames, no IPs, no user data.
type ErrorPayload struct {
	// Code is a short machine-readable token usable in bug reports.
	// Examples: ERR_SLOT_NOT_FOUND, ERR_SLOT_FULL, ERR_INTERNAL.
	Code string `json:"code"`

	// Message is a human-readable description safe to display in a UI.
	Message string `json:"message"`
}

// PeerJoinedPayload is sent to all existing members when a new peer joins.
type PeerJoinedPayload struct {
	// PeerID is the connection ID of the new peer.
	PeerID string `json:"peer_id"`
	// PeerCount is the current total number of connected participants.
	PeerCount int `json:"peer_count"`
	// MaxPeers is the session maximum.
	MaxPeers int `json:"max_peers"`
}

// PeerLeftPayload is sent to all remaining members when a peer leaves.
type PeerLeftPayload struct {
	// PeerID is the connection ID of the peer that left.
	PeerID string `json:"peer_id"`
	// PeerCount is the updated total number of connected participants.
	PeerCount int `json:"peer_count"`
	// MaxPeers is the session maximum.
	MaxPeers int `json:"max_peers"`
}

// TargetedPayload wraps a message for delivery to a specific peer.
// Used for per-peer PAKE and SDP routing in multi-peer sessions.
type TargetedPayload struct {
	// TargetPeerID is the intended recipient connection ID.
	TargetPeerID string `json:"target_peer_id"`
	// FromPeerID identifies the sender.
	FromPeerID string `json:"from_peer_id"`
	// Inner is the actual message envelope, base64-encoded JSON.
	Inner json.RawMessage `json:"inner"`
}

// ─────────────────────────────────────────────────────────────────────────────
// Helpers
// ─────────────────────────────────────────────────────────────────────────────

// NewEnvelope marshals payload into an Envelope ready to send.
func NewEnvelope(msgType string, payload any) (Envelope, error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return Envelope{}, err
	}
	return Envelope{Type: msgType, Payload: raw}, nil
}

// MustEnvelope is like NewEnvelope but panics on marshal error.
// Use only for statically-known payload types.
func MustEnvelope(msgType string, payload any) Envelope {
	env, err := NewEnvelope(msgType, payload)
	if err != nil {
		panic(err)
	}
	return env
}

// ErrorEnvelope builds a MsgError envelope from a code and message.
func ErrorEnvelope(code, message string) Envelope {
	return MustEnvelope(MsgError, ErrorPayload{Code: code, Message: message})
}
