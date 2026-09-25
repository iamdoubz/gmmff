---
type: Documentation
title: Source Map
description: A map of the gmmff repository structure, highlighting the responsibilities of each directory and package.
tags: [source, structure, reference]
verified:
  - by: openwiki/0.6.0
    at: 2026-09-25T13:12:29.362Z
sources:
  - id: openwiki-source-3c5dff77bae4df4110d95849
    resource: repo://cmd/gmmff/main.go
  - id: openwiki-source-be280cbfa07439808aa8c357
    resource: repo://configs/gmmff.conf
  - id: openwiki-source-2e94f5fb7613b123957d6f38
    resource: repo://docs/ENV.md
  - id: openwiki-source-17506c01deef3bc65f2fb2fc
    resource: repo://internal/archive/archive.go
  - id: openwiki-source-273db603bca41af9296b84d6
    resource: repo://internal/broker/broker.go
  - id: openwiki-source-4582c6a89368e5cde22f6f64
    resource: repo://internal/chat/session.go
  - id: openwiki-source-f829cd39bef5d058ebc5b7f7
    resource: repo://internal/crypto/codegen.go
  - id: openwiki-source-400a30650b0992157b91a808
    resource: repo://internal/display/format.go
  - id: openwiki-source-f57e1ae06340d7c7821d0b3e
    resource: repo://internal/localmode/local.go
  - id: openwiki-source-b3b67094b286e55952c7bbfa
    resource: repo://internal/log/log.go
  - id: openwiki-source-d32161ee45da410429870c3e
    resource: repo://internal/pake/session.go
  - id: openwiki-source-4b847332166285c0b52606b7
    resource: repo://internal/peer/peer.go
  - id: openwiki-source-9a48c23d60df6b6493b6e3ad
    resource: repo://internal/peerconfig/peerconfig.go
  - id: openwiki-source-0a096f13c8843e70690e1e41
    resource: repo://internal/schedule/handler.go
  - id: openwiki-source-c3138a2e20a1bb95abc1c522
    resource: repo://internal/session/session.go
  - id: openwiki-source-bad4c127fd033aaeea5409b9
    resource: repo://internal/signaling/b64.go
  - id: openwiki-source-ce826a3573a98651b26c85cd
    resource: repo://internal/slot/slot.go
  - id: openwiki-source-4a81fcd95533ed8ba5a77739
    resource: repo://internal/store/store.go
  - id: openwiki-source-9a9417680bcbb0e418af0de0
    resource: repo://internal/transfer/transfer.go
  - id: openwiki-source-2588ae43c486537fdca4a70b
    resource: repo://internal/turn/turn.go
  - id: openwiki-source-9ecbcb3b47691105152b258f
    resource: repo://web/server.go
generated: { by: "openwiki/0.6.0", at: "2026-09-25T13:12:29.362Z" }
---
# Source Map

This document maps the gmmff repository structure to the responsible systems and components.

## Top-Level Directories

### `/cmd`
Contains application entrypoints and command-line interfaces.
- `/cmd/gmmff`: Main gmmff application
  - `main.go`: Root command and server entrypoint
  - `create.go`: `gmmff create` command (initiates file transfer)
  - `join.go`: `gmmff join` command (joins an existing transfer)
  - `chat.go`: `gmmff chat` command (starts chat session)
  - `send.go`: `gmmff send` command (sends files in existing session)
  - `local.go`: `gmmff local` command (runs in local mode)
  - `schedule.go`: `gmmff schedule` command (manages scheduled transfers)
  - `cleanup.go`: Cleanup utilities for expired slots

### `/configs`
Configuration examples and templates for deployment.
- `.env.example`: Example environment variables
- `gmmff.conf`: Example configuration file
- `gmmff.service`: Example systemd service unit
- `portainer.yml`: Portainer stack configuration
- `stack.env`: Environment file for Portainer deployments

### `/docs`
User-facing documentation and guides.
- `ENV.md`: Environment variable reference
- `CMDS.md`: Command-line interface reference
- `SYSTEMD.md`: Systemd deployment instructions
- `NGINX.md`: Nginx reverse proxy configuration
- `TEST-PLAN.md`: Testing strategy and test cases
- Other guides covering installation, usage, and troubleshooting

### `/internal`
Private application logic and library code, organized by concern.

#### `/internal/broker`
WebSocket signaling broker responsible for connection management and message routing.
- `broker.go`: Main broker logic
- `hub.go`: WebSocket hub managing connections and slots
- `http.go`: HTTP routes (health checks, metrics, WebSocket upgrade)
- **Responsibilities**: Accept WebSocket connections, route protocol messages between peers, enforce slot lifecycle, forward payloads opaquely without decoding

#### `/internal/chat`
Chat messaging over WebRTC data channels.
- `session.go`: Chat session management (note: the package uses session.go for chat state)
- **Responsibilities**: Enable peer-to-peer text chat via data channels, handle message serialization

#### `/internal/crypto`
Cryptographic primitives used for key derivation and authentication.
- `codegen.go`: Slot code generation and HKDF/HMAC utilities
- **Responsibilities**: Provide secure cryptographic operations for PAKE and session encryption

