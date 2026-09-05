---
type: Documentation
title: Key Workflows
description: Step-by-step walkthroughs of common gmmff operations including file transfer, chat, local mode, and scheduled transfers.
tags: [workflows, file-transfer, chat, local-mode, schedule]
verified:
  - by: openwiki/0.5.0
    at: 2026-09-05T11:33:48.919Z
sources:
  - id: openwiki-source-b6800ab98842381129ec0353
    resource: repo://cmd/gmmff/create.go
generated: { by: "openwiki/0.5.0", at: "2026-09-05T11:33:48.919Z" }
---
# Key Workflows

## File Transfer Session

The most common workflow involves two peers establishing a session to transfer files and messages.

### Step-by-Step Flow

1. **Peer A initiates session**
   ```bash
   gmmff create
   # Output: Created session: abc-def-ghi
   #         Share this code with your peer: apple-banana-cherry
   ```

2. **Peer A shares code**  
   Peer A communicates the 3-word code (`apple-banana-cherry`) to Peer B via an out-of-band channel (verbal, QR code, etc.)

3. **Peer B joins session**
   ```bash
   gmmff join apple-banana-cherry
   ```

4. **Session establishment**
   - Both peers connect to the signaling server
   - Server resolves code → slot UUID
   - Peers exchange PAKE messages to derive shared key
   - SDP offer/answer exchanged (HMAC-signed with PAKE secret)
   - ICE candidates exchanged to establish direct connection
   - WebRTC data channel opens
   - Signaling server's role is complete

5. **Session REPL active**
   Both peers see:
   ```
   gmmff> 
   ```
   Available commands:
   - `send <file|dir>` - Send file(s) or directory
   - `msg <message>` - Send a chat message
   - `peers` - List connected peers
   - `exit` - Leave session

6. **File transfer**
   - Peer A: `send document.pdf`
   - File is chunked, encrypted, and sent over WebRTC data channel
   - Progress bar shows transfer progress
   - Receiver gets prompt: `Accept document.pdf? [y/N]`
   - On acceptance, file is verified via hash and saved

7. **Session termination**
   - Either peer types `exit` or presses Ctrl+C
   - Peer sends `bye` frame to signaling server
   - Server deletes slot keys, notifies remaining peer
   - WebRTC connection closes

### One-off File Transfer (`gmmff send`)

For simple file transfers without interactive REPL:

```bash
# Peer A
gmmff send document.pdf --message "Here's the document"
# Output: Created session: jkl-mno-pqr
#         Share this code with your peer: dog-cat-bird
#         Waiting for peer...
#         Peer connected!
#         Sending document.pdf (1.2 MB)...
#         Transfer complete and verified.
```

Peer B runs:
```bash
gmmff join dog-cat-bird
# Accept document.pdf? [y/N] y
# Receiving document.pdf (1.2 MB)...
# Transfer complete.
# Session ended.
```

The `send` command:
1. Creates a session
2. Waits for exactly one peer to join
3. Sends the specified file(s)
4. Verifies transfer via hash
5. Automatically exits

### Chat Session

For pure text communication:

```bash
# Peer A
gmmff chat
# Output: Created session: stu-vwx-yzx
#         Share this code with your peer: red-green-blue
#         Waiting for peer...

# Peer B
gmmff chat red-green-blue
# Connected! Type messages to send.

# Both peers see:
# Peer A: Hello!
# Peer B: Hi there!
```

The chat session remains active until either party types `\q`, the connection is lost, or no message is sent/received for 10 minutes.

## Universal Join Workflow

Both `gmmff create` and `gmmff chat` print a join command for the other peer. The `join` command works for any session type (file+message or chat):

```bash
gmmff join <3-word-code>
```

Upon joining:
- The client connects to the signaling server
- Resolves the code to a slot UUID
- Participates in PAKE key exchange to derive session keys
- Waits for `slot.ready` to determine session type
- Enters the appropriate REPL (file transfer or chat) based on session type

## Local-Network Mode (`gmmff local`)

For environments without internet access:

