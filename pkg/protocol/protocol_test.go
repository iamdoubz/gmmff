package protocol

import (
	"encoding/json"
	"testing"
)

func TestNewEnvelope_RoundTrip(t *testing.T) {
	payload := SlotCreatePayload{ProtocolVersion: "1", SessionType: "files", MaxPeers: 3}
	env, err := NewEnvelope(MsgSlotCreate, payload)
	if err != nil {
		t.Fatalf("NewEnvelope: %v", err)
	}
	if env.Type != MsgSlotCreate {
		t.Errorf("Type = %q, want %q", env.Type, MsgSlotCreate)
	}
	var got SlotCreatePayload
	if err := json.Unmarshal(env.Payload, &got); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if got.ProtocolVersion != "1" || got.SessionType != "files" || got.MaxPeers != 3 {
		t.Errorf("payload mismatch: %+v", got)
	}
}

func TestNewEnvelope_NilPayload(t *testing.T) {
	env, err := NewEnvelope(MsgPing, nil)
	if err != nil {
		t.Fatalf("NewEnvelope with nil: %v", err)
	}
	if env.Type != MsgPing {
		t.Errorf("Type = %q, want %q", env.Type, MsgPing)
	}
	if string(env.Payload) != "null" {
		t.Errorf("Payload = %s, want null", env.Payload)
	}
}

func TestMustEnvelope_Success(t *testing.T) {
	env := MustEnvelope(MsgBye, ErrorPayload{Code: "X", Message: "bye"})
	if env.Type != MsgBye {
		t.Errorf("Type = %q, want %q", env.Type, MsgBye)
	}
}

func TestMustEnvelope_Panics(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic for unmarshalable payload")
		}
	}()
	MustEnvelope("bad", make(chan int))
}

func TestErrorEnvelope(t *testing.T) {
	env := ErrorEnvelope("ERR_TEST", "something broke")
	if env.Type != MsgError {
		t.Errorf("Type = %q, want %q", env.Type, MsgError)
	}
	var ep ErrorPayload
	if err := json.Unmarshal(env.Payload, &ep); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if ep.Code != "ERR_TEST" || ep.Message != "something broke" {
		t.Errorf("ErrorPayload = %+v", ep)
	}
}

func TestEnvelope_JSONRoundTrip(t *testing.T) {
	env := MustEnvelope(MsgSlotCreated, SlotCreatedPayload{
		SlotID: "abc-123", Code: "bear-cozy-cone", TTLSeconds: 600,
	})
	b, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("marshal envelope: %v", err)
	}
	var decoded Envelope
	if err := json.Unmarshal(b, &decoded); err != nil {
		t.Fatalf("unmarshal envelope: %v", err)
	}
	if decoded.Type != MsgSlotCreated {
		t.Errorf("Type = %q, want %q", decoded.Type, MsgSlotCreated)
	}
	var p SlotCreatedPayload
	if err := json.Unmarshal(decoded.Payload, &p); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if p.SlotID != "abc-123" || p.Code != "bear-cozy-cone" || p.TTLSeconds != 600 {
		t.Errorf("payload mismatch: %+v", p)
	}
}
