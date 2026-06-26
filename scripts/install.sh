#!/usr/bin/env bash
set -euo pipefail

PROJECT_DIR="${PROJECT_DIR:-/opt/ryo-monitor}"
DOMAIN="${DOMAIN:-mon.example.com}"
AUTH_ENV="/etc/ryo-mon-auth.env"
COLLECTOR_ENV="/etc/ryo-monitor.env"

if [ "$(id -u)" -ne 0 ]; then
  echo "install.sh must be run as root" >&2
  exit 1
fi

cd "$PROJECT_DIR"

install -m 0755 app/mon-auth.py "$PROJECT_DIR/app/mon-auth.py"
install -m 0755 scripts/ryo-monitor.sh "$PROJECT_DIR/scripts/ryo-monitor.sh"
install -m 0644 systemd/ryo-monitor.service /etc/systemd/system/ryo-monitor.service
install -m 0644 systemd/ryo-mon-auth.service /etc/systemd/system/ryo-mon-auth.service

mkdir -p "$PROJECT_DIR/app"
touch "$PROJECT_DIR/app/status.json"
chmod 0644 "$PROJECT_DIR/app/index.html" "$PROJECT_DIR/app/status.json"

if [ ! -f "$AUTH_ENV" ]; then
  read -r -s -p "RyoMonitor password: " password
  printf "\n"
  python3 - "$password" <<'PY' > "$AUTH_ENV"
import base64
import hashlib
import os
import secrets
import sys

password = sys.argv[1].encode("utf-8")
salt = os.urandom(16)
iterations = 260000
digest = hashlib.pbkdf2_hmac("sha256", password, salt, iterations)

def b64(raw):
    return base64.urlsafe_b64encode(raw).decode("ascii").rstrip("=")

print("MON_AUTH_HOST=127.0.0.1")
print("MON_AUTH_PORT=8090")
print("MON_AUTH_WEB_ROOT=/opt/ryo-monitor/app")
print("MON_AUTH_SESSION_TTL=604800")
print(f"MON_AUTH_PASSWORD_HASH=pbkdf2_sha256${iterations}${b64(salt)}${b64(digest)}")
print("MON_AUTH_SECRET=" + secrets.token_urlsafe(48))
PY
  chmod 0600 "$AUTH_ENV"
else
  echo "$AUTH_ENV already exists; keeping it."
fi

if [ ! -f "$COLLECTOR_ENV" ]; then
  cat > "$COLLECTOR_ENV" <<'EOF'
RYO_MONITOR_STATUS_FILE=/opt/ryo-monitor/app/status.json
RYO_MONITOR_IFACE=eth0
RYO_MONITOR_SERVICES="OpenList=openlist Caddy=caddy SSH=ssh"
EOF
  chmod 0644 "$COLLECTOR_ENV"
else
  echo "$COLLECTOR_ENV already exists; keeping it."
fi

systemctl daemon-reload
systemctl enable --now ryo-monitor.service ryo-mon-auth.service

cat <<EOF

Installed RyoMonitor.

Add this Caddy site and reload Caddy:

$DOMAIN {
    reverse_proxy 127.0.0.1:8090
}

EOF
