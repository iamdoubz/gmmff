---
type: Documentation
title: Key Workflows
description: Detailed workflows for gmmff including interactive file transfer, one-off send, chat, local mode, and scheduled transfers with CLI steps and internal signaling behavior.
tags: [workflows, file-transfer, chat, local-mode, scheduling]
verified:
  - by: openwiki/0.5.2
    at: 2026-09-18T12:39:02.785Z
sources:
  - id: openwiki-source-ff3d82780276939149b4b693
    resource: repo://cmd/gmmff/chat.go
  - id: openwiki-source-b6800ab98842381129ec0353
    resource: repo://cmd/gmmff/create.go
  - id: openwiki-source-b658f28c78c13e77abb9b6df
    resource: repo://cmd/gmmff/local.go
  - id: openwiki-source-2973e123ef2def36be13b873
    resource: repo://cmd/gmmff/schedule.go
  - id: openwiki-source-dd84d717f510181ac3f05975
    resource: repo://cmd/gmmff/send.go
  - id: openwiki-source-273db603bca41af9296b84d6
    resource: repo://internal/broker/broker.go
generated: { by: "openwiki/0.5.2", at: "2026-09-18T12:39:02.785Z" }
---

# Key Workflows

## Interactive File Transfer Session

The most common workflow involves two peers establishing a session to transfer files and messages bidirectionally.

### User-Facing Steps

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

4. **Session establishment** (internal)
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

### Internal Signaling Flow (Mermaid)
```mermaid
sequenceDiagram
    participant A as Peer A
    participant B as Peer B
    participant S as Signaling Server

    A->>S: Connect WebSocket
    B->>S: Connect WebSocket
    A->>S: CreateSlot(files, 2)
    S->>A: SlotCreated (code: abc-def-ghi, ttl: 600)
    A->>B: Share code (out-of-band)
    B->>S: JoinSlot(abc-def-ghi)
    S->>B: SlotReady (type: files)
    A->>S: Wait SlotReady
    S->>A: SlotReady (type: files)
    A->>B: PAKE1 (opaque)
    B->>A: PAKE2 (opaque)
    A->>B: PAKE3 (opaque)
    B->>A: PAKE4 (opaque)
    A->>B: SDP offer (HMAC-signed)
    B->>A: SDP answer (HMAC-signed)
    A->>B: ICE candidates
    B->>A: ICE candidates
    A->>B: WebRTC data channel open
    B->>A: WebRTC data channel open
    A->>B: File metadata + chunks (encrypted)
    B->>A: File acceptance prompt
    B->>A: File accepted
    B->>A: File chunks (encrypted)
    A->>B: Transfer complete
    A->>S: Bye
    S->>B: Peer left notification
    B->>S: Bye
```

## One-off File Transfer (`gmmff send`)

For simple file transfers without interactive REPL:

### User-Facing Steps

```bash
# Peer A (sender)
gmmff send document.pdf --message "Here's the document"
# Output: Created session: jkl-mno-pqr
#         Share this code with your peer: dog-cat-bird
#         Waiting for peer...
#         Peer connected!
#         Sending document.pdf (1.2 MB)...
#         Transfer complete and verified.

# Peer B (receiver)
gmmff join dog-cat-bird
# Accept document.pdf? [y/N] y
# Receiving document.pdf (1.2 MB)...
# Transfer complete.
# Session ended.
```

### Internal Behavior
1. Creates a session with slot type "files" and max peers 2
2. Waits for exactly one peer to join (slot.ready)
3. Streams file(s) over WebRTC data channel:
   - Single file: streams directly from disk
   - Multiple files/directory: creates in-memory zip stream
4. Attaches optional message to transfer
5. Verifies transfer via SHA-256 hash
6. Automatically exits after verification

### Internal Signaling Flow (Mermaid)
```mermaid
sequenceDiagram
    participant S as Sender
    participant R as Receiver
    participant Sig as Signaling Server

    S->>Sig: Connect WebSocket
    R->>Sig: Connect WebSocket
    S->>Sig: CreateSlot(files, 2)
    Sig->>S: SlotCreated (code: jkl-mno-pqr)
    S->>R: Share code (out-of-band)
    R->>Sig: JoinSlot(jkl-mno-pqr)
    Sig->>R: SlotReady (type: files)
    S->>Sig: Wait SlotReady
    Sig->>S: SlotReady (type: files)
    S->>R: PAKE exchange (4 messages)
    R->>S: SDP offer/answer exchange
    S->>R: ICE candidate exchange
    S->>R: WebRTC data channel open
    S->>R: File metadata + encrypted chunks
    R->>S: File acceptance prompt (via UI)
    R->>S: File accepted
    S->>R: Remaining file chunks
    R->>S: Transfer complete (hash verification)
    S->>Sig: Bye
    Sig->>R: Peer left notification
    R->>Sig: Bye
```

