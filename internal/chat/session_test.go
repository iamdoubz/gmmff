package chat

import (
	"testing"

	"github.com/iamdoubz/gmmff/v2/internal/transfer"
)

func TestHandleChatFrame_Message(t *testing.T) {
	var gotFrom, gotText string
	s := &Session{
		remoteLabel: "Peer",
		onMsg:       func(from, text string) { gotFrom = from; gotText = text },
		onClose:     func(string) {},
		onLeave:     func(string) {},
	}
	done := make(chan struct{})
	called := false
	resetIdle := func() { called = true }

	frame := transfer.BuildMessageFrame("hello")
	s.handleChatFrame(frame, done, resetIdle)

	if gotFrom != "Peer" || gotText != "hello" {
		t.Errorf("onMsg(%q, %q), want (Peer, hello)", gotFrom, gotText)
	}
	if !called {
		t.Error("resetIdle not called on message")
	}
}

func TestHandleChatFrame_ChatClose(t *testing.T) {
	var closeReason string
	s := &Session{
		remoteLabel: "Peer",
		onMsg:       func(string, string) {},
		onClose:     func(r string) { closeReason = r },
		onLeave:     func(string) {},
	}
	done := make(chan struct{})

	frame := transfer.BuildChatCloseFrame()
	s.handleChatFrame(frame, done, func() {})

	if closeReason == "" {
		t.Error("onClose not called")
	}
	select {
	case <-done:
	default:
		t.Error("done channel not closed on ChatClose")
	}
}

func TestHandleChatFrame_Cancelled(t *testing.T) {
	var closeReason string
	s := &Session{
		remoteLabel: "Peer",
		onMsg:       func(string, string) {},
		onClose:     func(r string) { closeReason = r },
		onLeave:     func(string) {},
	}
	done := make(chan struct{})

	frame := transfer.BuildCancelledFrame()
	s.handleChatFrame(frame, done, func() {})

	if closeReason == "" {
		t.Error("onClose not called on cancelled")
	}
	select {
	case <-done:
	default:
		t.Error("done channel not closed on Cancelled")
	}
}

func TestHandleChatFrame_ParticipantLeave(t *testing.T) {
	var leaveWho string
	s := &Session{
		remoteLabel: "Peer",
		onMsg:       func(string, string) {},
		onClose:     func(string) {},
		onLeave:     func(who string) { leaveWho = who },
	}
	done := make(chan struct{})

	frame := transfer.BuildParticipantLeaveFrame()
	s.handleChatFrame(frame, done, func() {})

	if leaveWho != "Peer" {
		t.Errorf("onLeave(%q), want Peer", leaveWho)
	}
	select {
	case <-done:
		t.Error("done channel should NOT be closed on leave")
	default:
	}
}

func TestHandleChatFrame_EmptyFrame(t *testing.T) {
	s := &Session{
		remoteLabel: "Peer",
		onMsg:       func(string, string) { t.Error("unexpected onMsg") },
		onClose:     func(string) { t.Error("unexpected onClose") },
		onLeave:     func(string) { t.Error("unexpected onLeave") },
	}
	done := make(chan struct{})
	s.handleChatFrame([]byte{}, done, func() {})
}

func TestHandleChatFrame_DoubleClose(t *testing.T) {
	s := &Session{
		remoteLabel: "Peer",
		onMsg:       func(string, string) {},
		onClose:     func(string) {},
		onLeave:     func(string) {},
	}
	done := make(chan struct{})
	close(done)

	// Should not panic when done is already closed.
	frame := transfer.BuildChatCloseFrame()
	s.handleChatFrame(frame, done, func() {})
}

func TestNewSession_DefaultCallbacks(t *testing.T) {
	s := NewSession(nil, "Test", true, nil, nil, nil)
	if s.onMsg == nil || s.onClose == nil || s.onLeave == nil {
		t.Error("default callbacks should not be nil")
	}
}
