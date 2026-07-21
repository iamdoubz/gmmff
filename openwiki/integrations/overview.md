---
type: Documentation
title: Integration Points
description: How gmmff integrates with external systems and services.
---
# Integration Points

## External Services

### Redis/Valkey
- **Purpose**: Distributed storage for slot state enabling horizontal scaling
- **Integration point**: `internal/store/redis.go`
- **Configuration**: `GMMFF_REDIS_URL` environment variable
- **Features used**:
  - Key-value storage with TTL (10-minute slot expiration)
  - Atomic operations for slot creation (slot UUID + code mapping)
  - Pub/Sub (not currently used but available for future extensions)
- **Fallback**: In-memory map store (`internal/store/memory.go`) for development

### WebRTC (via Pion)
- **Purpose**: Peer-to-peer data channel establishment
- **Integration**: `github.com/pion/webrtc/v2` (see `go.mod`)
- **Usage**:
  - Peer connection creation (`internal/peer/peer.go`)
  - Data channel creation for file transfer and chat (`internal/transfer/datachannel.go`, `internal/chat/chat.go`)
  - DTLS-SRTP encryption handled automatically by Pion
- **Configuration**: STUN/TURN servers via `GMMFF_STUN` and `GMMFF_TURN` environment variables

### STUN/TURN Servers
- **Purpose**: NAT traversal for WebRTC connections
- **Integration**: `internal/turn/`
- **STUN**: Session Traversal Utilities for NAT (discover public IP)
- **TURN**: Traversal Using Relays around NAT (relay traffic when direct connection fails)
- **Configuration**:
  - `GMMFF_STUN`: STUN server URLs (repeatable, default: Google STUN)
  - `GMMFF_TURN`: TURN server URLs with credentials (repeatable)
- **See**: [TURN documentation](docs/TURN.md)

### mDNS (Multicast DNS)
- **Purpose**: Local network peer discovery in local mode
- **Integration**: `internal/localmode/`
- **Usage**: `gmmff local` command uses mDNS to discover other `gmmff local` instances on the same network
- **Protocol**: DNS-based Service Discovery over Multicast DNS (RFC 6763)
- **Service type**: `_gmmff._tcp.local.`

### PAKE Library
- **Purpose**: Password Authenticated Key Exchange for secure secret verification
- **Integration**: `internal/pake/` (CPace implementation)
- **Dependencies**:
  - `golang.org/x/crypto` for cryptographic primitives
  - `github.com/decred/dcrd/dcrec/secp256k1/v4` for elliptic curve operations
- **Protocol**: CPace (Composable Password-authenticated Compact Protocol)
- **Security Properties**:
  - Mutual authentication
  - Key derivation
  - Resistance to offline dictionary attacks
  - Server obliviousness (signaling server cannot derive secret)

### WebSocket Client Libraries
- **Purpose**: Browser-based client communication
- **Integration**: 
  - `internal/signaling/client_js.go` - WebSocket wrapper for WASM
  - `internal/signaling/client_native.go` - WebSocket wrapper for native (uses `github.com/gorilla/websocket`)
- **Usage**: WASM webclient and native clients connect to signaling server via WebSocket

### Command Line Interface
- **Purpose**: User interaction and configuration
- **Library**: `github.com/spf13/cobra` (see `go.mod`)
- **Location**: `cmd/gmmff/`
- **Features**:
  - Hierarchical command structure (`create`, `join`, `chat`, `send`, `local`, `schedule`, `serve`)
  - Automatic help generation
  - Flag parsing with environment variable binding
  - Command validation

### WebAssembly/WASM
- **Purpose**: Browser-based file transfer client
- **Integration**: `internal/signaling/client_js.go` and `/web/` directory
- **Build**: `GOOS=js GOARCH=wasm go build -o web/gmmff.wasm ./internal/signaling/client_js.go`
- **Usage**: Served via HTTP endpoint (`/`) when server runs with webclient enabled
- **Documentation**: See [WASM documentation](docs/WASM.md)

### HTTP/Web Server
- **Purpose**: Landing page, health checks, metrics, and configuration endpoint
- **Integration**: `internal/broker/http.go`
- **Endpoints**:
  - `GET /` - Landing page (HTML)
  - `GET /healthz` - Liveness probe
  - `GET /readyz` - Readiness probe (includes Redis check)
  - `GET /metrics` - Prometheus metrics
  - `GET /config.json` - Non-sensitive configuration
  - `GET /ws` - WebSocket upgrade (signaling)
- **Middleware**: Security headers (CSP, X-Frame-Options, COOP, COEP)

