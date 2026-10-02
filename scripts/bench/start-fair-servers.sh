#!/usr/bin/env bash
set -euo pipefail

REDIS_PORT="${REDIS_PORT:-6390}"
SNUG_PORT="${SNUG_PORT:-6383}"
PPROF_PORT="${PPROF_PORT:-6060}"
REDIS_SERVER_BIN="${REDIS_SERVER_BIN:-redis-server}"
SNUG_BIN="${SNUG_BIN:-/tmp/snugkv-bench}"
BUILD_SNUG="${BUILD_SNUG:-1}"

REDIS_PIDFILE="${REDIS_PIDFILE:-/tmp/snug-bench-redis.pid}"
SNUG_PIDFILE="${SNUG_PIDFILE:-/tmp/snug-bench-snug.pid}"

if [[ "$BUILD_SNUG" == "1" ]]; then
  echo "Building SnugKV native benchmark binary..."
  go build -trimpath -o "$SNUG_BIN" ./cmd/snugkv
fi

BUILD_SNUG=0 bash scripts/bench/start-one-server.sh redis >/dev/null
redis_pid="$(cat "$REDIS_PIDFILE")"

# start-one-server enforces isolation, so start Snug manually here because this
# helper intentionally keeps both processes alive for diagnostic comparisons.
"$SNUG_BIN" \
  -listen "127.0.0.1:$SNUG_PORT" \
  -admin-listen "" \
  -pprof-listen "127.0.0.1:$PPROF_PORT" \
  -aof "" \
  -snapshot "" \
  > /tmp/snug-bench-snug.log 2>&1 &
snug_pid=$!
echo "$snug_pid" > "$SNUG_PIDFILE"

for port in "$REDIS_PORT" "$SNUG_PORT"; do
  for _ in $(seq 1 200); do
    if redis-cli -h 127.0.0.1 -p "$port" PING >/dev/null 2>&1; then
      break
    fi
    sleep 0.025
  done
  redis-cli -h 127.0.0.1 -p "$port" PING >/dev/null
done

echo "Native benchmark endpoints:"
echo "  Redis:  127.0.0.1:$REDIS_PORT pid=$redis_pid"
echo "  SnugKV: 127.0.0.1:$SNUG_PORT pid=$snug_pid"
echo "  pprof:  http://127.0.0.1:$PPROF_PORT/debug/pprof/"
echo
echo "Persistence is disabled on both benchmark servers."
