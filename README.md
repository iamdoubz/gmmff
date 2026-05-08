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

### Attaching a message to a file transfer

Use `-m` / `--message` to include a note alongside any file send. With a single
file the message is printed on the receiver's terminal before the file saves.
With multiple files the message is injected as `message.txt` inside the zip.

```bash
gmmff send report.pdf -m "Here is the Q3 report, let me know if you have questions"
```

### Starting a chat session (CLI)

Machine A starts the session and receives a code:

```bash
gmmff chat --server wss://your-server/ws
```

```
  ╔══════════════════════════════════════╗
  ║   Share this code to start chatting: ║
  ║                                      ║
  ║    river-stone-fog                   ║
  ║                                      ║
  ║  Expires in 10 minutes               ║
  ╚══════════════════════════════════════╝

  Run on the other machine:
    gmmff join river-stone-fog
```

Machine B joins with the code:

```bash
gmmff join river-stone-fog --server wss://your-server/ws
```

Once connected both sides can type freely. The session closes automatically
after 10 minutes of inactivity.

**Session control:**

| Who | Action | Effect |
|-----|--------|--------|
| Initiator | `\q` | Ends the session for everyone |
| Initiator | `Ctrl+C` | Leaves quietly; session stays open |
| Responder | `\q` or `Ctrl+C` | Leaves quietly; session stays open |

### Chat tab (browser UI)

Open the **Chat** tab, click **Start session** to get a code, or click
**Join with a code** to enter one. The session works the same as the CLI:
type `\q` in the message box to end the session (initiator) or leave quietly
(responder). The **End session** button always leaves quietly.

---

## Send flags

| Flag | Default | Description |
|------|---------|-------------|
| `--server` | `ws://localhost:8080/ws` | Signaling server WebSocket URL (`GMMFF_SERVER`) |
| `--stun` | Google STUN | STUN/STUNS server URL, repeatable (`GMMFF_STUN` accepts comma-separated list) |
| `--window` | `2` | Sliding window size — chunks in flight simultaneously |
| `--chunk-size` | `65526` | Chunk size in bytes (SCTP maximum; tune for your network) |

## Receive flags

| Flag | Default | Description |
|------|---------|-------------|
| `--server` | `ws://localhost:8080/ws` | Signaling server WebSocket URL (`GMMFF_SERVER`) |
| `--stun` | Google STUN | STUN/STUNS server URL, repeatable (`GMMFF_STUN` accepts comma-separated list) |
| `--out` / `-o` | `.` | Directory to save the received file |

## Chat flags (`gmmff chat` / `gmmff join`)

| Flag | Default | Description |
|------|---------|-------------|
| `--server` | `ws://localhost:8080/ws` | Signaling server WebSocket URL (`GMMFF_SERVER`) |
| `--stun` | Google STUN | STUN/STUNS server URL, repeatable (`GMMFF_STUN` accepts comma-separated list) |

Set `GMMFF_SERVER` in your environment to avoid passing `--server` every time:

```bash
export GMMFF_SERVER=wss://your-server/ws
gmmff send myfile.zip
```

Use multiple STUN servers by repeating the flag, or via a comma-separated env var:

```bash
# Flag — repeat for each server
gmmff send myfile.zip --stun stun:stun1.example.com:3478 --stun stuns:stun2.example.com:5349

# Environment variable — comma-separated
export GMMFF_STUN=stun:stun1.example.com:3478,stuns:stun2.example.com:5349
```

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
| `--web` | `GMMFF_WEB_DIR` | — | Path to `web/static/` — serves browser UI at `/` alongside signaling |
| `--csp-report-only` | — | `false` | Use `CSP-Report-Only` header for debugging — **NOT for production** |

**Production TLS**: use a reverse proxy (Caddy, nginx, AWS ALB).  The server
speaks plain HTTP internally; the proxy handles TLS termination and forwards
`wss://` connections.

---

## Browser UI (Wasm)

The same Go code that powers the CLI compiles to WebAssembly and runs directly
in the browser — one codebase, two delivery targets.

### Build

**Note**: this is for go 1.23 and lower

```bash
make wasm
# Outputs: web/static/gmmff.wasm + web/static/wasm_exec.js
```

Or manually for go <= 1.23:

```bash
GOOS=js GOARCH=wasm go build -o web/static/gmmff.wasm ./web/cmd/gmmff-wasm
cp "$(go env GOROOT)/misc/wasm/wasm_exec.js" web/static/wasm_exec.js
```

Or manually for go > 1.23:

```bash
GOOS=js GOARCH=wasm go build -o web/static/gmmff.wasm ./web/cmd/gmmff-wasm
cp "$(go env GOROOT)/lib/wasm/wasm_exec.js" web/static/wasm_exec.js
```

### Run locally

```bash
make wasm-serve
# → http://localhost:9000
```

### Deploy

Copy `web/static/` to any static host (S3, Cloudflare Pages, nginx `root`).
The `gmmff.wasm` file is typically 8–15 MB — serve it with `Content-Type: application/wasm`
and gzip/brotli compression enabled for fast first-load.

### Theming

Copy `web/static/themes/default.json`, edit the values, and point the `THEME_URL`
constant at the top of `index.html` at your new file. Every CSS custom property
is overridable — colors, spacing, radii, fonts, max-width — with no build step required.

### Translations

