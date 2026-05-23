# STUN configuration

`--stun` is repeatable. User-supplied servers **append** to the default Google
STUN server — the default is always present as a baseline.

```bash
# Add one more STUN server alongside the default
gmmff create --stun stun:mystun.example.com:3478

# Add two more
gmmff create \
  --stun stun:stun1.example.com:3478 \
  --stun stuns:stun2.example.com:5349

# Via environment variable (comma-separated)
export GMMFF_STUN=stun:stun1.example.com:3478,stuns:stun2.example.com:5349
```

---

# TURN configuration

TURN servers are specified in a single string with auth embedded as query
parameters. Maximum 3 TURN servers. Mixing auth types across servers is
fully supported.

## URL format

```
turn:host:port[?transport=udp|tcp][&user=u&pass=p]
turn:host:port[?transport=udp|tcp][&secret=s]
turns:host:port[?transport=tcp][&user=u&pass=p]
turns:host:port[?transport=tcp][&secret=s]
```

## Long-term credentials (username + password)

```bash
gmmff create --turn "turn:turn.example.com:3478?user=alice&pass=s3cr3t"

# With transport hint and TLS
gmmff create --turn "turns:turn.example.com:5349?transport=tcp&user=alice&pass=s3cr3t"
```

## Ephemeral credentials (coturn static-auth-secret)

Credentials are derived via RFC 8489 §9.2 (HMAC-SHA1) and expire after 24 hours.

```bash
gmmff create --turn "turn:turn.example.com:3478?transport=udp&secret=mystaticsecret"
```

## Mixed auth types across servers

```bash
# Local ephemeral (UDP) + remote long-term (TCP/TLS)
gmmff create \
  --turn "turn:local.host:3478?transport=udp&secret=abc" \
  --turn "turns:paid.host:5349?transport=tcp&user=alice&pass=xyz"
```

## Via environment variable

```bash
export GMMFF_TURN="turn:local.host:3478?transport=udp&secret=abc,turns:paid.host:5349?user=alice&pass=xyz"
```

## Transport parameter

| Value | When to use |
|-------|-------------|
| `transport=udp` | Prefer UDP — lower latency, works in most networks |
| `transport=tcp` | Prefer TCP — better through strict firewalls |
| *(omitted)* | coturn tries both automatically |
| `turns:` scheme | Always TLS/TCP — `transport=tcp` implied |

## TURN validation errors

```
turn: too many servers — maximum is 3, got 4
turn: "turn:host:port" has no credentials — provide user+pass or secret
turn: "turn:host:port" has user or pass but not both
turn: turns: scheme requires TCP/TLS — transport=udp is not valid
turn: URL must begin with turn: or turns:
```
---

## Server-side ICE push (`GMMFF_PUSH_STUN` / `GMMFF_PUSH_TURN`)

When enabled, the server pushes its configured STUN and/or TURN servers directly to every peer before a session starts, via a per-session `GET /api/ice` request. Pushed servers **replace** any user-defined ICE servers entirely.

### Recommended companion settings

```bash
GMMFF_PUSH_STUN=true
GMMFF_PUSH_TURN=true

# Lock down the ICE panel so users cannot override pushed servers:
GMMFF_SHOW_ICE_SETTINGS=false
GMMFF_ALLOW_STUN=false
GMMFF_ALLOW_TURN=false
```

Hiding the ICE settings panel is strongly recommended when using push — there is no point exposing configuration controls that have no effect.

### Credential security

**Ephemeral credentials (`secret=` parameter):** The master shared secret never leaves the server. A 30-minute HMAC-SHA1 credential is derived per `/api/ice` request. This is the recommended and secure configuration:

```bash
GMMFF_TURN=turn:your-server:3478?transport=tcp&secret=your-secret
GMMFF_PUSH_TURN=true
```

**Static credentials (`user=` / `pass=` parameters):** The username and password are sent to all peers in plaintext over HTTPS. Only use this with public or anonymous TURN servers where credential exposure is acceptable. The server logs a warning at startup when static credentials are pushed:

```
⚠  TURN push enabled — TURN credentials will be sent to all peers
```

### How it works

1. Peer opens the browser → `/config.json` returns `push_stun: true` and/or `push_turn: true`
2. User clicks "Start session" or "Join with a code"
3. Browser fetches `GET /api/ice` — server generates fresh ephemeral TURN credentials (30-min TTL) and returns them
4. Browser passes the resolved ICE config to the Wasm peer connection — user-defined servers are ignored entirely
5. WebRTC negotiation proceeds with the server-mandated ICE config

The `/api/ice` request is made per-session, not cached, so each session gets fresh ephemeral credentials with a clean 30-minute window.
