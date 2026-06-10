package broker

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/iamdoubz/gmmff/v2/internal/store"
	"github.com/iamdoubz/gmmff/v2/pkg/protocol"
)

// ─────────────────────────────────────────────────────────────────────────────
// Suite helpers
// ─────────────────────────────────────────────────────────────────────────────

// newBrokerSuite creates a Broker backed by a MemStore with its hub goroutine
// running, an httptest server to accept WebSocket upgrades, and registers
// cleanup. Tests call dialWS to open client connections.
func newBrokerSuite(t *testing.T) (*Broker, *store.MemStore, *httptest.Server) {
	t.Helper()
	mem := store.NewMemStore()
	b := New(mem)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go b.Run(ctx)

	ts := httptest.NewServer(http.HandlerFunc(b.ServeHTTP))
	t.Cleanup(ts.Close)
	return b, mem, ts
}

// dialWS opens a WebSocket connection to the test server's /ws endpoint.
func dialWS(t *testing.T, ts *httptest.Server) *websocket.Conn {
	t.Helper()
	u := "ws" + strings.TrimPrefix(ts.URL, "http") + "/ws"
	ws, _, err := websocket.DefaultDialer.Dial(u, nil)
	if err != nil {
		t.Fatalf("dialWS: %v", err)
	}
	t.Cleanup(func() { ws.Close() })
	return ws
}

// sendEnv marshals an envelope and writes it to ws.
func sendEnv(t *testing.T, ws *websocket.Conn, msgType string, payload any) {
	t.Helper()
	b, _ := json.Marshal(protocol.MustEnvelope(msgType, payload))
	if err := ws.WriteMessage(websocket.TextMessage, b); err != nil {
		t.Fatalf("sendEnv %q: %v", msgType, err)
	}
}

// recvEnv reads one envelope from ws with a 3-second deadline.
func recvEnv(t *testing.T, ws *websocket.Conn) protocol.Envelope {
	t.Helper()
	ws.SetReadDeadline(time.Now().Add(3 * time.Second)) //nolint:errcheck
	_, msg, err := ws.ReadMessage()
	if err != nil {
		t.Fatalf("recvEnv: %v", err)
	}
	var env protocol.Envelope
	if err := json.Unmarshal(msg, &env); err != nil {
		t.Fatalf("recvEnv unmarshal: %v", err)
	}
	return env
}

// assertErrorCode verifies an envelope is a MsgError with the given code.
func assertErrorCode(t *testing.T, env protocol.Envelope, wantCode string) {
	t.Helper()
	if env.Type != protocol.MsgError {
		t.Errorf("type: got %q, want %q", env.Type, protocol.MsgError)
		return
	}
	var p protocol.ErrorPayload
	json.Unmarshal(env.Payload, &p) //nolint:errcheck
	if p.Code != wantCode {
		t.Errorf("error code: got %q, want %q", p.Code, wantCode)
	}
}

// doSlotCreate sends slot.create and returns the assigned code.
func doSlotCreate(t *testing.T, ws *websocket.Conn, sessionType string, maxPeers int) (code string) {
	t.Helper()
	sendEnv(t, ws, protocol.MsgSlotCreate, protocol.SlotCreatePayload{
		ProtocolVersion: protocol.Version,
		SessionType:     sessionType,
		MaxPeers:        maxPeers,
	})
	env := recvEnv(t, ws)
	if env.Type != protocol.MsgSlotCreated {
		t.Fatalf("slot.create: expected slot.created, got %q", env.Type)
	}
	var p protocol.SlotCreatedPayload
	json.Unmarshal(env.Payload, &p) //nolint:errcheck
	return p.Code
}

// doSlotJoin sends slot.join and returns the slot.ready envelope.
func doSlotJoin(t *testing.T, ws *websocket.Conn, code string) protocol.Envelope {
	t.Helper()
	sendEnv(t, ws, protocol.MsgSlotJoin, protocol.SlotJoinPayload{
		ProtocolVersion: protocol.Version,
		Code:            code,
	})
	return recvEnv(t, ws)
}

// ─────────────────────────────────────────────────────────────────────────────
// Slot creation
// ─────────────────────────────────────────────────────────────────────────────

