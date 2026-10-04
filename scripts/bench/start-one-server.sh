#!/usr/bin/env bash
set -euo pipefail

SERVER="${1:?usage: start-one-server.sh redis|snug}"

REDIS_PORT="${REDIS_PORT:-6390}"
SNUG_PORT="${SNUG_PORT:-6383}"
PPROF_PORT="${PPROF_PORT:-6060}"

REDIS_SERVER_BIN="${REDIS_SERVER_BIN:-redis-server}"
SNUG_BIN="${SNUG_BIN:-/tmp/snugkv-bench}"
BUILD_SNUG="${BUILD_SNUG:-0}"

REDIS_PIDFILE="${REDIS_PIDFILE:-/tmp/snug-bench-redis.pid}"
SNUG_PIDFILE="${SNUG_PIDFILE:-/tmp/snug-bench-snug.pid}"
REDIS_LOG="${REDIS_LOG:-/tmp/snug-bench-redis.log}"
SNUG_LOG="${SNUG_LOG:-/tmp/snug-bench-snug.log}"

stop_pidfile() {
  local pidfile="$1"
  if [[ ! -f "$pidfile" ]]; then
    return
  fi
  local pid
  pid="$(cat "$pidfile" 2>/dev/null || true)"
  if [[ -n "$pid" ]] && kill -0 "$pid" 2>/dev/null; then
    kill "$pid" 2>/dev/null || true
    for _ in $(seq 1 50); do
      kill -0 "$pid" 2>/dev/null || break
      sleep 0.02
    done
    kill -9 "$pid" 2>/dev/null || true
  fi
  rm -f "$pidfile"
}

# Every isolated benchmark starts with no benchmark-owned database process.
stop_pidfile "$REDIS_PIDFILE"
stop_pidfile "$SNUG_PIDFILE"

if [[ "$SERVER" == "snug" && "$BUILD_SNUG" == "1" ]]; then
  echo "Building SnugKV native benchmark binary..."
  go build -trimpath -o "$SNUG_BIN" ./cmd/snugkv
fi

case "$SERVER" in
  redis)
    command -v "$REDIS_SERVER_BIN" >/dev/null 2>&1 || {
      echo "redis-server not found: $REDIS_SERVER_BIN" >&2
      exit 2
    }
    "$REDIS_SERVER_BIN" \
      --bind 127.0.0.1 \
      --port "$REDIS_PORT" \
      --save "" \
      --appendonly no \
      --protected-mode no \
      --daemonize no \
      >"$REDIS_LOG" 2>&1 &
    pid=$!
    echo "$pid" > "$REDIS_PIDFILE"
    port="$REDIS_PORT"
    ;;
  snug)
    if [[ ! -x "$SNUG_BIN" ]]; then
      echo "SnugKV binary missing: $SNUG_BIN (set BUILD_SNUG=1)" >&2
      exit 2
    fi
    "$SNUG_BIN" \
      -listen "127.0.0.1:$SNUG_PORT" \
      -admin-listen "" \
      -pprof-listen "127.0.0.1:$PPROF_PORT" \
      -aof "" \
      -snapshot "" \
      >"$SNUG_LOG" 2>&1 &
    pid=$!
    echo "$pid" > "$SNUG_PIDFILE"
    port="$SNUG_PORT"
    ;;
  *)
    echo "unknown server: $SERVER" >&2
    exit 2
    ;;
esac

for _ in $(seq 1 200); do
  # The newly started process must still be alive before accepting a successful
  # PING. Otherwise an older process already bound to the same port can make the
  # launcher falsely report readiness after the new process exits with EADDRINUSE.
  if ! kill -0 "$pid" 2>/dev/null; then
    echo "$SERVER exited during startup" >&2
    [[ "$SERVER" == "redis" ]] && tail -50 "$REDIS_LOG" >&2 || tail -50 "$SNUG_LOG" >&2
    exit 1
  fi
  if redis-cli -h 127.0.0.1 -p "$port" PING >/dev/null 2>&1; then
    echo "$SERVER ready on 127.0.0.1:$port pid=$pid"
    exit 0
  fi
  sleep 0.025
done

echo "$SERVER failed to become ready" >&2
kill "$pid" 2>/dev/null || true
exit 1
