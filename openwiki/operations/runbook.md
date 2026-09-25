---
type: Operations
title: Operations & Runbook
description: Deployment, configuration, monitoring, and maintenance procedures for gmmff.
verified:
  - by: openwiki/0.6.0
    at: 2026-09-25T13:12:29.362Z
sources:
  - id: openwiki-source-3c5dff77bae4df4110d95849
    resource: repo://cmd/gmmff/main.go
  - id: openwiki-source-3b59060d90820d6a392f85ff
    resource: repo://internal/broker/server.go
  - id: openwiki-source-b3b67094b286e55952c7bbfa
    resource: repo://internal/log/log.go
generated: { by: "openwiki/0.6.0", at: "2026-09-25T13:12:29.362Z" }
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
See [docs/SYSTEMD.md](docs/SYSTEMD.md) for detailed instructions.

#### NGINX Reverse Proxy
<!-- openwiki: broken internal link [docs/NGINX.md] file "docs/NGINX.md" does not exist. Fix the href or restore the target, then delete this comment. -->
See [docs/NGINX.md](docs/NGINX.md) for TLS termination and WebSocket proxy configuration.

#### Portainer
<!-- openwiki: broken internal link [docs/PORTAINER.md] file "docs/PORTAINER.md" does not exist. Fix the href or restore the target, then delete this comment. -->
See [docs/PORTAINER.md](docs/PORTAINER.md) for container management.

#### Recommended Architecture
For production deployments, use a reverse proxy (such as NGINX, Caddy, or Traefik) to handle TLS termination, rate limiting, and request routing. The gmmff server itself should only listen on localhost or within a trusted network when TLS is handled by the proxy.

Example NGINX configuration snippet:
```nginx
server {
    listen 443 ssl http2;
    server_name example.com;

    ssl_certificate /path/to/cert.pem;
    ssl_certificate_key /path/to/key.pem;

    location /ws/ {
        proxy_pass http://localhost:8080/ws;
        proxy_http_version 1.1;
        proxy_set_header Upgrade $http_upgrade;
        proxy_set_header Connection "upgrade";
        proxy_set_header Host $host;
        proxy_read_timeout 60s;
    }

    # Optional: serve static assets directly
    location / {
        proxy_pass http://localhost:8080;
        proxy_set_header Host $host;
    }
}
```

## Configuration

### Environment Variables
<!-- openwiki: broken internal link [docs/ENV.md] file "docs/ENV.md" does not exist. Fix the href or restore the target, then delete this comment. -->
All configuration is done via environment variables with the `GMMFF_` prefix. See [docs/ENV.md](docs/ENV.md) for the full reference.

Key variables:
- `GMMFF_ADDR`: TCP address to listen on (default: `:8080`)
- `GMMFF_SERVER`: Signaling server WebSocket URL (default: `ws://localhost:8080/ws`)
- `GMMFF_REDIS_URL`: Redis/Valkey connection string (optional, enables persistence and horizontal scaling)
- `GMMFF_LOG_LEVEL`: Log level (`trace`, `debug`, `info`, `warn`, `error`)
- `GMMFF_LOG_PRETTY`: Enable pretty-logging (`true`/`false`)
- `GMMFF_STUN`: STUN server URL (repeatable, comma-separated)
- `GMMFF_TURN`: TURN server URL (repeatable, comma-separated)
- `GMMFF_SLOT_TTL`: How long a waiting slot is kept alive before expiry (default: `10m`)
- `GMMFF_WEB_DIR`: Path to web/static directory to serve the browser UI
- `GMMFF_CSP_REPORT_ONLY`: Use Content-Security-Policy-Report-Only instead of enforcing CSP
- `GMMFF_TLS_CERT`: Path to TLS certificate (optional; prefer terminating TLS at the proxy)
- `GMMFF_TLS_KEY`: Path to TLS private key (optional)
- `GMMFF_SCHEDULE_DIR`: Directory for schedule mode storage
- `GMMFF_SCHEDULE_CLEANUP_INTERVAL`: Cron expression for schedule cleanup (e.g., `@hourly`)
- `GMMFF_SCHEDULE_ENABLED`: Enable schedule mode (`true`/`false`)

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
Logs are structured and privacy-preserving by design. The logger is initialized via `internal/log.Init()`.

By default, they contain:
- Timestamp (RFC3339 format)
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
- Any data that could identify a transfer or user

Log format can be toggled between JSON and pretty-printed text via `GMMFF_LOG_PRETTY`.
- Set `GMMFF_LOG_PRETTY=true` for human-readable output (development)
- Set `GMMFF_LOG_PRETTY=false` for JSON output (production)

The logger implements a strict privacy contract: no personally identifiable information or transfer metadata is ever logged, making logs safe to ship to shared ops dashboards without data-processing agreements.

## Maintenance

### Database Maintenance
When using Redis/Valkey:
- Keys automatically expire after the slot TTL period (default: 10 minutes)
- No manual cleanup required under normal operation
- Monitor Redis memory usage with `INFO MEMORY`
- Use `Redis-cli --bigkeys` to identify large keys if needed
- For persistence, configure Redis AOF or RDB snapshots according to your durability requirements

