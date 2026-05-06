// Package store implements the Redis-backed persistence layer for gmmff slots.
//
// Storage layout
//
//	Redis key                     Type    TTL           Content
//	slot:<slot_id>                Hash    10 min        Slot JSON fields
//	code:<code>                   String  10 min        slot_id
//
// Both keys share the same TTL so expiry is atomic from the user's perspective.
// The store never writes connection IDs or any data that could identify a user.
package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	applog "github.com/iamdoubz/gmmff/internal/log"
	"github.com/iamdoubz/gmmff/internal/slot"
	"github.com/redis/go-redis/v9"
	"github.com/rs/zerolog"
)

const (
	slotKeyPrefix = "slot:"
	codeKeyPrefix = "code:"
)

var log zerolog.Logger

func init() {
	log = applog.Component("store")
}

// Store is the Redis-backed slot repository.
type Store struct {
	rdb *redis.Client
	ttl time.Duration
}

// New creates a Store from a connected Redis client.
// ttl controls slot lifetime; pass 0 to use slot.DefaultTTL.
func New(rdb *redis.Client, ttl time.Duration) *Store {
	if ttl == 0 {
		ttl = slot.DefaultTTL
	}
	return &Store{rdb: rdb, ttl: ttl}
}

// ─────────────────────────────────────────────────────────────────────────────
// Write operations
// ─────────────────────────────────────────────────────────────────────────────

// Create persists a new slot and a code→slot_id index.
// Both keys are set with the same TTL in a single pipeline (atomic w.r.t. expiry).
func (s *Store) Create(ctx context.Context, sl *slot.Slot) error {
	data, err := json.Marshal(sl)
	if err != nil {
		return fmt.Errorf("store.Create marshal: %w", err)
	}

	slotKey := slotKeyPrefix + sl.ID
	codeKey := codeKeyPrefix + sl.Code

	pipe := s.rdb.Pipeline()
	pipe.Set(ctx, slotKey, data, s.ttl)
	pipe.Set(ctx, codeKey, sl.ID, s.ttl)
	if _, err := pipe.Exec(ctx); err != nil {
		log.Error().Str("error_code", "ERR_STORE_CREATE").Str("slot_id", sl.ID).
			Msg("failed to persist slot")
		return fmt.Errorf("store.Create pipeline: %w", err)
	}
	return nil
}

// Update overwrites the slot JSON.  The TTL is refreshed to prevent
// expiry during an active session.
func (s *Store) Update(ctx context.Context, sl *slot.Slot) error {
	data, err := json.Marshal(sl)
	if err != nil {
		return fmt.Errorf("store.Update marshal: %w", err)
	}

	slotKey := slotKeyPrefix + sl.ID
	if err := s.rdb.Set(ctx, slotKey, data, s.ttl).Err(); err != nil {
		log.Error().Str("error_code", "ERR_STORE_UPDATE").Str("slot_id", sl.ID).
			Msg("failed to update slot")
		return fmt.Errorf("store.Update: %w", err)
	}
	return nil
}

// Delete removes both the slot and code keys immediately.
func (s *Store) Delete(ctx context.Context, slotID string) error {
	sl, err := s.GetByID(ctx, slotID)
	if err != nil {
		if errors.Is(err, slot.ErrSlotNotFound) {
			return nil // already gone
		}
		return err
	}

	pipe := s.rdb.Pipeline()
	pipe.Del(ctx, slotKeyPrefix+slotID)
	pipe.Del(ctx, codeKeyPrefix+sl.Code)
	if _, err := pipe.Exec(ctx); err != nil {
		log.Error().Str("error_code", "ERR_STORE_DELETE").Str("slot_id", slotID).
			Msg("failed to delete slot")
		return fmt.Errorf("store.Delete pipeline: %w", err)
	}
	return nil
}

// ─────────────────────────────────────────────────────────────────────────────
// Read operations
// ─────────────────────────────────────────────────────────────────────────────

// GetByID fetches a slot by its UUID.
func (s *Store) GetByID(ctx context.Context, slotID string) (*slot.Slot, error) {
	return s.get(ctx, slotKeyPrefix+slotID)
}

