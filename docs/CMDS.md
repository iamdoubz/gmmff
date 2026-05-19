## Commands

### `gmmff create` — start a file + message session

```
Usage: gmmff create [flags]
```

| Flag | Env var | Default | Description |
|------|---------|---------|-------------|
| `--server` | `GMMFF_SERVER` | `ws://localhost:8080/ws` | Signaling server WebSocket URL |
| `--stun` | `GMMFF_STUN` | Google STUN | STUN/STUNS URL, repeatable |
| `--turn` | `GMMFF_TURN` | — | TURN server, repeatable (see TURN section) |
| `--out` / `-o` | — | `.` | Directory to save received files |
| `--max-peers` | — | `2` | Maximum participants including yourself (2–10) |

### `gmmff join <code>` — join any session

```
Usage: gmmff join <code> [flags]
```

Detects the session type from the server automatically — routes to the file
session REPL for `files` sessions, or the chat REPL for `chat` sessions.

| Flag | Env var | Default | Description |
|------|---------|---------|-------------|
| `--server` | `GMMFF_SERVER` | `ws://localhost:8080/ws` | Signaling server WebSocket URL |
| `--stun` | `GMMFF_STUN` | Google STUN | STUN/STUNS URL, repeatable |
| `--turn` | `GMMFF_TURN` | — | TURN server, repeatable |

### `gmmff chat` — start a pure text chat session

```
Usage: gmmff chat [flags]
```

| Flag | Env var | Default | Description |
|------|---------|---------|-------------|
| `--server` | `GMMFF_SERVER` | `ws://localhost:8080/ws` | Signaling server WebSocket URL |
| `--stun` | `GMMFF_STUN` | Google STUN | STUN/STUNS URL, repeatable |
| `--turn` | `GMMFF_TURN` | — | TURN server, repeatable |

### `gmmff local` — self-contained local-network mode

```
Usage: gmmff local [flags]
```

No internet required. Starts an embedded signaling server, web server, and
session all in one process. Discovers other `gmmff local` instances via mDNS.

| Flag | Default | Description |
|------|---------|-------------|
| `--port` | random | Port to listen on |
| `--no-tls` | `false` | Use plain HTTP instead of self-signed TLS |
| `--max-peers` | `2` | Maximum participants (2–10, including yourself) |

**TLS behaviour:** By default a self-signed certificate is generated each run
and written to `$TMPDIR/gmmff-cert.pem` and `$TMPDIR/gmmff-key.pem`. These
files are removed on clean shutdown. Use `--no-tls` to skip certificate
generation — recommended for Chrome/Firefox-only sessions.

```bash
# Default (TLS on, random port)
gmmff local

# Plain HTTP — Chrome and Firefox only (no cert warning)
gmmff local --no-tls

# Fixed port for firewall rules
gmmff local --no-tls --port 8787

# Allow up to 5 participants
gmmff local --max-peers 5
```

# Environment variables

Set these to avoid passing flags on every command:

```bash
export GMMFF_SERVER=wss://your-server/ws
gmmff create
```

| Variable | Used by | Description |
|----------|---------|-------------|
| `GMMFF_SERVER` | all client commands | Signaling server WebSocket URL |
| `GMMFF_STUN` | all client commands | Comma-separated STUN/STUNS URLs |
| `GMMFF_TURN` | all client commands | Comma-separated TURN URLs |

# Next steps

- STUN and TURN [configuration documentation](TURN.md)