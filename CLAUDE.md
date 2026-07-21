# CLAUDE.md — gmmff Project Guide

This file tells Claude how to work on this repository. Read it fully before
making changes. It encodes hard-won conventions, security invariants, and
bugs already fixed — do not reintroduce them.

---

## MEMANTO - Your Active Memory Companion

**MEMANTO is not a passive store. It is an active companion agent that works alongside you.**
Don't treat MEMANTO like a static blob you query once and forget. It's a teammate you keep
talking to, every preference, decision, and correction flows through it. MEMANTO remembers,
recalls, and answers so you hold context across sessions, honor prior decisions, and avoid
repeating mistakes the user already corrected.

Every memory operation in this session goes through MEMANTO. There is no exception.

> **CRITICAL**: All `memanto` commands are **shell commands**. Always run them using the Bash tool.
> Never simulate, describe, or "pretend to call" them. If you cannot run the shell, say so explicitly instead of inventing memory state.

### NON-NEGOTIABLE RULES

These are not suggestions. Follow each one on every turn.

0. **Activate the `gmmff` agent at the start of every session, before any memory op.** Run
   `memanto agent activate gmmff` first thing. This machine hosts multiple projects and the
   session-start sync may activate a *different* project's agent (e.g. `gmmff`), so the
   auto-synced `MEMORY.md` can belong to the wrong project — do not trust it as LastERP context
   until you've activated `gmmff` and re-synced. Confirm with `memanto agent list` (the
   active one is marked). All `recall`/`remember`/`answer` calls read and write the *active*
   agent's store, so getting this wrong silently pollutes or mis-reads another project's memory.

---

## Companion docs — read these too

Two files in `docs/` extend this guide. Read both at the start of any
non-trivial session; treat them as authoritative alongside this file.

- **`docs/DECISIONS.md`** — architecture decision log. The *why* behind choices
  that aren't obvious from the code: why `/api/ice` is gated, why chat uses the
  full session model, why wire bytes are frozen, why `0.0.0.0` means allow-all.
  Before changing anything load-bearing, check whether a decision record already
  explains the current design. When you make a new non-obvious decision, append
  an ADR entry.
- **`docs/TEST-PLAN.md`** — the tiered test strategy: what's covered (Tiers 1–5),
  what's pending (Tiers 6–8), the coverage snapshot, and which tests are
  security-load-bearing. Consult it before writing tests or planning coverage
  work, and update the status as tiers land.

If guidance here conflicts with a companion doc, this file wins for *conventions*
and `DECISIONS.md` wins for *rationale*; reconcile and flag the conflict.

---

## What gmmff is

gmmff (pronounced "gimph") is a secure peer-to-peer file transfer and chat
system with a scheduled-delivery ("Schedule") feature.

- **Module path:** `github.com/iamdoubz/gmmff/v2` — this is a **v2 module**.
  The `/v2` suffix is **mandatory** in every internal import. Never drop it.
- **Architecture:** Star-topology WebRTC. A Go signaling server (backed by
  Redis **or** Valkey — they are wire-compatible drop-ins, selected via
  `GMMFF_REDIS_URL`) brokers WebSocket connections between peers. Peers then
  establish a CPace PAKE + HKDF-authenticated
  WebRTC DTLS 1.3 data channel (SCTP) for the actual transfer. The signaling
  server never sees file contents.
- **Clients:** Browser client is Go compiled to Wasm (`web/cmd/gmmff-wasm`).
  There is also a native CLI (`cmd/gmmff`).
- **Repo:** https://github.com/iamdoubz/gmmff

### Security model in one paragraph

Two peers exchange a human-readable code (e.g. `bear-cozy-cone`) out of band.
That code drives a CPace PAKE handshake that produces a shared secret, which is
HKDF-expanded into separate offer/answer subkeys used to MAC the SDP exchange.
This authenticates the WebRTC handshake against a man-in-the-middle on the
signaling server. The data channel is DTLS 1.3. The signaling server is
untrusted by design — it only brokers introductions.

---

## Build, test, and format commands

