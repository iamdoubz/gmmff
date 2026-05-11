// Package broker implements the WebSocket signaling broker for gmmff.
//
// Responsibilities:
//  1. Accept WebSocket connections and register them as named connections.
//  2. Route protocol messages between the two peers sharing a slot.
//  3. Enforce the slot lifecycle (create → waiting → ready → closed).
//  4. Gate-keep relay: the broker NEVER decodes PAKE/SDP/ICE payloads —
//     it forwards them opaquely so the server cannot intercept the session.
//
// Concurrency model:
//   - One goroutine per connection (readPump) reads inbound messages.
//   - One goroutine per connection (writePump) serialises outbound writes.
//   - The hub goroutine owns all slot/connection maps — no mutexes needed.
//   - All cross-goroutine communication uses channels.
package broker

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sync"
	"time"

	"github.com/iamdoubz/gmmff/internal/crypto"
	applog "github.com/iamdoubz/gmmff/internal/log"
	"github.com/iamdoubz/gmmff/internal/slot"
	"github.com/iamdoubz/gmmff/internal/store"
	"github.com/iamdoubz/gmmff/pkg/protocol"
	"github.com/google/uuid"
	"github.com/gorilla/websocket"
)

// ─────────────────────────────────────────────────────────────────────────────
// Tuning constants
// ─────────────────────────────────────────────────────────────────────────────

const (
	// writeWait is the deadline for a single WebSocket write.
	writeWait = 10 * time.Second

	// pongWait is the maximum time we wait for a pong after sending a ping.
	pongWait = 60 * time.Second

	// pingPeriod is how often we send keep-alive pings (must be < pongWait).
	pingPeriod = 50 * time.Second

	// maxMessageBytes caps individual incoming frames.
	maxMessageBytes = slot.MaxMessageSize

	// sendBufSize is the outbound channel depth per connection.
	sendBufSize = 16
)

var logger = applog.Component("broker")

// ─────────────────────────────────────────────────────────────────────────────
// Conn — a single WebSocket peer
// ─────────────────────────────────────────────────────────────────────────────

// conn represents one live WebSocket connection.
type conn struct {
	id     string          // unique connection UUID (not shown to users)
	slotID string          // slot this connection belongs to (empty until joined)
	ws     *websocket.Conn
	send   chan []byte // buffered outbound queue; writePump drains it
	broker *Broker
	once   sync.Once // ensures close() is idempotent
}

// enqueue puts a message on the send channel, dropping silently if full.
// Dropping is safer than blocking the hub goroutine.
func (c *conn) enqueue(msg []byte) {
	select {
	case c.send <- msg:
	default:
		logger().Warn().Str("error_code", "ERR_SEND_BUFFER_FULL").
			Str("conn_id", c.id).
			Msg("outbound buffer full — frame dropped")
	}
}

// sendEnvelope marshals an Envelope and enqueues it.
func (c *conn) sendEnvelope(env protocol.Envelope) {
	b, err := json.Marshal(env)
	if err != nil {
		logger().Error().Str("error_code", "ERR_MARSHAL").Msg("envelope marshal failed")
		return
	}
	c.enqueue(b)
}

