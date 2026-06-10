package store

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/iamdoubz/gmmff/v2/internal/slot"
)

// ─────────────────────────────────────────────────────────────────────────────
// Shared contract suite
// ─────────────────────────────────────────────────────────────────────────────

// storeContractSuite runs the full SlotStore contract against any implementation.
// Run it against MemStore now (Tier 6) and against the Redis-backed Store in
// Tier 8 integration tests — both must pass the same assertions.
func storeContractSuite(t *testing.T, s SlotStore) {
	t.Helper()
	ctx := context.Background()

	t.Run("Create_GetByID_RoundTrip", func(t *testing.T) {
		sl := testSlot("id-create-1", "cat-blue-sky")
		if err := s.Create(ctx, sl); err != nil {
			t.Fatalf("Create: %v", err)
		}
		got, err := s.GetByID(ctx, sl.ID)
		if err != nil {
			t.Fatalf("GetByID: %v", err)
		}
		if got.ID != sl.ID {
			t.Errorf("ID: got %q, want %q", got.ID, sl.ID)
		}
		if got.Code != sl.Code {
			t.Errorf("Code: got %q, want %q", got.Code, sl.Code)
		}
		if got.State != slot.StateWaiting {
			t.Errorf("State: got %q, want Waiting", got.State)
		}
	})

	t.Run("Create_GetByCode_RoundTrip", func(t *testing.T) {
		sl := testSlot("id-create-2", "dog-red-moon")
		if err := s.Create(ctx, sl); err != nil {
			t.Fatalf("Create: %v", err)
		}
		got, err := s.GetByCode(ctx, sl.Code)
		if err != nil {
			t.Fatalf("GetByCode: %v", err)
		}
		if got.ID != sl.ID {
			t.Errorf("GetByCode resolved wrong slot: %q", got.ID)
		}
	})

	t.Run("GetByID_Unknown_ErrSlotNotFound", func(t *testing.T) {
		_, err := s.GetByID(ctx, "no-such-id-xyz")
		if !errors.Is(err, slot.ErrSlotNotFound) {
			t.Errorf("want ErrSlotNotFound, got %v", err)
		}
	})

	t.Run("GetByCode_Unknown_ErrSlotNotFound", func(t *testing.T) {
		_, err := s.GetByCode(ctx, "no-such-code-xyz")
		if !errors.Is(err, slot.ErrSlotNotFound) {
			t.Errorf("want ErrSlotNotFound, got %v", err)
		}
	})

	t.Run("Update_OverwritesState", func(t *testing.T) {
		sl := testSlot("id-update-1", "fox-green-hill")
		if err := s.Create(ctx, sl); err != nil {
			t.Fatalf("Create: %v", err)
		}

		sl.State = slot.StateActive
		sl.PeerIDs = []string{"peer-99"}
		if err := s.Update(ctx, sl); err != nil {
			t.Fatalf("Update: %v", err)
		}

		got, err := s.GetByID(ctx, sl.ID)
		if err != nil {
			t.Fatalf("GetByID after Update: %v", err)
		}
		if got.State != slot.StateActive {
			t.Errorf("State after Update: got %q, want Active", got.State)
		}
		if len(got.PeerIDs) != 1 || got.PeerIDs[0] != "peer-99" {
			t.Errorf("PeerIDs after Update: got %v, want [peer-99]", got.PeerIDs)
		}
	})

	t.Run("Delete_RemovesSlotAndCodeIndex", func(t *testing.T) {
		sl := testSlot("id-delete-1", "owl-dark-lake")
		if err := s.Create(ctx, sl); err != nil {
			t.Fatalf("Create: %v", err)
		}
		if err := s.Delete(ctx, sl.ID); err != nil {
			t.Fatalf("Delete: %v", err)
		}

		if _, err := s.GetByID(ctx, sl.ID); !errors.Is(err, slot.ErrSlotNotFound) {
			t.Errorf("after Delete, GetByID should return ErrSlotNotFound, got %v", err)
		}
		if _, err := s.GetByCode(ctx, sl.Code); !errors.Is(err, slot.ErrSlotNotFound) {
			t.Errorf("after Delete, GetByCode should return ErrSlotNotFound, got %v", err)
		}
	})

	t.Run("Delete_AlreadyGone_NoError", func(t *testing.T) {
		// Both MemStore and Redis.Delete treat an absent slot as success.
		if err := s.Delete(ctx, "never-existed-slot"); err != nil {
			t.Errorf("Delete on unknown ID should not error, got %v", err)
		}
	})

	t.Run("Create_PreservesAllFields", func(t *testing.T) {
		sl := testSlot("id-fields-1", "bear-cozy-cone")
		sl.MaxPeers = 4
		sl.SessionType = "chat"
		if err := s.Create(ctx, sl); err != nil {
			t.Fatalf("Create: %v", err)
		}
		got, err := s.GetByID(ctx, sl.ID)
		if err != nil {
			t.Fatalf("GetByID: %v", err)
		}
		if got.MaxPeers != 4 {
			t.Errorf("MaxPeers: got %d, want 4", got.MaxPeers)
		}
		if got.SessionType != "chat" {
			t.Errorf("SessionType: got %q, want chat", got.SessionType)
		}
		if got.InitiatorID != "conn-test" {
			t.Errorf("InitiatorID: got %q, want conn-test", got.InitiatorID)
		}
		if got.CreatedAt.IsZero() {
			t.Error("CreatedAt should not be zero")
		}
		if time.Since(got.CreatedAt) > 5*time.Second {
			t.Errorf("CreatedAt looks stale: %v ago", time.Since(got.CreatedAt))
		}
	})

	t.Run("Ping_ReturnsNil", func(t *testing.T) {
		if err := s.Ping(ctx); err != nil {
			t.Errorf("Ping: %v", err)
		}
	})
}