func TestBroker_SlotCreate_ReturnsCodeAndSlotID(t *testing.T) {
	_, _, ts := newBrokerSuite(t)
	ws := dialWS(t, ts)

	sendEnv(t, ws, protocol.MsgSlotCreate, protocol.SlotCreatePayload{
		ProtocolVersion: protocol.Version,
		SessionType:     "files",
		MaxPeers:        2,
	})
	env := recvEnv(t, ws)

	if env.Type != protocol.MsgSlotCreated {
		t.Fatalf("expected slot.created, got %q", env.Type)
	}
	var p protocol.SlotCreatedPayload
	json.Unmarshal(env.Payload, &p) //nolint:errcheck
	if p.Code == "" {
		t.Error("code should not be empty")
	}
	if p.SlotID == "" {
		t.Error("slot_id should not be empty")
	}
	if p.TTLSeconds <= 0 {
		t.Errorf("ttl_seconds: got %d, want > 0", p.TTLSeconds)
	}
	if p.MaxPeers != 2 {
		t.Errorf("max_peers: got %d, want 2", p.MaxPeers)
	}
}

func TestBroker_SlotCreate_VersionMismatch_ReturnsError(t *testing.T) {
	_, _, ts := newBrokerSuite(t)
	ws := dialWS(t, ts)

	sendEnv(t, ws, protocol.MsgSlotCreate, protocol.SlotCreatePayload{
		ProtocolVersion: "999",
	})
	assertErrorCode(t, recvEnv(t, ws), "ERR_VERSION_MISMATCH")
}

// ─────────────────────────────────────────────────────────────────────────────
// Slot join — validation
// ─────────────────────────────────────────────────────────────────────────────

func TestBroker_SlotJoin_VersionMismatch_ReturnsError(t *testing.T) {
	_, _, ts := newBrokerSuite(t)
	initiatorWS := dialWS(t, ts)
	joinerWS := dialWS(t, ts)

	code := doSlotCreate(t, initiatorWS, "files", 2)

	sendEnv(t, joinerWS, protocol.MsgSlotJoin, protocol.SlotJoinPayload{
		ProtocolVersion: "999",
		Code:            code,
	})
	assertErrorCode(t, recvEnv(t, joinerWS), "ERR_VERSION_MISMATCH")
}

func TestBroker_SlotJoin_InvalidCodeFormat_ReturnsError(t *testing.T) {
	_, _, ts := newBrokerSuite(t)
	ws := dialWS(t, ts)

	// "x-x-x" fails ValidateCode (each part is 1 char, minimum is 2).
	sendEnv(t, ws, protocol.MsgSlotJoin, protocol.SlotJoinPayload{
		ProtocolVersion: protocol.Version,
		Code:            "x-x-x",
	})
	assertErrorCode(t, recvEnv(t, ws), "ERR_INVALID_CODE")
}

func TestBroker_SlotJoin_SlotNotFound_ReturnsError(t *testing.T) {
	_, _, ts := newBrokerSuite(t)
	ws := dialWS(t, ts)

	// Valid 3-part format but no such slot exists in the store.
	sendEnv(t, ws, protocol.MsgSlotJoin, protocol.SlotJoinPayload{
		ProtocolVersion: protocol.Version,
		Code:            "abc-def-ghi",
	})
	assertErrorCode(t, recvEnv(t, ws), "ERR_SLOT_NOT_FOUND")
}

func TestBroker_SlotJoin_ExpiredSlot_ReturnsError(t *testing.T) {
	_, mem, ts := newBrokerSuite(t)
	initiatorWS := dialWS(t, ts)
	joinerWS := dialWS(t, ts)

	code := doSlotCreate(t, initiatorWS, "files", 2)

	// Expire the slot by backdating it in the MemStore.
	ctx := context.Background()
	sl, err := mem.GetByCode(ctx, code)
	if err != nil {
		t.Fatalf("GetByCode: %v", err)
	}
	sl.ExpiresAt = sl.ExpiresAt.Add(-24 * time.Hour) // now in the past
	if err := mem.Update(ctx, sl); err != nil {
		t.Fatalf("Update: %v", err)
	}

	sendEnv(t, joinerWS, protocol.MsgSlotJoin, protocol.SlotJoinPayload{
		ProtocolVersion: protocol.Version,
		Code:            code,
	})
	assertErrorCode(t, recvEnv(t, joinerWS), "ERR_SLOT_EXPIRED")
}

