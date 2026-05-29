package slot

import (
	"testing"
	"time"
)

// ─────────────────────────────────────────────────────────────────────────────
// Constructor
// ─────────────────────────────────────────────────────────────────────────────

func TestNew_Defaults(t *testing.T) {
	s := New("id-1", "bear-cozy-cone", "conn-init", "files", 2)

	if s.ID != "id-1"               { t.Errorf("ID = %q, want id-1", s.ID) }
	if s.Code != "bear-cozy-cone"   { t.Errorf("Code = %q", s.Code) }
	if s.InitiatorID != "conn-init" { t.Errorf("InitiatorID = %q", s.InitiatorID) }
	if s.SessionType != "files"     { t.Errorf("SessionType = %q", s.SessionType) }
	if s.MaxPeers != 2              { t.Errorf("MaxPeers = %d, want 2", s.MaxPeers) }
	if s.State != StateWaiting      { t.Errorf("State = %q, want waiting", s.State) }
	if s.EverFull                   { t.Error("EverFull should be false on construction") }
	if s.PeerIDs == nil             { t.Error("PeerIDs should not be nil") }
	if len(s.PeerIDs) != 0          { t.Errorf("PeerIDs length = %d, want 0", len(s.PeerIDs)) }
}

func TestNew_ExpiresAt(t *testing.T) {
	before := time.Now().UTC()
	s := New("x", "c", "i", "files", 2)
	after  := time.Now().UTC()

	minExpiry := before.Add(DefaultTTL)
	maxExpiry := after.Add(DefaultTTL)

	if s.ExpiresAt.Before(minExpiry) || s.ExpiresAt.After(maxExpiry) {
		t.Errorf("ExpiresAt %v outside expected range [%v, %v]",
			s.ExpiresAt, minExpiry, maxExpiry)
	}
}

func TestNew_MaxPeersFloor(t *testing.T) {
	// maxPeers below 2 should be clamped to DefaultMaxPeers (2).
	s := New("x", "c", "i", "files", 0)
	if s.MaxPeers != DefaultMaxPeers {
		t.Errorf("MaxPeers = %d for input 0, want %d", s.MaxPeers, DefaultMaxPeers)
	}
	s2 := New("x", "c", "i", "files", 1)
	if s2.MaxPeers != DefaultMaxPeers {
		t.Errorf("MaxPeers = %d for input 1, want %d", s2.MaxPeers, DefaultMaxPeers)
	}
}

