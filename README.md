<p align="center">
  <img src="imgs/gmmff-social.png" alt="A view from space of a giant worm hole sucking in your favorite file types... oh the horror!">
</p>

# gmmff — peer-to-peer file transfer

[![GitHub Release](https://img.shields.io/github/v/release/iamdoubz/gmmff?display_name=tag&style=for-the-badge&logo=refinedgithub&logoColor=fff&label=Latest&color=007EC6)](https://github.com/iamdoubz/gmmff/releases/latest)
[![GitHub Actions Workflow Docker Status](https://img.shields.io/github/actions/workflow/status/iamdoubz/gmmff/docker.yml?style=for-the-badge&logo=githubactions&logoColor=fff&label=Builds)](https://github.com/iamdoubz/gmmff/actions/workflows/docker.yml)
[![GitHub Issues](https://img.shields.io/github/issues-raw/iamdoubz/gmmff?style=for-the-badge&logo=freecodecamp&logoColor=fff&color=ec7013&label=Issues)](https://github.com/iamdoubz/gmmff/issues)
[![GitHub Closed Pulls](https://img.shields.io/github/issues-pr-closed/iamdoubz/gmmff?style=for-the-badge&logo=git&logoColor=fff&color=a64dff&label=Pulls)](https://github.com/iamdoubz/gmmff/pulls?q=is%3Apr+is%3Aclosed)
[![GitHub License](https://img.shields.io/github/license/iamdoubz/gmmff?style=for-the-badge&logo=readthedocs&color=67AC09)](LICENSE)

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

### Schedule — encrypted server-side transfers

[Schedule Guide](docs/SCHEDULE.md)

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

## Schedule — server-side encrypted transfers

The **Schedule** tab enables asynchronous file delivery: you upload an encrypted
file to the server and share a link.  The recipient downloads and decrypts it
later — no simultaneous connection required.

**The server never sees plaintext.** The file is encrypted in the browser using
AES-256-GCM before a single byte leaves your device.  The decryption key lives
only in the URL fragment (`#key=…`), which browsers never send to the server,
so it cannot appear in access logs even if the server is compromised.

### How it works

**Uploading (Create)**

1. Browser generates a random 256-bit AES-GCM key via `crypto.getRandomValues`
2. The file is encrypted in 2 MB chunks — each chunk gets a unique nonce derived from `[4-byte chunk index || 8 random bytes]`
3. The filename is also encrypted with the same key before being sent
4. Encrypted chunks are uploaded sequentially with real-time progress (speed, ETA, bytes transferred)
5. A SHA-256 hash of the full ciphertext is computed in the browser and sent to the server for integrity verification
6. On completion the server returns a `file_id`; the browser constructs the share URL:
   `https://host/?type=schedule&id={file_id}#key={hex_key}`
7. The upload screen shows the share URL, a QR code, a direct download link, and a private delete link

**Downloading (Join)**

1. Recipient opens the share URL — the browser extracts `file_id` from `?id=` and the key from `#key=`
2. Ciphertext is fetched from the server
3. Each chunk is decrypted in the browser using the key from the fragment
4. The original file is reassembled and a browser download is triggered automatically
5. For `?dl=1` in the URL, the download starts without any button click — useful for sharing links that work like a direct download

Multiple files follow the same logic as the P2P transfer: they are zipped in
the browser before encryption, so the recipient always receives a single file.

### Enabling Schedule

Schedule is **disabled by default**.  Enable it in your env file:

```bash
GMMFF_SHOW_SCHEDULE=true
GMMFF_SCHEDULE_DIR=./data/schedule   # storage root; pending/ and complete/ created automatically
```

### Nginx configuration

Two additional location blocks are needed (see `configs/gmmff.conf` for the
full example):

```nginx
# Auth endpoint — needs real client IP for access control
location = /api/schedule/auth {
    proxy_pass         http://gmmff_backend;
    proxy_http_version 1.1;
    proxy_set_header   Host             $host;
    proxy_set_header   X-Real-IP        $remote_addr;
    proxy_set_header   X-Forwarded-For  $proxy_add_x_forwarded_for;
    proxy_read_timeout 10s;
    proxy_send_timeout 10s;
}

# Upload endpoints — needs IP + large body + long timeout for big files
location /api/schedule/upload {
    proxy_pass         http://gmmff_backend;
    proxy_http_version 1.1;
    proxy_set_header   Host             $host;
    proxy_set_header   X-Real-IP        $remote_addr;
    proxy_set_header   X-Forwarded-For  $proxy_add_x_forwarded_for;
    proxy_buffering    off;
    proxy_read_timeout 1200s;
    proxy_send_timeout 1200s;
    client_max_body_size 1025M;   # must exceed GMMFF_SCHEDULE_MAX_SIZE
}
```

### Schedule environment variables

| Env var | Default | Description |
|---------|---------|-------------|
| `GMMFF_SHOW_SCHEDULE` | `false` | Show the Schedule tab; also gates the upload API |
| `GMMFF_SCHEDULE_DIR` | `./data/schedule` | Storage root; `pending/` and `complete/` created automatically |
| `GMMFF_SCHEDULE_MAX_SIZE` | `1gb` | Maximum upload size (`gb`/`mb`/`kb` suffix supported) |
| `GMMFF_SCHEDULE_MAX_DOWNLOADS` | `1` | Server cap on downloads per file; `0` = unlimited |
| `GMMFF_SCHEDULE_UPLOAD_IP` | — | Comma-separated IPs/CIDRs allowed to upload without a password |
| `GMMFF_SCHEDULE_PASSWORD` | — | Required upload password (bypassed if caller's IP is in `UPLOAD_IP`) |
| `GMMFF_SCHEDULE_DOWNLOAD_IP` | `0.0.0.0` | Comma-separated IPs/CIDRs allowed to download; `0.0.0.0` = anyone |
| `GMMFF_SCHEDULE_CLEANUP_INTERVAL` | — | Crontab-format background cleanup schedule, e.g. `*/30 * * * *` |
| `GMMFF_TTL_SETTINGS` | `1h,8h,1 day,3 days,7 days,30 days` | Comma-separated TTL options shown in the upload dropdown |

### Access control

The Schedule tab enforces upload access before a file is selected:

- **IP in `GMMFF_SCHEDULE_UPLOAD_IP`** → upload allowed immediately, no password
- **IP not in list, password set** → browser prompts for the upload password before proceeding
- **Neither set** → anyone can upload
- **Download** → unrestricted by default; set `GMMFF_SCHEDULE_DOWNLOAD_IP` to restrict

### Cleanup

Expired files and stale incomplete uploads are removed by the cleanup service.
Two modes are available:

**Background goroutine** (runs inside `gmmff serve`):
```bash
GMMFF_SCHEDULE_CLEANUP_INTERVAL=*/30 * * * *
```

**One-shot via cron** (runs and exits — no server restart needed):
```bash
# /etc/cron.d/gmmff-cleanup
*/30 * * * *  gmmff  /usr/local/bin/gmmff cleanup
```

The cleanup removes completed files past their expiry time, files that have
reached their download limit, and pending uploads older than 24 hours.

### Share URL format

```
# Standard share URL — recipient opens in browser
https://host/?type=schedule&id={file_id}#key={hex_key}

# Auto-download — browser decrypts and downloads immediately on open
https://host/?type=schedule&id={file_id}&dl=1#key={hex_key}

# Delete URL — only the uploader has this
https://host/?type=schedule&id={file_id}&action=delete&dk={delete_key}
```

The decryption key is in the URL **fragment** (`#…`).  Fragments are never
transmitted to the server — they exist only in the browser.  The delete key
is a separate short token shown only to the uploader on the success screen.

### Limitations

The crypto api is only available in secure contexts: https and localhost. If
you attempt to use schedule using http, it will not work!

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

A collapsible **ICE servers** panel sits below the tab bar on the Files and Chat
tabs. STUN servers you add are appended to the default. TURN servers use the
same format as the CLI (`turn:host:port?transport=udp&secret=s`).
Settings persist in `localStorage` for 7 days.

---

## UI feature flags

The browser UI behaviour is controlled by environment variables served via
`GET /config.json`.  All default to the most permissive setting.

| Env var | Default | Description |
|---------|---------|-------------|
| `GMMFF_SHOW_FILES` | `true` | Show the Files tab |
| `GMMFF_SHOW_CHAT` | `true` | Show the Chat tab |
| `GMMFF_SHOW_SCHEDULE` | `false` | Show the Schedule tab |
| `GMMFF_SHOW_ICE_SETTINGS` | `true` | Show the ICE settings panel |
| `GMMFF_ALLOW_STUN` | `true` | Allow user modification of STUN servers |
| `GMMFF_ALLOW_TURN` | `true` | Allow user modification of TURN servers |
| `GMMFF_SHOW_SHARE_LINK` | `true` | Show copy-link / QR buttons on code screens |
| `GMMFF_SHOW_QR_CODE` | `true` | Show QR codes on code screens |
| `GMMFF_ALLOW_CUSTOM_SERVER` | `false` | Show the signaling server URL field |
| `GMMFF_SHOW_PEERS_LIMIT` | `true` | Show the max-peers slider |
| `GMMFF_MAX_PEERS_LIMIT` | `10` | Hard cap on the max-peers slider (2–10) |
| `GMMFF_MAX_WINDOW` | `2` | Transfer sliding window size (server-enforced, 1–16) |
| `GMMFF_MAX_CHUNK_SIZE` | `65526` | Transfer chunk size in bytes (server-enforced) |
| `GMMFF_ALLOWED_LANGS` | `all` | Comma-separated language codes, or `all`; single code hides the picker |
| `GMMFF_MOTD` | — | Message of the day shown as a banner at the top of the UI |
| `GMMFF_TAB_ORDER` | `files,chat,schedule` | Comma-separated display order of tabs; valid names: `files`, `chat`, `schedule` |
| `GMMFF_TAB_DEFAULT` | (first in `GMMFF_TAB_ORDER`) | Tab shown on page load; valid names: `files`, `chat`, `schedule` |

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

- **Browser extension** — use your favourite browser to send/receive files
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
