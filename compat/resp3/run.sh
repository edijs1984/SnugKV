#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
HOST="${REDIS_HOST:-127.0.0.1}"
PORT="${REDIS_PORT:-6380}"
TARGET="${TARGET_NAME:-snugkv}"

echo "RESP3 client smoke: target=${TARGET} addr=${HOST}:${PORT}"

echo
echo "== node-redis + ioredis =="
(
  cd "$ROOT/compat/node"
  REDIS_HOST="$HOST" REDIS_PORT="$PORT" TARGET_NAME="$TARGET" node resp3-smoke.cjs
)

echo
echo "== redis-py =="
(
  cd "$ROOT/compat/python"
  PYTHON="python3"
  if [[ -x ".venv/bin/python" ]]; then
    PYTHON=".venv/bin/python"
  fi
  REDIS_HOST="$HOST" REDIS_PORT="$PORT" TARGET_NAME="$TARGET" "$PYTHON" resp3_smoke.py
)

echo
echo "== go-redis =="
(
  cd "$ROOT/compat/go"
  REDIS_HOST="$HOST" REDIS_PORT="$PORT" TARGET_NAME="$TARGET" go run ./resp3
)

echo
echo "ALL RESP3 CLIENT SMOKE TESTS PASSED: ${TARGET}"