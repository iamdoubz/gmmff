<p align="center">
  <img src="imgs/architecture.png" alt="A diagram explaining the high level design of gmmff">
</p>

# Signaling Server Architecture

## Overview

The signaling server is a stateless (from the perspective of peer data),
horizontally-scalable WebSocket broker.  Its single responsibility is
**rendezvous**: given two peers who share a secret code, link them so they can
exchange the messages needed to establish a direct WebRTC connection.

Once the WebRTC data channel is open, the signaling server is completely
out of the loop. File data never passes through it.

---

## Concurrency model

```
                ┌───────────────────────────────────────────────┐
                │                   Hub goroutine               │
                │  owns: conns map, slot dispatch               │
                │  communicates via: register/unregister/inbound│
                └────────────┬──────────────────────────────────┘
                             │  channels (no shared memory)
          ┌──────────────────┼──────────────────────┐
          │                  │                      │
    readPump(A)         readPump(B)           writePump(A/B)
    (one goroutine      (one goroutine        (one per conn)
     per conn)          per conn)
```

- The hub goroutine is the **only** place that reads or writes the `conns` map
  and dispatches slot operations.  No mutexes are needed for these structures.
- Each connection has a buffered `send` channel (`sendBufSize = 16`).
  `writePump` drains it; `readPump` feeds the hub's `inbound` channel.
- If a peer's send buffer fills (slow network, misbehaving client), frames are
  dropped with an `ERR_SEND_BUFFER_FULL` warning.  This is preferable to
  blocking the hub.

---

## Redis / Valkey storage layout

> The store is any RESP-compatible server. **Redis** and **Valkey** are both
> supported — Valkey is a wire-compatible drop-in, so the `go-redis` client and
> the key layout below are identical for either. Pick one via `GMMFF_REDIS_URL`
> (a `valkey://` URL is accepted as an alias for `redis://`).

```
Key              Type     TTL      Value
────────────────────────────────────────────────────────────────
slot:<uuid>      String   10 min   JSON-encoded Slot struct
code:<word-word-word>  String   10 min   slot UUID
```

Both keys share the same TTL so expiry is consistent.  The `store.Create`
pipeline sets both atomically.

The `MemStore` fallback (`--memory` flag) is backed by plain Go maps.  It
provides no TTL enforcement and is single-node only — use only for local
development.

---

## Slot lifecycle

```
      slot.create
         │
         ▼
    ┌─────────┐      slot.join        ┌───────┐     bye / expire
    │ WAITING │ ──────────────────► │ READY │ ──────────────────► CLOSED
    └─────────┘                     └───────┘
         │
         │ TTL expires (no join)
         ▼
      CLOSED (auto-reaped by Redis TTL)
```

State transitions are validated in `internal/slot/slot.go` before any store
write, ensuring the broker never persists an invalid state.

---

## Security properties

| Property | Mechanism |
|----------|-----------|
| Server cannot read file data | All file transfer is WebRTC data channel, post-signaling |
| Server cannot decrypt PAKE | PAKE messages are base64-opaque; server forwards without parsing |
| Slot code brute-force resistance | 32-bit entropy + 10-min TTL + no enumeration endpoint |
| No user-identifying logs | Privacy logger strips IPs, UAs, file names, codes from all log lines |
| Memory exhaustion protection | `maxMessageBytes = 64 KiB` per frame; `sendBufSize = 16` per conn |
| Slow-read protection | `writeWait = 10s`, `pongWait = 60s` per WebSocket connection |
| Injection protection | All slot codes are validated against a format regex before store lookup |

---

## Horizontal scaling

Because all hot state lives in Redis, multiple `gmmff` instances can run
behind a load balancer.

**Important**: WebSocket connections are long-lived.  Two peers in the same
slot must land on the **same server instance** for the in-memory `conns` map
to route messages correctly.

Solutions (choose one):
1. **Sticky sessions** at the load balancer (easiest, recommended for small
   deployments).
2. **Redis pub/sub relay** (Phase 3 enhancement): when the hub cannot find
   a peer in `conns`, it publishes the message to a Redis channel keyed by
   `conn_id`.  Every instance subscribes and delivers if it holds that conn.

---

## Operational runbook

### Liveness vs readiness

- `/healthz` — always 200 if the process is alive.  Use for Kubernetes
  `livenessProbe`.
- `/readyz` — 200 only if Redis is reachable.  Use for `readinessProbe` to
  drain traffic during Redis unavailability.

### Graceful shutdown

`SIGTERM` or `SIGINT` triggers a 15-second graceful shutdown window.
In-flight HTTP requests and WebSocket connections are allowed to complete.
After 15 seconds, remaining connections are force-closed.

### Metrics

`GET /metrics` returns JSON counters safe to scrape with a simple HTTP check.
For production, add a Prometheus exporter (Phase 3) — the counter fields are
already `atomic.Int64` and trivially exportable.
