---
type: Overview
title: Integrations
description: How gmmff integrates with external systems including TURN servers, web clients, storage backends, and feature flags.
tags: [integrations, TURN, web, storage, configuration]
verified:
  - by: openwiki/0.6.0
    at: 2026-09-25T13:12:29.362Z
sources:
  - id: openwiki-source-4a81fcd95533ed8ba5a77739
    resource: repo://internal/store/store.go
  - id: openwiki-source-2588ae43c486537fdca4a70b
    resource: repo://internal/turn/turn.go
  - id: openwiki-source-9ecbcb3b47691105152b258f
    resource: repo://web/server.go
generated: { by: "openwiki/0.6.0", at: "2026-09-25T13:12:29.362Z" }
---

# Integrations

## TURN Server Configuration and Usage

gmmff supports TURN (Traversal Using Relays around NAT) servers for WebRTC peer-to-peer connections when direct connections fail due to NAT traversal issues.

### Configuration
TURN servers are configured via the `GMMFF_TURN` environment variable, which accepts a comma-separated list of TURN URLs. Each URL follows the format:
- `turn:host:port[?transport=udp|tcp&user=username&pass=password]` for long-term credentials
- `turn:host:port[?transport=udp|tcp&secret=static-auth-secret&user=username]` for ephemeral credentials (RFC 8489)

Example:
```bash
GMMFF_TURN="turn:turn.example.com:3478?user=gmmff&pass=secret,turns:turns.example.com:5349?transport=tcp&secret=mysecret&user=gmmff"
```

### Implementation
The TURN parsing logic resides in `internal/turn/turn.go`, which:
- Supports up to 3 TURN servers (MaxServers constant)
- Parses both long-term and ephemeral credential types
- For ephemeral auth, derives HMAC-SHA1 credentials per RFC 8489 §9.2 with 24-hour TTL
- Converts parsed TURN servers to `github.com/pion/webrtc/v4.ICEServer` instances for WebRTC peer connections

### Usage
TURN servers are used during WebRTC peer connection establishment in `internal/peer/peer.go`. When creating a WebRTC PeerConnection, the parsed TURN servers are provided in the ICEServer configuration, enabling Pion to attempt relayed connections when STUN and direct UDP/TCP connections fail.

## Web Client Build and Serving

gmmff provides a browser-based client compiled to WebAssembly (WASM) for file transfers directly from the browser.

### Build Process
The web client is built from `internal/signaling/client_js.go`:
```bash
GOOS=js GOARCH=wasm go build -o web/gmmff.wasm ./internal/signaling/client_js.go
```
This produces a WebAssembly module that runs in the browser.

### Serving
In development, the web client is served by `web/server.go`, which:
- Serves static files from `web/static/` directory
- Sets required headers for SharedArrayBuffer support:
  - `Cross-Origin-Opener-Policy: same-origin`
  - `Cross-Origin-Embedder-Policy: require-corp`
- Serves the WASM module with correct MIME type (`application/wasm`)

In production, the static files under `web/static/` should be served by nginx or any static file host (S3, Cloudflare Pages, etc.). The WASM module is loaded by the HTML/JavaScript frontend and communicates with the signaling server via WebSocket.

### Communication
The web client uses the WebSocket wrapper in `internal/signaling/client_js.go` to connect to the signaling server at the `/ws` endpoint, enabling real-time coordination for WebRTC peer-to-peer file transfers.

## Redis/Valkey Integration

gmmff uses Redis/Valkey for distributed slot state storage, enabling horizontal scaling of signaling servers.

### Configuration
Redis connection is configured via the `GMMFF_REDIS_URL` environment variable (standard Redis URL format). When this variable is not set, gmmff falls back to an in-memory store.

### Storage Layout
As implemented in `internal/store/store.go`:
- `slot:<slot_id>`: Hash storing Slot JSON fields with 10-minute TTL
- `code:<code>`: String storing slot_id for lookup by short code with 10-minute TTL
Both keys share the same TTL for atomic expiry from the user's perspective.

### Operations
The Store interface (`internal/store/store.go`) provides:
- `Create`: Persists new slot and code→slot_id index via Redis pipeline (atomic)
- `Get`: Retrieves slot by ID
- `GetByCode`: Retrieves slot by short code
- `Update`: Overwrites slot JSON and refreshes TTL
- `Delete`: Removes both slot and code keys
- `List`: Returns all active slots (admin use only)

### Fallback
When Redis is not configured, gmmff uses an in-memory map store (`internal/store/memory.go`) for development and testing. This store implements the same Store interface but persists data only in process memory.

## Feature Flags and Configuration

gmmff includes several feature flags and configuration options that modify behavior.

### Memory Store
When `GMMFF_REDIS_URL` is not set, gmmff automatically uses the in-memory store. This flag enables:
- Development mode without external dependencies
- Single-process deployment
- Non-persistent slot state (lost on restart)

### CSP Report-Only
The HTTP server includes Content Security Policy headers that can operate in report-only mode for testing. Configuration is managed through the centralized configuration system in `internal/conf/`.

### Configuration System
Centralized configuration management resides in `internal/conf/` with features:
- Environment variable parsing with `GMMFF_` prefix
- Default values and validation
- Byte size parsing (e.g., `10MB`, `1GB`)
- Duration parsing (e.g., `1h30m`, `10s`)
- CIDR list parsing
- The `ValidateEnv()` function validates all configuration on startup

### WebSocket Proxying
For production deployments behind reverse proxies, WebSocket proxying requires specific configuration (see `docs/NGINX.md`):
- `proxy_pass` with `proxy_http_version 1.1`
- `proxy_set_header Upgrade $http_upgrade;`
- `proxy_set_header Connection "upgrade";`

## Related Integrations

### mDNS (Multicast DNS)
Used in local mode (`gmmff local`) for peer discovery on the same network via `_gmmff._tcp.local.` service type (implemented in `internal/localmode/mdns.go`).

### WebSocket Client Libraries
- WASM clients: `internal/signaling/client_js.go`
- Native clients: `internal/signaling/client_native.go` (uses `github.com/gorilla/websocket`)

### HTTP/Web Server
Implemented in `internal/broker/server.go` with endpoints:
- `GET /` - Landing page
- `GET /healthz` - Liveness probe
- `GET /readyz` - Readiness probe (includes Redis check)
- `GET /metrics` - Prometheus metrics
- `GET /config.json` - Non-sensitive configuration
- `GET /ws` - WebSocket upgrade (signaling)

### STUN Servers
Configured via `GMMFF_STUN` environment variable (repeatable, default: Google STUN) for NAT discovery in WebRTC connections.