### Configuration System
- **Purpose**: Centralized configuration management
- **Integration**: `internal/conf/`
- **Features**:
  - Environment variable parsing with `GMMFF_` prefix
  - Default values and validation
  - Byte size parsing (e.g., `10MB`, `1GB`)
  - Duration parsing (e.g., `1h30m`, `10s)
  - CIDR list parsing
- **Validation**: `ValidateEnv()` function called onfiguration System Integration**: `docs/SYSTEMD.md`Purpose**: `docs/SYSTEMD.md`
- **Service file**: `gmmff.service`
- **User**: Runs as dedipurpose**: Creates dedicated system user `gmmff`
- **Binary**: Installs binary to `/usr/local/bin/gmmff`
- **Configuration**: Uses `/etc/gmmff/.env` for environment variables
- **Logging**: Uses `journalctld` for logs
- **Redis socket**: Configures access to Redis Unix socket if used

### N
**Purpose**: Reverse proxy with TLS termiWebSocket proxying
- **Integration**: `docs/NGINX.md`
- **Features**:
  - TLS termination with Let's Encrypt support
  - WebSocket proxying (`proxy_pass` with `proxy_http_version 1.1`)
  - Security headers (HSTS, CSP, etc.)
  - Rate limiting
  - Logging and access control
- **Configuration**: 
  - Listens on ports 80/443
  - Proxies WebSocket connections to `http://localhost:8080/ws`
  - Serves static assets for landing page
  - Health check endpoints

### Schedule Mode
- **Purpose**: Encrypted server-side scheduled transfers
- **Integration**: `internal/schedule/`
- **Features**:
  - AES-256-GCM encryption for data at rest
  - HTTP-based API for upload/download/delete
  - Password-protected uploads (key derivation via scrypt)
  - TTL-based expiration
  - Chunked transfer support
- **Endpoints**:
  - `POST /api/schedule/upload` - Initiate upload
  - `GET /api/schedule/:id/meta` - Fetch metadata
  - `GET /api/schedule/:id/download` - Download file
  - `DELETE /api/schedule/:id/delete` - Delete file
- **See**: [Schedule documentation](docs/SCHEDULE.md)

### Docker
- **Purpose**: Containerized deployment
- **Integration**: `Dockerfile` and `docker-compose.yml`
- **Image**: Based on `golang:alpine` for build, `alpine` for runtime
- **Ports**: Exposes 8080 (WebSocket/HTTP)
- **Environment**: Uses `.env` file for configuration
- **Volumes**: Persists Redis data if using external Redis
- **Healthcheck**: `/healthz` endpoint

### Cryptographic Libraries
- **Purpose**: Low-level cryptographic primitives
- **Dependencies** (see `go.mod`):
  - `golang.org/x/crypto` - HKDF, HMAC, scrypt, etc.
  - `github.com/decred/dcrd/dcrec/secp256k1/v4` - Elliptic curve operations (for PAKE)
- **Usage**:
  - PAKE: CPace protocol implementation
  - Key derivation: HKDF for generating symmetric keys from PAKE secret
  - Message authentication: HMAC-SHA256 for SDP signing
  - Password hashing: scrypt for schedule mode password protection

## Internal Abstractions

### Storage Interface
- **Purpose**: Abstract storage backend for slot state
- **Interface**: `internal/store/store.go`
- **Implementations**:
  - `memory.go` - In-memory map (development)
  - `redis.go` - Redis/Valkey (production)
- **Methods**: `Create`, `Get`, `Update`, `Delete`, `DeleteByCode`, `List`

### Logger
- **Purpose**: Privacy-preserving structured logging
- **Implementation**: `internal/log/`
- **Based on**: `github.com/charmbracelet/log`
- **Features**:
  - Structured JSON output
  - Field redaction for sensitive data
  - Component-based logging (`broker`, `store`, `main`)
  - No PII in logs (IPs, user agents, file names, etc. are stripped)

### Metrics
- **Purpose**: Prometheus-compatible metrics endpoint
- **Implementation**: `internal/metrics/`
- **Exported metrics**:
  - Connection counts
  - Slot state distributions
  - Request counters
  - Bytes transferred (signaling only)
  - Error counts
- **Endpoint**: `GET /metrics`

### Error Handling
- **Purpose**: Consistent error wrapping and context
- **Implementation**: `internal/err/`
- **Features**:
  - Error types with codes
  - Context wrapping (`WithContext`)
  - Error unwrapping
  - Standardized error codes for CLI/user display

## Development Dependencies

### Testing
- **Framework**: Go's built-in `testing` package
- **Mocking**: Hand-crafted mocks (see `internal/transfer/mockDataChannel.go`)
- **Coverage**: `go tool cover` and `make test-cover`
- **Integration tests**: Planned for Tier 8e (see `docs/TEST-PLAN.md`)

### CI/CD
- **GitHub Actions**: `.github/workflows/`
  - `docker.yml` - Docker image build and push
  - `vuln.yml` - Vulnerability scanning (govulncheck)
  - `test.yml` - Test execution (implied from repository structure)
- **Release automation**: `goreleaser` (see `.goreleaser.yaml`)

## Extending gmmff

### Adding New Storage Backends
1. Implement the `Store` interface in `internal/store/store.go`
2. Add constructor in `internal/store/` (e.g., `newMyStore`)
3. Update store factory in `internal/store/store.go` to select based on config
4. Add configuration validation in `internal/conf/`

### Adding New CLI Commands
1. Create new file in `cmd/gmmff/` (e.g., `newcommand.go`)
2. Implement `cobra.Command` with appropriate flags and run function
3. Register command in `cmd/gmmff/main.go` `init()` function
4. Add documentation in `docs/` directory
5. Add unit tests in corresponding `*_test.go` file

### Adding New Storage Backends (Advanced)
1. Implement the `Store` interface in `internal/store/store.go`
2. Add constructor in `internal/store/` (e.g., `newMyStore`)
3. Update store factory in `internal/store/store.go` to select based on config
4. Add configuration validation in `internal/conf/`

### WebRTC Data Channels
1. For new data channel types, modify `internal/peer/peer.go` to create additional channels
2. Implement handlers in relevant packages (e.g., new chat features in `internal/chat/`)
3. Ensure proper cleanup in session close handling

## See Also

- [Architecture Overview](/openwiki/architecture/overview.md) - System components and deployment
- [Key Workflows](/openwiki/workflows/key-workflows.md) - Step-by-step operational guides
- [Domain Concepts](/openwiki/domain-concepts/overview.md) - Core abstractions and models
- [Source Map](/openwiki/source-map.md) - Direct mapping of concepts to source files
- [go.mod] - Complete dependency list
- [docs/] - Detailed documentation for specific features