| Command | What it does |
|---|---|
| `make build` | Compiles the binary. Run `make wasm` first if the embedded static dir is stale. |
| `make wasm` | Builds the Wasm browser client into `web/static/`. |
| `make test` | `go test -count=1 ./...` — CGO-free. **This is the default test command.** |
| `make test-race` | Race detector; needs `CGO_ENABLED=1 CC=clang`. **Does not work on Windows** (MSVC `-mthreads` error) — use `make test` there. |
| `make cover` / `make test-cover` | Coverage profile / HTML report. |
| `make fmt` | `gofmt -s -w` on all files. **Run before every commit.** |
| `make fmt-check` | CI format verification. |

`coverage.out` and `coverage.html` are generated artifacts — never commit them
(already in `.gitignore`).

---

## Repository layout

```
cmd/gmmff/              CLI + signaling server entrypoints
  main.go               runServe + setup helpers (store, schedule, ICE, HTTP)
  create.go join.go     session REPL commands
  chat.go schedule.go   chat + schedule CLI subcommands
internal/
  broker/               WebSocket hub (broker.go), HTTP server (server.go),
                        UI config + env validation (uiconfig.go)
  schedule/             config.go store.go handler.go cleanup.go client.go
  peer/peer.go          WebRTC + PAKE orchestration (protocol state machines)
  session/session.go    multi-peer session coordinator
  transfer/transfer.go  wire protocol: frames, chunking, sanitiseName
  turn/turn.go          STUN/TURN parsing + ephemeral credential derivation
  slot/slot.go          slot domain model + state machine
  pake/session.go       CPace wrapper + SDP MAC
  crypto/codegen.go     3-word passphrase generation + wordlist
  localmode/            mDNS local discovery, TLS, embedded static server
  signaling/            WebSocket signaling client (native + js builds)
web/
  static/               index.html, css/app.css, js/app.js, i18n/ (32 langs)
  cmd/gmmff-wasm/main.go Wasm entrypoint — bridges JS <-> Go session model
configs/                gmmff.conf (nginx), .env.example, gmmff.service
docs/                   NGINX.md and other operator docs
```

---

## Non-negotiable conventions

### Module path
Every internal import is `github.com/iamdoubz/gmmff/v2/internal/...`. When
adding a file or moving code, keep the `/v2`. Go v2+ modules require it.

### Wire protocol is frozen
The tag bytes `0x01`–`0x11` in `transfer.go` are a permanent contract.
Changing a value breaks every deployed client against every deployed server.
`TestTagConstants_WireValues` pins all 17 — if you change protocol behavior,
add new tags, never renumber existing ones.

