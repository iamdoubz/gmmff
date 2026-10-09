# DECISIONS.md — Architecture Decision Log

Short records of decisions whose rationale is **not obvious from reading the
code**. Each entry: the decision, alternatives considered, and why. Add a new
entry when you make a choice a future maintainer might second-guess. Newest at
the bottom.

---

## ADR-001 — v2 module path migration

**Decision:** Module path is `github.com/iamdoubz/gmmff/v2`; all internal
imports carry the `/v2` suffix.

**Why:** Go requires the major-version suffix in the module path for v2+.
Without it, the module proxy and Go Report Card report the project as v1
regardless of git tags. The suffix must match the import path or the module
won't resolve.

**Notes:** After tagging a v2.x release, the proxy may need a manual nudge:
`curl https://proxy.golang.org/github.com/iamdoubz/gmmff/v2/@v/vX.Y.Z.info`
before Go Report Card at `/v2` resolves.

---

## ADR-002 — Signaling server is untrusted; PAKE authenticates the handshake

**Decision:** A code exchanged out-of-band drives a CPace PAKE handshake whose
shared secret is HKDF-expanded into separate offer/answer subkeys that MAC the
SDP exchange.

**Alternatives:** Trust the signaling server to faithfully relay SDP (simplest);
TLS-only to the server (protects transit, not the server itself).

**Why:** The threat model includes a malicious or compromised signaling server
performing a man-in-the-middle on the WebRTC handshake. The PAKE-derived MAC
binds the SDP to the shared secret, so a tampered offer/answer fails
verification. The server brokers introductions but can never silently MITM.

> **Status (2026-10): achieved in v2.4.0 (protocol version 2) — see ADR-014.**
> Until then the server generated the whole code and received it in
> `slot.join`, so it knew the CPace password and could MITM. Codes now carry a
> client-only secret the server never sees. Browser users still trust the
> server for the client code it serves (ADR-014).

**Consequences:** Offer and answer subkeys must stay distinct (role separation),
or a responder could replay the initiator's MAC. Tests pin both the cross-key
rejection and offer≠answer separation.

---

## ADR-003 — TURN is gated, not open and not removed

**Decision:** `GET /api/ice` requires `Authorization: Bearer <slot-code>`,
generates credentials fresh per request, and is nginx rate-limited to
5 requests/hour/IP.

**Alternatives:** (a) Open endpoint — anyone fetches free TURN credentials.
(b) IP allowlist — breaks the cross-network peer-to-peer case, which is the
whole point. (c) Remove push entirely — the browser Wasm client has no local
config to fall back to. (d) Move credentials into the WebSocket `slot.ready`
message — cleaner long-term but a signaling-protocol change.

**Why:** An open endpoint let anyone on the internet harvest TURN credentials
and burn relay bandwidth. Slot-gating ties credential issuance to a peer that
has already completed the signaling handshake, which is exactly the population
that legitimately needs TURN. Per-request generation gives each caller a
full-TTL credential. Rate limiting is a cheap defense-in-depth layer in nginx.

**Consequences:** Exactly one `/api/ice` call should fire per session. Initiator
paths fetch after the slot code is known (Wasm `fetchICEWithCode`); joiner paths
pass the code directly. A prior bug fired three calls (one unauthenticated 401,
one dead JS call, one real) — watch the call count if you touch this flow.

---

## ADR-004 — TURN is connectivity, never discovery

**Decision:** TURN/STUN are treated purely as connectivity aids; peer discovery
is always the signaling server.

**Why:** TURN only matters for the ~8–15% of connections that can't go direct or
via STUN (symmetric NAT, restrictive firewalls). This justifies gating and rate-
limiting `/api/ice` without fear of breaking peer-finding. The CLI uses local
`GMMFF_TURN` rather than the push endpoint, so server-side TURN changes never
affect CLI users.

---

## ADR-005 — Ephemeral TURN credentials by default

