# Configuring nginx as a reverse proxy for gmmff

gmmff's signaling server speaks plain HTTP internally on `localhost:8080`.
nginx sits in front of it, terminates TLS, and forwards WebSocket connections
through.  This is the recommended production setup.

---

## Prerequisites

- nginx installed (`sudo apt install nginx`)
- A domain name pointed at your server (e.g. `signal.yourdomain.com`)
- Certbot installed for Let's Encrypt certificates (`sudo apt install certbot python3-certbot-nginx`)
- gmmff running via systemd on port `8080` (see [SYSTEMD.md](SYSTEMD.md))

---

## 1. Issue a TLS certificate

```bash
sudo certbot certonly --nginx -d signal.yourdomain.com
```

Certbot will write the certificate and key to:

```
/etc/letsencrypt/live/signal.yourdomain.com/fullchain.pem
/etc/letsencrypt/live/signal.yourdomain.com/privkey.pem
```

Auto-renewal is handled by the `certbot.timer` systemd unit installed with
Certbot — no manual intervention needed.

---

## 2. Install the nginx config

Copy the provided config and substitute your domain:

```bash
sudo cp configs/gmmff.conf /etc/nginx/sites-available/gmmff.conf
sudo sed -i 's/your.domain.com/signal.yourdomain.com/g' /etc/nginx/sites-available/gmmff.conf
```

Enable the site:

```bash
sudo ln -s /etc/nginx/sites-available/gmmff.conf /etc/nginx/sites-enabled/
```

Test and reload:

```bash
sudo nginx -t          # must print "syntax is ok"
sudo systemctl reload nginx
```

---

## 3. Connect clients

```bash
gmmff send myfile.zip --server wss://signal.yourdomain.com/ws
gmmff receive word-word-word --server wss://signal.yourdomain.com/ws
```

Or set it once in your environment:

```bash
export GMMFF_SERVER=wss://signal.yourdomain.com/ws
```

---

## Key WebSocket directives explained

The three lines that make WebSocket work through nginx are:

```nginx
proxy_http_version 1.1;
proxy_set_header Upgrade    $http_upgrade;
proxy_set_header Connection "upgrade";
```

nginx's default proxy mode uses HTTP/1.0, which does not support the
`Connection: Upgrade` mechanism that WebSockets require.  Setting
`proxy_http_version 1.1` and forwarding the `Upgrade` header tells nginx
to pass the upgrade handshake through to gmmff rather than consuming it.
Every WebSocket-behind-nginx failure traces back to one of these three lines
being missing.

### Timeout settings

```nginx
proxy_read_timeout  90s;
proxy_send_timeout  90s;
```

nginx's default `proxy_read_timeout` is 60 seconds — exactly equal to
gmmff's `pongWait`.  If a peer is connected and waiting for the other side
to join, nginx would close the connection at 60 seconds before the ping/pong
cycle has a chance to keep it alive.  Setting 90 seconds provides headroom.

### Buffering

```nginx
proxy_buffering off;
```

WebSocket frames must flow through immediately.  With buffering on, nginx
holds data in memory before forwarding it, which introduces latency and
can cause frames to be lost if the buffer fills.

---

## Privacy note

The provided config intentionally omits `X-Real-IP` and `X-Forwarded-For`
headers from the proxy configuration:

```nginx
# Do NOT forward the real IP — gmmff is privacy-focused and does not
# log or use client IPs.
```

gmmff's logging never records IP addresses.  Forwarding them to the backend
would be pointless and contrary to the project's privacy goals.

---

## Restricting access to internal endpoints

The `/metrics` endpoint exposes operational counters and should not be
publicly accessible.  Uncomment and adjust the `allow`/`deny` block in
`gmmff.conf` to restrict it to your monitoring system:

```nginx
location /metrics {
    allow 10.0.0.5;   # your monitoring server
    deny all;
    proxy_pass http://gmmff_backend;
    ...
}
```

The same pattern applies to `/healthz` and `/readyz` if you want to lock
those down to your load balancer or uptime monitor only.

---

## Verifying the proxy

```bash
# Health check through nginx (plain HTTP redirects to HTTPS)
curl -L https://signal.yourdomain.com/healthz     # → ok

# WebSocket connection test (requires wscat: npm install -g wscat)
wscat -c wss://signal.yourdomain.com/ws
# Once connected, paste:
# {"type":"slot.create","payload":{"protocol_version":"1"}}
# Should respond with slot.created payload.
```

---

## Troubleshooting

**`502 Bad Gateway`** — gmmff is not running or not listening on port 8080.
Check `sudo systemctl status gmmff` and `sudo journalctl -u gmmff -f`.

**`101 Switching Protocols` not returned** — the `Upgrade` headers are missing
or nginx is not using HTTP/1.1 for the proxy connection.  Double-check the
three WebSocket directives in the `/ws` location block.

**Connection drops after ~60 seconds** — `proxy_read_timeout` is too low.
Make sure it is set to at least `90s` in the `/ws` location block.

**`SSL_ERROR_RX_RECORD_TOO_LONG`** — the client is connecting with `ws://`
instead of `wss://`.  The server only accepts HTTPS/WSS on port 443.