// close tears down the connection once.
func (c *conn) close() {
	c.once.Do(func() {
		close(c.send)
		_ = c.ws.Close()
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// Hub message types (internal)
// ─────────────────────────────────────────────────────────────────────────────

type registerMsg struct{ c *conn }
type unregisterMsg struct{ c *conn }
type inboundMsg struct {
	c   *conn
	env protocol.Envelope
}

// ─────────────────────────────────────────────────────────────────────────────
// Broker
// ─────────────────────────────────────────────────────────────────────────────

// Broker is the central hub that owns all connection and slot state.
type Broker struct {
	store    store.SlotStore
	upgrader websocket.Upgrader

	// All fields below are owned exclusively by the hub goroutine.
	conns   map[string]*conn   // conn_id → conn
	register   chan registerMsg
	unregister chan unregisterMsg
	inbound    chan inboundMsg
}

// New creates a Broker backed by the given SlotStore.
func New(st store.SlotStore) *Broker {
	b := &Broker{
		store: st,
		upgrader: websocket.Upgrader{
			ReadBufferSize:  4096,
			WriteBufferSize: 4096,
			// Allow all origins; TLS + PAKE handle security.
			// A production deployment should restrict this to the
			// known frontend origin.
			CheckOrigin: func(r *http.Request) bool { return true },
		},
		conns:      make(map[string]*conn),
		register:   make(chan registerMsg, 64),
		unregister: make(chan unregisterMsg, 64),
		inbound:    make(chan inboundMsg, 256),
	}
	return b
}

// Run starts the hub event loop.  Call in a dedicated goroutine.
// Blocks until ctx is cancelled.
func (b *Broker) Run(ctx context.Context) {
	logger().Info().Msg("hub started")
	for {
		select {
		case <-ctx.Done():
			logger().Info().Msg("hub stopping")
			return

		case msg := <-b.register:
			b.conns[msg.c.id] = msg.c
			logger().Debug().Str("conn_id", msg.c.id).Msg("connection registered")

		case msg := <-b.unregister:
			b.handleDisconnect(ctx, msg.c)

		case msg := <-b.inbound:
			b.handleMessage(ctx, msg.c, msg.env)
		}
	}
}

// ServeHTTP upgrades HTTP connections to WebSocket and starts per-conn pumps.
func (b *Broker) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	ws, err := b.upgrader.Upgrade(w, r, nil)
	if err != nil {
		// Upgrader already wrote a 4xx response.
		return
	}

	c := &conn{
		id:     uuid.New().String(),
		ws:     ws,
		send:   make(chan []byte, sendBufSize),
		broker: b,
	}

	b.register <- registerMsg{c}

	go c.writePump()
	go c.readPump(b)
}

// ─────────────────────────────────────────────────────────────────────────────
// Hub message handlers (all called from the hub goroutine)
// ─────────────────────────────────────────────────────────────────────────────

func (b *Broker) handleDisconnect(ctx context.Context, c *conn) {
	if _, ok := b.conns[c.id]; !ok {
		return // already removed
	}
	delete(b.conns, c.id)
	c.close()

	if c.slotID == "" {
		return
	}

	// Notify the peer if the slot still exists.
	sl, err := b.store.GetByID(ctx, c.slotID)
	if err != nil {
		return
	}
	if peerID, ok := sl.PeerOf(c.id); ok {
		if peer, ok := b.conns[peerID]; ok {
			peer.sendEnvelope(protocol.MustEnvelope(protocol.MsgBye, nil))
		}
	}

	// Mark closed and clean up.
	sl.Close()
	_ = b.store.Delete(ctx, c.slotID)

	logger().Info().Str("slot_id", c.slotID).Msg("slot closed on disconnect")
}

func (b *Broker) handleMessage(ctx context.Context, c *conn, env protocol.Envelope) {
	switch env.Type {

	case protocol.MsgSlotCreate:
		b.handleSlotCreate(ctx, c, env)

	case protocol.MsgSlotJoin:
		b.handleSlotJoin(ctx, c, env)

	case protocol.MsgPakeA, protocol.MsgPakeB,
		protocol.MsgSDPOffer, protocol.MsgSDPAnswer,
		protocol.MsgICECandidate:
		// Opaque relay — forward to the other peer without inspection.
		b.relay(ctx, c, env)

	case protocol.MsgBye:
		b.handleBye(ctx, c)

	case protocol.MsgPong:
		// Keep-alive pong — nothing to do.

	default:
		c.sendEnvelope(protocol.ErrorEnvelope("ERR_UNKNOWN_MSG_TYPE",
			"unrecognised message type"))
	}
}

// handleSlotCreate processes a slot.create request from the initiator.
func (b *Broker) handleSlotCreate(ctx context.Context, c *conn, env protocol.Envelope) {
	var payload protocol.SlotCreatePayload
	if err := json.Unmarshal(env.Payload, &payload); err != nil {
		c.sendEnvelope(protocol.ErrorEnvelope("ERR_BAD_PAYLOAD", "malformed slot.create payload"))
		return
	}
	if payload.ProtocolVersion != protocol.Version {
		c.sendEnvelope(protocol.ErrorEnvelope("ERR_VERSION_MISMATCH",
			"unsupported protocol version; please update your gmmff client"))
		return
	}

	code, err := crypto.GenerateCode()
	if err != nil {
		logger().Error().Str("error_code", "ERR_CODEGEN").Msg("code generation failed")
		c.sendEnvelope(protocol.ErrorEnvelope("ERR_INTERNAL", "server error; please retry"))
		return
	}

	slotID := uuid.New().String()
	sl := slot.New(slotID, code, c.id, payload.SessionType)
	if err := b.store.Create(ctx, sl); err != nil {
		logger().Error().Str("error_code", "ERR_STORE_CREATE").Str("slot_id", slotID).Msg("failed to persist slot")
		c.sendEnvelope(protocol.ErrorEnvelope("ERR_INTERNAL", "server error; please retry"))
		return
	}

	c.slotID = slotID

	c.sendEnvelope(protocol.MustEnvelope(protocol.MsgSlotCreated, protocol.SlotCreatedPayload{
		SlotID:      slotID,
		Code:        code,
		TTLSeconds:  int(slot.DefaultTTL.Seconds()),
		SessionType: payload.SessionType,
	}))

	logger().Info().Str("slot_id", slotID).Msg("slot created")
}

// handleSlotJoin processes a slot.join request from the responder.
func (b *Broker) handleSlotJoin(ctx context.Context, c *conn, env protocol.Envelope) {
	var payload protocol.SlotJoinPayload
	if err := json.Unmarshal(env.Payload, &payload); err != nil {
		c.sendEnvelope(protocol.ErrorEnvelope("ERR_BAD_PAYLOAD", "malformed slot.join payload"))
		return
	}
	if payload.ProtocolVersion != protocol.Version {
		c.sendEnvelope(protocol.ErrorEnvelope("ERR_VERSION_MISMATCH",
			"unsupported protocol version; please update your gmmff client"))
		return
	}
	if !crypto.ValidateCode(payload.Code) {
		c.sendEnvelope(protocol.ErrorEnvelope("ERR_INVALID_CODE", "invalid slot code format"))
		return
	}

	sl, err := b.store.GetByCode(ctx, payload.Code)
	if err != nil {
		if errors.Is(err, slot.ErrSlotNotFound) {
			c.sendEnvelope(protocol.ErrorEnvelope("ERR_SLOT_NOT_FOUND",
				"slot not found — check the code or ask the sender to create a new one"))
			return
		}
		logger().Error().Str("error_code", "ERR_STORE_LOOKUP").Msg("slot lookup failed")
		c.sendEnvelope(protocol.ErrorEnvelope("ERR_INTERNAL", "server error; please retry"))
		return
	}

	if sl.IsExpired() {
		_ = b.store.Delete(ctx, sl.ID)
		c.sendEnvelope(protocol.ErrorEnvelope("ERR_SLOT_EXPIRED",
			"slot has expired — ask the sender to create a new one"))
		return
	}

	if err := sl.Join(c.id); err != nil {
		switch {
		case errors.Is(err, slot.ErrSlotFull):
			c.sendEnvelope(protocol.ErrorEnvelope("ERR_SLOT_FULL",
				"slot already has two peers"))
		case errors.Is(err, slot.ErrSlotClosed):
			c.sendEnvelope(protocol.ErrorEnvelope("ERR_SLOT_CLOSED",
				"slot is closed — ask the sender to create a new one"))
		default:
			c.sendEnvelope(protocol.ErrorEnvelope("ERR_INTERNAL", "server error"))
		}
		return
	}

	if err := b.store.Update(ctx, sl); err != nil {
		logger().Error().Str("error_code", "ERR_STORE_UPDATE").Str("slot_id", sl.ID).Msg("failed to update slot")
		c.sendEnvelope(protocol.ErrorEnvelope("ERR_INTERNAL", "server error; please retry"))
		return
	}

	c.slotID = sl.ID

	// Notify both peers of their roles.
	initiator, ok := b.conns[sl.InitiatorID]
	if !ok {
		// Initiator disconnected while we were processing.
		c.sendEnvelope(protocol.ErrorEnvelope("ERR_PEER_GONE",
			"the sender disconnected before the session could start"))
		_ = b.store.Delete(ctx, sl.ID)
		return
	}

	initiator.sendEnvelope(protocol.MustEnvelope(protocol.MsgSlotReady,
		protocol.SlotReadyPayload{Role: "initiator", SessionType: sl.SessionType}))
	c.sendEnvelope(protocol.MustEnvelope(protocol.MsgSlotReady,
		protocol.SlotReadyPayload{Role: "responder", SessionType: sl.SessionType}))

	logger().Info().Str("slot_id", sl.ID).Msg("slot ready — both peers connected")
}

// relay forwards an opaque message to the other peer in the slot.
func (b *Broker) relay(ctx context.Context, c *conn, env protocol.Envelope) {
	if c.slotID == "" {
		c.sendEnvelope(protocol.ErrorEnvelope("ERR_NOT_IN_SLOT",
			"must join a slot before sending signaling messages"))
		return
	}

	sl, err := b.store.GetByID(ctx, c.slotID)
	if err != nil {
		c.sendEnvelope(protocol.ErrorEnvelope("ERR_SLOT_NOT_FOUND", "slot not found"))
		return
	}

	peerID, ok := sl.PeerOf(c.id)
	if !ok {
		c.sendEnvelope(protocol.ErrorEnvelope("ERR_PEER_GONE", "peer is not connected"))
		return
	}

	peer, ok := b.conns[peerID]
	if !ok {
		c.sendEnvelope(protocol.ErrorEnvelope("ERR_PEER_GONE",
			"peer disconnected — the transfer cannot continue"))
		return
	}

	peer.sendEnvelope(env)
}

// handleBye tears down the slot on explicit close.
func (b *Broker) handleBye(ctx context.Context, c *conn) {
	if c.slotID == "" {
		return
	}
	sl, err := b.store.GetByID(ctx, c.slotID)
	if err != nil {
		return
	}
	if peerID, ok := sl.PeerOf(c.id); ok {
		if peer, ok := b.conns[peerID]; ok {
			peer.sendEnvelope(protocol.MustEnvelope(protocol.MsgBye, nil))
		}
	}
	_ = b.store.Delete(ctx, c.slotID)
	logger().Info().Str("slot_id", c.slotID).Msg("slot closed by bye")
}

// ─────────────────────────────────────────────────────────────────────────────
// Per-connection pumps
// ─────────────────────────────────────────────────────────────────────────────

// readPump reads WebSocket frames from the client and forwards parsed
// Envelopes to the hub inbound channel.
func (c *conn) readPump(b *Broker) {
	defer func() {
		b.unregister <- unregisterMsg{c}
	}()

	c.ws.SetReadLimit(maxMessageBytes)
	_ = c.ws.SetReadDeadline(time.Now().Add(pongWait))
	c.ws.SetPongHandler(func(string) error {
		return c.ws.SetReadDeadline(time.Now().Add(pongWait))
	})

	for {
		_, msg, err := c.ws.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err,
				websocket.CloseGoingAway,
				websocket.CloseNormalClosure,
				websocket.CloseNoStatusReceived) {
				logger().Debug().Str("error_code", "ERR_WS_READ").
					Str("conn_id", c.id).Msg("unexpected WebSocket close")
			}
			return
		}

		var env protocol.Envelope
		if err := json.Unmarshal(msg, &env); err != nil {
			// Bad JSON from client — enqueue an error directly (no hub round-trip needed).
			c.enqueue(mustMarshal(protocol.ErrorEnvelope("ERR_BAD_JSON",
				"message must be a JSON envelope")))
			continue
		}

		b.inbound <- inboundMsg{c: c, env: env}
	}
}

// writePump drains c.send and writes frames to the WebSocket.
// Runs one ping ticker to keep the connection alive.
func (c *conn) writePump() {
	ticker := time.NewTicker(pingPeriod)
	defer func() {
		ticker.Stop()
		_ = c.ws.Close()
	}()

	for {
		select {
		case msg, ok := <-c.send:
			_ = c.ws.SetWriteDeadline(time.Now().Add(writeWait))
			if !ok {
				// Channel closed — send a normal close frame.
				_ = c.ws.WriteMessage(websocket.CloseMessage,
					websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""))
				return
			}
			if err := c.ws.WriteMessage(websocket.TextMessage, msg); err != nil {
				return
			}

		case <-ticker.C:
			_ = c.ws.SetWriteDeadline(time.Now().Add(writeWait))
			if err := c.ws.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}
}

// mustMarshal marshals v to JSON or panics — only for statically-known types.
func mustMarshal(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}
