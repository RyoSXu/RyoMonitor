#!/usr/bin/env bash
set -euo pipefail

PROJECT_DIR="${PROJECT_DIR:-/opt/ryo-monitor}"
DOMAIN="${DOMAIN:-mon.example.com}"
ENV_FILE="/etc/ryo-monitor.env"
BIN="$PROJECT_DIR/bin/ryo-monitor"

if [ "$(id -u)" -ne 0 ]; then
  echo "install.sh must be run as root" >&2
  exit 1
fi

cd "$PROJECT_DIR"

# 构建二进制（若尚未构建）。需要本机 Go，或预先用 scripts/build.sh（支持 Docker）。
if [ ! -x "$BIN" ]; then
  if command -v go >/dev/null 2>&1 || command -v docker >/dev/null 2>&1; then
    bash "$PROJECT_DIR/scripts/build.sh"
  else
    echo "未找到已构建的 $BIN，且本机无 go/docker。" >&2
    echo "请先运行 scripts/build.sh 构建 bin/ryo-monitor。" >&2
    exit 1
  fi
fi

install -m 0644 systemd/ryo-monitor.service /etc/systemd/system/ryo-monitor.service
chmod 0644 "$PROJECT_DIR/app/index.html"

# 清理旧版（python/bash 双服务）遗留
if [ -f /etc/systemd/system/ryo-mon-auth.service ]; then
  systemctl disable --now ryo-mon-auth.service 2>/dev/null || true
  rm -f /etc/systemd/system/ryo-mon-auth.service
fi

if [ ! -f "$ENV_FILE" ]; then
  read -r -s -p "RyoMonitor password: " password
  printf "\n"
  "$BIN" genenv "$password" > "$ENV_FILE"
  chmod 0600 "$ENV_FILE"
else
  echo "$ENV_FILE already exists; keeping it."
fi

systemctl daemon-reload
systemctl enable --now ryo-monitor.service

cat <<EOF

Installed RyoMonitor (Go).

Add this Caddy site and reload Caddy:

$DOMAIN {
    reverse_proxy 127.0.0.1:8090
}

EOF
