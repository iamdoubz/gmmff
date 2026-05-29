//go:build js

// Package signaling — browser implementation (GOOS=js).
//
// In the browser, gorilla/websocket cannot be used because it relies on Go's
// net stack which has no DNS or TCP in Wasm.  Instead we wrap the browser's
// native WebSocket API via syscall/js.  The exported API (Connect, Client,
// Message, WaitFor, etc.) is identical to the native implementation so all
// callers in internal/peer work unchanged.
package signaling

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/iamdoubz/gmmff/v2/pkg/protocol"
	"syscall/js"
)

// Message is a decoded inbound message from the signaling server.
type Message struct {
	Type    string
	Payload json.RawMessage
}

// Client is a live WebSocket connection backed by the browser's native API.
type Client struct {
	ws   js.Value // browser WebSocket object
	send chan []byte
	recv chan Message
	done chan struct{}
	once sync.Once
}

// Connect opens a browser-native WebSocket to wsURL.
// The browser resolves the hostname — no Go DNS lookup occurs.
func Connect(ctx context.Context, wsURL string) (*Client, error) {
	ws := js.Global().Get("WebSocket").New(wsURL)
	if ws.IsUndefined() || ws.IsNull() {
		return nil, fmt.Errorf("signaling: browser WebSocket not available")
	}

	c := &Client{
		ws:   ws,
		send: make(chan []byte, 32),
		recv: make(chan Message, 64),
		done: make(chan struct{}),
	}

	// Wait for the connection to open (or fail) before returning.
	opened := make(chan error, 1)

	onOpen := js.FuncOf(func(_ js.Value, _ []js.Value) any {
		opened <- nil
		return nil
	})
	onErr := js.FuncOf(func(_ js.Value, args []js.Value) any {
		msg := "WebSocket connection failed"
		if len(args) > 0 && !args[0].IsUndefined() {
			if m := args[0].Get("message"); !m.IsUndefined() {
				msg = m.String()
			}
		}
		opened <- fmt.Errorf("signaling: %s", msg)
		return nil
	})
	ws.Set("onopen", onOpen)
	ws.Set("onerror", onErr)

	select {
	case <-ctx.Done():
		ws.Call("close")
		onOpen.Release()
		onErr.Release()
		return nil, ctx.Err()
	case err := <-opened:
		onOpen.Release()
		onErr.Release()
		if err != nil {
			return nil, err
		}
	}

	// Register persistent message and close handlers.
	onMsg := js.FuncOf(func(_ js.Value, args []js.Value) any {
		if len(args) == 0 {
			return nil
		}
		data := args[0].Get("data").String()
		var env protocol.Envelope
		if jsonErr := json.Unmarshal([]byte(data), &env); jsonErr != nil {
			return nil
		}
		select {
		case c.recv <- Message{Type: env.Type, Payload: env.Payload}:
		default:
		}
		return nil
	})
	onClose := js.FuncOf(func(_ js.Value, _ []js.Value) any {
		c.closeOnce()
		onMsg.Release()
		return nil
	})
	ws.Set("onmessage", onMsg)
	ws.Set("onclose", onClose)

	go c.writePump()
	return c, nil
}

// ─────────────────────────────────────────────────────────────────────────────
// Public API — identical surface to client_native.go
// ─────────────────────────────────────────────────────────────────────────────

func (c *Client) Recv() <-chan Message  { return c.recv }
func (c *Client) Done() <-chan struct{} { return c.done }

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

func (c *Client) Close() {
	_ = c.Send(protocol.MustEnvelope(protocol.MsgBye, nil))
	c.closeOnce()
}

func (c *Client) closeOnce() {
	c.once.Do(func() {
		close(c.send)
		close(c.done)
		close(c.recv)
		c.ws.Call("close")
	})
}

func (c *Client) CreateSlot(sessionType string, maxPeers int) error {
	return c.Send(protocol.MustEnvelope(protocol.MsgSlotCreate,
		protocol.SlotCreatePayload{ProtocolVersion: protocol.Version, SessionType: sessionType, MaxPeers: maxPeers}))
}

func (c *Client) JoinSlot(code string) error {
	return c.Send(protocol.MustEnvelope(protocol.MsgSlotJoin,
		protocol.SlotJoinPayload{Code: code, ProtocolVersion: protocol.Version}))
}

func (c *Client) SendOpaque(msgType string, data []byte) error {
	return c.Send(protocol.MustEnvelope(msgType, protocol.OpaquePayload{Data: encodeB64(data)}))
}

func (c *Client) SendSignedSDP(msgType string, sdpJSON []byte, mac string) error {
	return c.Send(protocol.MustEnvelope(msgType, signedSDPPayload{
		SDP: encodeB64(sdpJSON),
		MAC: mac,
	}))
}

type signedSDPPayload struct {
	SDP string `json:"sdp"`
	MAC string `json:"mac"`
}

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
// Write pump
// ─────────────────────────────────────────────────────────────────────────────

func (c *Client) writePump() {
	for {
		select {
		case msg, ok := <-c.send:
			if !ok {
				return
			}
			c.ws.Call("send", string(msg))
		case <-c.done:
			return
		}
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Helpers (shared with native via b64.go which has no build tag)
// ─────────────────────────────────────────────────────────────────────────────

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
		}
	}
}

func DecodeOpaque(msg Message) ([]byte, error) {
	var p protocol.OpaquePayload
	if err := json.Unmarshal(msg.Payload, &p); err != nil {
		return nil, fmt.Errorf("signaling: decode opaque: %w", err)
	}
	return decodeB64(p.Data)
}

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

func DecodeICE(msg Message) (protocol.ICECandidatePayload, error) {
	var p protocol.ICECandidatePayload
	if err := json.Unmarshal(msg.Payload, &p); err != nil {
		return p, fmt.Errorf("signaling: decode ICE: %w", err)
	}
	return p, nil
}
