# RyoMonitor

<p align="center">
  <img src="app/assets/logo.svg" alt="RyoMonitor logo" width="120">
</p>

[English](README.md) | [简体中文](README.zh-CN.md)

RyoMonitor is a lightweight self-hosted VPS monitor with a light/dark dashboard, password login, and no frontend build step.

It is built for small servers where a full monitoring stack is more than you need.

<p align="center">
  <img src="docs/screenshot-light.png" alt="RyoMonitor dashboard (light)" width="49%">
  <img src="docs/screenshot-dark.png" alt="RyoMonitor dashboard (dark)" width="49%">
</p>

## Why RyoMonitor

- Single Go binary, standard library only, no runtime deps
- No database
- No frontend build step
- Single VPS deployment
- Password-protected dashboard (with an optional trusted-reverse-proxy mode)
- Chinese and English web UI, with a light/dark theme toggle
- Installable to the iOS / Android home screen (PWA icons + web manifest)
- Git-based update workflow

## Footprint

RyoMonitor is intentionally small. On the current VPS deployment:

```text
Binary size: about 7 MB
Runtime memory: about 12 MB RSS (single Go process: collector + auth gateway + static)
status.json: served from memory, never written to disk
Database: none
Frontend build: none
```

## What It Shows

- CPU usage
- Memory and swap usage
- Disk usage
- Network throughput
- Load average
- Uptime
- Service status (systemd units and Docker containers)
- Top processes by memory usage

## How It Works

```text
ryo-monitor.service
  -> bin/ryo-monitor (single Go binary)
       background goroutine: collect metrics into memory every second
       HTTP server: password login + static dashboard + /status.json

Caddy
  -> HTTPS
  -> reverse_proxy 127.0.0.1:8090
```

> Since v2 the backend is rewritten in Go: the collector and the auth gateway are
> merged into a single binary and a single systemd service, with no Python / Bash
> dependency. status.json is served straight from memory (never written to disk).

## Files

```text
app/index.html              Dashboard UI
app/assets/logo.svg         Project logo and frontend icon
app/assets/*.png            Home-screen / PWA icons (apple-touch-icon, 192, 512)
app/assets/site.webmanifest Web app manifest (add-to-home-screen metadata)
cmd/ryo-monitor/main.go     Backend: collector + auth gateway (Go)
cmd/ryo-monitor/login.html  Login page (embedded into the binary)
bin/ryo-monitor             Build output (git-ignored)
scripts/install.sh          First install helper
scripts/update.sh           Git pull + rebuild + restart helper
systemd/ryo-monitor.service systemd unit template
caddy/Caddyfile.example     Caddy reverse proxy example
docs/screenshot-*.png       Dashboard screenshots (light / dark theme)
.env.example                Example environment variables
```

## Requirements

- Linux VPS with systemd
- Caddy
- Git, if you want GitHub-based updates
- Go 1.22+ to build the backend (installed locally, or build with the Docker `golang:1-alpine` image)

## Build

With Go installed locally:

```bash
CGO_ENABLED=0 go build -ldflags='-s -w' -o bin/ryo-monitor ./cmd/ryo-monitor
```

Or build with Docker (no local Go needed):

```bash
docker run --rm -v "$PWD":/src -w /src golang:1-alpine \
  sh -c "CGO_ENABLED=0 go build -ldflags='-s -w' -o bin/ryo-monitor ./cmd/ryo-monitor"
```

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
/etc/ryo-monitor.env
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

The update script runs `git pull --ff-only`, rebuilds the Go binary, restarts the service, and checks the health endpoint.

## Configuration

All environment variables live in a single file (the installer generates it with `bin/ryo-monitor genenv <password>`):

```text
/etc/ryo-monitor.env
```

Example:

```bash
MON_AUTH_HOST=127.0.0.1
MON_AUTH_PORT=8090
MON_AUTH_WEB_ROOT=/opt/ryo-monitor/app
MON_AUTH_SESSION_TTL=604800
MON_AUTH_PASSWORD_HASH=pbkdf2_sha256$260000$<salt>$<hash>
MON_AUTH_SECRET=<random>
RYO_MONITOR_IFACE=eth0
RYO_MONITOR_SERVICES="OpenList=openlist Caddy=caddy SSH=ssh"
```

| Variable | Purpose |
| --- | --- |
| `MON_AUTH_HOST` / `MON_AUTH_PORT` | Address the gateway binds to (keep on `127.0.0.1`). |
| `MON_AUTH_WEB_ROOT` | Directory served as the dashboard (`app/`). |
| `MON_AUTH_SESSION_TTL` | Login session lifetime in seconds. |
| `MON_AUTH_PASSWORD_HASH` / `MON_AUTH_SECRET` | Login password hash and session-signing secret. |
| `MON_AUTH_TRUST_PROXY` | Set to `1` to trust an upstream reverse proxy / SSO and skip the built-in login (then `HASH`/`SECRET` may be empty). |
| `MON_AUTH_COOKIE` | Session cookie name (default `ryo_mon_session`). |
| `RYO_MONITOR_IFACE` | Network interface used for throughput. |
| `RYO_MONITOR_SERVICES` | Service pills to show (see below). |

### Trusting a Reverse Proxy / SSO

If authentication is already handled in front of RyoMonitor (e.g. Caddy `forward_auth`, an SSO portal, or a private network), set:

```bash
MON_AUTH_TRUST_PROXY=1
```

RyoMonitor then skips its own login page and serves the dashboard directly. Only enable this when access is already restricted upstream.

### Custom Service Checks

RyoMonitor checks systemd services by default. Configure the dashboard service pills with `RYO_MONITOR_SERVICES`:

```bash
RYO_MONITOR_SERVICES="Nginx=nginx Docker=docker PostgreSQL=postgresql"
```

Each item uses this format:

```text
DisplayName=systemd-unit-name
```

The display name is shown as-is in the dashboard. The unit name is passed to:

```bash
systemctl is-active <unit>
```

To monitor a **Docker container** instead of a systemd unit, prefix the name with `docker:`:

```bash
RYO_MONITOR_SERVICES="Caddy=caddy MyApp=docker:myapp"
```

`docker:<name>` is checked via the Docker socket (`/var/run/docker.sock`) and shown as active when the container is running.

## Security Notes

- Keep `MON_AUTH_SECRET` private.
- Keep `/etc/ryo-monitor.env` out of Git.
- Bind the auth gateway to `127.0.0.1`.
- Expose the dashboard only through Caddy HTTPS.
- Rotate the password by regenerating `/etc/ryo-monitor.env` and restarting `ryo-monitor.service`.
