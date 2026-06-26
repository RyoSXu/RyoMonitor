<p align="center">
  <img src="app/assets/logo.svg" alt="RyoMonitor logo" width="120">
</p>

# RyoMonitor

[English](README.md) | [简体中文](README.zh-CN.md)

RyoMonitor is a lightweight self-hosted VPS monitor with a dark dashboard, password login, and zero build step.

It is built for small servers where a full monitoring stack is more than you need.

<p align="center">
  <img src="docs/screenshot.png" alt="RyoMonitor dashboard" width="900">
</p>

## Why RyoMonitor

- Lightweight Bash + Python runtime
- No database
- No frontend build step
- Single VPS deployment
- Password-protected dashboard
- Chinese and English web UI
- Git-based update workflow

## What It Shows

- CPU usage
- Memory and swap usage
- Disk usage
- Network throughput
- Load average
- Service status
- Top processes by memory usage

## How It Works

```text
ryo-monitor.service
  -> scripts/ryo-monitor.sh
  -> app/status.json

ryo-mon-auth.service
  -> app/mon-auth.py
  -> password login + static dashboard

Caddy
  -> HTTPS
  -> reverse_proxy 127.0.0.1:8090
```

## Files

```text
app/index.html              Dashboard UI
app/mon-auth.py             Password login and static file gateway
app/assets/logo.svg         Project logo and frontend icon
scripts/ryo-monitor.sh      Metrics collector
scripts/install.sh          First install helper
scripts/update.sh           Git pull + restart helper
systemd/*.service           systemd unit templates
caddy/Caddyfile.example     Caddy reverse proxy example
docs/screenshot.png         Dashboard screenshot
.env.example                Example environment variables
```

## Requirements

- Linux VPS with systemd
- Python 3.10+
- Bash
- Caddy
- Git, if you want GitHub-based updates

## Install

Clone the repository to `/opt/ryo-monitor`:

```bash
git clone https://github.com/RyoSXu/RyoMonitor.git /opt/ryo-monitor
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
