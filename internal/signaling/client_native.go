//go:build !js

// Package signaling implements the gmmff WebSocket signaling client.
//
// It connects to the signaling server, manages the slot lifecycle, and
// provides a channel-based API for exchanging opaque PAKE/SDP/ICE messages
// with the remote peer.  The WebRTC and PAKE logic live elsewhere; this
// package is purely the transport layer to the server.
package signaling

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/iamdoubz/gmmff/pkg/protocol"
)

const (
	writeWait      = 10 * time.Second
	pongWait       = 60 * time.Second
	pingPeriod     = 50 * time.Second
	maxMessageSize = 64 * 1024
)

// Message is a decoded inbound message from the signaling server.
type Message struct {
	Type    string
	Payload json.RawMessage
}

// Client is a live WebSocket connection to the gmmff signaling server.
type Client struct {
	conn   *websocket.Conn
	send   chan []byte
	recv   chan Message
	done   chan struct{}
	once   sync.Once
	closeErr error
}

// Connect dials the signaling server at wsURL and starts the read/write pumps.
// wsURL should be "ws://host/ws" or "wss://host/ws".
func Connect(ctx context.Context, wsURL string) (*Client, error) {
	dialer := websocket.Dialer{HandshakeTimeout: 10 * time.Second}
	conn, _, err := dialer.DialContext(ctx, wsURL, nil)
	if err != nil {
		return nil, fmt.Errorf("signaling: dial %s: %w", wsURL, err)
	}

	c := &Client{
		conn: conn,
		send: make(chan []byte, 32),
		recv: make(chan Message, 64),
		done: make(chan struct{}),
	}

	go c.readPump()
	go c.writePump()

	return c, nil
}

// ─────────────────────────────────────────────────────────────────────────────
// Public API
// ─────────────────────────────────────────────────────────────────────────────

// Recv returns the inbound message channel.  Closed when the connection drops.
func (c *Client) Recv() <-chan Message { return c.recv }

// Done returns a channel closed when the connection is fully torn down.
func (c *Client) Done() <-chan struct{} { return c.done }

// Send enqueues an Envelope for delivery to the server.
func (c *Client) Send(env protocol.Envelope) error {
	b, err := json.Marshal(env)
	if err != nil {
		return fmt.Errorf("signaling: marshal envelope: %w", err)
	}
	select {
	case c.send <- b:
		return nil
	case <-c.done:
		return fmt.Errorf("signaling: connection closed")
	}
}

// Close sends a graceful bye then tears down the WebSocket.
func (c *Client) Close() {
	_ = c.Send(protocol.MustEnvelope(protocol.MsgBye, nil))
	c.closeOnce()
}

