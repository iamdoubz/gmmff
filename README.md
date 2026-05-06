<p align="center">
  <img src="imgs/gmmff.png" alt="A view from space of a giant worm hole sucking in your favorite file types... oh the horror!">
</p>

# gmmff — signaling server

> **gmmff** (pronounced *gimph*) is a brutally simple, cryptographically sound
> peer-to-peer file transfer system.  This repository contains the **signaling
> server** — Phase 1 of the build.

The signaling server is a dumb rendezvous relay.  It never sees file contents.
Its only job is to give two peers a channel to exchange PAKE, SDP, and ICE
frames so they can establish a direct WebRTC data channel.

---

## Architecture at a glance

```
Peer A ──┐                          ┌── Peer B
         │  wss://host/ws           │
         └──── Signaling server ────┘
                    │
               Redis (slot state)
```

<p align="center">
  <img src="imgs/architecture.png" alt="A diagram explaining the high level design of gmmff">
</p>

| Phase | What the server does |
|-------|----------------------|
| `slot.create`  | Generates a UUID + 3-word code, persists in Redis with 10-min TTL |
| `slot.join`    | Resolves code → slot, links the responder, sends `slot.ready` to both |
| Relay          | Forwards `pake.*`, `sdp.*`, `ice.*` frames opaquely to the other peer |
| `bye` / expire | Deletes both Redis keys; notifies peer |

The server **cannot** decrypt the session because PAKE authentication happens
between the two clients.  Even if the server is compromised, it has no access
to transferred data.

---

## Quick start (development)

### Option A — Docker Compose (recommended)

```bash
git clone https://github.com/iamdoubz/gmmff
cd gmmff
docker compose up
# Server available at ws://localhost:8080/ws
```

### Option B — Local Go + Redis

Prerequisites: **Go 1.23+**, **Redis 7+**

```bash
# Start Redis
redis-server

# Run the server (in-memory store for dev, no Redis needed)
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

## Configuration

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

**Production TLS**: use a reverse proxy (Caddy, nginx, AWS ALB).  The server
will speak plain HTTP internally; the proxy handles TLS termination and forwards
`wss://` connections.

---

## Wire protocol

All messages are JSON `{ "type": "...", "payload": { ... } }`.

### Slot creation (initiator)

```
Client → Server:   { "type": "slot.create", "payload": { "protocol_version": "1" } }
Server → Client:   { "type": "slot.created", "payload": { "slot_id": "...", "code": "word-word-word", "ttl_seconds": 600 } }
```

### Slot join (responder)

```
Client → Server:   { "type": "slot.join", "payload": { "code": "word-word-word", "protocol_version": "1" } }
Server → both:     { "type": "slot.ready", "payload": { "role": "initiator|responder" } }
```

### Opaque relay (PAKE → SDP → ICE)

```
Client → Server:   { "type": "pake.a", "payload": { "data": "<base64>" } }
Server → peer:     { "type": "pake.a", "payload": { "data": "<base64>" } }   ← forwarded unchanged
```

The same relay applies to `pake.b`, `sdp.offer`, `sdp.answer`, and
`ice.candidate`.  The server **never decodes these payloads**.

### Error frames

```json
{ "type": "error", "payload": { "code": "ERR_SLOT_NOT_FOUND", "message": "slot not found..." } }
```

Error codes are designed to be safely included in bug reports.  They contain
no user-identifying information.

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
├── cmd/gmmff/          # Binary entrypoint (Cobra CLI)
│   └── main.go
├── internal/
│   ├── broker/         # WebSocket hub, message router, HTTP server
│   │   ├── broker.go
│   │   └── server.go
│   ├── store/          # Redis + in-memory slot persistence
│   │   └── store.go
│   ├── slot/           # Slot domain model & state machine
│   │   └── slot.go
│   ├── crypto/         # Slot code generation (3-word passphrase)
│   │   └── codegen.go
│   └── log/            # Privacy-safe structured logger
│       └── log.go
├── pkg/protocol/       # Wire message types (shared with client)
│   └── protocol.go
├── configs/
│   └── .env.example
├── docs/
│   └── ARCHITECTURE.md
├── Dockerfile
├── docker-compose.yml
├── go.mod
└── README.md
```

---

## Planned upcoming features

- **CLI client** (`gmmff send <file>` / `gmmff receive <code>`) using Pion WebRTC
- **CPace PAKE** handshake in the client library (`filippo.io/cpace`)
- **WebAssembly** browser client compiled from the same Go source
- **coturn** STUN/TURN integration and credential rotation
- **Chunk pipeline**: 64 KB chunks, per-chunk CRC, final SHA-256 verification

---

## License

MIT — see [LICENSE](LICENSE).  All dependencies are MIT or Apache-2.0.
