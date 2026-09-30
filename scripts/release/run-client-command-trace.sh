#!/usr/bin/env bash
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$ROOT"
OUT="${OUT:-docs/release/client-traces}"
REDIS_NAME="${REDIS_NAME:-snugkv-a3-redis82}"
REDIS_PORT="${REDIS_PORT:-6398}"
PROXY_PORT="${PROXY_PORT:-6380}"
mkdir -p "$OUT"
rm -f "$OUT"/*.jsonl
cleanup(){
  [[ -n "${PROXY_PID:-}" ]] && kill "$PROXY_PID" 2>/dev/null || true
  docker rm -f "$REDIS_NAME" >/dev/null 2>&1 || true
}
trap cleanup EXIT
cleanup
docker run -d --rm --name "$REDIS_NAME" -p "127.0.0.1:$REDIS_PORT:6379" redis:8.2 redis-server --save "" --appendonly no >/dev/null
for _ in $(seq 1 50); do docker exec "$REDIS_NAME" redis-cli PING >/dev/null 2>&1 && break; sleep 0.1; done
python3 scripts/release/resp-command-proxy.py --listen-port "$PROXY_PORT" --target-port "$REDIS_PORT" --log "$OUT/standalone.jsonl" &
PROXY_PID=$!
sleep 0.2

pushd compat/node >/dev/null
[[ -d node_modules ]] || npm ci
SNUGKV_HOST=127.0.0.1 SNUGKV_PORT="$PROXY_PORT" node smoke.cjs
REDIS_HOST=127.0.0.1 REDIS_PORT="$PROXY_PORT" TARGET_NAME=redis82-trace node resp3-smoke.cjs
REDIS_HOST=127.0.0.1 REDIS_PORT="$PROXY_PORT" node trace-workflows.cjs
popd >/dev/null

PY_VENV=/tmp/snugkv-a3-client-venv
[[ -x "$PY_VENV/bin/python" ]] || python3 -m venv "$PY_VENV"
"$PY_VENV/bin/python" -c "import redis" >/dev/null 2>&1 || "$PY_VENV/bin/pip" install --disable-pip-version-check redis==6.4.0
"$PY_VENV/bin/python" compat/python/smoke.py
REDIS_HOST=127.0.0.1 REDIS_PORT="$PROXY_PORT" TARGET_NAME=redis82-trace "$PY_VENV/bin/python" compat/python/resp3_smoke.py

pushd compat/go >/dev/null
go run .
popd >/dev/null

python3 scripts/release/summarize-client-command-traces.py \
  --trace "$OUT/standalone.jsonl" \
  --candidates docs/release/redis82-command-gap-classification.json \
  --markdown docs/release/CLIENT-COMMAND-TRACE.md
