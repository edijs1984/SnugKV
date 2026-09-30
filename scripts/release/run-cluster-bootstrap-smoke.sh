#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
BOOTSTRAP="$ROOT/scripts/release/snug-cluster-bootstrap.py"
BIN=/tmp/snugkv-first-release-bootstrap-bin
TMP=/tmp/snugkv-first-release-bootstrap

cleanup_nodes() {
  if [[ -d "$TMP" ]]; then
    for pidfile in "$TMP"/*.pid; do
      [[ -f "$pidfile" ]] || continue
      pid="$(cat "$pidfile")"
      kill "$pid" 2>/dev/null || true
      wait "$pid" 2>/dev/null || true
    done
  fi
}

cleanup() {
  cleanup_nodes
  rm -f "$BIN"
}
trap cleanup EXIT

require_cmd() {
  command -v "$1" >/dev/null 2>&1 || {
    echo "missing required command: $1" >&2
    exit 1
  }
}

require_cmd go
require_cmd python3
require_cmd redis-cli

rm -rf "$TMP"
mkdir -p "$TMP"

cd "$ROOT"
go build -o "$BIN" ./cmd/snugkv

run_mode() {
  local mode="$1"
  local base_port="$2"
  local dir="$TMP/$mode"
  local password="bootstrap-test-password"
  local control="bootstrap-test-control"

  mkdir -p "$dir"

  python3 "$BOOTSTRAP" generate     --mode "$mode"     --nodes "127.0.0.1:$base_port,127.0.0.1:$((base_port+1)),127.0.0.1:$((base_port+2))"     --output "$dir"     --password "$password"     --control-auth "$control"     --group-id "bootstrap-$mode"

  for i in 0 1 2; do
    "$BIN" -config "$dir/node-$i.json" >"$TMP/$mode-node-$i.log" 2>&1 &
    echo $! >"$TMP/$mode-node-$i.pid"
  done

  python3 "$BOOTSTRAP" apply     --manifest "$dir/manifest.json"     --password "$password"     --timeout 30

  python3 "$BOOTSTRAP" verify     --manifest "$dir/manifest.json"     --password "$password"     --timeout 30

  for i in 0 1 2; do
    pid="$(cat "$TMP/$mode-node-$i.pid")"
    kill "$pid" 2>/dev/null || true
    wait "$pid" 2>/dev/null || true
    rm -f "$TMP/$mode-node-$i.pid"
  done

  echo "bounded bootstrap $mode: PASS"
}

run_mode sharded 7160
run_mode ha 7170

echo "first-release bounded cluster bootstrap smoke: PASS"
