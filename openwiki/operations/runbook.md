---
type: Operations
title: Operations & Runbook
description: Deployment, configuration, monitoring, and maintenance procedures for gmmff in production.
verified:
  - by: openwiki/0.5.0
    at: 2026-09-05T11:33:48.919Z
sources:
  - id: openwiki-source-a2371d6362e5db4bc834ad03
    resource: repo://CLAUDE.md
generated: { by: "openwiki/0.5.0", at: "2026-09-05T11:33:48.919Z" }
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

### Production Deployment

#### Systemd Service
<!-- openwiki: broken internal link [docs/SYSTEMD.md] file "docs/SYSTEMD.md" does not exist. Fix the href or restore the target, then delete this comment. -->
See [docs/SYSTEMD.md](docs/SYSTEMD.md) for detailed instructions on:
- Creating a dedicated unprivileged system user (`gmmff`)
- Installing the binary with proper permissions
- Creating configuration and log directories
- Setting up Redis/Valkey Unix socket access (if applicable)
- Installing and enabling the systemd service
- Day-to-day operations: configuration changes, log viewing, service control
- Health verification via `/healthz` and `/readyz` endpoints

#### NGINX Reverse Proxy
<!-- openwiki: broken internal link [docs/NGINX.md] file "docs/NGINX.md" does not exist. Fix the href or restore the target, then delete this comment. -->
See [docs/NGINX.md](docs/NGINX.md) for:
- TLS termination with Let's Encrypt via Certbot
- WebSocket proxy configuration (essential `proxy_http_version`, `Upgrade`, and `Connection` headers)
- Timeout and buffering settings optimized for gmmff
- Privacy considerations (no IP forwarding)
- Securing internal endpoints like `/metrics`, `/healthz`, and `/readyz`
- Rate limiting for `/api/ice` endpoint
- ICE credential endpoint security (slot code authentication + nginx rate limiting)

#### Portainer
<!-- openwiki: broken internal link [docs/PORTAINER.md] file "docs/PORTAINER.md" does not exist. Fix the href or restore the target, then delete this comment. -->
See [docs/PORTAINER.md](docs/PORTAINER.md) for container management.

## Configuration

### Environment Variables
<!-- openwiki: broken internal link [docs/ENV.md] file "docs/ENV.md" does not exist. Fix the href or restore the target, then delete this comment. -->
All configuration is done via environment variables with the `GMMFF_` prefix. See [docs/ENV.md](docs/ENV.md) for the full reference.

Key variables:
- `GMMFF_ADDR`: TCP listen address (default: `:8080`)
- `GMMFF_REDIS_URL`: Redis/Valkey connection string (optional, enables persistence and horizontal scaling)
- `GMMFF_LOG_LEVEL`: Log level (`trace`, `debug`, `info`, `warn`, `error`)
- `GMMFF_LOG_PRETTY`: Enable pretty-logging (`true`/`false`)
- `GMMFF_STUN`: STUN server URL (repeatable, e.g., `stun:stun.l.google.com:19302`)
- `GMMFF_TURN`: TURN server URL (repeatable, supports `secret=` for ephemeral credentials)
- `GMMFF_SHOW_FILES`: Show Files tab (`true`/`false`, default: `true`)
- `GMMFF_SHOW_CHAT`: Show Chat tab (`true`/`false`, default: `true`)
- `GMMFF_SHOW_SCHEDULE`: Enable Schedule tab and API endpoints (`true`/`false`, default: `false`)
- `GMMFF_WEB_DIR`: Path to `web/static/` for serving browser UI (optional)
- `GMMFF_TLS_CERT`: TLS certificate path (for direct TLS termination)
- `GMMFF_TLS_KEY`: TLS private key path (for direct TLS termination)
- `GMMFF_PUSH_STUN`: Push server STUN config to browser via `/api/ice` (`true`/`false`)
- `GMMFF_PUSH_TURN`: Push server TURN config to browser via `/api/ice` (`true`/`false`)
- `GMMFF_PUSH_TTL`: Ephemeral TURN credential lifetime in seconds (default: `1800` for 30 minutes)