### Filenames from peers are hostile
Any filename arriving from a peer must pass through `sanitiseName` before it
touches the filesystem. It strips path separators (`/`, `\`), null bytes, and
`..` traversal sequences. Both the Wasm receiver (`ReceiveStateMem`) and the
CLI receiver (`ReceiveState`) call it before `filepath.Join`. Do not bypass it.

### IP allowlists: "0.0.0.0" means allow-all
In `schedule/config.go`, `isAllowAllCIDR` treats `""`, `0.0.0.0`, `0.0.0.0/0`,
`::`, `::/0` (and comma combinations) as "no restriction" — leaving the IP list
nil. Do not let these be parsed as literal `/32` host addresses; that blocks
everyone instead of allowing everyone.

### Schedule auth precedence (security-critical)
In `schedule/handler.go`, `handleAuth` and `authorizeUpload` must follow this
exact order:
1. No IP list **and** no password → allow everyone.
2. IP in a non-empty allowlist → allow (no password needed).
3. Password is set → require and verify it.
4. Otherwise → deny.
A previous bug let `IPAllowedToUpload` return `true` for an empty list and
short-circuit the password check, so `GMMFF_SCHEDULE_PASSWORD` was silently
ignored unless an IP allowlist was also set. Never reintroduce that ordering.

### `/api/ice` is gated, per-request, and rate-limited
- Requires `Authorization: Bearer <slot-code>`; the handler validates the code
  against the store (slot must be `waiting`/`active`/`full`) else 401.
- Credentials are generated **fresh per request** from the raw TURN config —
  never pre-computed at startup, never cached.
- nginx rate-limits it to 5 requests/hour/IP (see `configs/gmmff.conf` and
  `docs/NGINX.md`).
- Exactly **one** `/api/ice` call should fire per session. The browser passes
  the slot code; initiator paths fetch after the code is known (Wasm
  `fetchICEWithCode`), joiner paths pass the code directly. If you touch this
  flow, verify the call count stays at one.

### TURN is connectivity, not discovery
TURN/STUN affect only the ~8–15% of connections that can't go direct or via
STUN. Peer discovery is always the signaling server. Gating or rate-limiting
TURN never breaks peer-finding. The CLI uses local `GMMFF_TURN`, not the push
endpoint, so server-side TURN changes don't affect CLI users.

### Ephemeral vs static TURN credentials
Prefer ephemeral (`secret=` → HMAC-derived short-lived credential). The master
secret must never leave the server. `GMMFF_PUSH_TTL` controls the lifetime
(default 30m). Static `user=`/`pass=` credentials are sent verbatim to every
peer — only acceptable for intentionally public/anonymous TURN.

---

## Code style

- **Cyclomatic complexity < 15** (gocyclo). When a function exceeds it, extract
  named helpers with behavior-preserving refactors.
- **Exception:** the protocol state machines in `peer.go`, `session.go`,
  `transfer.go` (`runFromReader`), and `schedule/client.go` (`Upload`/`Download`)
  are intentionally complex. Do **not** refactor them without test coverage in
  place first — they coordinate goroutines, channels, ICE, DTLS, and PAKE.
- **Refactors must preserve behavior.** Verify against existing tests before and
  after. When extracting helpers, keep the original public signatures.
- **Formatting:** always `make fmt` before committing. Goreportcard "Line 1"
  gofmt warnings mean the file needs formatting somewhere, not literally line 1.
- **Run `make build` after edits.** Watch for: missing `func` declarations after
  large extractions, duplicate definitions, and type mismatches
  (e.g. `FetchMeta` returns `*PublicFileMeta`, not `*FileMeta`).

### Internationalization
32 language files in `web/static/i18n/`. New UI strings need a key added to
**all 32** files; reuse an existing key where the text already matches (e.g.
Files and Chat share `files_max_peers_label`). The page `<title>` is set in
`index.html` and must **not** be overwritten when translations load.

### Frontend (app.js)
Files and Chat share unified messaging helpers keyed by tab (`'files'`/`'chat'`):
`appendSystemMsg`, `appendBubble`, `disableTabInput`, `setTabSessionEnded`,
`handleIncomingMessage`, `handleParticipantLeft`, `updatePeerCount`, with
`TAB_IDS` mapping tab → element IDs. When adding messaging behavior, add it to
the shared helper so both tabs benefit — don't fork per-tab logic.

---

## Testing philosophy

> Full strategy, coverage snapshot, and pending work live in
> **`docs/TEST-PLAN.md`** — that file is the source of truth; the summary here
> is orientation only.

Tests have repeatedly caught **real production bugs** (auth bypass, path
traversal, non-deterministic byte-size parsing, `formatDurationLabel(0)`).
Treat them as a safety net worth investing in.

- When a test fails, first decide whether the **test** or the **code** is wrong.
  Both happen. The max-downloads cap is a ceiling, not a floor — a test once
  asserted the wrong direction.
- Tiered suites built so far (Tier 1–5): config, turn, store, HTTP handlers,
  transfer wire protocol, crypto codegen, slot state machine, pake MAC.
- Security-relevant tests to preserve and extend: cross-key PAKE MAC rejection
  (MITM detection), offer≠answer MAC role separation, `sanitiseName` traversal
  stripping, schedule auth precedence, wire-tag pinning.
- Pending tiers: Tier 6 (archive zip round-trip, in-memory store), Tier 7
  (transfer/broker coverage with a mock DataChannelWriter), Tier 8 (Redis
  integration, session via `net.Pipe`).
- `make test` is CGO-free and the default. Use it on Windows. `-race` needs
  clang and a non-Windows host.

---

## How Claude should work in this repo

### As a feature adder
- Read the relevant file **fully** before editing. Do not pattern-match from
  memory — reconstructing from partial context has caused real mismatches here.
- Trace the full call path for anything touching sessions, ICE, or the wire
  protocol before changing it.
- For UI features, check whether the Files/Chat shared helpers already cover the
  behavior. Add i18n keys to all 32 files.
- Prefer the smallest change that fully solves the request. State assumptions
  inline rather than asking when the answer is inferable from the code.

### As a code scrutineer
- After any change, mentally run `make build` and `make test` — flag likely
  compile errors (missing `func`, duplicate defs, wrong types) before claiming
  done.
- Watch cyclomatic complexity on new functions; extract helpers proactively.
- Check that refactors are behavior-preserving and that public signatures are
  unchanged unless the task requires otherwise.
- Call out dead assignments (ineffassign), unhandled errors, and goroutine/
  channel leaks.

### As a security expert
- Treat every input crossing a trust boundary as hostile: peer-supplied
  filenames, slot codes, SDP, env-var config, HTTP request bodies and headers.
- Never weaken the PAKE/MAC path, the `sanitiseName` guard, the schedule auth
  precedence, or the `/api/ice` gating. If a change would touch these, call out
  the security implication explicitly.
- Do not log secrets (TURN master secret, passwords, delete keys, decrypt keys).
- For self-harm-irrelevant safety: never reproduce malware, and never add
  endpoints that leak credentials or bypass the slot-gating.
- When uncertain whether something is exploitable, say so and reason it through
  rather than asserting it's fine.

### General
- For factual claims about Anthropic products or third-party libraries, verify
  rather than assume — versions and APIs drift.
- Output changes directly to files. The user runs `make fmt`, `make build`, and
  `make test` locally.
- When packaging or summarizing, be concise: what changed, why, and any risk.

---

## Outstanding security tracking (verify before release)

- Go toolchain ≥ go1.26.5 (GO-2026-5856 crypto/tls ECH privacy leak — reachable
  via outbound TLS in signaling dial, schedule client, embedded web server;
  pinned via the `toolchain` directive in `go.mod`, not the `go` directive)
- filippo.io/edwards25519 ≥ v1.1.1 (GO-2026-4503, indirect via cpace/ristretto)
- pion/dtls/v3 ≥ v3.1.1 (CVE-2026-26014)
- Redis ≥ 7.4.6 / Valkey ≥ 7.2.8 (CVE-2025-49844 — the "RediShell" Lua flaw is
  shared lineage and affects both; patch whichever backend you deploy)
- golang.org/x/crypto ≥ v0.45.0 (CVE-2025-47914, CVE-2025-58181)

Run `make vuln` (govulncheck, reachability-aware) to check these; it also runs
in CI on every push/PR (`.github/workflows/vuln.yml`). Confirm current versions
in `go.mod` against these floors when cutting a release. Note: `x/crypto/openpgp`
(GO-2026-5932) is unmaintained with no fix, but gmmff does not import it —
govulncheck reports it as not-called, which is expected.

---

## Key environment variables (operator-facing)

```
GMMFF_SHOW_FILES / GMMFF_SHOW_CHAT / GMMFF_SHOW_SCHEDULE   tab visibility
GMMFF_TAB_ORDER / GMMFF_TAB_DEFAULT                        tab arrangement
GMMFF_PUSH_STUN / GMMFF_PUSH_TURN                          push ICE to browser
GMMFF_PUSH_TTL                                            ephemeral cred TTL (default 30m)
GMMFF_STUN / GMMFF_TURN                                   ICE server config
GMMFF_SCHEDULE_PASSWORD                                   upload password gate
GMMFF_SCHEDULE_UPLOAD_IP / GMMFF_SCHEDULE_DOWNLOAD_IP     IP allowlists (0.0.0.0 = all)
GMMFF_SCHEDULE_MAX_SIZE / GMMFF_SCHEDULE_MAX_DOWNLOADS    upload limits
GMMFF_SCHEDULE_CLEANUP_INTERVAL                           cron expression
GMMFF_TTL_SETTINGS                                        schedule TTL dropdown options
GMMFF_LOG_LEVEL                                           trace|debug|info|warn|error|fatal|panic
```

`ValidateEnv` in `uiconfig.go` warns (never fatally) on malformed values and
applies safe defaults. Keep that behavior: invalid config should degrade, not
crash.

<!-- OPENWIKI:START -->

## OpenWiki

This repository uses OpenWiki for recurring code documentation. Start with `openwiki/quickstart.md`, then follow its links to architecture, workflows, domain concepts, operations, integrations, testing guidance, and source maps.

The scheduled OpenWiki GitHub Actions workflow refreshes the repository wiki. Do not hand-edit generated OpenWiki pages unless explicitly asked; prefer updating source code/docs and letting OpenWiki regenerate.

<!-- OPENWIKI:END -->
