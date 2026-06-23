# Server configuration

You can pass in all these flags from the terminal, or you can create a `.env` file.

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

## Next steps

- Read how to start `gmmff` signalling server at boot using [systemd](SYSTEMD.md)
- Read how to setup a [reverse proxy](NGINX.md) for your signalling server