// GetByCode resolves a human-readable code to a slot.
func (s *Store) GetByCode(ctx context.Context, code string) (*slot.Slot, error) {
	slotID, err := s.rdb.Get(ctx, codeKeyPrefix+code).Result()
	if err != nil {
		if errors.Is(err, redis.Nil) {
			return nil, slot.ErrSlotNotFound
		}
		log.Error().Str("error_code", "ERR_STORE_CODE_LOOKUP").Msg("code index lookup failed")
		return nil, fmt.Errorf("store.GetByCode index: %w", err)
	}
	return s.GetByID(ctx, slotID)
}

func (s *Store) get(ctx context.Context, key string) (*slot.Slot, error) {
	data, err := s.rdb.Get(ctx, key).Bytes()
	if err != nil {
		if errors.Is(err, redis.Nil) {
			return nil, slot.ErrSlotNotFound
		}
		log.Error().Str("error_code", "ERR_STORE_GET").Msg("Redis GET failed")
		return nil, fmt.Errorf("store.get: %w", err)
	}

	var sl slot.Slot
	if err := json.Unmarshal(data, &sl); err != nil {
		log.Error().Str("error_code", "ERR_STORE_UNMARSHAL").Msg("slot JSON corrupt")
		return nil, fmt.Errorf("store.get unmarshal: %w", err)
	}
	return &sl, nil
}

// ─────────────────────────────────────────────────────────────────────────────
// Health
// ─────────────────────────────────────────────────────────────────────────────

// Ping checks that Redis is reachable.
func (s *Store) Ping(ctx context.Context) error {
	return s.rdb.Ping(ctx).Err()
}

// ─────────────────────────────────────────────────────────────────────────────
// In-memory fallback
// ─────────────────────────────────────────────────────────────────────────────

// MemStore is a non-persistent in-memory store for development / single-node
// deployments where Redis is not available.  NOT suitable for production
// (no TTL enforcement, no distributed safety).
type MemStore struct {
	slots map[string]*slot.Slot // key: slot_id
	codes map[string]string     // key: code → slot_id
}

// NewMemStore creates a MemStore.
func NewMemStore() *MemStore {
	return &MemStore{
		slots: make(map[string]*slot.Slot),
		codes: make(map[string]string),
	}
}

func (m *MemStore) Create(_ context.Context, sl *slot.Slot) error {
	m.slots[sl.ID] = sl
	m.codes[sl.Code] = sl.ID
	return nil
}

func (m *MemStore) Update(_ context.Context, sl *slot.Slot) error {
	m.slots[sl.ID] = sl
	return nil
}

func (m *MemStore) Delete(_ context.Context, slotID string) error {
	if sl, ok := m.slots[slotID]; ok {
		delete(m.codes, sl.Code)
	}
	delete(m.slots, slotID)
	return nil
}

func (m *MemStore) GetByID(_ context.Context, slotID string) (*slot.Slot, error) {
	sl, ok := m.slots[slotID]
	if !ok {
		return nil, slot.ErrSlotNotFound
	}
	return sl, nil
}

func (m *MemStore) GetByCode(_ context.Context, code string) (*slot.Slot, error) {
	id, ok := m.codes[code]
	if !ok {
		return nil, slot.ErrSlotNotFound
	}
	return m.GetByID(context.Background(), id)
}

func (m *MemStore) Ping(_ context.Context) error { return nil }

// ─────────────────────────────────────────────────────────────────────────────
// Interface
// ─────────────────────────────────────────────────────────────────────────────

// SlotStore is the interface both Store and MemStore satisfy.
type SlotStore interface {
	Create(ctx context.Context, sl *slot.Slot) error
	Update(ctx context.Context, sl *slot.Slot) error
	Delete(ctx context.Context, slotID string) error
	GetByID(ctx context.Context, slotID string) (*slot.Slot, error)
	GetByCode(ctx context.Context, code string) (*slot.Slot, error)
	Ping(ctx context.Context) error
}
