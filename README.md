<p align="center">
  <img src="imgs/gmmff.png" alt="A view from space of a giant worm hole sucking in your favorite file types... oh the horror!">
</p>

# gmmff — peer-to-peer file transfer

> **gmmff** (pronounced *gimph*) is a brutally simple, cryptographically sound
> peer-to-peer file and message transfer system.

gmmff consists of two parts: a **signaling server** that brokers the initial
connection, and a **CLI client** that handles the actual transfer.  The server
never sees file contents — once two (or more) peers are connected, all data flows
directly between them over an encrypted WebRTC data channel.

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

## Application overview

### Installing

Please use the [guide here](docs/INSTALL.md) for installing `gmmff`.

### Building

Please use the [guide here](docs/BUILD.md) for building `gmmff`.

### CLI

[CLI Guide](docs/CLI.md)

### WASM Webclient

[WASM Guide](docs/WASM.md)

### Local-network mode (no internet required)

[Local mode Guide](docs/LOCAL.md)

### Starting a pure chat session (CLI)

For a text-only session without file transfer, use `gmmff chat`:

```bash
# Machine A
gmmff chat --server wss://your-server/ws

# Machine B — gmmff join detects the session type and routes to the chat REPL
gmmff join river-stone-fog --server wss://your-server/ws
```

---

## Commands

See the [Commands Guide](docs/CMDS.md)

---

## Environment variables

See the [Commands Guide](docs/CMDS.md) and the [env example](configs/.env.example)

---

## STUN/TURN configuration

See the [STUN/TURN Guide](docs/TURN.md)

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

### Option B — Local Go + Redis

Prerequisites: **Go 1.23+**, **Redis 7+**

```bash
# Start Redis
redis-server

# Run with in-memory store (no Redis needed for dev)
go run ./cmd/gmmff serve --memory --log-pretty --log-level debug

# Or with Redis
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

All flags have environment variable equivalents with the `GMMFF_` prefix.
Copy `configs/.env.example` to `.env` and adjust.

| Flag | Env var | Default | Description |
|------|---------|---------|-------------|
| `--addr` | `GMMFF_ADDR` | `:8080` | Listen address |
| `--redis-url` | `GMMFF_REDIS_URL` | `redis://localhost:6379/0` | Redis URL |
| `--memory` | — | `false` | Use in-memory store (dev only) |
| `--log-level` | `GMMFF_LOG_LEVEL` | `info` | `trace\|debug\|info\|warn\|error` |
| `--log-pretty` | — | `false` | Human-readable logs |
| `--slot-ttl` | — | `10m` | Slot expiry duration |
| `--tls-cert` | `GMMFF_TLS_CERT` | — | TLS certificate path |
| `--tls-key` | `GMMFF_TLS_KEY` | — | TLS private key path |
| `--web` | `GMMFF_WEB_DIR` | — | Path to `web/static/` — serves browser UI at `/` alongside signaling |
| `--csp-report-only` | — | `false` | Use `CSP-Report-Only` header for debugging — **NOT for production** |

**Production TLS**: use a reverse proxy (Caddy, nginx, AWS ALB).  The server
speaks plain HTTP internally; the proxy handles TLS termination and forwards
`wss://` connections.

---

## Browser UI (Wasm)

The same Go code that powers the CLI compiles to WebAssembly and runs directly
in the browser — one codebase, two delivery targets.

---

## Theming

Copy `web/static/themes/default.json`, edit the values, and point the `THEME_URL`
constant at the top of `app.js` at your new file. Every CSS custom property
is overridable — colors, spacing, radii, fonts, max-width — with no build step required.

---

## Translations

The UI ships with 32 languages including English, Spanish, French, German,
Italian, Swedish, Portuguese, Arabic, Bengali, Persian, Finnish, Hindi,
Indonesian, Japanese, Korean, Marathi, Malay, Dutch, Norwegian, Polish,
Russian, Thai, Filipino, Turkish, Ukrainian, Urdu, Vietnamese, Chinese
(Simplified and Traditional), Tamil, and Sinhala. The language picker in
the footer auto-detects your browser preference and persists your choice
for 7 days.

To add a language: copy `web/static/i18n/en.json`, translate the values, save
as `web/static/i18n/<code>.json`, and add an entry to `web/static/i18n/languages.json`.
No build step required.

---

## ICE settings

A collapsible **ICE servers** panel sits below the tab bar, shared across all
tabs. STUN servers you add are appended to the default. TURN servers use the
same Option A format as the CLI (`turn:host:port?transport=udp&secret=s`).
Settings persist in `localStorage` for 7 days.

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

## Project structure

