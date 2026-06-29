#!/usr/bin/env bash
# 统一构建：若 /etc/ryo-monitor.env 含 MON_AUTH_TRUST_PROXY=1，则裁掉内置鉴权（-tags trustproxy）。
set -euo pipefail

PROJECT_DIR="${PROJECT_DIR:-/opt/ryo-monitor}"
BIN="${BIN:-$PROJECT_DIR/bin/ryo-monitor}"
ENV_FILE="${ENV_FILE:-/etc/ryo-monitor.env}"

TAGS=""
if [ -f "$ENV_FILE" ] && grep -qE '^MON_AUTH_TRUST_PROXY=1' "$ENV_FILE"; then
  TAGS="trustproxy"
fi

LDFLAGS='-s -w'
BUILD=(CGO_ENABLED=0 go build -ldflags="$LDFLAGS" -o "$BIN" ./cmd/ryo-monitor)
if [ -n "$TAGS" ]; then
  BUILD=(CGO_ENABLED=0 go build -tags "$TAGS" -ldflags="$LDFLAGS" -o "$BIN" ./cmd/ryo-monitor)
fi

cd "$PROJECT_DIR"

if command -v go >/dev/null 2>&1; then
  echo "Building ryo-monitor${TAGS:+ (tags: $TAGS)} ..."
  "${BUILD[@]}"
else
  echo "Building ryo-monitor via Docker${TAGS:+ (tags: $TAGS)} ..."
  docker run --rm -v "$PROJECT_DIR":/src -w /src golang:1-alpine \
    sh -c "CGO_ENABLED=0 go build ${TAGS:+-tags $TAGS} -ldflags='$LDFLAGS' -o bin/ryo-monitor ./cmd/ryo-monitor"
fi
