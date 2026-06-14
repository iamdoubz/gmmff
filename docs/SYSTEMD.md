# Setting up a dedicated system user for gmmff

Running gmmff under a dedicated unprivileged system user is strongly recommended
for production deployments.  It limits what the process can access if it is ever
compromised, and integrates cleanly with systemd's security hardening directives.

---

## Create the user and group

```bash
sudo useradd \
  --system \
  --no-create-home \
  --shell /usr/sbin/nologin \
  --comment "gmmff signaling server" \
  gmmff
```

| Flag | Purpose |
|------|---------|
| `--system` | Allocates a UID below 1000 — marked as a service account, not a login user |
| `--no-create-home` | No home directory is created — gmmff does not need one |
| `--shell /usr/sbin/nologin` | Prevents anyone from logging in as this user interactively |

---

## Install the binary

```bash
sudo cp gmmff-linux-amd64 /usr/local/bin/gmmff
sudo chmod 755 /usr/local/bin/gmmff
sudo chown root:root /usr/local/bin/gmmff
```

The binary is owned by `root` and only executable — the `gmmff` user can run it
but cannot modify or replace it.

---

## Create the configuration directory

```bash
sudo mkdir -p /etc/gmmff
sudo cp configs/.env.example /etc/gmmff/gmmff.env

# Only root can write it; gmmff user can read it.
sudo chown root:gmmff /etc/gmmff/gmmff.env
sudo chmod 640 /etc/gmmff/gmmff.env
```

Edit the configuration before starting the service:

```bash
sudo nano /etc/gmmff/gmmff.env
```

See `configs/.env.example` for all available options.

---

## Create the log directory

```bash
sudo mkdir -p /var/log/gmmff
sudo chown gmmff:gmmff /var/log/gmmff
sudo chmod 750 /var/log/gmmff
```

---

## Redis / Valkey Unix socket access (optional)

The store may be Redis or Valkey — Valkey is a wire-compatible drop-in, so the
steps below are identical (substitute `valkey-server` / `valkey-cli` and the
`valkey` group/socket path if you run Valkey).

If you are connecting via a Unix socket (`unix:///var/run/redis/redis.sock`),
the `gmmff` user needs to be in the `redis` group to access the socket file:

```bash
sudo usermod -aG redis gmmff
```

Verify the socket is reachable:

```bash
sudo -u gmmff redis-cli -s /var/run/redis/redis.sock ping
# → PONG
```

---

## Install and enable the systemd service

```bash
sudo cp configs/gmmff.service /etc/systemd/system/gmmff.service
sudo systemctl daemon-reload
sudo systemctl enable gmmff
sudo systemctl start gmmff
```

Check that it started cleanly:

```bash
sudo systemctl status gmmff
sudo journalctl -u gmmff -f
```

---

## Day-to-day operations

All configuration changes are made by editing `/etc/gmmff/gmmff.env` — you
never need to touch the service file itself.

```bash
# Edit configuration
sudo nano /etc/gmmff/gmmff.env

# Apply changes
sudo systemctl reload-or-restart gmmff

# View live logs
sudo journalctl -u gmmff -f

# Stop / start
sudo systemctl stop gmmff
sudo systemctl start gmmff

# Check startup-on-boot status
sudo systemctl is-enabled gmmff
```

---

## Verifying the service is healthy

```bash
curl http://localhost:8080/healthz   # → ok
curl http://localhost:8080/readyz    # → ok  (503 if Redis is unreachable)
curl http://localhost:8080/metrics   # → JSON operational counters
```

---

## Removing the user (if uninstalling)

```bash
sudo systemctl stop gmmff
sudo systemctl disable gmmff
sudo rm /etc/systemd/system/gmmff.service
sudo systemctl daemon-reload

sudo userdel gmmff
sudo rm -rf /etc/gmmff /var/log/gmmff
sudo rm /usr/local/bin/gmmff
```