// ─────────────────────────────────────────────────────────────────────────────
// Slot join — successful handshake
// ─────────────────────────────────────────────────────────────────────────────

func TestBroker_SlotJoin_Success_BothPeersGetSlotReady(t *testing.T) {
	_, _, ts := newBrokerSuite(t)
	initiatorWS := dialWS(t, ts)
	joinerWS := dialWS(t, ts)

	code := doSlotCreate(t, initiatorWS, "files", 2)

	// Joiner joins — expects slot.ready with role=responder.
	env := doSlotJoin(t, joinerWS, code)
	if env.Type != protocol.MsgSlotReady {
		t.Fatalf("joiner: expected slot.ready, got %q", env.Type)
	}
	var joinerReady protocol.SlotReadyPayload
	json.Unmarshal(env.Payload, &joinerReady) //nolint:errcheck
	if joinerReady.Role != "responder" {
		t.Errorf("joiner role: got %q, want responder", joinerReady.Role)
	}

	// Initiator gets peer.joined first, then slot.ready (first join).
	peerJoinedEnv := recvEnv(t, initiatorWS)
	if peerJoinedEnv.Type != protocol.MsgPeerJoined {
		t.Errorf("initiator: expected peer.joined first, got %q", peerJoinedEnv.Type)
	}

	slotReadyEnv := recvEnv(t, initiatorWS)
	if slotReadyEnv.Type != protocol.MsgSlotReady {
		t.Fatalf("initiator: expected slot.ready second, got %q", slotReadyEnv.Type)
	}
	var initiatorReady protocol.SlotReadyPayload
	json.Unmarshal(slotReadyEnv.Payload, &initiatorReady) //nolint:errcheck
	if initiatorReady.Role != "initiator" {
		t.Errorf("initiator role: got %q, want initiator", initiatorReady.Role)
	}
}

func TestBroker_SlotJoin_SlotFull_ThirdPeerRejected(t *testing.T) {
	_, _, ts := newBrokerSuite(t)
	initiatorWS := dialWS(t, ts)
	joiner1WS := dialWS(t, ts)
	joiner2WS := dialWS(t, ts)

	// Create with max_peers=2: initiator+1 joiner fills the slot.
	code := doSlotCreate(t, initiatorWS, "files", 2)

	// First joiner fills the slot.
	if env := doSlotJoin(t, joiner1WS, code); env.Type != protocol.MsgSlotReady {
		t.Fatalf("joiner1: expected slot.ready, got %q", env.Type)
	}
	// Drain initiator's peer.joined + slot.ready.
	recvEnv(t, initiatorWS)
	recvEnv(t, initiatorWS)

	// Second joiner should be rejected.
	rejectedEnv := doSlotJoin(t, joiner2WS, code)
	assertErrorCode(t, rejectedEnv, "ERR_SLOT_FULL")
}

// ─────────────────────────────────────────────────────────────────────────────
// Relay
// ─────────────────────────────────────────────────────────────────────────────

func TestBroker_Relay_NotInSlot_ReturnsError(t *testing.T) {
	_, _, ts := newBrokerSuite(t)
	ws := dialWS(t, ts)

	// Send a pake.a without having joined any slot first.
	sendEnv(t, ws, protocol.MsgPakeA, map[string]string{"data": "opaque"})
	assertErrorCode(t, recvEnv(t, ws), "ERR_NOT_IN_SLOT")
}

func TestBroker_Relay_JoinerToInitiator_StarTopology(t *testing.T) {
	_, _, ts := newBrokerSuite(t)
	initiatorWS := dialWS(t, ts)
	joinerWS := dialWS(t, ts)

	code := doSlotCreate(t, initiatorWS, "files", 2)
	if env := doSlotJoin(t, joinerWS, code); env.Type != protocol.MsgSlotReady {
		t.Fatalf("join failed: %v", env.Type)
	}
	// Drain initiator's peer.joined + slot.ready before testing relay.
	recvEnv(t, initiatorWS)
	recvEnv(t, initiatorWS)

	// Joiner sends a pake.b — should be routed to initiator via star topology.
	sendEnv(t, joinerWS, protocol.MsgPakeB, map[string]string{"data": "pake-payload"})

	relayed := recvEnv(t, initiatorWS)
	if relayed.Type != protocol.MsgPakeB {
		t.Errorf("initiator: expected pake.b relay, got %q", relayed.Type)
	}
}

