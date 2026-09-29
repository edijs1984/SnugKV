#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
BIN=/tmp/snugkv-cluster-client-smoke-bin
TMP=/tmp/snugkv-cluster-client-smoke

cleanup() {
  for pidfile in "$TMP"/node*.pid; do
    [[ -f "$pidfile" ]] || continue
    pid="$(cat "$pidfile")"
    kill "$pid" 2>/dev/null || true
    wait "$pid" 2>/dev/null || true
  done
  rm -f "$BIN"
}
trap cleanup EXIT

rm -rf "$TMP"
mkdir -p "$TMP"

cd "$ROOT"
go build -o "$BIN" ./cmd/snugkv

make_config() {
  local port="$1"
  local path="$2"
  cat >"$path" <<JSON
{
  "listen": "127.0.0.1:$port",
  "admin_listen": "",
  "metrics_listen": "",
  "cluster_enabled": true,
  "cluster_node_addr": "127.0.0.1:$port",
  "cluster_control_auth": "cluster-smoke-control-secret",
  "cluster_slots": {
    "0-5460": "127.0.0.1:7000",
    "5461-10922": "127.0.0.1:7001",
    "10923-16383": "127.0.0.1:7002"
  }
}
JSON
}

for port in 7000 7001 7002; do
  cfg="$TMP/node-$port.json"
  make_config "$port" "$cfg"
  "$BIN" -config "$cfg" >"$TMP/node-$port.log" 2>&1 &
  echo $! >"$TMP/node-$port.pid"
done

for port in 7000 7001 7002; do
  for _ in $(seq 1 50); do
    if redis-cli -h 127.0.0.1 -p "$port" PING >/dev/null 2>&1; then
      break
    fi
    sleep 0.1
  done
  redis-cli -h 127.0.0.1 -p "$port" PING | grep -qx PONG
done

cd "$ROOT/compat/node"

if [[ ! -d node_modules ]]; then
  npm ci
fi

node cluster-smoke.cjs

cd "$ROOT"

if ! python3 -c 'import redis' >/dev/null 2>&1; then
  echo "python redis package is required for redis-py cluster smoke" >&2
  echo "install it with: python3 -m pip install redis" >&2
  exit 1
fi

REDIS_HOST=127.0.0.1 REDIS_PORT=7000 \
  python3 compat/python/cluster_smoke.py

cd "$ROOT/compat/go"
go run ./cluster