func TestNew_MaxPeersCeiling(t *testing.T) {
	// maxPeers above MaxAllowedPeers should be clamped.
	s := New("x", "c", "i", "files", 9999)
	if s.MaxPeers != MaxAllowedPeers {
		t.Errorf("MaxPeers = %d for input 9999, want %d", s.MaxPeers, MaxAllowedPeers)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// ConnectedCount
// ─────────────────────────────────────────────────────────────────────────────

func TestConnectedCount(t *testing.T) {
	s := New("x", "c", "init", "files", 4)
	if s.ConnectedCount() != 1 {
		t.Errorf("ConnectedCount on fresh slot = %d, want 1 (initiator only)", s.ConnectedCount())
	}
	s.PeerIDs = []string{"p1", "p2"}
	if s.ConnectedCount() != 3 {
		t.Errorf("ConnectedCount with 2 peers = %d, want 3", s.ConnectedCount())
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// CanJoin
// ─────────────────────────────────────────────────────────────────────────────

func TestCanJoin(t *testing.T) {
	t.Run("fresh_slot_can_join", func(t *testing.T) {
		s := New("x", "c", "init", "files", 2)
		if !s.CanJoin() { t.Error("fresh slot should accept a join") }
	})

	t.Run("full_slot_cannot_join", func(t *testing.T) {
		s := New("x", "c", "init", "files", 2)
		s.State = StateFull
		if s.CanJoin() { t.Error("full slot should not accept a join") }
	})

	t.Run("closed_slot_cannot_join", func(t *testing.T) {
		s := New("x", "c", "init", "files", 2)
		s.State = StateClosed
		if s.CanJoin() { t.Error("closed slot should not accept a join") }
	})

	t.Run("ever_full_slot_cannot_rejoin", func(t *testing.T) {
		// EverFull=true means no new peers even if count is below max.
		s := New("x", "c", "init", "files", 3)
		s.EverFull = true
		if s.CanJoin() { t.Error("EverFull slot should not accept a join") }
	})

	t.Run("slot_at_capacity_cannot_join", func(t *testing.T) {
		s := New("x", "c", "init", "files", 2)
		s.PeerIDs = []string{"p1"} // now at max (initiator + 1 = 2)
		if s.CanJoin() { t.Error("slot at MaxPeers should not accept a join") }
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// Join — state machine transitions
// ─────────────────────────────────────────────────────────────────────────────

func TestJoin_WaitingToActive(t *testing.T) {
	s := New("x", "c", "init", "files", 3) // 3 total: init + 2 peers
	if err := s.Join("p1"); err != nil {
		t.Fatalf("Join(p1): %v", err)
	}
	if s.State != StateActive {
		t.Errorf("state after first join = %q, want active", s.State)
	}
	if !s.HasPeer("p1") {
		t.Error("p1 should be in PeerIDs after join")
	}
	if s.EverFull {
		t.Error("EverFull should not be set after a non-filling join")
	}
}

func TestJoin_ActiveToFull(t *testing.T) {
	s := New("x", "c", "init", "files", 2) // 2 total: init + 1 peer
	if err := s.Join("p1"); err != nil {
		t.Fatalf("Join(p1): %v", err)
	}
	if s.State != StateFull {
		t.Errorf("state after filling join = %q, want full", s.State)
	}
	if !s.EverFull {
		t.Error("EverFull should be set when slot reaches MaxPeers")
	}
}

func TestJoin_FullSlotReturnsError(t *testing.T) {
	s := New("x", "c", "init", "files", 2)
	if err := s.Join("p1"); err != nil {
		t.Fatalf("first join: %v", err)
	}
	err := s.Join("p2")
	if err == nil {
		t.Fatal("joining a full slot should return an error")
	}
	if err != ErrSlotFull {
		t.Errorf("err = %v, want ErrSlotFull", err)
	}
}

func TestJoin_ClosedSlotReturnsError(t *testing.T) {
	s := New("x", "c", "init", "files", 3)
	s.Close()
	err := s.Join("p1")
	if err != ErrSlotClosed {
		t.Errorf("err = %v, want ErrSlotClosed", err)
	}
}

func TestJoin_EverFullSlotReturnsError(t *testing.T) {
	s := New("x", "c", "init", "files", 3)
	s.EverFull = true
	err := s.Join("p1")
	if err == nil {
		t.Fatal("joining an EverFull slot should return an error")
	}
}

func TestJoin_MultiPeerProgression(t *testing.T) {
	// 4-peer session: init + 3 peers
	s := New("x", "c", "init", "files", 4)
	for i, id := range []string{"p1", "p2", "p3"} {
		if err := s.Join(id); err != nil {
			t.Fatalf("Join(%s): %v", id, err)
		}
		if i < 2 && s.State != StateActive {
			t.Errorf("after join %d state = %q, want active", i+1, s.State)
		}
	}
	if s.State != StateFull {
		t.Errorf("after 3 joins state = %q, want full", s.State)
	}
	if s.ConnectedCount() != 4 {
		t.Errorf("ConnectedCount = %d, want 4", s.ConnectedCount())
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// RemovePeer — state machine transitions
// ─────────────────────────────────────────────────────────────────────────────

func TestRemovePeer_LastPeerGoesBackToWaiting(t *testing.T) {
	s := New("x", "c", "init", "files", 3)
	s.Join("p1") //nolint:errcheck
	s.RemovePeer("p1")

	if s.State != StateWaiting {
		t.Errorf("state after removing last peer = %q, want waiting", s.State)
	}
	if len(s.PeerIDs) != 0 {
		t.Errorf("PeerIDs = %v, want empty", s.PeerIDs)
	}
}

func TestRemovePeer_OneOfManyGoesBackToActive(t *testing.T) {
	s := New("x", "c", "init", "files", 4)
	s.Join("p1") //nolint:errcheck
	s.Join("p2") //nolint:errcheck
	s.RemovePeer("p1")

	if s.State != StateActive {
		t.Errorf("state after removing one of two peers = %q, want active", s.State)
	}
	if s.HasPeer("p1") {
		t.Error("p1 should not be in PeerIDs after removal")
	}
	if !s.HasPeer("p2") {
		t.Error("p2 should still be in PeerIDs")
	}
}

func TestRemovePeer_EverFullSlotStaysClosed(t *testing.T) {
	// After a slot was ever full, removing a peer does NOT reopen it.
	s := New("x", "c", "init", "files", 2)
	s.Join("p1") //nolint:errcheck  // slot becomes full, EverFull=true
	s.RemovePeer("p1")

	// Should stay full/closed — not revert to waiting.
	if s.State == StateWaiting {
		t.Error("EverFull slot should not revert to waiting after peer removal")
	}
	if s.CanJoin() {
		t.Error("EverFull slot should not accept new joins after peer removal")
	}
}

func TestRemovePeer_UnknownIDIsNoOp(t *testing.T) {
	s := New("x", "c", "init", "files", 3)
	s.Join("p1") //nolint:errcheck
	s.RemovePeer("nobody")

	if !s.HasPeer("p1") {
		t.Error("p1 should still be present after removing unknown peer")
	}
	if s.ConnectedCount() != 2 {
		t.Errorf("ConnectedCount = %d, want 2", s.ConnectedCount())
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Close / IsExpired
// ─────────────────────────────────────────────────────────────────────────────

func TestClose(t *testing.T) {
	s := New("x", "c", "i", "files", 2)
	s.Close()
	if s.State != StateClosed {
		t.Errorf("State after Close = %q, want closed", s.State)
	}
	if s.CanJoin() {
		t.Error("closed slot must not accept joins")
	}
}

func TestIsExpired(t *testing.T) {
	s := New("x", "c", "i", "files", 2)
	if s.IsExpired() {
		t.Error("fresh slot should not be expired")
	}

	// Backdate ExpiresAt to the past.
	s.ExpiresAt = time.Now().UTC().Add(-time.Second)
	if !s.IsExpired() {
		t.Error("backdated slot should be expired")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Membership helpers
// ─────────────────────────────────────────────────────────────────────────────

func TestIsInitiator(t *testing.T) {
	s := New("x", "c", "init-conn", "files", 2)
	if !s.IsInitiator("init-conn")  { t.Error("initiator ID not recognised") }
	if s.IsInitiator("other-conn")  { t.Error("non-initiator ID falsely recognised") }
}

func TestHasPeer(t *testing.T) {
	s := New("x", "c", "init", "files", 3)
	s.Join("p1") //nolint:errcheck
	if !s.HasPeer("p1")   { t.Error("p1 should be found") }
	if s.HasPeer("init")  { t.Error("initiator should not be found via HasPeer") }
	if s.HasPeer("ghost") { t.Error("unknown peer should not be found") }
}

func TestIsMember(t *testing.T) {
	s := New("x", "c", "init", "files", 3)
	s.Join("p1") //nolint:errcheck
	if !s.IsMember("init")  { t.Error("initiator should be a member") }
	if !s.IsMember("p1")    { t.Error("joined peer should be a member") }
	if s.IsMember("ghost")  { t.Error("unknown ID should not be a member") }
}

func TestRoleOf(t *testing.T) {
	s := New("x", "c", "init", "files", 2)
	if s.RoleOf("init")  != "initiator"  { t.Errorf("RoleOf(init) = %q", s.RoleOf("init")) }
	if s.RoleOf("other") != "responder"  { t.Errorf("RoleOf(other) = %q", s.RoleOf("other")) }
}

func TestOtherMembers(t *testing.T) {
	s := New("x", "c", "init", "files", 4)
	s.Join("p1") //nolint:errcheck
	s.Join("p2") //nolint:errcheck

	// From init's perspective: others are p1 and p2.
	others := s.OtherMembers("init")
	if len(others) != 2 {
		t.Fatalf("OtherMembers(init) = %v, want 2 entries", others)
	}
	found := map[string]bool{}
	for _, id := range others { found[id] = true }
	if !found["p1"] || !found["p2"] {
		t.Errorf("OtherMembers(init) = %v, want [p1 p2]", others)
	}

	// From p1's perspective: others are init and p2.
	othersP1 := s.OtherMembers("p1")
	if len(othersP1) != 2 {
		t.Fatalf("OtherMembers(p1) = %v, want 2 entries", othersP1)
	}
	found2 := map[string]bool{}
	for _, id := range othersP1 { found2[id] = true }
	if !found2["init"] || !found2["p2"] {
		t.Errorf("OtherMembers(p1) = %v, want [init p2]", othersP1)
	}
}

func TestAllPeerIDs_ReturnsCopy(t *testing.T) {
	s := New("x", "c", "init", "files", 3)
	s.Join("p1") //nolint:errcheck
	ids := s.AllPeerIDs()
	// Mutating the returned slice must not affect the slot's internal state.
	ids[0] = "hacked"
	if s.PeerIDs[0] != "p1" {
		t.Error("AllPeerIDs should return a copy, not a reference")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// State constant values — part of the persisted data format
// ─────────────────────────────────────────────────────────────────────────────

func TestStateConstants(t *testing.T) {
	// These string values are persisted in Redis and must never change.
	if StateWaiting != "waiting" { t.Errorf("StateWaiting = %q, want waiting", StateWaiting) }
	if StateActive  != "active"  { t.Errorf("StateActive = %q, want active", StateActive) }
	if StateFull    != "full"    { t.Errorf("StateFull = %q, want full", StateFull) }
	if StateClosed  != "closed"  { t.Errorf("StateClosed = %q, want closed", StateClosed) }
}
