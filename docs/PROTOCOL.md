# Wire protocol

All signaling messages are JSON `{ "type": "...", "payload": { ... } }`.

## Slot creation

```
Client → Server:   { "type": "slot.create", "payload": { "protocol_version": "2", "session_type": "files|chat" } }
Server → Client:   { "type": "slot.created", "payload": { "slot_id": "...", "code": "<nameplate>", "ttl_seconds": 600, "session_type": "files|chat" } }
```

## Slot join

```
Client → Server:   { "type": "slot.join", "payload": { "code": "<nameplate>", "protocol_version": "2" } }
Server → both:     { "type": "slot.ready", "payload": { "role": "initiator|responder", "session_type": "files|chat" } }
```

`code` on the wire is always the 3-word nameplate (`word-word-word`). The
code users share is the nameplate plus a 2-word secret the initiator's client
generates (`word-word-word-word-word`); the secret is never sent to the
server and the CPace password is the full 5-word code (ADR-014). The server
rejects any `protocol_version` other than `"2"`.

The `session_type` in `slot.ready` lets `gmmff join` route automatically to
the correct REPL without the user needing to know what kind of session they
are joining.

## PAKE relay (opaque)

```
Client → Server:   { "type": "pake.a", "payload": { "data": "<base64>" } }
Server → peer:     { "type": "pake.a", "payload": { "data": "<base64>" } }
```

The same opaque relay applies to `pake.b`.  The server never decodes these.

## Signed SDP

```
Client → Server:   { "type": "sdp.offer", "payload": { "sdp": "<base64>", "mac": "<base64>" } }
Server → peer:     { "type": "sdp.offer", "payload": { "sdp": "<base64>", "mac": "<base64>" } }
```

`sdp` is the base64-encoded WebRTC `SessionDescription` JSON.  `mac` is the
base64-encoded HMAC-SHA256 over the raw SDP bytes, computed with the
appropriate HKDF subkey.  The same structure applies to `sdp.answer`.

## Data channel binary tags

Once a WebRTC data channel opens, all frames are binary with a one-byte tag
prefix. Sessions use two kinds of channels: a persistent **control channel**
and ephemeral **transfer channels** (one opened per file, named
`transfer-<timestamp>`).

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

## Error frames

```json
{ "type": "error", "payload": { "code": "ERR_SLOT_NOT_FOUND", "message": "slot not found..." } }
```

Error codes contain no user-identifying information and are safe to include
in bug reports.