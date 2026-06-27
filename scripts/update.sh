#!/usr/bin/env bash
set -euo pipefail

PROJECT_DIR="${PROJECT_DIR:-/opt/ryo-monitor}"
BIN="$PROJECT_DIR/bin/ryo-monitor"

cd "$PROJECT_DIR"

if command -v git >/dev/null 2>&1 && [ -d .git ]; then
  git pull --ff-only
fi

if command -v go >/dev/null 2>&1; then
  CGO_ENABLED=0 go build -ldflags='-s -w' -o "$BIN" ./cmd/ryo-monitor
else
  docker run --rm -v "$PROJECT_DIR":/src -w /src golang:1-alpine \
    sh -c "CGO_ENABLED=0 go build -ldflags='-s -w' -o bin/ryo-monitor ./cmd/ryo-monitor"
fi

install -m 0644 systemd/ryo-monitor.service /etc/systemd/system/ryo-monitor.service
systemctl daemon-reload
systemctl restart ryo-monitor.service
systemctl is-active --quiet ryo-monitor.service

for _ in $(seq 1 10); do
  if curl -fsS --max-time 5 http://127.0.0.1:8090/healthz >/dev/null; then
    echo "RyoMonitor updated successfully."
    exit 0
  fi
  sleep 1
done

echo "RyoMonitor health check failed." >&2
exit 1
