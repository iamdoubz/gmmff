<p align="center">
  <img src="imgs/gmmff.png" alt="A view from space of a giant worm hole sucking in your favorite file types... oh the horror!">
</p>

# gmmff — peer-to-peer file transfer

> **gmmff** (pronounced *gimph*) is a brutally simple, cryptographically sound
> peer-to-peer file transfer system.

gmmff consists of two parts: a **signaling server** that brokers the initial
connection, and a **CLI client** that handles the actual transfer.  The server
never sees file contents — once two peers are connected, all data flows
directly between them over an encrypted WebRTC data channel.

---

## How it works

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

1. The sender runs `gmmff send <file>` and receives a one-time 3-word code
2. The sender shares that code out-of-band with the receiver
3. The receiver runs `gmmff receive <code>` on any machine, anywhere
4. CPace PAKE authenticates both sides — the signaling server stays blind
5. The SDP offer/answer is HMAC-signed with the PAKE shared key, preventing man-in-the-middle substitution
6. A direct WebRTC/DTLS data channel opens and the file transfers peer-to-peer

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

---

## Quick start

### Sending a file

```bash
gmmff send myfile.zip --server wss://your-server/ws
```

```
  ╔══════════════════════════════════════╗
  ║  Share this code with the receiver:  ║
  ║                                      ║
  ║    acid-lake-drum                    ║
  ║                                      ║
  ║  Expires in 10 minutes               ║
  ╚══════════════════════════════════════╝

  Run on the other machine:
    gmmff receive acid-lake-drum
```

### Receiving a file

```bash
gmmff receive acid-lake-drum --server wss://your-server/ws
```

### Cancelling a transfer

Press `Ctrl+C` on either side at any time. Both peers receive a clean
cancellation message. The partial file is preserved on the receiver so
the transfer can be resumed in a future session.

### Resuming a transfer

Just run the same `send` and `receive` commands again with the same file.
The receiver detects the partial file automatically and the transfer picks
up from where it left off — on both progress bars.

---

## Send flags

| Flag | Default | Description |
|------|---------|-------------|
| `--server` | `ws://localhost:8080/ws` | Signaling server WebSocket URL (`GMMFF_SERVER`) |
| `--stun` | Google STUN | STUN server URL (`GMMFF_STUN`) |
| `--window` | `2` | Sliding window size — chunks in flight simultaneously |
| `--chunk-size` | `65526` | Chunk size in bytes (SCTP maximum; tune for your network) |

## Receive flags

| Flag | Default | Description |
|------|---------|-------------|
| `--server` | `ws://localhost:8080/ws` | Signaling server WebSocket URL (`GMMFF_SERVER`) |
| `--stun` | Google STUN | STUN server URL (`GMMFF_STUN`) |
| `--out` / `-o` | `.` | Directory to save the received file |

Set `GMMFF_SERVER` in your environment to avoid passing `--server` every time:

```bash
export GMMFF_SERVER=wss://your-server/ws
gmmff send myfile.zip
```

---

## Running the signaling server

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

**Production TLS**: use a reverse proxy (Caddy, nginx, AWS ALB).  The server
speaks plain HTTP internally; the proxy handles TLS termination and forwards
`wss://` connections.

---

## Security model

### CPace PAKE
Both peers authenticate using CPace over the ristretto255 group
(`filippo.io/cpace`).  The signaling server forwards PAKE messages opaquely
and never learns the shared secret.

### SDP MAC binding (zero-trust signaling)
After the PAKE handshake, two subkeys are derived from the shared secret using
HKDF-SHA256:

```
offerKey  = HKDF(sharedKey, salt="gmmff-v1", info="sdp-offer-mac")
answerKey = HKDF(sharedKey, salt="gmmff-v1", info="sdp-answer-mac")
```

The initiator HMAC-signs the SDP offer with `offerKey` before sending it to
the relay.  The responder verifies the MAC before calling `SetRemoteDescription`
— and vice versa for the answer.  A compromised signaling server cannot
substitute its own SDP fingerprints because it does not know the shared key.

### DTLS 1.3
All data channel traffic is encrypted end-to-end by Pion's DTLS 1.3
implementation.  The signaling server is out of the loop once ICE completes.

### Resumable transfers
Partial files are written as `<name>.gmmff_partial` with a `<name>.gmmff_meta`
sidecar (SHA256 + chunk size + bytes written).  On resume, the receiver
replays the partial file through SHA-256 to reconstruct the running hash and
sends a `ResumeFrom` frame to the sender.  Both progress bars advance to the
correct offset before transfer continues.  On completion, both temp files are
deleted and the final file is renamed into place.