## Chat Session

For pure text communication:

### User-Facing Steps

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

### Internal Behavior
1. Creates a session with slot type "chat"
2. Waits for peer to join via `gmmff join <code>`
3. Establishes WebRTC data channel for text only
4. Messages sent directly over data channel
5. Session ends when either peer types `\q` or connection is lost
6. No file transfer capabilities

### Internal Signaling Flow (Mermaid)
```mermaid
sequenceDiagram
    participant A as Peer A (chat initiator)
    participant B as Peer B (chat joiner)
    participant S as Signaling Server

    A->>S: Connect WebSocket
    B->>S: Connect WebSocket
    A->>S: CreateSlot("chat", 2)
    S->>A: SlotCreated (code: stu-vwx-yzx)
    A->>B: Share code (out-of-band)
    B->>S: JoinSlot(stu-vwx-yzx)
    S->>B: SlotReady (type: chat)
    A->>S: Wait SlotReady
    S->>A: SlotReady (type: chat)
    A->>B: PAKE exchange (4 messages)
    B->>A: SDP offer/answer exchange
    A->>B: ICE candidate exchange
    A->>B: WebRTC data channel open
    B->>A: WebRTC data channel open
    A->>B: Encrypted text message
    B->>A: Decrypt & display message
    B->>A: Encrypted text message
    A->>B: Decrypt & display message
    A->>S: Bye (on \q)
    S->>B: Peer left notification
    B->>S: Bye
```

## Local-Network Mode (`gmmff local`)

For environments without internet access:

### User-Facing Steps

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

### Internal Behavior
1. Starts embedded signaling server (WebSocket + HTTP)
2. Registers mDNS service `_gmmff._tcp.local.`
3. Serves browser UI at `http://<local-ip>:<port>`
4. Uses host candidates only for WebRTC (no STUN/TURN)
5. Optional self-signed TLS (disable with `--no-tls`)
6. All components run in single process
7. Supports file transfer and chat like normal mode

### Components Interaction (Mermaid)
```mermaid
graph TD
    A[gmmff local Process] --> B[Embedded Signaling Server]
    A --> C[mDNS Responder]
    A --> D[HTTP Server (Browser UI)]
    A --> E[WebRTC Peer Connection]
    
    B -->|WebSocket| F[Peer A Browser/UI]
    B -->|WebSocket| G[Peer B gmmff local]
    C -->|mDNS| G
    D -->|HTTP| F
    D -->|HTTP| G
    E -->|WebRTC Data Channel| F
    E -->|WebRTC Data Channel| G
    
    style A fill:#f9f,stroke:#333
```

## Schedule Mode (Encrypted Server-Side Transfers)

For scheduled, server-mediated transfers:

### User-Facing Steps

```bash
# Schedule an upload
gmmff schedule upload --local-path ./backup.zip --remote-path backups/weekly.zip --recur "@weekly"

# Schedule a download
gmmff schedule download --remote-path backups/weekly.zip --local-path ./latest.zip --recur "@daily"
```

### Internal Behavior
1. Client encrypts file with AES-256-GCM using random key
2. Uploads ciphertext to signaling server via HTTPS
3. Server returns share URL containing file ID
4. Decryption key stored in URL fragment (`#key=...`) - never sent to server
5. Recipient downloads ciphertext and decrypts locally using key from fragment
6. Supports TTL, max-downloads, password protection
7. Multiple files/directories zipped before encryption

### Upload Flow (Mermaid)
```mermaid
sequenceDiagram
    participant U as Uploader
    participant S as Signaling Server (HTTP)
    participant R as Receiver

    U->>S: POST /upload (encrypted file + metadata)
    S-->>U: 200 OK {fileID, shareURL, keyHex, expiresAt}
    U->>R: Share shareURL and keyHex (separate channels)
    R->>S: GET /download/{fileID}?dl=1
    S-->>R: 200 OK {encrypted file}
    R->>R: Decrypt file using keyHex from URL fragment
    R->>R: Verify integrity (GCM tag)
```

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
   - Use `--no-tls` in local mode for browser compatibility

See [Operations & Runbook](/openwiki/operations/runbook.md) for detailed troubleshooting.