#### `/internal/display`
Display utilities for user interface elements.
- `format.go`: Terminal formatting and QR code generation
- **Responsibilities**: Generate QR codes, format terminal output, handle visual presentation

#### `/internal/localmode`
Local mode implementation using mDNS discovery and embedded server.
- `local.go`: Embedded signaling server and mDNS integration
- `mdns.go`: mDNS discovery logic
- `embed.go`: Embedding static assets
- `tls.go`: TLS configuration for local mode
- **Responsibilities**: Enable peer discovery on local network via mDNS, run embedded signaling server for offline use

#### `/internal/log`
Privacy-preserving structured logger.
- `log.go`: Logger implementation
- **Responsibilities**: Log application events without leaking sensitive information, configurable log levels and formats

#### `/internal/pake`
CPace Password-Authenticated Key Exchange (PAKE) implementation.
- `pake.go`: PAKE protocol wrapper
- `pace.go`: Low-level CPace primitives
- **Responsibilities**: Establish shared secrets between peers using passwords, resistant to dictionary attacks

#### `/internal/peer`
Peer connection management for WebRTC data channels.
- `peer.go`: Peer connection lifecycle and configuration
- **Responsibilities**: Create and manage WebRTC peer connections, handle ICE/DTLS negotiation, configure data channels

#### `/internal/peerconfig`
Peer configuration utilities.
- `peerconfig.go`: WebRTC configuration (ICEServers, constraints) for peer connections
- **Responsibilities**: Manage WebRTC configuration (ICE servers, constraints) for peer connections

#### `/internal/schedule`
Server-side scheduled transfer management.
- `schedule.go`: Scheduled transfer logic and storage
- **Responsibilities**: Store and trigger scheduled transfers, handle cron-like scheduling logic

#### `/internal/session`
Session REPL and user interaction layer.
- `session.go`: Session state and command processing
- **Responsibilities**: Manage interactive session state, process user commands (chat, send, schedule), coordinate with slot and transfer layers

#### `/internal/signaling`
WebSocket signaling helpers and client implementations.
- `b64.go`: Base64 encoding/decoding helpers
- `client_js.go`: WebSocket client for JavaScript/WASM
- `client_native.go`: WebSocket client for native environments
- **Responsibilities**: Provide WebSocket signaling helpers and client implementations for both JavaScript/WASM and native environments

#### `/internal/slot`
Slot state machine and metadata storage.
- `slot.go`: Slot struct and state transitions (created → waiting → ready → closed)
- **Responsibilities**: Manage the slot state machine, store slot metadata, coordinate with broker and store

#### `/internal/store`
Storage abstraction layer for persisting slot and session data.
- `store.go`: Storage interface
- `memory.go`: In-memory store (development)
- `redis.go`: Redis/Valkey store
- **Responsibilities**: Provide a storage abstraction layer with in-memory and Redis/Valkey implementations

#### `/internal/archive`
Archive (zip) creation and extraction for file transfers.
- `archive.go`: Archive creation and extraction logic
- **Responsibilities**: Handle archive (zip) creation and extraction for file transfers

#### `/internal/transfer`
File transfer logic over WebRTC data channels.
- `sender.go`: File sending logic (chunking, encryption, transmission)
- `receiver.go`: File receiving logic (reassembly, decryption, validation)
- `datachannel.go`: Data channel wrapper for reliable transfer
- **Responsibilities**: Implement file transfer logic over WebRTC data channels, including chunking, encryption, transmission, and reassembly

#### `/internal/turn`
TURN relay traversal for establishing relay connections.
- `turn.go`: TURN client implementation
- **Responsibilities**: Manage TURN relay traversal for establishing relay connections when direct peer-to-peer fails

### `/web`
Web client and server components.
- `/web/cmd`: Web server entrypoint
  - `server.go`: HTTP server for web client
- `/web/static`: Static assets (HTML, CSS, JavaScript) for the web interface
- **Responsibilities**: Serve static assets and provide an HTTP API for browser-based file transfer

### Other Top-Level Files and Directories
- `pkg`: Public Go packages (if any)
- `README.md`: Project overview and quick start
- `LICENSE`: License text
- `.gitignore`: Git ignore patterns
- `go.mod`: Go module definition
- `go.sum`: Dependency checksums
- `Makefile`: Build automation and common tasks
- `Dockerfile`: Container image definition
- `docker-compose.yml`: Multi-container orchestration
- `.goreleaser.yaml`: GoReleaser configuration for releases

## Finding Related Code

To find where a specific concept is implemented:

1. **Session lifecycle**: Look in `internal/slot/slot.go` for state transitions and `internal/broker/hub.go` for slot operations.
2. **File transfer**: Trace from `cmd/gmmff/create.go` → `internal/session/session.go` → `internal/transfer/sender.go`/`receiver.go`.
3. **Chat**: See `internal/chat/session.go` and how it's used in `internal/session/session.go`.
4. **Local mode**: See `internal/localmode/` for embedded server and mDNS discovery.
5. **Schedule**: See `internal/schedule/` for server-side scheduled transfers.