**Decision:** Prefer `secret=` (HMAC-derived, short-lived) credentials over
static `user=`/`pass=`. Lifetime configurable via `GMMFF_PUSH_TTL` (default 30m).

**Why:** With ephemeral mode the TURN master secret never leaves the server —
only a short-lived derived credential reaches the browser, and it expires before
it can be meaningfully shared or abused. Static credentials are transmitted
verbatim to every peer and are only acceptable for intentionally public TURN.

---

## ADR-006 — Chat uses the full multi-peer session model

**Decision:** Chat is built on the same `session.Session` infrastructure as
Files, not a dedicated 1-to-1 data channel.

**Alternatives:** Keep the simpler `ChatWithCallback` 1-to-1 path and cap chat
at 2 peers.

**Why:** The original chat ran a single PAKE exchange, so a third peer's join
hung forever — the initiator had already completed its one handshake. Files
already solved multi-peer with `initiatorAcceptMorePeers` and a star-topology
relay. Reusing that infrastructure (`StartChatSession`/`JoinChatSession` as thin
wrappers over `StartSession`/`JoinSession`) gave chat working N-peer support for
free rather than building a parallel relay layer.

**Consequences:** `activeChatSession` is a `*session.Session`. Chat events map to
`uiChat*` callbacks the same way Files events map to `uiFiles*`.

---

## ADR-007 — Wire-protocol tag bytes are frozen

**Decision:** Tag bytes `0x01`–`0x11` in `transfer.go` are a permanent contract.
New behavior gets new tags; existing tags are never renumbered.

**Why:** A deployed browser client and a deployed server may be different
versions. Renumbering a tag silently breaks interoperability with no error.
`TestTagConstants_WireValues` pins all 17 values so any accidental change fails
loudly in CI.

---

## ADR-008 — Peer-supplied filenames are hostile input

