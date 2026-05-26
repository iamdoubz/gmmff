# Local mode (no internet access required)

`gmmff local` is a fully self-contained mode that requires no external server,
no internet connection, and no configuration. It is designed for transferring
files between devices on the same Wi-Fi or LAN.

```bash
gmmff local
```

On startup it:
- Starts an embedded signaling server and web server
- Generates a self-signed TLS certificate automatically
- Registers on mDNS so other `gmmff local` instances on the network can find it
- Prints a QR code and URL encoding the session code

```
Scanning for other gmmff instances on the local network... none found.
Using port 51423
Starting embedded server... done.
Registering on mDNS... done.
Connecting to local broker... done.
Creating session... done.

╔══════════════════════════════════════════════════════╗
║  gmmff local mode                                    ║
╠══════════════════════════════════════════════════════╣
║  Server:   https://192.168.1.25:51423                ║
║  Code:     acid-lake-drum                            ║
║  Join URL: https://192.168.1.25:51423/?code=acid-... ║
╚══════════════════════════════════════════════════════╝

Scan this QR code to join:
[QR code]

Or open: https://192.168.1.25:51423/?code=acid-lake-drum&type=files&autoconnect=1&local=1

Waiting for first peer to connect...
```

Any device on the same network scans the QR code — the browser opens, the
session connects automatically, and the Files UI appears ready to receive.
No typing, no code entry, no external server.

**Browser compatibility in local mode:**

| Browser | TLS (default) | `--no-tls` |
|---------|--------------|------------|
| Chrome / Edge (desktop + Android) | Tap "Advanced → Proceed" once | ✅ works |
| Firefox | Accept the risk once | ✅ works |
| Safari / iOS Safari | ⚠ Cert must be trusted in Settings | ❌ Apple blocks non-tls webrtc connections |

For mixed-device sessions where Safari is *not* involved, use `--no-tls` — WebRTC
works over plain HTTP on local networks in Chrome and Firefox, but iOS Safari
requires HTTPS with a trusted certificate.

**Session control in local mode:**

| Command | Effect |
|---------|--------|
| `send <file\|dir>` | Send file(s) to all connected peers |
| `message <text>` | Send a text message |
| `\q` | End session and shut down the local server |

## Docker

If using docker to run local mode, we need to change a few things in the `docker-compose.yml`:

**Replace ports with network mode**:

This is needed because Docker's default bridge network (`docker0`) does not forward multicast traffic between containers or between containers and the host.

```yml
    network_mode: host
    #ports:
    #  - "8080:8080"
```

**Change the default command**:

The default command in the Dockerfile is `/gmmff serve --web /web/static`. Because we are using `local`, we must add a command line:

```yml
...
network_mode: host
command: /gmmff local --port 8080 #change port to what you want or omit entirely
...
```