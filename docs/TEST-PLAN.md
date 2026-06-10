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

---

## Pending tiers (6–8)

### Tier 6 — Archive & in-memory store

**Status:** not started. Pure additive; no production changes expected.

- `internal/archive`: zip round-trip — create an archive from a set of files,
  extract it, assert byte-identical contents and preserved structure. Edge
  cases: empty archive, nested directories, a file whose name needs
  `sanitiseName` treatment on extraction.
- `internal/store` `MemStore`: full `SlotStore` interface coverage — create,
  get-by-id, get-by-code, update, delete, expiry. Assert `MemStore` and the
  Redis store behave identically for the same operations (shared test table).

### Tier 7 — Transfer & broker coverage with mocks

**Status:** not started. Target the largest coverage gaps.

- `internal/transfer` (currently ~32%): exercise `runFromReader` (the sender
  sliding-window loop) with a mock `DataChannelWriter` that records frames,
  simulates backpressure, and can inject ACK timing. Verify window advancement,
  resume after a gap, and integrity-failure handling.
- `internal/broker` (currently ~18%): drive `handleSlotJoin`,
  `validateJoinRequest`, `notifyJoinedPeers`, and `relay` with fake connections.
  Cover version mismatch, invalid code, expired slot, full slot, initiator
  disconnect mid-join, and targeted vs star relay.

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
| transfer | ~32% (Tier 7 target) |
| broker | ~18% (Tier 7 target) |
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
