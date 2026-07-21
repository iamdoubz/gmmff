---
type: Documentation
title: Key Workflows
description: Step-by-step walkthroughs of common gmmff operations including file transfer, chat, and local mode.
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

## Schedule Mode (Encrypted Server-Side Transfers)

For scheduled, server-mediated transfers:

```bash
# Schedule an upload
gmmff schedule upload --local-path ./backup.zip --remote-path backups/weekly.zip --recur "@weekly"

# Schedule a download
gmmff schedule download --remote-path backups/weekly.zip --local-path ./latest.zip --recur "@daily"
```

See [Schedule Documentation](docs/SCHEDULE.md) for details.

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
```

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
   - Use `--no-tls` in local mode for browser compatibility

See [Operations & Runbook](/openwiki/operations/runbook.md) for detailed troubleshooting.