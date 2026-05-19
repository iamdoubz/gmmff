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