```
gmmff/
├── cmd/gmmff/              # Binary entrypoint (Cobra CLI)
│   ├── main.go             # Root command + serve subcommand + shared helpers
│   ├── create.go           # gmmff create — starts file+message session, session REPL
│   ├── chat.go             # gmmff chat — pure chat; gmmff join — joins any session
│   └── local.go            # gmmff local — self-contained local-network mode
├── internal/
│   ├── broker/             # WebSocket hub, message router, HTTP server
│   │   ├── broker.go
│   │   └── server.go
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
│   ├── chat/               # Pure text chat session (CLI REPL + idle timer)
│   │   └── session.go
│   ├── pake/               # HKDF subkey derivation + SDP MAC signing
│   │   └── session.go
│   ├── peer/               # WebRTC + PAKE orchestration; StartSession/JoinSession
│   │   └── peer.go
│   ├── peerconfig/         # Shared Config type (avoids peer↔session import cycle)
│   │   └── peerconfig.go
│   ├── session/            # Bidirectional session coordinator (Option B architecture)
│   │   └── session.go
│   ├── signaling/          # WebSocket signaling client
│   │   ├── client_native.go  # gorilla/websocket (CLI)
│   │   ├── client_js.go      # browser native WebSocket (Wasm)
│   │   └── b64.go
│   ├── transfer/           # Binary chunk protocol (send + receive state machines)
│   │   └── transfer.go
│   ├── localmode/          # Self-contained local-network mode
│   │   ├── embed.go        # //go:embed of web/static (built by make build)
│   │   ├── tls.go          # Self-signed cert generation
│   │   ├── mdns.go         # mDNS registration and peer discovery
│   │   └── local.go        # Orchestrator: broker + web server + session REPL
│   └── turn/               # TURN URL parsing and ephemeral credential derivation
│       └── turn.go
├── pkg/protocol/           # Wire message types (shared server/client)
│   └── protocol.go
├── web/                    # browser UI (Wasm)
│   ├── cmd/gmmff-wasm/     # Go→Wasm entry point (syscall/js bridge)
│   │   └── main.go
│   ├── static/             # served files
│   │   ├── index.html      # mobile-first single-page UI (Files + Chat tabs)
│   │   ├── css/
│   │   │   └── app.css     # all styles (no inline CSS)
│   │   ├── js/
│   │   │   └── app.js      # all UI logic (no inline JS)
│   │   ├── themes/
│   │   │   └── default.json
│   │   └── i18n/
│   │       ├── languages.json
│   │       ├── en.json
│   │       └── ...         # es, fr, de, it, sv, pt-BR, pt-PT, ta, si
│   └── server.go           # dev-only static file server
├── configs/
│   ├── .env.example        # environment variable reference
│   ├── gmmff.conf          # nginx reverse proxy configuration
│   └── gmmff.service       # systemd service unit
├── docs/
│   ├── ARCHITECTURE.md     # signaling server architecture deep-dive
│   ├── BUILD.md            # how to build gmmff from source
│   ├── CLI.md              # cli usage and examples
│   ├── CMDS.md             # all flags and env variables used here
│   ├── INSTALL.md          # installation guide (generic)
│   ├── LOCAL.md            # gmmff local usage document
│   ├── NGINX.md            # nginx reverse proxy setup guide
│   ├── PROTOCOL.md         # wire protocol
│   ├── SECURITY.md         # shows and explains each step used to secure communications
│   ├── SYSTEMD.md          # dedicated system user + systemd setup guide
│   ├── TURN.md             # flags to use STUN and TURN servers with gmmff
│   └── WASM.md             # how to use the wasm webclient
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
- **Browser UI (Wasm)** — same Go source compiled to WebAssembly; Files tab + Chat tab
- **Drag and drop** — drop files anywhere on the browser UI to queue them for sending
- **32 languages** — English, Spanish, French, German, Italian, Swedish, Portuguese (BR/EU), Arabic, Bengali, Persian, Finnish, Hindi, Indonesian, Japanese, Korean, Marathi, Malay, Dutch, Norwegian, Polish, Russian, Thai, Filipino, Turkish, Ukrainian, Urdu, Vietnamese, Chinese (Simplified/Traditional), Tamil, Sinhala; language picker with 7-day persistence
- **ICE settings panel** — configurable STUN/TURN in the browser UI, persisted 7 days
- **Share links + QR codes** — shareable URLs and scannable QR codes on all code screens
- **Display names** — both initiator and joiner can set a name; names are announced to peers on connect and used as message labels throughout the session

### Backlog

- **Browser extension** — use your favourite browser to send/receive files
- **Docker images** — pipeline to package, build, and publish Docker images
- **More languages** — 32 languages shipped; contributions welcome
- **Trusted local CA** — one-time CA install for iOS Safari support in `gmmff local`
- **Quantum-safe encryption** — post-quantum algorithms with elliptic-curve fallback

### Probably won't do

- wasm webclient: window slider (defaults to 2, 1–16 range)
- **Password-protected zips** — optional encryption on the zip archive

---

## Inspiration

[https://xkcd.com/949](https://xkcd.com/949)

<p align="center">
  <a href="https://xkcd.com/949" target="_blank"><img src="https://imgs.xkcd.com/comics/file_transfer.png" alt="xkcd comic explaining the difficulties of sending large files between two people"></a>
</p>

- [X] [webwormhole](https://github.com/saljam/webwormhole) by [@saljam](https://github.com/saljam)
- [X] [FilePizza](https://github.com/kern/filepizza) by [@kern](https://github.com/kern) and [@neerajbaid](https://github.com/neerajbaid)

---

## License

MIT — see [LICENSE](LICENSE).  All dependencies are MIT or Apache-2.0.