```bash
# On Peer A
gmmff local
# Output: mDNS service registered: _gmmff._tcp.local.
#         Local server listening on :12345
#         Visit http://[::1]:12345 in your browser
#         or run: gmmff local --no-tls --port 12345

# On Peer B (same network)
gmmff local
# Automatically discovers Peer A via mDNS
# Can connect via browser or another gmmff local instance
```

Features:
- Embedded signaling server (WebSocket + HTTP)
- mDNS-based peer discovery
- Optional self-signed TLS (disable with `--no-tls`)
- Browser-accessible UI at `http://<local-ip>:<port>`
- All components in single process
- Uses host-only ICE candidates (no STUN/TURN required)
- Session ends and server shuts down when typing `\q`

## Schedule Mode (Encrypted Server-Side Transfers)

For scheduled, server-mediated transfers that do not require simultaneous online peers:

```bash
# Schedule an upload (encrypts and uploads to server)
gmmff schedule upload ./backup.zip --ttl 7d --max-downloads 3

# Schedule a download (decrypts and saves from server share URL)
gmmff schedule download "https://host/?type=schedule&id=X#key=Y" --out ./latest.zip
```

### Upload Flow
1. Encrypts file(s) with AES-256-GCM using a random key (never sent to server)
2. Splits into chunks, each with unique nonce
3. Uploads ciphertext to server via HTTP API
4. Server stores only encrypted chunks and metadata
5. Returns share URL containing file ID and decryption key in URL fragment (`#key=...`)
6. Key never transmitted to server — remains in client/browser only

### Download Flow
1. Client extracts file ID and decryption key from share URL fragment
2. Fetches encrypted metadata and chunks from server
3. Decrypts and verifies integrity locally
4. Writes decrypted file to output destination

### Security Properties
- Server never sees plaintext, filenames, or decryption keys
- URL fragment (`#key=...`) never leaves browser (not sent in HTTP requests)
- Delete key required for file deletion (shown only to uploader)
- Access control via IP allowlists and/or passwords

<!-- openwiki: broken internal link [docs/SCHEDULE.md] file "docs/SCHEDULE.md" does not exist. Fix the href or restore the target, then delete this comment. -->
See [Schedule Documentation](docs/SCHEDULE.md) for details on enabling schedule mode, access control, and advanced usage.

## Configuration & Environment

All services configure via environment variables (prefixed with `GMMFF_`):

```bash
# Essential for production
GMMFF_REDIS_URL=redis://localhost:6379
GMMFF_SERVER=ws://signaling.example.com/ws

# Optional
GMMFF_LOG_LEVEL=info
GMMFF_LOG_PRETTY=true
GMMFF_STUN=stun:stun.l.google.com:19302
GMMFF_TURN=turn:turn.example.com:3478?transport=udp
GMMFF_SHOW_SCHEDULE=true  # Enable schedule feature
GMMFF_SCHEDULE_DIR=./data/schedule  # Storage directory for schedule uploads
```

<!-- openwiki: broken internal link [docs/ENV.md] file "docs/ENV.md" does not exist. Fix the href or restore the target, then delete this comment. -->
<!-- openwiki: broken internal link [docs/CMDS.md] file "docs/CMDS.md" does not exist. Fix the href or restore the target, then delete this comment. -->
See [Environment Variables](docs/ENV.md) and [Commands Reference](docs/CMDS.md) for full details.

## Error Handling & Troubleshooting

Common issues and solutions:

1. **Connection timeout**
   - Check network connectivity to signaling server
   - Verify STUN/TURN settings if behind NAT
   - Ensure WebSocket port (default 8080) is accessible

2. **Session expired**
   - Codes expire after 10 minutes
   - Create a new session if joining takes too long

3. **Authentication failure**
   - Verify both peers entered identical code
   - Check for typos in 3-word code
   - Ensure no extra whitespace

4. **WebRTC connection failed**
   - Try different STUN/TURN servers
   - Check firewall rules blocking UDP/TCP ports
   - Use `--no-tls` in local mode for browser compatibility (Chrome/Firefox only)

5. **Schedule upload/download failed**
   - Ensure schedule feature is enabled (`GMMFF_SHOW_SCHEDULE=true`)
   - Verify server URL is correct and accessible
   - Check storage directory permissions and space

See [Operations & Runbook](/openwiki/operations/runbook.md) for detailed troubleshooting.
