---
type: Operations
title: Operations & Runbook
description: Deployment, configuration, monitoring, and maintenance procedures for gmmff.
verified:
  - by: openwiki/0.5.2
    at: 2026-09-18T12:39:02.785Z
sources:
  - id: openwiki-source-3b59060d90820d6a392f85ff
    resource: repo://internal/broker/server.go
generated: { by: "openwiki/0.5.2", at: "2026-09-18T12:39:02.785Z" }
---
# Operations & Runbook

## Deployment

### Docker Compose (Development/Testing)

```bash
git clone https://github.com/iamdoubz/gmmff
cd gmmff
cp configs/.env.example configs/.env
# Edit configs/.env as needed (e.g., set GMMFF_REDIS_URL if using external Redis)
docker compose up -d
```

The server will be available at `ws://localhost:8080/ws`.

### Local Development (Go + Redis/Valkey)

Prerequisites:
- Go 1.23+
- Redis 7+ or Valkey 7.2+ (or use `--memory` flag for in-memory store)

```bash
# Start Redis (or valkey-server)
redis-server

# Run with in-memory store (no Redis/Valkey needed for dev)
go run ./cmd/gmmff serve --memory --log-pretty --log-level debug

# Or with Redis/Valkey (set GMMFF_REDIS_URL; valkey:// is accepted)
go run ./cmd/gmmff serve --log-pretty --log-level debug
```

> **Note:** The `--memory` flag is for development only and does not persist data. For production, use Redis/Valkey.

### Production Deployment

#### Systemd Service
<!-- openwiki: broken internal link [docs/SYSTEMD.md] file "docs/SYSTEMD.md" does not exist. Fix the href or restore the target, then delete this comment. -->
See [docs/SYSTEMD.md](docs/SYSTEMD.md) for detailed instructions.

#### NGINX Reverse Proxy
<!-- openwiki: broken internal link [docs/NGINX.md] file "docs/NGINX.md" does not exist. Fix the href or restore the target, then delete this comment. -->
See [docs/NGINX.md](docs/NGINX.md) for TLS termination and WebSocket proxy configuration.
If terminating TLS directly at gmmff (not recommended), use the `--tls-cert` and `--tls-key` flags or `GMMFF_TLS_CERT` and `GMMFF_TLS_KEY` environment variables.

#### Portainer
<!-- openwiki: broken internal link [docs/PORTAINER.md] file "docs/PORTAINER.md" does not exist. Fix the href or restore the target, then delete this comment. -->
See [docs/PORTAINER.md](docs/PORTAINER.md) for container management.

## Configuration

Most configuration is done via environment variables with the `GMMFF_` prefix. 
Some settings are available only as command-line flags (see `gmmff serve --help`).

<!-- openwiki: broken internal link [docs/ENV.md] file "docs/ENV.md" does not exist. Fix the href or restore the target, then delete this comment. -->
See [docs/ENV.md](docs/ENV.md) for the full reference.

### Environment Variables

Key variables:
- `GMMFF_ADDR`: TCP address to listen on (default: `:8080`)
- `GMMFF_REDIS_URL`: Redis/Valkey connection string (optional, enables persistence and horizontal scaling)
- `GMMFF_LOG_LEVEL`: Log level (`trace`, `debug`, `info`, `warn`, `error`)
- `GMMFF_TLS_CERT`: Path to TLS certificate (optional)
- `GMMFF_TLS_KEY`: Path to TLS private key (optional)
- `GMMFF_WEB_DIR`: Path to web/static directory to serve the browser UI (optional)
- `GMMFF_SHOW_FILES`: Show/hide the Files tab in the UI (`true`/`false`)
- `GMMFF_SHOW_CHAT`: Show/hide the Chat tab in the UI (`true`/`false`)
- `GMMFF_SHOW_ICE_SETTINGS`: Show the ICE settings panel (STUN/TURN configuration) in the UI (`true`/`false`)
- `GMMFF_STUN`: STUN server URL (repeatable, used for ICE server pushing)
- `GMMFF_TURN`: TURN server URL (repeatable, used for ICE server pushing)

Note: The following settings are flag-only and do not have corresponding environment variables:
- `--memory`: Use in-memory store (development only)
- `--log-pretty`: Enable pretty-logging (human-readable output)
- `--slot-ttl`: Slot time-to-live (default: 10 minutes)
- `--csp-report-only`: Use Content-Security-Policy-Report-Only (not for production)

### Configuration Validation

The application validates configuration on startup. Invalid configuration will cause the server to exit with an error message.

See `internal/conf/` for validation logic.

## Monitoring

### Health Endpoints

The server exposes several HTTP endpoints for monitoring:

- `GET /healthz` - Liveness probe (returns `ok` if server is running)
- `GET /readyz` - Readiness probe (returns `ok` if server and Redis are ready)
- `GET /metrics` - Prometheus metrics endpoint
- `GET /config.json` - Non-sensitive configuration snapshot, including UI feature flags
- `GET /` - Landing page (HTML)

