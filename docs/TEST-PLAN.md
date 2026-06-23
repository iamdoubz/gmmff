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

## Completed tiers (1–8d)

| Tier | Area | Package(s) | Notable coverage |
|---|---|---|---|
| 1 | Config & env | `broker`, `schedule` | env parsing, `ValidateEnv`, byte sizes, fuzzy durations, CIDR lists |
| 2 | TURN/STUN | `turn` | URL parsing, transport validation, ephemeral credential derivation, TTL |
| 3 | Schedule HTTP | `schedule` (handler) | auth, probe, upload init/chunk/complete, download, meta, delete, TTL options, end-to-end — **found the auth-bypass bug** |
| 4 | Wire protocol | `transfer` | all 17 tag-byte values pinned, frame round-trips, relayed frames, resume frames, receive state machine — **found the path-traversal bug** |
| 5 | Crypto/slot/pake | `crypto`, `slot`, `pake` | 3-word code format + wordlist integrity, slot state machine, HKDF subkey derivation vs spec, MITM/cross-key rejection |
| 6 | Archive & store | `archive`, `store` | zip round-trip (fs + in-memory), nested dirs, large payloads, `InjectMessage`, `Summary`; `MemStore` full contract suite reusable for Tier 8 Redis integration |
| 7 | Transfer & broker | `transfer`, `broker` | sender sliding-window via `mockDataChannel` (8 tests); broker hub via httptest+gorilla WS (12 tests) covering version mismatch, code validation, expired/full slots, star relay, targeted relay, disconnect peer.left, bye propagation |
| 8a | Pure-logic units | `display`, `protocol`, `transfer`, `chat` | `FormatBytes` table tests; envelope marshal/roundtrip/panic; `ReceiveStateMem` edge cases (done-before-header, unknown tag, ack error, short chunk); chat frame dispatch + callbacks (8 tests) |
| 8b | Broker HTTP routes | `broker` (server) | `/healthz`, `/readyz`, `/metrics`, `/config.json`, landing page, security headers (CSP enforcing/report-only/local-mode, X-Frame-Options, COOP, COEP); **`/api/ice` gating** (no bearer→401, bad code→401, closed slot→401, waiting/active/full→200, STUN+TURN response); `bearerToken` helper — **20 tests** |
| 8c | Transfer disk receiver | `transfer` | full disk `ReceiveState.Feed` path: fresh single/multi-chunk, hash mismatch preserves partial, resume from partial+meta, `checkPartial` edge cases (no meta, SHA mismatch, size mismatch), ack error, resume error, filename sanitisation, `AckFrame` round-trip — **19 tests** |
| 8d | Schedule client | `schedule` (client) | `ParseDeleteURL` (5 cases), `ParseShareURL` (7 cases), `selectChunkSize` (8 boundary values), `NewClient`/`NormaliseServerURL` edge cases; **full round-trip** via httptest: Upload→FetchMeta→Download→Delete with AES-256-GCM crypto interop, password-gated upload, wrong-key decryption error, not-found/invalid-key delete — **21 tests** |

---

## Pending tiers (8e)

### Tier 8e — Integration (session + Redis)

**Status:** not started. Slower tests; consider a build tag (e.g.
`//go:build integration`) so `make test` stays fast and CI runs them separately.

- Redis store integration: run against a real Redis (miniredis or a container).
  Verify TTL expiry, concurrent updates (last-write-wins), and the code→id
  index consistency.
- `internal/session` via pion loopback: wire two `Session` instances together
  using `webrtc.NewAPI` with in-process transports to exercise `Run`,
  `handleControlFrame`, `execTransfer`, `prepareInboundTransfer`, `SendMessage`,
  `Close`, `Leave`, `saveReceivedFile`, and the idle timeout path — without
  real networking. This is the highest-value integration target: it covers the
  multi-peer coordination logic currently only exercised manually.
  **Estimated coverage: ~65–75%** of session statements (20 of 25 functions
  reachable; `broadcastToExtraPeers` and multi-peer `AddPeer` paths are harder
  to set up and may require a dedicated 3-peer harness).

---

## Coverage snapshot (recorded 2026-06-23, after Tier 8d)

| Package | Coverage | Notes |
|---|---|---|
| slot | **100%** | complete |
| protocol | **100%** | complete (Tier 8a) |
| display | **100%** | complete (Tier 8a) |
| turn | **93.3%** | complete |
| crypto | **91.7%** | complete |
| pake | **88.9%** | complete |
| transfer | **84.3%** | sender + disk receiver + in-memory receiver covered (Tiers 4, 7, 8c) |
| archive | **81.5%** | complete (Tier 6); small gap in `writeZip` error paths |
| schedule | **80.0%** | handler + client round-trip covered (Tiers 3, 8d); store integration pending (8e) |
| broker | **78.6%** | hub + HTTP routes + ICE gating covered (Tiers 7, 8b) |
| store | **25.7%** | MemStore covered (Tier 6); Redis Store needs real Redis (Tier 8e) |
| chat | **23.2%** | frame dispatch covered (Tier 8a); REPL needs live DC |
| cmd/gmmff | **9.5%** | CLI commands — hard to unit test, low ROI |
| session | **0%** | **Tier 8e target — estimated ~65–75% reachable with pion loopback** |
| peer | **0%** | live WebRTC orchestration — impractical to unit test (see Out-of-scope) |
| signaling | **0%** | WebSocket client — needs live server or full mock |
| localmode | **0%** | integration-only (embeds full server, mDNS, TLS) |
| log | **0%** | trivial init — low value |
| **Total** | **35.7%** | up from 26.3% before Tiers 8a–8d |

---

## Out-of-scope for now

The protocol state machines in `peer.go` (`doSessionHandshake`,
`initiatorHandshakeWithPeer`, `Send`/`Receive`) are not directly unit-tested —
they orchestrate live WebRTC/ICE/DTLS, which is impractical to mock cleanly.
Tier 8's `net.Pipe` session tests cover the layer just above them, which is the
pragmatic compromise. Revisit direct peer.go testing only if a bug there
warrants it.
