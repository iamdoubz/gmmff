---
type: Documentation
title: Source Map
description: Mapping of wiki topics to source code locations for easy navigation.
verified:
  - by: openwiki/0.5.0
    at: 2026-09-05T11:33:48.919Z
sources:
  - id: openwiki-source-17506c01deef3bc65f2fb2fc
    resource: repo://internal/archive/archive.go
generated: { by: "openwiki/0.5.0", at: "2026-09-05T11:33:48.919Z" }
---
# Source Map

This file maps wiki topics to their primary source code locations in the gmmff repository.

## Architecture

- [Architecture Overview](/openwiki/architecture/overview.md)
  - `/internal/signaling/` - WebSocket signaling server
  - `/internal/slot/` - Slot management and state machine
  - `/internal/store/` - Storage layer (Redis/memory)
  - `/internal/broker/` - WebSocket hub and connection handling
  - `/cmd/gmmff/` - CLI entrypoint and command implementations
  - `/internal/peer/` - Peer connection management (WebRTC)
  - `/internal/transfer/` - File transfer over data channels
  - `/internal/pake/` - CPace PAKE implementation
  - `/internal/crypto/` - Cryptographic primitives (HKDF, HMAC)
  - `/internal/session/` - Session REPL and user interaction
  - `/internal/archive/` - On-the-fly zip archiving for multi-file transfers
  - `/internal/display/` - Output formatting and display utilities
  - `/internal/peerconfig/` - Peer configuration management
  - `/internal/turn/` - TURN server integration for peer-to-peer connectivity

## Key Workflows

- [Key Workflows](/openwiki/workflows/key-workflows.md)
  - File transfer: `/cmd/gmmff/create.go`, `/cmd/gmmff/join.go`, `/internal/transfer/`, `/internal/archive/`
  - Chat: `/cmd/gmmff/chat.go`, `/internal/chat/`
  - Local mode: `/cmd/gmmff/local.go`, `/internal/localmode/`
  - Schedule: `/cmd/gmmff/schedule.go`, `/internal/schedule/`

## Domain Concepts

- [Domain Concepts Overview](/openwiki/domain-concepts/overview.md)
  - Session: `/internal/session/session.go`
  - Slot: `/internal/slot/slot.go`
  - PAKE: `/internal/pake/`
  - WebRTC Data Channel: `/internal/peer/`, `/internal/transfer/`
  - Slot State Machine: `/internal/slot/slot.go`
  - Archive: `/internal/archive/archive.go`
  - Display: `/internal/display/format.go`
  - Peerconfig: `/internal/peerconfig/peerconfig.go`
  - Turn: `/internal/turn/turn.go`

## Operations & Runbook

- [Operations & Runbook](/openwiki/operations/runbook.md)
  - Deployment: `docker-compose.yml`, `Dockerfile`, `docs/SYSTEMD.md`, `docs/NGINX.md`
  - Configuration: `docs/ENV.md`, `docs/CMDS.md`
  - Monitoring: `/healthz`, `/readyz`, `/metrics` endpoints (see `internal/broker/` and `internal/metrics/`)
  - Logging: `internal/log/`

## Testing Guidance

- [Testing Guidance](/openwiki/testing/guidance.md)
  - Unit tests: `*_test.go` files throughout the codebase
  - Test plan: `docs/TEST-PLAN.md`
  - Makefile targets: `make test`, `make test-race`, `make test-cover`
  - Mocks: `internal/transfer/mockDataChannel.go`, `internal/mocks/` (if exists)

## Integration Points

- [Integration Points](/openwiki/integrations/overview.md)
  - Redis/Valkey: `internal/store/redis.go`
  - WebRTC: Uses `github.com/pion/webrtc` (see `go.mod`)
  - STUN/TURN: `internal/turn/`
  - mDNS (local mode): `internal/localmode/`
  - PAKE: Uses `github.com/decred/dcrd/dcrec/secp256k1/v4` and `golang.org/x/crypto` (see `go.mod`)
  - Logging: Uses `github.com/charmbracelet/log` (see `internal/log/`)
  - Archive: Uses standard library `archive/zip` (no external dependency)

## Key Source Files by Component

### Signaling Server
- `internal/signaling/b64.go` - Base64 helpers
- `internal/signaling/client_js.go` - WebSocket client for JS (WASM)
- `internal/signaling/client_native.go` - WebSocket client for native
- `internal/broker/hub.go` - WebSocket hub (connection management)
- `internal/broker/http.go` - HTTP routes (healthz, readyz, metrics, etc.)
- `internal/broker/broker.go` - Main broker logic

### Slot & Storage
- `internal/slot/slot.go` - Slot struct and state transitions
- `internal/store/store.go` - Storage interface
- `internal/store/memory.go` - In-memory store (dev)
- `internal/store/redis.go` - Redis/Valkey store

### CLI Commands
- `cmd/gmmff/main.go` - Root command and serve subcommand
- `cmd/gmmff/create.go` - `gmmff create`
- `cmd/gmmff/join.go` - `gmmff join`
- `cmd/gmmff/chat.go` - `gmmff chat`
- `cmd/gmmff/send.go` - `gmmff send`
- `cmd/gmmff/local.go` - `gmmff local`
- `cmd/gmmff/schedule.go` - `gmmff schedule`
- `cmd/gmmff/cleanup.go` - `gmmff cleanup`

### Peer-to-Peer Transfer
- `internal/peer/peer.go` - Peer connection management
- `internal/transfer/sender.go` - File sending logic
- `internal/transfer/receiver.go` - File receiving logic
- `internal/transfer/datachannel.go` - Data channel wrapper
- `internal/chat/chat.go` - Chat messaging over data channel

### Cryptography
- `internal/pake/pace.go` - CPace PAKE implementation
- `internal/crypto/hkdf.go` - HKDF implementation
- `internal/crypto/hmac.go` - HMAC implementation

### Archive
- `internal/archive/archive.go` - On-the-fly zip archiving for multi-file transfers
- `internal/archive/archive_test.go` - Tests for archive package

### Display
- `internal/display/format.go` - Formatting utilities for CLI output
- `internal/display/format_test.go` - Tests for display package

### Peerconfig
- `internal/peerconfig/peerconfig.go` - Peer configuration management

### Turn
- `internal/turn/turn.go` - TURN client implementation
- `internal/turn/turn_test.go` - Tests for TURN client