func (c *Client) closeOnce() {
	c.once.Do(func() {
		close(c.send)
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// Convenience send helpers
// ─────────────────────────────────────────────────────────────────────────────

// CreateSlot sends a slot.create request.
func (c *Client) CreateSlot(sessionType string, maxPeers int) error {
	return c.Send(protocol.MustEnvelope(protocol.MsgSlotCreate,
		protocol.SlotCreatePayload{ProtocolVersion: protocol.Version, SessionType: sessionType, MaxPeers: maxPeers}))
}

// JoinSlot sends a slot.join request with the given code.
func (c *Client) JoinSlot(code string) error {
	return c.Send(protocol.MustEnvelope(protocol.MsgSlotJoin,
		protocol.SlotJoinPayload{Code: code, ProtocolVersion: protocol.Version}))
}

// SendOpaque sends a message with a base64-encoded byte payload.
func (c *Client) SendOpaque(msgType string, data []byte) error {
	return c.Send(protocol.MustEnvelope(msgType, protocol.OpaquePayload{Data: encodeB64(data)}))
}

// SendSignedSDP sends a signed SDP payload.
// sdpJSON is the raw SessionDescription JSON; mac is the base64 HMAC
// produced by pake.Session.SignOffer / SignAnswer.
func (c *Client) SendSignedSDP(msgType string, sdpJSON []byte, mac string) error {
	return c.Send(protocol.MustEnvelope(msgType, signedSDPPayload{
		SDP: encodeB64(sdpJSON),
		MAC: mac,
	}))
}

// signedSDPPayload is the JSON structure sent inside SDP envelopes.
type signedSDPPayload struct {
	SDP string `json:"sdp"`
	MAC string `json:"mac"`
}

// SendICE sends an ICE candidate.
func (c *Client) SendICE(candidate, sdpMid string, sdpMLineIndex uint16) error {
	return c.Send(protocol.MustEnvelope(protocol.MsgICECandidate,
		protocol.ICECandidatePayload{
			Candidate:     candidate,
			SDPMid:        sdpMid,
			SDPMLineIndex: sdpMLineIndex,
		}))
}

// SendTargeted sends a message routed to a specific peer by ID.
func (c *Client) SendTargeted(targetPeerID, fromPeerID string, inner protocol.Envelope) error {
	inner_raw, _ := json.Marshal(inner)
	return c.Send(protocol.MustEnvelope(protocol.MsgTargeted, protocol.TargetedPayload{
		TargetPeerID: targetPeerID,
		FromPeerID:   fromPeerID,
		Inner:        inner_raw,
	}))
}

// ─────────────────────────────────────────────────────────────────────────────
// Pumps
// ─────────────────────────────────────────────────────────────────────────────

func (c *Client) readPump() {
	defer func() {
		close(c.recv)
		_ = c.conn.Close()
		close(c.done)
	}()

	c.conn.SetReadLimit(maxMessageSize)
	_ = c.conn.SetReadDeadline(time.Now().Add(pongWait))
	c.conn.SetPongHandler(func(string) error {
		return c.conn.SetReadDeadline(time.Now().Add(pongWait))
	})

	for {
		_, data, err := c.conn.ReadMessage()
		if err != nil {
			return
		}
		var env protocol.Envelope
		if err := json.Unmarshal(data, &env); err != nil {
			continue
		}
		c.recv <- Message{Type: env.Type, Payload: env.Payload}
	}
}

func (c *Client) writePump() {
	ticker := time.NewTicker(pingPeriod)
	defer func() {
		ticker.Stop()
		_ = c.conn.Close()
	}()

	for {
		select {
		case msg, ok := <-c.send:
			_ = c.conn.SetWriteDeadline(time.Now().Add(writeWait))
			if !ok {
				_ = c.conn.WriteMessage(websocket.CloseMessage,
					websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""))
				return
			}
			if err := c.conn.WriteMessage(websocket.TextMessage, msg); err != nil {
				return
			}
		case <-ticker.C:
			_ = c.conn.SetWriteDeadline(time.Now().Add(writeWait))
			if err := c.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Helpers
// ─────────────────────────────────────────────────────────────────────────────

// WaitFor blocks until a message of one of the expected types arrives, or the
// context is cancelled.  Returns an error if a MsgError frame arrives instead.
func (c *Client) WaitFor(ctx context.Context, types ...string) (Message, error) {
	want := make(map[string]bool, len(types))
	for _, t := range types {
		want[t] = true
	}
	for {
		select {
		case <-ctx.Done():
			return Message{}, ctx.Err()
		case msg, ok := <-c.recv:
			if !ok {
				return Message{}, fmt.Errorf("signaling: connection closed")
			}
			if msg.Type == protocol.MsgError {
				var e protocol.ErrorPayload
				_ = json.Unmarshal(msg.Payload, &e)
				return Message{}, fmt.Errorf("server error [%s]: %s", e.Code, e.Message)
			}
			if want[msg.Type] {
				return msg, nil
			}
			// Ignore keep-alives and unknown frames while waiting.
		}
	}
}

// DecodeOpaque extracts the base64 data from an OpaquePayload message.
func DecodeOpaque(msg Message) ([]byte, error) {
	var p protocol.OpaquePayload
	if err := json.Unmarshal(msg.Payload, &p); err != nil {
		return nil, fmt.Errorf("signaling: decode opaque: %w", err)
	}
	return decodeB64(p.Data)
}

// DecodeSignedSDP extracts the raw SDP bytes and MAC string from a signed
// SDP message.  Callers must verify the MAC with pake.Session before use.
func DecodeSignedSDP(msg Message) (sdpJSON []byte, mac string, err error) {
	var p signedSDPPayload
	if err := json.Unmarshal(msg.Payload, &p); err != nil {
		return nil, "", fmt.Errorf("signaling: decode signed SDP: %w", err)
	}
	sdpJSON, err = decodeB64(p.SDP)
	if err != nil {
		return nil, "", fmt.Errorf("signaling: decode SDP bytes: %w", err)
	}
	return sdpJSON, p.MAC, nil
}

// DecodeICE extracts an ICECandidatePayload.
func DecodeICE(msg Message) (protocol.ICECandidatePayload, error) {
	var p protocol.ICECandidatePayload
	if err := json.Unmarshal(msg.Payload, &p); err != nil {
		return p, fmt.Errorf("signaling: decode ICE: %w", err)
	}
	return p, nil
}
