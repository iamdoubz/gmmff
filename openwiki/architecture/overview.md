---
type: Architecture
title: gmmff Architecture Overview
description: High-level architecture of the gmmff peer-to-peer file transfer system, including signaling server, WebRTC data channels, security model, and client options.
tags: [architecture, webrtc, pake, signaling]
verified:
  - by: openwiki/0.5.0
    at: 2026-09-05T11:33:48.919Z
sources:
  - id: openwiki-source-a2371d6362e5db4bc834ad03
    resource: repo://CLAUDE.md
  - id: openwiki-source-3c5dff77bae4df4110d95849
    resource: repo://cmd/gmmff/main.go
  - id: openwiki-source-e8e61d605125cac4d909755e
    resource: repo://docs/ARCHITECTURE.md
generated: { by: "openwiki/0.5.0", at: "2026-09-05T11:33:48.919Z" }
---
# gmmff Architecture Overview

## Overview

gmmff (pronounced "gimph") is a peer-to-peer file and message transfer system with a scheduled-delivery ("Schedule") feature. The core peer-to-peer transfer consists of two main components:

1. **Signaling Server** - A WebSocket broker that brokers initial connections between peers
2. **Clients** - Handle actual file/message transfer over encrypted WebRTC data channels

The signaling server never sees file contents—once peers connect, all data flows directly between them over encrypted WebRTC data channels.

## System Components

### Signaling Server (`internal/signaling/`)

The signaling server is a stateless (from the perspective of peer data), horizontally-scalable WebSocket broker responsible for **rendezvous**: given two peers who share a secret code, link them so they can exchange the messages needed to establish a direct WebRTC connection.

Key components:
- **Hub goroutine**: Central coordinator that owns connection map and slot dispatch, communicates via channels (no shared memory)
- **Connection handlers**: Each WebSocket connection has readPump, writePump goroutines
- **Slot management**: Manages the lifecycle of connection slots (WAITING → READY → CLOSED)
- **Redis/Valkey storage**: Persists slot state with TTL (10 minutes) for horizontal scaling
- **Memory store fallback**: In-memory map for development (no TTL, single-node only)
- **Schedule handling**: Optional scheduled-delivery feature via HTTP endpoints (when enabled)

### Clients

The system provides two client options for peer-to-peer transfer:

#### Native CLI (`cmd/gmmff/`)
- Handles file/message transfer sessions (`create`, `join`, `chat`)
- Implements PAKE authentication (CPace) for mutual authentication
- Manages WebRTC connection setup (SDP offer/answer, ICE candidates)
- Drives encrypted data transfer over WebRTC data channels (DTLS 1.3+ SCTP)
- Provides progress reporting, integrity checking, and file streaming
- Supports schedule commands when server feature is enabled

#### Browser Client (`web/cmd/gmmff-wasm/`)
- Go-compiled to WebAssembly, runs in browser
- Provides identical peer-to-peer transfer functionality as native CLI
- Uses same WebRTC/PAE stack and security guarantees
- Accessed via HTTP endpoint served by signaling server
- Shares core session model with native client via Go/Wasm bridge

### Cryptography & Security

1. **Password Authenticated Key Exchange (PAKE)** - CPace protocol establishes a shared secret between peers without revealing the password to the server
2. **SDP HMAC signing** - Session Description Protocol messages are HMAC-signed with a key derived from the PAKE secret via HKDF to prevent man-in-the-middle attacks
3. **WebRTC/DTLS encryption** - All data channels are encrypted with DTLS 1.3 over SCTP
4. **Privacy-preserving logging** - Logs contain no IPs, user agents, file names, or transfer contents
5. **Memory exhaustion protection** - 64 KiB max message size, 16-frame send buffer per connection
6. **Slow-read protection** - 10s write timeout, 60s pong timeout per WebSocket connection

## Data Flow

1. **Peer A** runs `gmmff create` → generates UUID + 3-word code, stores in Redis with 10-min TTL
2. **Peer A** shares code out-of-band with **Peer B**
3. **Peer B** runs `gmmff join <code>` → resolves code → slot UUID
4. **CPace PAKE** authenticates both peers → shared secret established
5. **SDP exchange** (offer/answer) → HMAC-signed with HKDF-derived key from PAKE secret
6. **ICE candidate exchange** → establishes direct connection
7. **WebRTC data channel opens** (DTLS 1.3+ SCTP) → signaling server's job is done
8. **Peers enter session REPL** → exchange files/messages directly over encrypted channel

## Slot Lifecycle

```text
slot.create
    │
    ▼
┌─────────┐      slot.join        ┌───────┐     bye / expire
│ WAITING │ ──────────────────► │ READY │ ──────────────────► CLOSED
└─────────┘                     └───────┘
    │
    │ TTL expires (no join)
    ▼
 CLOSED (auto-reaped by Redis TTL)
```

State transitions are validated in `internal/slot/slot.go` before any store write, ensuring the broker never persists invalid state.

## Deployment Options

1. **Docker Compose** - See `docker-compose.yml`
2. **Local Go + Redis/Valkey** - Requires Go 1.23+ and Redis/Valkey 7+
   - Development: `go run ./cmd/gmmff serve --memory`
   - Production: `go run ./cmd/gmmff serve` (with `GMMFF_REDIS_URL` set)
3. **Systemd** - See `docs/SYSTEMD.md`
4. **NGINX reverse proxy** - See `docs/NGINX.md`

## Key Source Files

- Signaling server: `/internal/signaling/`
- Slot management: `/internal/slot/`
- Storage layer: `/internal/store/`
- CLI commands: `/cmd/gmmff/`
- Web browser client: `/web/cmd/gmmff-wasm/`
- WebRTC/P2P logic: `/internal/peer/` and `/internal/transfer/`
- Cryptography: `/internal/pake/` and `/internal/crypto/`
- Schedule feature: `/internal/schedule/` (optional)
