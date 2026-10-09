---
type: Documentation
title: Source Map
description: Mapping of wiki topics to source code locations for easy navigation.
verified:
  - by: openwiki/0.7.1
    at: 2026-10-09T14:54:52.045Z
sources:
  - id: openwiki-source-273db603bca41af9296b84d6
    resource: repo://internal/broker/broker.go
generated: { by: "openwiki/0.7.1", at: "2026-10-09T14:54:52.045Z" }
---
# Source Map

This file maps wiki topics to their primary source code locations in the gmmff repository.

## Architecture

<!-- openwiki: broken internal link [/openwiki/architecture/overview.md] link "/openwiki/architecture/overview.md" is root-absolute, which no real consumer resolves against the repository root (not a coding agent reading the page, not GitHub's Markdown renderer, not a local viewer); use a path relative to this file instead. Fix the href or restore the target, then delete this comment. -->
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

## Key Workflows

<!-- openwiki: broken internal link [/openwiki/workflows/key-workflows.md] link "/openwiki/workflows/key-workflows.md" is root-absolute, which no real consumer resolves against the repository root (not a coding agent reading the page, not GitHub's Markdown renderer, not a local viewer); use a path relative to this file instead. Fix the href or restore the target, then delete this comment. -->
- [Key Workflows](/openwiki/workflows/key-workflows.md)
  - File transfer: `/cmd/gmmff/create.go`, `/cmd/gmmff/join.go`, `/internal/transfer/`
  - Chat: `/cmd/gmmff/chat.go`, `/internal/chat/`
  - Local mode: `/cmd/gmmff/local.go`, `/internal/localmode/`
  - Schedule: `/cmd/gmmff/schedule.go`, `/internal/schedule/`

## Domain Concepts

<!-- openwiki: broken internal link [/openwiki/domain-concepts/overview.md] link "/openwiki/domain-concepts/overview.md" is root-absolute, which no real consumer resolves against the repository root (not a coding agent reading the page, not GitHub's Markdown renderer, not a local viewer); use a path relative to this file instead. Fix the href or restore the target, then delete this comment. -->
- [Domain Concepts Overview](/openwiki/domain-concepts/overview.md)
  - Session: `/internal/session/session.go`
  - Slot: `/internal/slot/slot.go`
  - PAKE: `/internal/pake/`
  - WebRTC Data Channel: `/internal/peer/`, `/internal/transfer/`
  - Slot State Machine: `/internal/slot/slot.go`

## Operations & Runbook

<!-- openwiki: broken internal link [/openwiki/operations/runbook.md] link "/openwiki/operations/runbook.md" is root-absolute, which no real consumer resolves against the repository root (not a coding agent reading the page, not GitHub's Markdown renderer, not a local viewer); use a path relative to this file instead. Fix the href or restore the target, then delete this comment. -->
- [Operations & Runbook](/openwiki/operations/runbook.md)
  - Deployment: `docker-compose.yml`, `Dockerfile`, `docs/SYSTEMD.md`, `docs/NGINX.md`
  - Configuration: `docs/ENV.md`, `docs/CMDS.md`, `internal/conf/`
  - Monitoring: `/healthz`, `/readyz`, `/metrics` endpoints (see `internal/broker/` and `internal/metrics/`)
  - Logging: `internal/log/`

## Testing Guidance

<!-- openwiki: broken internal link [/openwiki/testing/guidance.md] link "/openwiki/testing/guidance.md" is root-absolute, which no real consumer resolves against the repository root (not a coding agent reading the page, not GitHub's Markdown renderer, not a local viewer); use a path relative to this file instead. Fix the href or restore the target, then delete this comment. -->
- [Testing Guidance](/openwiki/testing/guidance.md)
  - Unit tests: `*_test.go` files throughout the codebase
  - Test plan: `docs/TEST-PLAN.md`
  - Makefile targets: `make test`, `make test-race`, `make test-cover`
  - Mocks: `internal/transfer/mockDataChannel.go`, `internal/mocks/` (if exists)

## Integration Points

<!-- openwiki: broken internal link [/openwiki/integrations/overview.md] link "/openwiki/integrations/overview.md" is root-absolute, which no real consumer resolves against the repository root (not a coding agent reading the page, not GitHub's Markdown renderer, not a local viewer); use a path relative to this file instead. Fix the href or restore the target, then delete this comment. -->
- [Integration Points](/openwiki/integrations/overview.md)
  - Redis/Valkey: `internal/store/redis.go`
  - WebRTC: Uses `github.com/pion/webrtc` (see `go.mod`)
  - STUN/TURN: `internal/turn/`
  - mDNS (local mode): `internal/localmode/`
  - PAKE: Uses `github.com/decred/dcrd/dcrec/secp256k1/v4` and `golang.org/x/crypto` (see `go.mod`)
  - Logging: Uses `github.com/charmbracelet/log` (see `internal/log/`)

## Key Source Files by Component

### Signaling Server
- `internal/signaling/b64.go` - Base64 helpers
- `internal/signaling/client_js.go` - WebSocket client for JS (WASM)
- `internal/signaling/client_native.go` - WebSocket client for native
- `internal/broker/broker.go` - WebSocket hub and main broker logic
- `internal/broker/server.go` - HTTP routes (healthz, readyz, metrics, etc.) and UI configuration

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

### Peer-to-Peer Transfer
- `internal/peer/peer.go` - Peer connection management
- `internal/transfer/sender.go` - File sending logic
- `internal/transfer/receiver.go` - File receiving logic
- `internal/transfer/datachannel.go` - Data channel wrapper
- `internal/chat/session.go` - Chat messaging over data channel

### Cryptography
- `internal/pake/pace.go` - CPace PAKE implementation
- `internal/pake/pake.go` - PAKE protocol wrapper
- `internal/crypto/crypto.go` - HKDF, HMAC, and key derivation
- `internal/protocol/` - Protocol message definitions and signing

### Utilities
- `internal/log/` - Privacy-preserving logger
- `internal/conf/` - Configuration validation
- `internal/metrics/` - Prometheus metrics
- `internal/err/` - Error types and wrapping
- `internal/archive/` - On-the-fly zip archiving for multi-file transfers

## Finding Related Code

To find where a specific concept is implemented:

1. **Session lifecycle**: Look in `internal/slot/slot.go` for state transitions and `internal/broker/broker.go` for slot operations.
2. **File transfer**: Trace from `cmd/gmmff/create.go` → `internal/session/session.go` → `internal/transfer/sender.go`/`receiver.go`.
3. **Chat**: See `internal/chat/session.go` and how it's used in `internal/session/session.go`.
4. **Local mode**: See `internal/localmode/` for embedded server and mDNS discovery.
5. **Schedule**: See `internal/schedule/` for server-side scheduled transfers.

## See Also

<!-- openwiki: broken internal link [/openwiki/architecture/overview.md] link "/openwiki/architecture/overview.md" is root-absolute, which no real consumer resolves against the repository root (not a coding agent reading the page, not GitHub's Markdown renderer, not a local viewer); use a path relative to this file instead. Fix the href or restore the target, then delete this comment. -->
- [Architecture Overview](/openwiki/architecture/overview.md) - High-level system design
<!-- openwiki: broken internal link [/openwiki/workflows/key-workflows.md] link "/openwiki/workflows/key-workflows.md" is root-absolute, which no real consumer resolves against the repository root (not a coding agent reading the page, not GitHub's Markdown renderer, not a local viewer); use a path relative to this file instead. Fix the href or restore the target, then delete this comment. -->
- [Key Workflows](/openwiki/workflows/key-workflows.md) - Step-by-step operational guides
<!-- openwiki: broken internal link [/openwiki/domain-concepts/overview.md] link "/openwiki/domain-concepts/overview.md" is root-absolute, which no real consumer resolves against the repository root (not a coding agent reading the page, not GitHub's Markdown renderer, not a local viewer); use a path relative to this file instead. Fix the href or restore the target, then delete this comment. -->
- [Domain Concepts](/openwiki/domain-concepts/overview.md) - Core abstractions and models
<!-- openwiki: broken internal link [/README.md] link "/README.md" is root-absolute, which no real consumer resolves against the repository root (not a coding agent reading the page, not GitHub's Markdown renderer, not a local viewer); use a path relative to this file instead. Fix the href or restore the target, then delete this comment. -->
- [README](/README.md) - Project overview and quick start
