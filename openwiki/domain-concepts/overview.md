---
type: Documentation
title: Domain Concepts Overview
description: Explains the core domain concepts in gmmff such as sessions, slots, PAKE, WebRTC data channels, and the slot lifecycle.
tags: [domain-concepts, architecture, session, slot, pake, webrtc]
verified:
  - by: openwiki/0.5.0
    at: 2026-09-05T11:33:48.919Z
sources:
  - id: openwiki-source-a2371d6362e5db4bc834ad03
    resource: repo://CLAUDE.md
  - id: openwiki-source-eb7ce360a58ffe4cced33e72
    resource: repo://docs/PROTOCOL.md
  - id: openwiki-source-3ec59b00d289a615f79b6f15
    resource: repo://docs/SECURITY.md
  - id: openwiki-source-d32161ee45da410429870c3e
    resource: repo://internal/pake/session.go
  - id: openwiki-source-c3138a2e20a1bb95abc1c522
    resource: repo://internal/session/session.go
  - id: openwiki-source-ce826a3573a98651b26c85cd
    resource: repo://internal/slot/slot.go
generated: { by: "openwiki/0.5.0", at: "2026-09-05T11:33:48.919Z" }
---
# Domain Concepts Overview

gmmff revolves around several core domain concepts that enable secure peer-to-peer communication:

## Session

A **session** represents a peer-to-peer file and message transfer session between two or more peers. A session is established when peers share a secret code and successfully complete the PAKE authentication and WebRTC handshake.

Key characteristics:
- Ephemeral: exists only for the duration of the peer connection
- Secure: all data transferred is encrypted end-to-end
- Multi-peer: supports 2-10 peers in a single session
- Interactive: provides a REPL for sending files and messages

## Slot

A **slot** is the server-side representation of a session waiting for peers to join. It lives in the signaling server's storage (Redis/Valkey or in-memory map) and tracks the state of peers attempting to establish a session.

Slot lifecycle (as defined in `internal/slot/slot.go`):
1. **WAITING** (`StateWaiting`): Created by first peer (`gmmff create`), waiting for peers to join
2. **ACTIVE** (`StateActive`): At least one peer has joined, still accepting new peers if not full
3. **FULL** (`StateFull`): Maximum peers reached, no longer accepting new joins
4. **CLOSED** (`StateClosed`): Session ended (peer disconnected, initiator left, or TTL expired)

Slot structure:
- ID: Unique identifier for the slot
- Code: Human-readable 3-word passphrase for authentication
- State: Current state (WAITING, ACTIVE, FULL, CLOSED)
- SessionType: Type of session ("files" or "chat")
- CreatedAt: Timestamp when slot was created
- ExpiresAt: Timestamp when slot expires (10 minutes after creation)
- InitiatorID: Connection ID of the peer that created the slot
- PeerIDs: WebSocket connection IDs of connected peers (excluding initiator)
- MaxPeers: Total number of participants allowed (initiator counts as 1)
- EverFull: Boolean set when slot first reaches MaxPeers; prevents reopening after peers leave

State transitions are validated before any storage write, ensuring the signaling server never persists invalid state.

## PAKE (Password Authenticated Key Exchange)

**PAKE** is the cryptographic protocol that allows two peers to establish a shared secret over an insecure channel (the signaling server) without revealing the secret to the server.

gmmff uses the **CPace** protocol over the ristretto255 group (`filippo.io/cpace`):
- Input: low-entropy secret (the 3-word code)
- Output: strong shared secret key
- Properties:
  - Mutual authentication: both peers verify they know the same secret
  - Key derivation: generates cryptographic keys for subsequent encryption
  - Server oblivious: server sees only protocol messages, cannot derive secret
  - Resistant to offline dictionary attacks

The PAKE secret is used to:
1. Derive two subkeys via HKDF-SHA256:
   - `offerKey` = HKDF(sharedKey, salt="gmmff-v1", info="sdp-offer-mac")
   - `answerKey` = HKDF(sharedKey, salt="gmmff-v1", info="sdp-answer-mac")
2. The initiator signs the SDP offer with `offerKey` and verifies the answer with `answerKey`
3. The responder does the reverse
4. This cryptographically binds the WebRTC SDP exchange to the shared secret, preventing a compromised signaling server from substituting its own SDP fingerprints

## WebRTC Data Channel

Once peers have established a shared secret via PAKE, they establish a direct **WebRTC data channel** for transferring files and messages.

Key properties:
- **Peer-to-peer**: data flows directly between peers after initial signaling
- **Encrypted**: DTLS 1.3 provides encryption and authentication (Pion's implementation)
- **Ordered/Unordered**: can configure reliability per channel
- **Congestion controlled**: uses UDP-based congestion control (similar to TCP)
- **Message-oriented**: preserves message boundaries (unlike byte streams)

gmmff uses:
- **DTLS 1.3** for encryption (standard WebRTC security)
- **SCTP over DTLS** for data transport
- Separate data channels:
  - Persistent **control channel** (carries TagMessage, TagTransferAnnounce, etc.)
  - Ephemeral **transfer channels** (one opened per file, named `transfer-<ulid>`)

## Wire Protocol and Data Channel Frames

All signaling messages are JSON `{ "type": "...", "payload": { ... } }` as defined in `docs/PROTOCOL.md`.

Once a WebRTC data channel opens, all frames are binary with a one-byte tag prefix. The wire protocol is **frozen** - changing tag values breaks every deployed client against every deployed server.

Data channel binary tags:
| Tag | Direction | Channel | Meaning |
|-----|-----------|---------|---------|
| `0x01` | sender → receiver | transfer | File header (JSON: name, size, SHA-256, chunk count, optional message) |
| `0x02` | sender → receiver | transfer | Chunk (8-byte seq + payload) |
| `0x03` | receiver → sender | transfer | Chunk ack (8-byte seq) |
| `0x04` | sender → receiver | transfer | Transfer done |
| `0x05` | receiver → sender | transfer | Transfer OK (hash verified) |
| `0x06` | either direction | transfer | Transfer error |
| `0x07` | receiver → sender | transfer | Resume from chunk N (8-byte seq) |
| `0x08` | either direction | either | Cancelled |
| `0x09` | either direction | control | Chat / session message (UTF-8 text) |
| `0x0A` | initiator → all | control | Chat close — ends chat session for everyone |
| `0x0B` | any participant | control | Participant leave — quiet exit; session continues |
| `0x0C` | either direction | control | Session ready |
| `0x0D` | sender → receiver | control | Transfer announce (channel label) |
| `0x0E` | receiver → sender | control | Transfer accepted (channel label) |
| `0x0F` | initiator → all | control | Session close — ends file session for everyone |
| `0x10` | reserved for future use | | |
| `0x11` | reserved for future use | | |

## Filename Sanitization

Any filename arriving from a peer must pass through `sanitiseName` before it touches the filesystem. It strips:
- Path separators (`/`, `\`)
- Null bytes
- `..` traversal sequences

Both the Wasm receiver (`ReceiveStateMem`) and the CLI receiver (`ReceiveState`) call it before `filepath.Join`. This prevents path traversal attacks and is a critical security boundary.
