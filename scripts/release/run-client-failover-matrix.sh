#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
STATE="${STATE:-/tmp/snugkv-d-client-failover}"
PY_VENV="${PY_VENV:-/tmp/snugkv-d-client-venv}"
rm -rf "$STATE"
mkdir -p "$STATE/node" "$STATE/python" "$STATE/go"

cleanup() {
  for pidfile in "$STATE"/*/client.pid; do
    [[ -f "$pidfile" ]] || continue
    pid="$(cat "$pidfile")"
    kill "$pid" 2>/dev/null || true
    wait "$pid" 2>/dev/null || true
  done
}
trap cleanup EXIT

cd "$ROOT"

[[ -x "$PY_VENV/bin/python" ]] || python3 -m venv "$PY_VENV"
"$PY_VENV/bin/python" -c 'import redis' >/dev/null 2>&1 ||   "$PY_VENV/bin/pip" install --disable-pip-version-check 'redis==6.4.0'

PRE_HOOK=$(cat <<'EOF'
set -euo pipefail

cd "$ROOT/compat/node"
[[ -d node_modules ]] || npm ci
CLIENT_STATE_DIR="$STATE/node" P0="$P0" P1="$P1" P2="$P2" PASSWORD="$PASSWORD"   node failover-trace.cjs >"$STATE/node/client.log" 2>&1 &
echo $! >"$STATE/node/client.pid"

cd "$ROOT"
CLIENT_STATE_DIR="$STATE/python" P0="$P0" P1="$P1" P2="$P2" PASSWORD="$PASSWORD"   "$PY_VENV/bin/python" compat/python/failover_trace.py >"$STATE/python/client.log" 2>&1 &
echo $! >"$STATE/python/client.pid"

cd "$ROOT/compat/go"
CLIENT_STATE_DIR="$STATE/go" P0="$P0" P1="$P1" P2="$P2" PASSWORD="$PASSWORD"   go run ./failover >"$STATE/go/client.log" 2>&1 &
echo $! >"$STATE/go/client.pid"

for name in node python go; do
  ready="$STATE/$name/ready"
  failed="$STATE/$name/failed"
  for _ in $(seq 1 400); do
    [[ -f "$ready" ]] && break
    if [[ -f "$failed" ]]; then
      cat "$failed" >&2
      cat "$STATE/$name/client.log" >&2 || true
      exit 1
    fi
    sleep 0.05
  done
  if [[ ! -f "$ready" ]]; then
    echo "$name failover client did not become ready" >&2
    cat "$STATE/$name/client.log" >&2 || true
    exit 1
  fi
done
EOF
)

POST_HOOK=$(cat <<'EOF'
set -euo pipefail

for name in node python go; do
  touch "$STATE/$name/post-failover"
done

for name in node python go; do
  passed="$STATE/$name/passed"
  failed="$STATE/$name/failed"
  for _ in $(seq 1 500); do
    [[ -f "$passed" ]] && break
    if [[ -f "$failed" ]]; then
      cat "$failed" >&2
      cat "$STATE/$name/client.log" >&2 || true
      exit 1
    fi
    sleep 0.05
  done
  if [[ ! -f "$passed" ]]; then
    echo "$name failover client did not recover" >&2
    cat "$STATE/$name/client.log" >&2 || true
    exit 1
  fi
done

cat "$STATE/node/client.log"
cat "$STATE/python/client.log"
cat "$STATE/go/client.log"
EOF
)

export ROOT STATE PY_VENV
PRE_FAILOVER_HOOK="$PRE_HOOK" POST_FAILOVER_HOOK="$POST_HOOK"   bash scripts/cluster-chaos-failover-restart.sh

echo "persistent client failover matrix: PASS"