**Decision:** All filenames arriving from a peer pass through `sanitiseName`
before touching the filesystem; it strips `/`, `\`, null bytes, and `..`
sequences (looped, so `....` collapses fully).

**Alternatives:** Trust the sender's filename (it's "just" a filename).

**Why:** The sender fully controls `FileHeader.Name`. Without sanitisation a
crafted name like `../../.bashrc` could write outside the receiver's chosen
directory. The original implementation only stripped separators, leaving
`../../x` as `....x` — still containing `..`. The fix strips `..` sequences in a
loop. Both the Wasm receiver (`ReceiveStateMem`) and CLI receiver
(`ReceiveState`) call it before `filepath.Join`.

---

## ADR-009 — Schedule auth precedence

**Decision:** `handleAuth` / `authorizeUpload` evaluate in strict order:
(1) no IP list and no password → allow all; (2) IP in non-empty allowlist →
allow; (3) password set → require and verify; (4) else → deny.

**Why:** A prior bug had `IPAllowedToUpload` return `true` for an empty list,
short-circuiting before the password check — so `GMMFF_SCHEDULE_PASSWORD` was
silently ignored unless an IP allowlist was also configured. The explicit
sequential ordering closes that bypass. This is security-critical; do not
reorder.

---

## ADR-010 — "0.0.0.0" in IP allowlists means allow-all

**Decision:** `isAllowAllCIDR` treats `""`, `0.0.0.0`, `0.0.0.0/0`, `::`, `::/0`
(and comma combinations) as "no restriction" — leaving the parsed IP list nil.

**Alternatives:** Parse `0.0.0.0` literally (what the code did before).

**Why:** Operators naturally set `GMMFF_SCHEDULE_DOWNLOAD_IP=0.0.0.0` intending
"everyone." Parsed literally, that becomes `0.0.0.0/32` — a host address no real
client has — so it blocked *everyone* instead of allowing everyone. Treating the
any-host/any-route forms as nil makes `IPAllowedToDownload` return true for all,
which is the intended behavior.

---

## ADR-011 — ValidateEnv warns, never fatals

**Decision:** `ValidateEnv` emits warnings for malformed `GMMFF_*` values and the
server applies safe defaults; it never refuses to start.

**Why:** A typo in one feature flag should not take down the whole service. The
server logs a structured warning per bad variable and continues with the default
for that setting. Operators get visibility without an outage.

---

## ADR-012 — Forwarding headers are trusted only from known proxies

**Decision:** Schedule IP allowlists read the client IP via `Config.ClientIP`,
which honours `X-Real-IP` (then the rightmost `X-Forwarded-For` hop) only when
the TCP peer is in `GMMFF_TRUSTED_PROXIES` (default: loopback + private
ranges). chi `middleware.RealIP` was removed. Docker examples publish the port
on `127.0.0.1` only.

**Alternatives:** Trust headers from everyone (previous behaviour); trust only
loopback (breaks Docker, where nginx on the host appears as the bridge gateway).

**Why:** An IP in the upload allowlist skips the password (ADR-009). With
headers trusted from anyone and the backend port published on all interfaces,
any client could send `X-Real-IP: 10.0.0.1` and upload without the password.

**Consequences:** Proxies outside the default ranges must be listed explicitly.
Only a single proxy hop is supported for `X-Forwarded-For`; nginx's
`X-Real-IP $remote_addr` is the recommended path.

---

## ADR-013 — Slot codes are rate-limited per connection and claimed atomically

**Decision:** A WebSocket connection is disconnected after 5 `slot.join`
attempts with an unknown or malformed code. Code index keys are claimed with
`SETNX`; a collision regenerates the code.

**Why:** The code is also the PAKE password and carries ~30 bits, so unlimited
guessing turns the broker into an online oracle; a successful guess makes the
attacker a full session peer. A plain `SET` on collision silently re-pointed a
live code at a different slot whose PAKE password still matched. Pair with
nginx `limit_req` on `/ws` so reconnecting does not reset the budget cheaply.

---

## ADR-014 — Split codes: server nameplate + client-only secret

**Decision:** The code users share is `<nameplate>-<secret>`, e.g.
`bear-cozy-cone-maple-river`. The server generates the 3-word nameplate (as
before) and uses it only to find the slot. The initiator's client appends a
2-word secret (`crypto.WithSecret`) that is never sent to the server. CPace
runs on the full 5-word code. `signaling.JoinSlot` sends only the nameplate
(`joinPayload`), and `/api/ice` bearer tokens carry only the nameplate. Share
links put the code in the URL fragment (`#code=`), which browsers never send
to the server; `?code=` would land in proxy access logs. `protocol.Version` is
bumped to `"2"`, so the broker rejects v1 clients with its existing
"please update" error, and clients refuse 3-word codes.

**Alternatives:** Keep server-generated codes and trust the server (status
quo); let clients generate the whole code and register it (the server would
still see it on join); a Go `/v3` module path for the break (rejected: nothing
imports gmmff as a library, so it would only churn every import).

**Why:** With server-generated codes the server held the PAKE password and
could MITM every session (ADR-002). 20 bits of secret is enough because CPace
gives an attacker one online guess per handshake, and a wrong guess aborts the
session visibly (Magic Wormhole uses 16 bits). The CPace password includes
the nameplate, which already binds it to the live slot, so the slot ID is not
added to the CPace context.

**Consequences:** v1 and v2 clients cannot connect to each other, and old
3-word codes and `?code=` links stop working. CLI users are fully protected
from a malicious server. Browser users load the JS/Wasm from that same server,
so a server that actively serves a malicious client could still steal the
secret; the split protects them against a passive or log-reading server and
against Redis exposure, not against an actively malicious one.
`TestSplitCode_ServerWithNameplateCannotMITM` (pake) and
`TestJoinPayload_SendsOnlyNameplate` (signaling) pin the invariant.
