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
### `gmmff schedule` — server-side encrypted transfers

```
Usage: gmmff schedule <subcommand>
```

#### `gmmff schedule upload` — encrypt and upload a file

```
Usage: gmmff schedule upload <file> [file|dir ...] [flags]
```

Multiple files or directories are zipped automatically before encryption.

| Flag | Default | Description |
|------|---------|-------------|
| `--server` | `$GMMFF_SERVER` or `http://localhost:8080` | Server URL (`wss://`, `ws://`, `https://`, or `http://`) |
| `--ttl` | `24h` | Expiry duration — flexible format: `1h`, `8h`, `1 day`, `7d`, `30 days` |
| `--max-downloads` | `1` | Download limit (`0` = unlimited) |
| `--password` | — | Upload password; prompted interactively if required but not set |
| `--out` | — | Write the full share URL to this file |
| `--qr` | `false` | Print a QR code for the share URL |
| `--json` | `false` | Output result as JSON (key is a separate field) |
| `--chunk-size` | auto | Override chunk size in bytes (`0` = auto-select based on file size) |

**Example output:**

```
  Upload complete!

  File ID:         abc123def456
  Decryption key:  deadbeef1234...

  Share URL:       https://host/?type=schedule&id=abc123def456
  Full URL:        https://host/?type=schedule&id=abc123def456#key=deadbeef1234...

  Auto-download:   https://host/?type=schedule&id=abc123def456&dl=1#key=deadbeef1234...

  Delete URL:      https://host/?type=schedule&id=abc123def456&action=delete&dk=ff001122...

  Expires:         2026-05-28 09:22 CDT (in 24 hours)
  Avg speed:       1.2 MB/s
```

**JSON output (`--json`):**

```json
{
  "file_id":           "abc123def456",
  "key":               "deadbeef1234...",
  "share_url":         "https://host/?type=schedule&id=abc123def456",
  "full_url":          "https://host/?type=schedule&id=abc123def456#key=deadbeef1234...",
  "auto_download_url": "https://host/?type=schedule&id=abc123def456&dl=1#key=deadbeef1234...",
  "delete_url":        "https://host/?type=schedule&id=abc123def456&action=delete&dk=ff001122...",
  "expires_at":        "2026-05-28T14:22:00Z",
  "downloads_left":    1
}
```

#### `gmmff schedule download` — download and decrypt a file

```
Usage: gmmff schedule download <share-url> [flags]
```

The `<share-url>` must include the `#key=` fragment. Quote the URL to prevent
shell interpretation of `#` and `&`:

```bash
gmmff schedule download "https://host/?type=schedule&id=X#key=Y"
```

| Flag | Default | Description |
|------|---------|-------------|
| `--out` / `-o` | `.` (current directory) | Output directory, filename, or `-` for stdout |
| `--confirm` | `false` | Prompt for confirmation before downloading |
| `--json` | `false` | Output result as JSON |

**Pipe to stdout:**

```bash
# Decrypt and pipe to tar
gmmff schedule download "https://host/...#key=..." --out - | tar xz

# Redirect to a file
gmmff schedule download "https://host/...#key=..." --out - > archive.zip

# Use with wget/curl for the ciphertext then decrypt locally
gmmff schedule download "https://host/?type=schedule&id=X#key=Y" --out ./downloads/
```

**Auto-download URL** (for use with `wget`/`curl` — browser only, key required in fragment):

```
https://host/?type=schedule&id=X&dl=1#key=Y
```

#### `gmmff schedule delete` — delete an uploaded file

```
Usage: gmmff schedule delete [<delete-url>] [flags]
```

| Flag | Default | Description |
|------|---------|-------------|
| `--id` | — | File ID to delete (alternative to delete URL) |
| `--delete-key` | — | Delete key shown after upload |
| `--server` | `$GMMFF_SERVER` | Server URL (derived from delete URL if not set) |

Pass the full delete URL shown after upload:

```bash
gmmff schedule delete "https://host/?type=schedule&id=X&action=delete&dk=Y"
```

Or pass flags explicitly:

```bash
gmmff schedule delete --id abc123 --delete-key ff001122 --server https://host
```

---

### `gmmff cleanup` — remove expired schedule uploads

```
Usage: gmmff cleanup
```

Scans the schedule storage directory and removes:

- Completed uploads past their expiry time
- Completed uploads that have reached their download limit
- Pending (in-progress) uploads older than 24 hours

Runs once and exits — suitable for cron:

```bash
# /etc/cron.d/gmmff-cleanup
*/30 * * * *  gmmff  /usr/local/bin/gmmff cleanup
```

Requires `GMMFF_SHOW_SCHEDULE=true` and `GMMFF_SCHEDULE_DIR` to be set (or defaults).
The same cleanup logic runs automatically in the background when `GMMFF_SCHEDULE_CLEANUP_INTERVAL` is set in `gmmff serve`.

---

# Environment variables

Set these to avoid passing flags on every command:

```bash
export GMMFF_SERVER=wss://your-server/ws
gmmff create
gmmff schedule upload myfile.pdf
```

| Variable | Used by | Description |
|----------|---------|-------------|
| `GMMFF_SERVER` | all client commands | Signaling server WebSocket URL (also accepted as `https://` for schedule) |
| `GMMFF_STUN` | all client commands | Comma-separated STUN/STUNS URLs |
| `GMMFF_TURN` | all client commands | Comma-separated TURN URLs |
| `GMMFF_SCHEDULE_PASSWORD` | `schedule upload` | Upload password if server requires one |

# Next steps

- STUN and TURN [configuration documentation](TURN.md)
- Schedule feature [full documentation](SCHEDULE.md)