func TestBroker_Relay_Targeted_RoutesToSpecificPeer(t *testing.T) {
	_, _, ts := newBrokerSuite(t)
	initiatorWS := dialWS(t, ts)
	joinerWS := dialWS(t, ts)

	code := doSlotCreate(t, initiatorWS, "files", 2)

	// Capture joiner's peer ID from the peer.joined message.
	doSlotJoin(t, joinerWS, code) // joiner gets slot.ready

	peerJoinedEnv := recvEnv(t, initiatorWS) // initiator: peer.joined
	recvEnv(t, initiatorWS)                  // initiator: slot.ready (first join)

	var peerJoined protocol.PeerJoinedPayload
	json.Unmarshal(peerJoinedEnv.Payload, &peerJoined) //nolint:errcheck
	joinerPeerID := peerJoined.PeerID
	if joinerPeerID == "" {
		t.Fatal("peer_id in peer.joined should not be empty")
	}

	// Initiator sends a targeted message to the joiner.
	sendEnv(t, initiatorWS, protocol.MsgTargeted, protocol.TargetedPayload{
		TargetPeerID: joinerPeerID,
		FromPeerID:   "initiator-conn",
		Inner:        json.RawMessage(`{"type":"sdp.offer","payload":{}}`),
	})

	delivered := recvEnv(t, joinerWS)
	if delivered.Type != protocol.MsgTargeted {
		t.Errorf("joiner: expected targeted relay, got %q", delivered.Type)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Disconnect
// ─────────────────────────────────────────────────────────────────────────────

func TestBroker_Disconnect_InitiatorLeaves_JoinerGetsPeerLeft(t *testing.T) {
	_, _, ts := newBrokerSuite(t)
	initiatorWS := dialWS(t, ts)
	joinerWS := dialWS(t, ts)

	code := doSlotCreate(t, initiatorWS, "files", 2)
	if env := doSlotJoin(t, joinerWS, code); env.Type != protocol.MsgSlotReady {
		t.Fatalf("join: expected slot.ready, got %q", env.Type)
	}
	// Drain initiator's join notifications.
	recvEnv(t, initiatorWS) // peer.joined
	recvEnv(t, initiatorWS) // slot.ready

	// Initiator disconnects abruptly.
	initiatorWS.Close()

	// Joiner should receive peer.left.
	env := recvEnv(t, joinerWS)
	if env.Type != protocol.MsgPeerLeft {
		t.Errorf("joiner: expected peer.left after initiator disconnect, got %q", env.Type)
	}
}

func TestBroker_Bye_PropagatesToPeer(t *testing.T) {
	_, _, ts := newBrokerSuite(t)
	initiatorWS := dialWS(t, ts)
	joinerWS := dialWS(t, ts)

	code := doSlotCreate(t, initiatorWS, "files", 2)
	if env := doSlotJoin(t, joinerWS, code); env.Type != protocol.MsgSlotReady {
		t.Fatalf("join: expected slot.ready, got %q", env.Type)
	}
	recvEnv(t, initiatorWS) // peer.joined
	recvEnv(t, initiatorWS) // slot.ready

	// Joiner sends a graceful bye.
	sendEnv(t, joinerWS, protocol.MsgBye, nil)

	// Initiator should receive bye.
	env := recvEnv(t, initiatorWS)
	if env.Type != protocol.MsgBye {
		t.Errorf("initiator: expected bye, got %q", env.Type)
	}
}

func TestBroker_UnknownMessageType_ReturnsError(t *testing.T) {
	_, _, ts := newBrokerSuite(t)
	ws := dialWS(t, ts)

	sendEnv(t, ws, "totally.unknown", nil)
	assertErrorCode(t, recvEnv(t, ws), "ERR_UNKNOWN_MSG_TYPE")
}