// testSlot builds a minimal slot for use in tests.
func testSlot(id, code string) *slot.Slot {
	return slot.New(id, code, "conn-test", "files", 2)
}

// ─────────────────────────────────────────────────────────────────────────────
// MemStore — run the shared contract suite
// ─────────────────────────────────────────────────────────────────────────────

func TestMemStore_Contract(t *testing.T) {
	storeContractSuite(t, NewMemStore())
}

// ─────────────────────────────────────────────────────────────────────────────
// MemStore — behaviour that diverges from the Redis store
// ─────────────────────────────────────────────────────────────────────────────

func TestMemStore_Update_UnknownSlot_BlindWrite(t *testing.T) {
	// MemStore.Update is a blind map write — it succeeds and the slot becomes
	// retrievable.  The Redis store requires the key to exist.  This difference
	// is intentional: MemStore is a lightweight dev/single-node fallback.
	m := NewMemStore()
	ctx := context.Background()
	sl := testSlot("ghost-id", "ghost-code")

	if err := m.Update(ctx, sl); err != nil {
		t.Fatalf("MemStore.Update on unknown slot returned unexpected error: %v", err)
	}
	got, err := m.GetByID(ctx, "ghost-id")
	if err != nil {
		t.Fatalf("GetByID after blind Update: %v", err)
	}
	if got.ID != "ghost-id" {
		t.Errorf("ID after blind Update: got %q", got.ID)
	}
}

func TestMemStore_GetByCode_AfterDelete_CodeIndexCleaned(t *testing.T) {
	// Verify the code→id reverse index is removed alongside the slot on Delete,
	// so a stale code lookup doesn't resurrect a deleted slot.
	m := NewMemStore()
	ctx := context.Background()
	sl := testSlot("del-id", "del-code")

	if err := m.Create(ctx, sl); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := m.Delete(ctx, sl.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	_, err := m.GetByCode(ctx, "del-code")
	if !errors.Is(err, slot.ErrSlotNotFound) {
		t.Errorf("GetByCode after Delete: want ErrSlotNotFound, got %v", err)
	}
}

func TestMemStore_MultipleSlots_IndependentEntries(t *testing.T) {
	m := NewMemStore()
	ctx := context.Background()

	// Create several slots and verify they don't overwrite each other.
	const n = 10
	for i := range n {
		sl := testSlot(fmt.Sprintf("slot-%d", i), fmt.Sprintf("code-%d", i))
		if err := m.Create(ctx, sl); err != nil {
			t.Fatalf("Create slot-%d: %v", i, err)
		}
	}

	for i := range n {
		id := fmt.Sprintf("slot-%d", i)
		code := fmt.Sprintf("code-%d", i)

		byID, err := m.GetByID(ctx, id)
		if err != nil {
			t.Errorf("GetByID %q: %v", id, err)
			continue
		}
		if byID.ID != id {
			t.Errorf("GetByID %q: got ID %q", id, byID.ID)
		}

		byCode, err := m.GetByCode(ctx, code)
		if err != nil {
			t.Errorf("GetByCode %q: %v", code, err)
			continue
		}
		if byCode.ID != id {
			t.Errorf("GetByCode %q resolved to %q, want %q", code, byCode.ID, id)
		}
	}
}

func TestMemStore_Update_PreservesCodeIndex(t *testing.T) {
	// Update changes slot state but must not break the code→id lookup.
	m := NewMemStore()
	ctx := context.Background()
	sl := testSlot("upd-id", "upd-code")

	if err := m.Create(ctx, sl); err != nil {
		t.Fatalf("Create: %v", err)
	}
	sl.State = slot.StateClosed
	if err := m.Update(ctx, sl); err != nil {
		t.Fatalf("Update: %v", err)
	}

	got, err := m.GetByCode(ctx, "upd-code")
	if err != nil {
		t.Fatalf("GetByCode after Update: %v", err)
	}
	if got.State != slot.StateClosed {
		t.Errorf("State after Update via code lookup: got %q, want Closed", got.State)
	}
}