#### Backup and Recovery Considerations
The signaling server only stores ephemeral slot state (waiting connections) in Redis. This state is:
- Short-lived (expires automatically based on TTL)
- Non-persistent by design (no long-term storage of user data)
- Recoverable by clients simply retrying with a new code

**Backup Procedures:**
- No backup of Redis is required for the signaling server function
- If persistence is enabled in Redis for other reasons, standard Redis backup procedures apply (RDB snapshots or AOF)
- Recovery involves restarting Redis and the gmmff server; any unexpired slots will be available immediately

**High Availability:**
- For horizontal scaling, run multiple gmmff instances sharing the same Redis instance
- Redis should be configured for high availability (Redis Sentinel or Redis Cluster)
- Ensure all gmmff instances use the same `GMMFF_REDIS_URL`

### Log Rotation
When running via systemd or Docker, logs are handled by the respective logging drivers.
For bare-metal runs, consider using `logrotate` or similar.

Example logrotate configuration:
```
/var/log/gmmff.log {
    daily
    rotate 14
    compress
    delaycompress
    missingok
    notifempty
}
```

### Schedule Cleanup Goroutine
When schedule mode is enabled (`GMMFF_SCHEDULE_ENABLED=true`), a background goroutine periodically cleans up expired schedule files.

**Configuration:**
- `GMMFF_SCHEDULE_DIR`: Directory where schedule files are stored (required when schedule mode enabled)
- `GMMFF_SCHEDULE_CLEANUP_INTERVAL`: Cron expression defining cleanup frequency (e.g., `@hourly`, `0 */6 * * *`)
- `GMMFF_SCHEDULE_ENABLED`: Set to `true` to enable schedule mode

**How it works:**
1. On startup, if schedule mode is enabled, the server validates the schedule directory
2. A background goroutine is started that runs according to the cleanup interval
3. The goroutine removes files older than the schedule TTL (hardcoded to 24 hours in the schedule package)
4. Logging indicates when the cleanup goroutine starts and reports any errors in cron parsing

**Example configuration:**
```bash
export GMMFF_SCHEDULE_ENABLED=true
export GMMFF_SCHEDULE_DIR=/var/lib/gmmff/schedule
export GMMFF_SCHEDULE_CLEANUP_INTERVAL="@hourly"
```

### Backups
No persistent user data is stored by the signaling server (only ephemeral slot state).
No backup procedure is required for the server itself.

If using persistent storage for schedule mode (`GMMFF_SCHEDULE_DIR` is set), back up that directory according to your data retention requirements.

### Upgrades
1. Pull latest image or pull latest code
2. Review [CHANGELOG](https://github.com/iamdoubz/gmmff/releases) for breaking changes
3. Restart service
4. Verify health endpoints

### Troubleshooting

#### Common Issues

| Symptom | Likely Cause | Solution |
|---------|--------------|----------|
| `connection refused` | Server not running or wrong port | Check server status, verify `GMMFF_SERVER` |
| `context deadline exceeded` | Network connectivity or firewall blocking | Check network, STUN/TURN settings |
| `slot not found` or `invalid code` | Code expired (10 min TTL) or mistyped | Create new session, verify code |
| `failed to set up WebRTC connection` | STUN/TURN issues or symmetric NAT | Try different STUN/TURN servers |
| `server logs show ERR_REDIS_UNAVAILABLE` | Redis not reachable | Check Redis connection, `GMMFF_REDIS_URL` |
| `schedule cleanup: invalid cron expression` | Invalid `GMMFF_SCHEDULE_CLEANUP_INTERVAL` | Verify cron expression format |
| `schedule feature enabled` but no cleanup | Schedule directory not set or inaccessible | Check `GMMFF_SCHEDULE_DIR` permissions |

#### Debugging
Enable debug logging:
```bash
export GMMFF_LOG_LEVEL=debug
export GMMFF_LOG_PRETTY=true
```

#### Diagnostics
- Use `wscat -c ws://localhost:8080/ws` to test WebSocket connectivity
- Check Redis with `redis-cli monitor` to see slot operations
- Use browser devtools to inspect WebRTC connection stats
- Check schedule directory permissions and contents if using schedule mode

## Security Considerations

### Firewall Rules
- Server TCP port: 8080 (WebSocket) or custom via `GMMFF_ADDR`
- STUN: UDP 3478 (default Google STUN) or custom via `GMMFF_STUN`
- TURN: UDP/TCP 3478 (default) or custom via `GMMFF_TURN`
- For local mode: mDNS uses UDP 5353

### Secrets Management
- The 3-word code is a low-entropy secret; protect it via secure out-of-band channel
- No long-term secrets are stored by the server
- Consider using a secrets manager for `GMMFF_REDIS_URL` if it contains passwords
- TLS certificates should be managed by your reverse proxy, not the gmmff server

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