### Prometheus Metrics

Key metrics include:
- `gmmff_connections_total` - Total WebSocket connections
- `gmmff_slots_total` - Total slots by state (waiting, ready, closed)
- `gmmff_slot_create_total` - Total slot creation requests
- `gmmff_slot_join_total` - Total slot join requests
- `gmmff_slot_expire_total` - Total slots expired due to TTL
- `gmmff_bytes_sent_total` - Total bytes sent via WebSocket (signaling only)
- `gmmff_bytes_received_total` - Total bytes received via WebSocket (signaling only)

See `internal/metrics/` for implementation details.

### Logging

Logs are structured and privacy-preserving. By default, they contain:
- Timestamp
- Component name (`broker`, `store`, `main`)
- Slot UUID (opaque identifier)
- Error code (if applicable)
- HTTP method, path, and status code (for HTTP endpoints)

Logs do **not** contain:
- IP addresses
- User agents
- File names or sizes
- Slot codes (the 3-word codes)
- Transfer contents

Log format can be toggled between JSON and pretty-printed text via the `--log-pretty` flag (not via environment variable).

## Maintenance

### Database Maintenance
When using Redis/Valkey:
- Keys automatically expire after 10 minutes (slot TTL)
- No manual cleanup required under normal operation
- Monitor Redis memory usage with `INFO MEMORY`
- Use `Redis-cli --bigkeys` to identify large keys if needed

### Log Rotation
When running via systemd or Docker, logs are handled by the respective logging drivers.
For bare-metal runs, consider using `logrotate` or similar.

### Backups
No persistent user data is stored by the signaling server (only ephemeral slot state).
No backup procedure is required for the server itself.

If using persistent storage for other components (e.g., schedule mode), back up those stores separately.

### Upgrades
1. Pull latest image or pull latest code
2. Review [CHANGELOG](https://github.com/iamdoubz/gmmff/releases) for breaking changes
3. Restart service
4. Verify health endpoints

### Troubleshooting

#### Common Issues

| Symptom | Likely Cause | Solution |
|---------|--------------|----------|
| `connection refused` | Server not running or wrong port | Check server status, verify `GMMFF_ADDR` |
| `context deadline exceeded` | Network connectivity or firewall blocking | Check network, STUN/TURN settings |
| `slot not found` or `invalid code` | Code expired (10 min TTL) or mistyped | Create new session, verify code |
| `failed to set up WebRTC connection` | STUN/TURN issues or symmetric NAT | Try different STUN/TURN servers |
| `server logs show ERR_REDIS_UNAVAILABLE` | Redis not reachable | Check Redis connection, `GMMFF_REDIS_URL` |

#### Debugging
Enable debug logging and pretty logs:
```bash
export GMMFF_LOG_LEVEL=debug
go run ./cmd/gmmff serve --log-pretty
```

#### Diagnostics
- Use `wscat -c ws://localhost:8080/ws` to test WebSocket connectivity
- Check Redis with `redis-cli monitor` to see slot operations
- Use browser devtools to inspect WebRTC connection stats

## Security Considerations

### Firewall Rules
- Server TCP port: 8080 (WebSocket) or custom via `--addr`
- STUN: UDP 3478 (default Google STUN) or custom
- TURN: UDP/TCP 3478 (default) or custom
- For local mode: mDNS uses UDP 5353

### Secrets Management
- The 3-word code is a low-entropy secret; protect it via secure out-of-band channel
- No long-term secrets are stored by the server
- Consider using a secrets manager for `GMMFF_REDIS_URL` if it contains passwords

### Updates and Patching
- Monitor [GitHub Security Advisories](https://github.com/iamdoubz/gmmff/security/advisories)
- Update dependencies regularly with `go get -u ./...`
- Rebuild and redeploy after dependency updates

## Related Documentation
- [Architecture Overview](/openwiki/architecture/overview.md)
<!-- openwiki: broken internal link [docs/ENV.md] file "docs/ENV.md" does not exist. Fix the href or restore the target, then delete this comment. -->
- [Configuration Reference](docs/ENV.md)
<!-- openwiki: broken internal link [docs/CMDS.md] file "docs/CMDS.md" does not exist. Fix the href or restore the target, then delete this comment. -->
- [Commands Reference](docs/CMDS.md)
<!-- openwiki: broken internal link [docs/SECURITY.md] file "docs/SECURITY.md" does not exist. Fix the href or restore the target, then delete this comment. -->
- [Security Documentation](docs/SECURITY.md)
<!-- openwiki: broken internal link [docs/MONITORING.md] file "docs/MONITORING.md" does not exist. Fix the href or restore the target, then delete this comment. -->
- [Monitoring & Metrics](docs/MONITORING.md) *(if exists)*
