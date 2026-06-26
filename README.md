# RyoMonitor

RyoMonitor is a small self-hosted server monitor with a dark web dashboard and a local authentication gateway.

It is designed for a single VPS:

- A Bash collector writes `status.json` once per second.
- A static dashboard reads `status.json`.
- A Python auth gateway serves the dashboard after password login.
- Caddy terminates HTTPS and reverse proxies to `127.0.0.1:8090`.

## Files

```text
app/index.html              Dashboard UI
app/mon-auth.py             Password login and static file gateway
scripts/ryo-monitor.sh      Metrics collector
scripts/install.sh          First install helper
scripts/update.sh           Git pull + restart helper
systemd/*.service           systemd unit templates
caddy/Caddyfile.example     Caddy reverse proxy example
.env.example                Example environment variables
```

## Requirements

- Linux VPS with systemd
- Python 3.10+
- Bash
- Caddy
- Git, if you want GitHub-based updates

## Install On A VPS

Clone the repository to `/opt/ryo-monitor`:

```bash
git clone https://github.com/YOUR_NAME/RyoMonitor.git /opt/ryo-monitor
cd /opt/ryo-monitor
```

Run the installer as root:

```bash
DOMAIN=mon.example.com bash scripts/install.sh
```

The installer asks for a login password and writes a hashed password plus a random signing secret to:

```text
/etc/ryo-mon-auth.env
```

Do not commit that file.

## Caddy

Add a site like this:

```caddyfile
mon.example.com {
    reverse_proxy 127.0.0.1:8090
}
```

Then validate and reload:

```bash
caddy validate --config /etc/caddy/Caddyfile
systemctl reload caddy
```

## Update

After pushing changes to GitHub, update the VPS with:

```bash
cd /opt/ryo-monitor
bash scripts/update.sh
```

The update script runs `git pull --ff-only`, checks Python and Bash syntax, restarts both services, and checks the auth gateway health endpoint.

## Services

```bash
systemctl status ryo-monitor.service
systemctl status ryo-mon-auth.service
```

`ryo-monitor.service` writes:

```text
/opt/ryo-monitor/app/status.json
```

`ryo-mon-auth.service` listens on:

```text
127.0.0.1:8090
```

## Configuration

Authentication environment:

```text
/etc/ryo-mon-auth.env
```

Optional collector environment:

```text
/etc/ryo-monitor.env
```

Example:

```bash
RYO_MONITOR_STATUS_FILE=/opt/ryo-monitor/app/status.json
RYO_MONITOR_IFACE=eth0
```

## Security Notes

- Keep `MON_AUTH_SECRET` private.
- Keep `/etc/ryo-mon-auth.env` out of Git.
- Bind the auth gateway to `127.0.0.1`.
- Expose the dashboard only through Caddy HTTPS.
- Rotate the password by regenerating `/etc/ryo-mon-auth.env` and restarting `ryo-mon-auth.service`.