Schedule-specific variables (when `GMMFF_SHOW_SCHEDULE=true`):
- `GMMFF_SCHEDULE_DIR`: Storage root; `pending/` and `complete/` created automatically (default: `./data/schedule`)
- `GMMFF_SCHEDULE_MAX_SIZE`: Maximum upload size (supports `gb`/`mb`/`kb` suffix, default: `1gb`)
- `GMMFF_SCHEDULE_MAX_DOWNLOADS`: Server-wide cap on downloads per file (`0` = unlimited, default: `1`)
- `GMMFF_SCHEDULE_UPLOAD_IP`: Comma-separated IPs/CIDRs allowed to upload without a password
- `GMMFF_SCHEDULE_PASSWORD`: Required upload password (bypassed if caller's IP is in `UPLOAD_IP`)
- `GMMFF_SCHEDULE_DOWNLOAD_IP`: Comma-separated IPs/CIDRs allowed to download (default: `0.0.0.0` = anyone)
- `GMMFF_SCHEDULE_CLEANUP_INTERVAL`: Crontab-format background cleanup (e.g. `*/30 * * * *`)
- `GMMFF_TTL_SETTINGS`: Comma-separated TTL options for upload dropdown (default: `1h,8h,1 day,3 days,7 days,30 days`)

### Configuration Validation
The application validates configuration on startup. Invalid configuration will cause the server to exit with an error message.

See `internal/conf/` for validation logic.

## Monitoring

### Health Endpoints
The server exposes several HTTP endpoints for monitoring:

- `GET /healthz` - Liveness probe (returns `ok` if server is running)
- `GET /readyz` - Readiness probe (returns `ok` if server and Redis are ready)
- `GET /metrics` - Prometheus metrics endpoint
- `GET /config.json` - Non-sensitive configuration snapshot
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

Log format can be toggled between JSON and pretty-printed text via `GMMFF_LOG_PRETTY`.

## Maintenance

### Database Maintenance
When using Redis/Valkey:
- Keys automatically expire after 10 minutes (slot TTL)
- No manual cleanup required under normal operation
- Monitor Redis memory usage with `INFO MEMORY`
- Use `Redis-cli --bigkeys` to identify large keys if needed

### Schedule Storage Maintenance
When schedule mode is enabled (`GMMFF_SHOW_SCHEDULE=true`):
- Monitor disk usage in `$GMMFF_SCHEDULE_DIR`
- The `pending/` directory holds incomplete uploads (safe to remove files older than 24 hours)
- The `complete/` directory holds finalized encrypted files and metadata
- Background cleanup removes completed files past their expiration or with zero downloads left
- One-shot cleanup via `gmmff cleanup` removes the same items

### Log Rotation
When running via systemd or Docker, logs are handled by the respective logging drivers.
For bare-metal runs, consider using `logrotate` or similar.

### Backups
No persistent user data is stored by the signaling server (only ephemeral slot state).
No backup procedure is required for the server itself.

If using persistent storage for schedule mode, back up the `$GMMFF_SCHEDULE_DIR/complete/` directory.

### Upgrades
1. Pull latest image or pull latest code
2. Review [CHANGELOG](https://github.com/iamdoubz/gmmff/releases) for breaking changes
3. Restart service
4. Verify health endpoints

## Troubleshooting

### Common Issues

| Symptom | Likely Cause | Solution |
|---------|--------------|----------|
| `connection refused` | Server not running or wrong port | Check server status, verify `GMMFF_ADDR` |
| `context deadline exceeded` | Network connectivity or firewall blocking | Check network, STUN/TURN settings |
| `slot not found` or `invalid code` | Code expired (10 min TTL) or mistyped | Create new session, verify code |
| `failed to set up WebRTC connection` | STUN/TURN issues or symmetric NAT | Try different STUN/TURN servers |
| `server logs show ERR_REDIS_UNAVAILABLE` | Redis not reachable | Check Redis connection, `GMMFF_REDIS_URL` |
| Schedule upload fails with 403 | IP not in allowlist and/or missing password | Verify `GMMFF_SCHEDULE_UPLOAD_IP` and `GMMFF_SCHEDULE_PASSWORD` |
| Schedule download fails with 403 | IP not in download allowlist | Verify `GMMFF_SCHEDULE_DOWNLOAD_IP` |

### Debugging
Enable debug logging:
```bash
export GMMFF_LOG_LEVEL=debug
export GMMFF_LOG_PRETTY=true
```

### Diagnostics
- Use `wscat -c ws://localhost:8080/ws` to test WebSocket connectivity
- Check Redis with `redis-cli monitor` to see slot operations
- Use browser devtools to inspect WebRTC connection stats
- For schedule mode, check `$GMMFF_SCHEDULE_DIR` for file sizes and metadata

## Security Considerations

### Firewall Rules
- Server TCP port: 8080 (WebSocket) or custom via `GMMFF_ADDR`
- STUN: UDP 3478 (default Google STUN) or custom
- TURN: UDP/TCP 3478 (default) or custom
- For local mode: mDNS uses UDP 5353

### Secrets Management
- The 3-word code is a low-entropy secret; protect it via secure out-of-band channel
- No long-term secrets are stored by the server
- Consider using a secrets manager for `GMMFF_REDIS_URL` if it contains passwords
- For schedule mode, the decryption key never leaves the client and is not stored by the server

### IP Allowlists and Schedule Auth Precedence
In schedule mode, access control follows this exact order (security-critical):
1. No IP list **and** no password → allow everyone.
2. IP in a non-empty allowlist → allow (no password needed).
3. Password is set → require and verify it.
4. Otherwise → deny.
This prevents the password from being silently ignored when an IP allowlist is set.

### TURN Credentials
- Prefer ephemeral credentials (`secret=` → HMAC-derived short-lived credential). The master secret must never leave the server.
- `GMMFF_PUSH_TTL` controls the lifetime of ephemeral credentials (default 30 minutes).
- Static `user=`/`pass=` credentials are sent verbatim to every peer — only acceptable for intentionally public/anonymous TURN.

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
<!-- openwiki: broken internal link [docs/SCHEDULE.md] file "docs/SCHEDULE.md" does not exist. Fix the href or restore the target, then delete this comment. -->
- [Schedule Documentation](docs/SCHEDULE.md)
<!-- openwiki: broken internal link [docs/SECURITY.md] file "docs/SECURITY.md" does not exist. Fix the href or restore the target, then delete this comment. -->
- [Security Documentation](docs/SECURITY.md)
<!-- openwiki: broken internal link [docs/MONITORING.md] file "docs/MONITORING.md" does not exist. Fix the href or restore the target, then delete this comment. -->
- [Monitoring & Metrics](docs/MONITORING.md) *(if exists)*
