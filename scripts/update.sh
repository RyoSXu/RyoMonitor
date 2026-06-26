#!/usr/bin/env bash
set -euo pipefail

PROJECT_DIR="${PROJECT_DIR:-/opt/ryo-monitor}"

cd "$PROJECT_DIR"

if command -v git >/dev/null 2>&1 && [ -d .git ]; then
  git pull --ff-only
fi

python3 -m py_compile app/mon-auth.py
bash -n scripts/ryo-monitor.sh

chmod 0755 app/mon-auth.py scripts/ryo-monitor.sh
systemctl daemon-reload
systemctl restart ryo-monitor.service ryo-mon-auth.service

systemctl is-active --quiet ryo-monitor.service
systemctl is-active --quiet ryo-mon-auth.service
curl -fsS --max-time 5 http://127.0.0.1:8090/healthz >/dev/null

echo "RyoMonitor updated successfully."