Copy `web/static/i18n/en.json`, translate the values, and point `I18N_URL` in
`index.html` at your new file. All visible strings are in the i18n file — the
HTML contains only `data-i18n` keys, never literal text.

---

## Deployment

For production deployments, see the dedicated guides in the `docs/` directory:

- **[docs/SYSTEMD.md](docs/SYSTEMD.md)** — Creating a dedicated system user, installing the binary and service file, managing configuration without editing the service file, and Redis Unix socket access.

- **[docs/NGINX.md](docs/NGINX.md)** — Configuring nginx as a reverse proxy with TLS termination, WebSocket upgrade headers, timeout tuning, and endpoint access control.

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
| `0x09` | either direction | Chat message (UTF-8 text) |
| `0x0A` | initiator → all | Chat close — ends session for everyone |
| `0x0B` | any participant | Participant leave — quiet exit; session continues |

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
│   ├── send.go             # gmmff send <file> [-m message]
│   ├── receive.go          # gmmff receive <code>
│   └── chat.go             # gmmff chat / gmmff join <code>
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
│   ├── chat/               # Symmetric chat session (CLI REPL + idle timer)
│   │   └── session.go
│   ├── pake/               # HKDF subkey derivation + SDP MAC signing
│   │   └── session.go
│   ├── peer/               # WebRTC + PAKE orchestration
│   │   └── peer.go
│   ├── signaling/          # WebSocket signaling client
│   │   ├── client_native.go  # gorilla/websocket (CLI)
│   │   ├── client_js.go      # browser native WebSocket (Wasm)
│   │   └── b64.go
│   └── transfer/           # Binary chunk protocol (send + receive state)
│       └── transfer.go
├── pkg/protocol/           # Wire message types (shared server/client)
│   └── protocol.go
├── web/                    # browser UI (Wasm)
│   ├── cmd/gmmff-wasm/     # Go→Wasm entry point (syscall/js bridge)
│   │   └── main.go
│   ├── static/             # served files
│   │   ├── index.html      # mobile-first single-page UI
│   │   ├── css/
│   │   │   └── app.css     # all styles (no inline CSS)
│   │   ├── js/
│   │   │   └── app.js      # all UI logic (no inline JS)
│   │   ├── themes/
│   │   │   └── default.json
│   │   └── i18n/
│   │       └── en.json
│   └── server.go           # dev-only static file server
├── configs/
│   ├── .env.example        # environment variable reference
│   ├── gmmff.conf          # nginx reverse proxy configuration
│   └── gmmff.service       # systemd service unit
├── docs/
│   ├── ARCHITECTURE.md     # signaling server architecture deep-dive
│   ├── NGINX.md            # nginx reverse proxy setup guide
│   └── SYSTEMD.md          # dedicated system user + systemd setup guide
├── Dockerfile
├── docker-compose.yml
├── go.mod
└── README.md
```

---

## Features

### Current

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
- **Browser UI (Wasm)** — same Go source compiled to WebAssembly; mobile-first HTML/CSS/JS UI with theme and i18n support
- **Current languages** — English (en, default), Spanish (es), French (fr), German (de), Italian (it), Swedish (sv), Brazilian Portuguese (pt-BR), European Portuguese (pt-PT), Sinhala (si), Tamil (ta)
- **Multi-file and directory transfers** — transfer multiple files and directories support (CLI and wasm webclient)
- **Secure chat** — chat back and forth with someone by exchanging messages (CLI and wasm webclient)

### Backlog

- **coturn** STUN/TURN integration and credential rotation
- **QR Codes** generate easy to share QR codes to scan
- **Browser extension** use your favorite web browser to send/receive files
- **Docker images** create a pipeline to package, create, and update docker images
- **Languages** continue to add more languages
- **Multiple recipients** share a *link* with multiple people and enable Multiple P2P transfers between all
- **Transfer files in chat** add files directly into chat session (sender and receiver)
- **Quantum safe encryption** use quantum safe first algorithyms, falling back to elyptic if unavailable

---

### In queue

- wasm webclient: "Choose a file to send. You will receive a code to share with the receiver." -> "Choose/drop a/some file(s) to send. You will receive a code to share with the receiver."
- zip file: add ability to password protect zip file. Prompt sender for input, if blank, no password. If set, encrypt zip file with provided input password.
- wasm webclient: window slider (defaults to 2, 1-16 range)
- STUN: convert STUN into a list of stun servers with format stun:url:port. Can be called with --stun stun:url1:3748 --stun stun:url2:19302. If none specifiec, use default
- TURN: add TURN config. List of TURN servers with format turn:url:port. Can use multiple. Each turn server must have appropriate auth configured: standard long-term credential and ephemeral credential (static-auth-secret) must be supported

---

## Inspiration

[https://xkcd.com/949](https://xkcd.com/949)

<p align="center">
  <img src="https://imgs.xkcd.com/comics/file_transfer.png" alt="xkcd comic explaining the difficulties of sending large files between two people">
</p> 

- [X] [webwormhole](https://github.com/saljam/webwormhole) by [@saljam](https://github.com/saljam)
- [X] [FilePizza](https://github.com/kern/filepizza) by [@kern](https://github.com/kern) and [@neerajbaid](https://github.com/neerajbaid)


## License

MIT — see [LICENSE](LICENSE).  All dependencies are MIT or Apache-2.0.