---

## Wire protocol

All signaling messages are JSON `{ "type": "...", "payload": { ... } }`.

### Slot creation

```
Client → Server:   { "type": "slot.create", "payload": { "protocol_version": "1" } }
Server → Client:   { "type": "slot.created", "payload": { "slot_id": "...", "code": "word-word-word", "ttl_seconds": 600 } }
```

### Slot join

```
Client → Server:   { "type": "slot.join", "payload": { "code": "word-word-word", "protocol_version": "1" } }
Server → both:     { "type": "slot.ready", "payload": { "role": "initiator|responder" } }
```

### PAKE relay (opaque)

```
Client → Server:   { "type": "pake.a", "payload": { "data": "<base64>" } }
Server → peer:     { "type": "pake.a", "payload": { "data": "<base64>" } }
```

The same opaque relay applies to `pake.b`.  The server never decodes these.

### Signed SDP

```
Client → Server:   { "type": "sdp.offer", "payload": { "sdp": "<base64>", "mac": "<base64>" } }
Server → peer:     { "type": "sdp.offer", "payload": { "sdp": "<base64>", "mac": "<base64>" } }
```

`sdp` is the base64-encoded WebRTC `SessionDescription` JSON.  `mac` is the
base64-encoded HMAC-SHA256 over the raw SDP bytes, computed with the
appropriate HKDF subkey.  The same structure applies to `sdp.answer`.

### Data channel transfer tags

Once the WebRTC data channel opens, all transfer frames are binary with a
one-byte tag prefix:

| Tag | Direction | Meaning |
|-----|-----------|---------|
| `0x01` | sender → receiver | File header (JSON: name, size, SHA-256, chunk count) |
| `0x02` | sender → receiver | Chunk (8-byte seq + payload) |
| `0x03` | receiver → sender | Chunk ack (8-byte seq) |
| `0x04` | sender → receiver | Transfer done |
| `0x05` | receiver → sender | Transfer OK (hash verified) |
| `0x06` | either direction | Transfer error |
| `0x07` | receiver → sender | Resume from chunk N (8-byte seq) |
| `0x08` | either direction | Cancelled |

### Error frames

```json
{ "type": "error", "payload": { "code": "ERR_SLOT_NOT_FOUND", "message": "slot not found..." } }
```

Error codes contain no user-identifying information and are safe to include
in bug reports.

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
│   ├── main.go             # Root command + serve subcommand
│   ├── send.go             # gmmff send <file>
│   └── receive.go          # gmmff receive <code>
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
│   ├── pake/               # HKDF subkey derivation + SDP MAC signing
│   │   └── session.go
│   ├── peer/               # WebRTC + PAKE orchestration
│   │   └── peer.go
│   ├── signaling/          # WebSocket signaling client
│   │   ├── client.go
│   │   └── b64.go
│   └── transfer/           # Binary chunk protocol (send + receive state)
│       └── transfer.go
├── pkg/protocol/           # Wire message types (shared server/client)
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

## Current features

- **Signaling server** — Go, Redis-backed, privacy-safe structured logs, Docker-ready
- **CLI client** — `gmmff send <file>` / `gmmff receive <code>`
- **CPace PAKE** — zero-knowledge authentication; server stays blind to the shared secret
- **SDP MAC binding** — HMAC-signed SDP with HKDF-derived subkeys; prevents MITM via signaling relay
- **DTLS 1.3** — all data channel traffic encrypted end-to-end via Pion WebRTC
- **Sliding window** — configurable in-flight chunks (`--window`); default 2
- **Configurable chunk size** — up to SCTP maximum 65526 bytes (`--chunk-size`); default 65526
- **Resumable transfers** — partial + meta sidecar files; both progress bars pick up at the correct offset
- **Clean cancellation** — Ctrl+C on either side delivers a clear message to both peers; partial file preserved for resume
- **SHA-256 integrity** — full-file hash verified before `TransferOK` is sent; corrupt or incomplete files are rejected

## Planned upcoming features

- **WebAssembly** browser client compiled from the same Go source
- **coturn** STUN/TURN integration and credential rotation
- **Sliding window optimisation** — per-session adaptive window sizing

---

## License

MIT — see [LICENSE](LICENSE).  All dependencies are MIT or Apache-2.0.
