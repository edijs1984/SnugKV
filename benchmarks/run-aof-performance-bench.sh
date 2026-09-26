#!/usr/bin/env bash
set -euo pipefail

ROOT="${ROOT:-$(cd "$(dirname "$0")/.." && pwd)}"
SNUG_BIN="${SNUG_BIN:-/tmp/snugkv-aofbench}"
BENCH_BIN="${BENCH_BIN:-/tmp/rediswirebench-aofbench}"
REDIS_SERVER="${REDIS_SERVER:-redis-server}"

SNUG_PORT="${SNUG_PORT:-6420}"
REDIS_PORT="${REDIS_PORT:-6421}"
KEYS="${KEYS:-100000}"
VALUE_BYTES="${VALUE_BYTES:-256}"
VALUE_SHAPE="${VALUE_SHAPE:-random}"
WORKERS="${WORKERS:-4}"
PIPELINE="${PIPELINE:-256}"
REPEATS="${REPEATS:-3}"
MODES="${MODES:-off everysec always}"

SNUG_AOF="/tmp/snugkv-aofbench.aof"
REDIS_ROOT="/tmp/redis-aofbench"
OUT="${OUT:-/tmp/aof-performance-bench.jsonl}"

PIDS=()

cleanup() {
  for pid in "${PIDS[@]:-}"; do
    kill "$pid" 2>/dev/null || true
  done
  wait 2>/dev/null || true
}
trap cleanup EXIT INT TERM

wait_ping() {
  local port="$1"
  for _ in $(seq 1 200); do
    if redis-cli -p "$port" PING >/dev/null 2>&1; then
      return 0
    fi
    sleep 0.05
  done
  echo "server on port $port did not start" >&2
  return 1
}

stop_all() {
  cleanup
  PIDS=()
  sleep 0.25
}

kill_port() {
  local port="$1"
  local old
  old="$(lsof -ti tcp:"$port" 2>/dev/null || true)"
  if [[ -n "$old" ]]; then
    kill $old 2>/dev/null || true
    sleep 0.25
  fi
}

build() {
  cd "$ROOT"
  go build -o "$SNUG_BIN" ./cmd/snugkv
  go build -o "$BENCH_BIN" ./cmd/rediswirebench
}

snug_aof_bytes() {
  if [[ -f "$SNUG_AOF" ]]; then
    stat -c %s "$SNUG_AOF"
  else
    echo 0
  fi
}

redis_aof_bytes() {
  if [[ -f "$REDIS_ROOT/appendonly.aof" ]]; then
    stat -c %s "$REDIS_ROOT/appendonly.aof"
    return
  fi
  local dir="$REDIS_ROOT/appendonlydir"
  if [[ -d "$dir" ]]; then
    find "$dir" -type f -printf '%s\n' 2>/dev/null |
      awk '{sum += $1} END {print sum+0}'
    return
  fi
  echo 0
}

start_snug() {
  local mode="$1"
  rm -f "$SNUG_AOF" "$SNUG_AOF.lock" "$SNUG_AOF.replication"

  local args=(
    -listen "127.0.0.1:${SNUG_PORT}"
    -admin-listen ""
    -metrics-listen ""
    -snapshot ""
  )

  if [[ "$mode" == "off" ]]; then
    args+=( -aof "" )
  else
    args+=( -aof "$SNUG_AOF" -fsync "$mode" )
  fi

  "$SNUG_BIN" "${args[@]}" >"/tmp/snugkv-aofbench.log" 2>&1 &
  PIDS+=("$!")
  wait_ping "$SNUG_PORT"
}

start_redis() {
  local mode="$1"
  rm -rf "$REDIS_ROOT"
  mkdir -p "$REDIS_ROOT"

  local args=(
    --bind 127.0.0.1
    --protected-mode no
    --port "$REDIS_PORT"
    --save ""
    --dir "$REDIS_ROOT"
    --loglevel warning
    --logfile ""
  )

  if [[ "$mode" == "off" ]]; then
    args+=( --appendonly no )
  else
    args+=(
      --appendonly yes
      --appendfsync "$mode"
      --aof-use-rdb-preamble no
    )
  fi

  "$REDIS_SERVER" "${args[@]}" >"/tmp/redis-aofbench.log" 2>&1 &
  PIDS+=("$!")
  wait_ping "$REDIS_PORT"
}

run_snug() {
  local mode="$1"
  local repeat="$2"
  stop_all
  kill_port "$SNUG_PORT"
  start_snug "$mode"

  echo "=== Snug mode=${mode} repeat=${repeat}/${REPEATS} ===" >&2
  "$BENCH_BIN" \
    -server "snug-aof-${mode}" \
    -addr "127.0.0.1:${SNUG_PORT}" \
    -workload load \
    -keys "$KEYS" \
    -workers "$WORKERS" \
    -pipeline "$PIPELINE" \
    -value-bytes "$VALUE_BYTES" \
    -value-shape "$VALUE_SHAPE" \
    -reset | tee -a "$OUT"

  local bytes
  bytes="$(snug_aof_bytes)"
  echo "AOF_RESULT server=snug mode=${mode} repeat=${repeat} bytes=${bytes} bytes_per_key=$(awk -v b="$bytes" -v k="$KEYS" 'BEGIN {printf "%.3f", b/k}')" | tee -a "$OUT"
}

run_redis() {
  local mode="$1"
  local repeat="$2"
  stop_all
  kill_port "$REDIS_PORT"
  start_redis "$mode"

  echo "=== Redis mode=${mode} repeat=${repeat}/${REPEATS} ===" >&2
  "$BENCH_BIN" \
    -server "redis-aof-${mode}" \
    -addr "127.0.0.1:${REDIS_PORT}" \
    -workload load \
    -keys "$KEYS" \
    -workers "$WORKERS" \
    -pipeline "$PIPELINE" \
    -value-bytes "$VALUE_BYTES" \
    -value-shape "$VALUE_SHAPE" \
    -reset | tee -a "$OUT"

  local bytes
  bytes="$(redis_aof_bytes)"
  echo "AOF_RESULT server=redis mode=${mode} repeat=${repeat} bytes=${bytes} bytes_per_key=$(awk -v b="$bytes" -v k="$KEYS" 'BEGIN {printf "%.3f", b/k}')" | tee -a "$OUT"
}

: > "$OUT"
build
kill_port "$SNUG_PORT"
kill_port "$REDIS_PORT"

if ! [[ "$REPEATS" =~ ^[1-9][0-9]*$ ]]; then
  echo "REPEATS must be a positive integer" >&2
  exit 2
fi

for mode in $MODES; do
  case "$mode" in
    off|everysec|always) ;;
    *)
      echo "unknown mode: $mode (expected off, everysec, always)" >&2
      exit 2
      ;;
  esac

  for repeat in $(seq 1 "$REPEATS"); do
    run_snug "$mode" "$repeat"
    run_redis "$mode" "$repeat"
  done
done

stop_all

echo >&2
echo "AOF benchmark results: $OUT" >&2
