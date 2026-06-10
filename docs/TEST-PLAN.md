# TEST-PLAN.md — Tiered Testing Plan

This file tracks the test strategy: what's covered, what's pending, and the
philosophy behind it. Update the status column as tiers land.

---

## Philosophy

Tests here have caught **real production bugs**, not hypothetical ones:

- Schedule auth bypass (empty IP list short-circuited the password check)
- Path traversal (`sanitiseName` left `..` in `../../x`)
- Non-deterministic byte-size parsing (`"gb"` matching as `"b"`)
- `formatDurationLabel(0)` edge case

Because of that track record, tests are treated as a first-class safety net, not
a box to tick. Two working rules:

1. **When a test fails, decide whether the test or the code is wrong.** Both
   happen. The schedule max-downloads cap is a *ceiling*, not a floor — a test
   once asserted the wrong direction and the test was the bug.
2. **Security-relevant tests are load-bearing.** The PAKE cross-key rejection,
   offer≠answer MAC separation, `sanitiseName` traversal stripping, schedule
   auth precedence, and wire-tag pinning all encode security invariants.
   Changing them should require deliberate justification.

`make test` is the default (CGO-free, works on Windows). `make test-race` needs
clang and a non-Windows host.

---

## Completed tiers (1–5)

| Tier | Area | Package(s) | Notable coverage |
|---|---|---|---|
| 1 | Config & env | `broker`, `schedule` | env parsing, `ValidateEnv`, byte sizes, fuzzy durations, CIDR lists |
| 2 | TURN/STUN | `turn` | URL parsing, transport validation, ephemeral credential derivation, TTL |
| 3 | Schedule HTTP | `schedule` (handler) | auth, probe, upload init/chunk/complete, download, meta, delete, TTL options, end-to-end — **found the auth-bypass bug** |
| 4 | Wire protocol | `transfer` | all 17 tag-byte values pinned, frame round-trips, relayed frames, resume frames, receive state machine — **found the path-traversal bug** |
| 5 | Crypto/slot/pake | `crypto`, `slot`, `pake` | 3-word code format + wordlist integrity, slot state machine, HKDF subkey derivation vs spec, MITM/cross-key rejection |
| 6 | Archive & store | `archive`, `store` | zip round-trip (fs + in-memory), nested dirs, large payloads, `InjectMessage`, `Summary`; `MemStore` full contract suite reusable for Tier 8 Redis integration |
| 7 | Transfer & broker | `transfer`, `broker` | sender sliding-window via `mockDataChannel` (8 tests); broker hub via httptest+gorilla WS (12 tests) covering version mismatch, code validation, expired/full slots, star relay, targeted relay, disconnect peer.left, bye propagation |

---

## Pending tiers (8)

### Tier 6 — Archive & in-memory store

**Status:** complete.

- `internal/archive`: 25 tests covering `Prepare` (pass-through, single dir,
  multiple files, nested dirs, error cases), `Result.Cleanup` (removes temp,
  safe on non-temp, idempotent), `ZipFilesFromMemory` (pass-through, nested,
  round-trip byte-identical, large payload, common/mixed prefix naming),
  `InjectMessage`, and `Summary`.
- `internal/store` `MemStore`: `storeContractSuite` shared test table (9
  sub-tests) run against `MemStore`; plus 4 MemStore-specific tests covering
  blind-write Update, code index cleanup on Delete, independent multi-slot
  entries, and Update preserving code index. The contract suite is designed to
  be reused against the Redis-backed Store in Tier 8.

### Tier 7 — Transfer & broker coverage with mocks

**Status:** complete.

- `internal/transfer`: 8 sender tests exercising `RunFromBytes` through
  `runFromReader` via a `mockDataChannel` that records frames and fires a
  synchronous `onSend` callback. Covers: single-chunk frame sequence, file-header
  metadata, multi-chunk ordering, window-size-1 sequential delivery, context
  cancel (emits `TagCancelled`), remote cancel (`ErrCancelled`), resume-from-seq
  (seeks to offset, skips earlier chunks), and dc.Send error propagation.
  The `resumeFrom <- 0` pre-load trick skips the 2-second wait in `runFromReader`
  making all tests deterministic and fast.
- `internal/broker`: 12 tests driving the full hub goroutine via an httptest
  server and real gorilla WebSocket connections. Covers: slot creation (code,
  slot ID, TTL returned), version mismatch on create and join, invalid code
  format (`ERR_INVALID_CODE`), slot not found, expired slot (backdated via
  MemStore), slot full (third peer rejected), successful join (joiner gets
  `role=responder`, initiator gets `peer.joined` then `slot.ready`), relay without
  slot (`ERR_NOT_IN_SLOT`), star-topology relay (joiner→initiator), targeted relay
  (initiator→specific joiner peer ID), initiator disconnect triggers `peer.left`
  to joiner, graceful bye propagation, and unknown message type.

### Tier 8 — Integration

**Status:** not started. Slower tests; consider a build tag (e.g.
`//go:build integration`) so `make test` stays fast and CI runs them separately.

- Redis store integration: run against a real Redis (miniredis or a container).
  Verify TTL expiry, concurrent updates (last-write-wins), and the code→id
  index consistency.
- `internal/session` via `net.Pipe`: wire two `Session` instances together over
  an in-memory pipe to exercise `execTransfer`, `handleControlFrame`,
  `broadcastToExtraPeers`, and `prepareInboundTransfer` without real WebRTC.
  This is the highest-value integration target — it covers the multi-peer
  coordination logic that's currently only exercised manually.

---

## Coverage snapshot (last recorded)

| Package | Coverage |
|---|---|
| turn | ~93% |
| transfer | sender path covered (Tier 7); receiver path pending |
| broker | hub + relay covered (Tier 7); ICE endpoint pending |
| schedule | handler covered; store integration pending (Tier 8) |
| crypto, slot, pake | covered (Tier 5) |
| session | ~0% (Tier 8 target — the big one) |

Re-run `make cover` to refresh these numbers before planning the next tier.

---

## Out-of-scope for now

The protocol state machines in `peer.go` (`doSessionHandshake`,
`initiatorHandshakeWithPeer`, `Send`/`Receive`) are not directly unit-tested —
they orchestrate live WebRTC/ICE/DTLS, which is impractical to mock cleanly.
Tier 8's `net.Pipe` session tests cover the layer just above them, which is the
pragmatic compromise. Revisit direct peer.go testing only if a bug there
warrants it.
