<p align="center">
  <img src="imgs/gmmff-social.png" alt="A view from space of a giant worm hole sucking in your favorite file types... oh the horror!">
</p>

# gmmff — peer-to-peer file transfer

[![GitHub Release](https://img.shields.io/github/v/release/iamdoubz/gmmff?display_name=tag&style=for-the-badge&logo=refinedgithub&logoColor=fff&label=Latest&color=007EC6)](https://github.com/iamdoubz/gmmff/releases/latest)
[![GitHub Actions Workflow Docker Status](https://img.shields.io/github/actions/workflow/status/iamdoubz/gmmff/docker.yml?style=for-the-badge&logo=githubactions&logoColor=fff&label=Builds)](https://github.com/iamdoubz/gmmff/actions/workflows/docker.yml)
[![GitHub Issues](https://img.shields.io/github/issues-raw/iamdoubz/gmmff?style=for-the-badge&logo=freecodecamp&logoColor=fff&color=ec7013&label=Issues)](https://github.com/iamdoubz/gmmff/issues)
[![GitHub Closed Pulls](https://img.shields.io/github/issues-pr-closed/iamdoubz/gmmff?style=for-the-badge&logo=git&logoColor=fff&color=a64dff&label=Pulls)](https://github.com/iamdoubz/gmmff/pulls?q=is%3Apr+is%3Aclosed)
[![GitHub License](https://img.shields.io/github/license/iamdoubz/gmmff?style=for-the-badge&logo=readthedocs&color=67AC09)](LICENSE)
[![Go Report Card](https://goreportcard.com/badge/github.com/iamdoubz/gmmff/v2)](https://goreportcard.com/report/github.com/iamdoubz/gmmff/v2)

> **gmmff** (pronounced *gimph*) is a brutally simple, cryptographically sound
> peer-to-peer file and message transfer system.

gmmff consists of two parts: a **signaling server** that brokers the initial
connection, and a **CLI client** that handles the actual transfer.  The server
never sees file contents — once two (or more) peers are connected, all data flows
directly between them over an encrypted WebRTC data channel.

---

## Application overview

### Installing

Please use the [guide here](docs/INSTALL.md) for installing `gmmff`.

### Building

Please use the [guide here](docs/BUILD.md) for building `gmmff`.

### CLI

[CLI documentation](docs/CLI.md)

### WASM Webclient

[WASM documentation](docs/WASM.md)

### Schedule — encrypted server-side transfers

[Schedule documentation](docs/SCHEDULE.md)

#### Limitations

The crypto api is only available in secure contexts: https and localhost. If
you attempt to use schedule using http, it will not work!

### Local-network mode (no internet required)

[Local mode documentation](docs/LOCAL.md)

---

## Commands

See the [Commands documentation](docs/CMDS.md)

---

## Environment variables

See the [Commands documentation](docs/CMDS.md) and the [env example](configs/.env.example)

---

## STUN/TURN configuration

See the [STUN/TURN documentation](docs/TURN.md)

---

## Quick Start

### Option A — Docker Compose

```bash
git clone https://github.com/iamdoubz/gmmff
cd gmmff
cp configs/.env.example configs/.env
docker compose up -d
# Server available at ws://localhost:8080/ws
```

### Option B — Local Go + Redis or Valkey

Prerequisites: **Go 1.23+**, and **Redis 7+** *or* **Valkey 7.2+** (wire-compatible
drop-in — the same client talks to either; use a `valkey://` URL if you prefer).

```bash
# Start Redis (or: valkey-server)
redis-server

# Run with in-memory store (no Redis/Valkey needed for dev)
go run ./cmd/gmmff serve --memory --log-pretty --log-level debug

# Or with Redis/Valkey (set GMMFF_REDIS_URL; valkey:// is accepted)
go run ./cmd/gmmff serve --log-pretty --log-level debug
```

### Verify

```bash
curl http://localhost:8080/healthz   # → ok
curl http://localhost:8080/readyz    # → ok (or 503 if Redis is down)
curl http://localhost:8080/metrics   # → JSON counters
```

---

## Server configuration

*Most* flags have environment variable equivalents with the `GMMFF_` prefix.
Copy `configs/.env.example` to `.env` and adjust.

See [ENV.md](docs/ENV.md) and the [example env file](configs/.env.example) for more information.

---

## Deployment

For production deployments, see the dedicated guides in the `docs/` directory:

- **[docs/SYSTEMD.md](docs/SYSTEMD.md)** — Creating a dedicated system user, installing the binary and service file, managing configuration without editing the service file, and Redis Unix socket access.
- **[docs/NGINX.md](docs/NGINX.md)** — Configuring nginx as a reverse proxy with TLS termination, WebSocket upgrade headers, timeout tuning, and endpoint access control.

---

## Security model

See [Security Documentation](docs/SECURITY.md) for more information.

---

## Wire protocol

See [Protocol Documentation](docs/PROTOCOL.md) for more information.

---

## Privacy & logging

Logs contain **only**:

- Timestamp
- Component name (`broker`, `store`, `main`)
- Slot UUID (opaque — means nothing to outsiders)
- Error code (e.g. `ERR_REDIS_UNAVAILABLE`)
- HTTP method + path + status code

Logs **never** contain: file names, file sizes, IP addresses, user agents,
slot codes, or any data that could identify a transfer or a user.

---

## Architecture overview

```
Peer A ──┐                          ┌── Peer B
         │  wss://host/ws           │
         └──── Signaling server ────┘
                    │
               Redis (slot state)
```

1. Peer A runs `gmmff create` and receives a one-time 3-word code
2. Peer A shares that code out-of-band with Peer B
3. Peer B runs `gmmff join <code>` on any machine, anywhere
4. CPace PAKE authenticates both sides — the signaling server stays blind
5. The SDP offer/answer is HMAC-signed with the PAKE shared key, preventing man-in-the-middle substitution
6. A direct WebRTC/DTLS control channel opens; the signaling server's job is done
7. Both peers enter the session REPL and can freely exchange files and messages

| Phase | What the server does |
|-------|----------------------|
| `slot.create`  | Generates a UUID + 3-word code, persists in Redis with 10-min TTL |
| `slot.join`    | Resolves code → slot, links the responder, sends `slot.ready` to both |
| Relay          | Forwards `pake.*`, `sdp.*`, `ice.*` frames opaquely to the other peer |
| `bye` / expire | Deletes both Redis keys; notifies peer |

The server **cannot** intercept the session.  PAKE authentication happens
entirely between the two clients, and the DTLS session key is bound to the
PAKE shared secret via HMAC — so a compromised signaling server cannot
substitute its own SDP fingerprints.

If you want to learn more, see the dedicated [Architecture document](docs/ARCHITECTURE.md).

---

## Project structure

```
gmmff/
├── cmd/gmmff/              # Binary entrypoint (Cobra CLI)
│   ├── main.go             # Root command + serve subcommand + shared helpers
│   ├── create.go           # gmmff create — starts file+message session, session REPL
│   ├── chat.go             # gmmff chat — pure chat; gmmff join — joins any session
│   ├── local.go            # gmmff local — self-contained local-network mode
│   └── cleanup.go          # gmmff cleanup — remove expired schedule uploads (cron-friendly)
├── internal/
│   ├── broker/             # WebSocket hub, message router, HTTP server, UI config
│   │   ├── broker.go
│   │   ├── server.go
│   │   └── uiconfig.go     # Feature flags served via /config.json
│   ├── schedule/           # Server-side encrypted file storage (Schedule feature)
│   │   ├── config.go       # Env parsing, TTL options, IP allowlists
│   │   ├── store.go        # Pending/complete file lifecycle, chunk storage
│   │   ├── handler.go      # HTTP handlers: /api/schedule/*
│   │   └── cleanup.go      # Crontab parser, background cleanup goroutine
│   ├── store/              # Redis + in-memory slot persistence
│   │   └── store.go
│   ├── slot/               # Slot domain model & state machine
│   │   └── slot.go
│   ├── crypto/             # Slot code generation (3-word passphrase)
│   │   └── codegen.go
│   ├── log/                # Privacy-safe structured logger
│   │   └── log.go
│   ├── archive/            # On-the-fly zip for multi-file transfers
│   │   └── archive.go
│   ├── peer/               # WebRTC + PAKE orchestration; StartSession/JoinSession
│   │   └── peer.go
│   ├── peerconfig/         # Shared Config type (avoids peer↔session import cycle)
│   │   └── peerconfig.go
│   ├── session/            # Bidirectional session coordinator
│   │   └── session.go
│   ├── signaling/          # WebSocket signaling client
│   │   ├── client_native.go
│   │   ├── client_js.go
│   │   └── b64.go
│   ├── transfer/           # Binary chunk protocol (send + receive state machines)
│   │   └── transfer.go
│   ├── localmode/          # Self-contained local-network mode
│   │   ├── embed.go
│   │   ├── tls.go
│   │   ├── mdns.go
│   │   └── local.go
│   └── turn/               # TURN URL parsing and ephemeral credential derivation
│       └── turn.go
├── pkg/protocol/           # Wire message types (shared server/client)
│   └── protocol.go
├── web/                    # Browser UI (Wasm + plain JS)
│   ├── cmd/gmmff-wasm/     # Go→Wasm entry point (syscall/js bridge)
│   │   └── main.go
│   └── static/             # Served files
│       ├── index.html      # Single-page UI (Files + Chat + Schedule tabs)
│       ├── css/
│       │   └── app.css
│       ├── js/
│       │   └── app.js      # UI logic + Schedule IIFE module (AES-GCM crypto)
│       ├── themes/
│       │   └── default.json
│       └── i18n/
│           ├── languages.json
│           ├── en.json
│           └── ...         # 32 languages total
├── configs/
│   ├── .env.example        # All environment variable reference
│   ├── gmmff.conf          # nginx reverse proxy configuration
│   └── gmmff.service       # systemd service unit
├── docs/
│   ├── ARCHITECTURE.md
│   ├── BUILD.md
│   ├── CLI.md
│   ├── CMDS.md
│   ├── INSTALL.md
│   ├── LOCAL.md
│   ├── NGINX.md
│   ├── PROTOCOL.md
│   ├── SCHEDULE.md
│   ├── SECURITY.md
│   ├── SYSTEMD.md
│   ├── TURN.md
│   └── WASM.md
├── Dockerfile
├── docker-compose.yml
├── go.mod
├── go.sum
└── README.md
```

---

## Features

### Current

- **Local-network mode** — `gmmff local` is a fully self-contained mode with embedded server, auto TLS, mDNS discovery, and QR code; no internet or external server required
- **Multi-peer sessions** — `gmmff create --max-peers N` allows 2–10 participants; 2-peer sessions are bidirectional, 3–10 peer sessions broadcast from the initiator to all
- **Display names** — initiator and joiners can set a name; names are announced to all peers on connect and shown as message labels throughout the session
- **Signaling server** — Go, Redis-backed, privacy-safe structured logs, Docker-ready
- **CPace PAKE** — zero-knowledge authentication; server stays blind to the shared secret
- **SDP MAC binding** — HMAC-signed SDP with HKDF-derived subkeys; prevents MITM via signaling relay
- **DTLS 1.3** — all data channel traffic encrypted end-to-end via Pion WebRTC
- **Multi-file and directory transfers** — multiple files and directories zipped on the fly
- **Transfer queue** — multiple transfers serialized automatically; each gets its own progress bar
- **Resumable transfers** — partial + meta sidecar files; progress bars pick up at the correct offset
- **Clean cancellation** — `Ctrl+C` or `\q` delivers clean messages to all peers; partial file preserved
- **SHA-256 integrity** — full-file hash verified before `TransferOK` is sent
- **Secure chat** — pure text chat (`gmmff chat`) or inline messaging within a file session
- **Sliding window** — configurable in-flight chunks (`--window`); default 2
- **Configurable chunk size** — up to SCTP maximum 65526 bytes (`--chunk-size`)
- **STUN multi-server** — append additional STUN servers via `--stun` (repeatable) or `GMMFF_STUN`
- **TURN support** — long-term and ephemeral credentials, mixed auth types, transport hints, max 3 servers
- **Browser UI (Wasm)** — same Go source compiled to WebAssembly; Files, Chat, and Schedule tabs
- **Schedule tab** — browser-side AES-256-GCM encrypted uploads; server never sees plaintext; TTL, download limits, IP/password access control, QR codes, auto-download links, cleanup service
- **Schedule CLI** — `gmmff schedule upload/download/delete` for terminal-based encrypted transfers; full browser↔CLI interoperability
- **Drag and drop** — drop files anywhere on the browser UI to queue them for sending
- **32 languages** — English, Spanish, French, German, Italian, Swedish, Portuguese (BR/EU), Arabic, Bengali, Persian, Finnish, Hindi, Indonesian, Japanese, Korean, Marathi, Malay, Dutch, Norwegian, Polish, Russian, Thai, Filipino, Turkish, Ukrainian, Urdu, Vietnamese, Chinese (Simplified/Traditional), Tamil, Sinhala; language picker with 7-day persistence
- **ICE settings panel** — configurable STUN/TURN in the browser UI, persisted 7 days
- **Share links + QR codes** — shareable URLs and scannable QR codes on all code screens
- **UI feature flags** — 15 server-side feature flags served via `/config.json` control tab visibility, ICE settings, share links, QR codes, server field, peers slider, MOTD, and allowed languages

### Backlog

- **Browser extension** — use your favorite browser to send/receive files
- **More languages** — 32 languages shipped; contributions welcome
- **Trusted local CA** — one-time CA install for iOS Safari support in `gmmff local`
- **Quantum-safe encryption** — post-quantum algorithms with elliptic-curve fallback

### Probably won't do

- wasm webclient: window slider (defaults to 2, 1–16 range)
- **Password-protected zips** — optional encryption on the zip archive

---

## Inspiration

<p align="center">
  <a href="https://xkcd.com/949" target="_blank"><img src="https://imgs.xkcd.com/comics/file_transfer.png" alt="xkcd comic explaining the difficulties of sending large files between two people"></a>
</p>

- [X] [webwormhole](https://github.com/saljam/webwormhole) by [@saljam](https://github.com/saljam)
- [X] [FilePizza](https://github.com/kern/filepizza) by [@kern](https://github.com/kern) and [@neerajbaid](https://github.com/neerajbaid)
- [X] [Firefox Send](https://gitlab.com/timvisee/send) by [@mozilla](https://github.com/mozilla/) new fork by [@timvisee](https://github.com/timvisee)
- [X] [Jirafeau](https://gitlab.com/jirafeau/Jirafeau) by [Jerome Jutteau](https://gitlab.com/mojo42) and many [others](https://gitlab.com/jirafeau/Jirafeau/-/blob/master/AUTHORS.md?ref_type=heads)...

---

## License

MIT — see [LICENSE](LICENSE).  All dependencies are MIT or Apache-2.0.
