#!/usr/bin/env bash
set -euo pipefail

ROOT="${ROOT:-$(cd "$(dirname "$0")/.." && pwd)}"
REDIS_SERVER="${REDIS_SERVER:-redis-server}"
BENCH_BIN="${BENCH_BIN:-/tmp/rediswirebench-redisreplbench}"
OUT="${OUT:-/tmp/redis-replication-bench.jsonl}"

PRIMARY_PORT="${PRIMARY_PORT:-6410}"
BASE_REPLICA_PORT="${BASE_REPLICA_PORT:-6411}"
KEYS="${KEYS:-200000}"
VALUE_BYTES="${VALUE_BYTES:-256}"
VALUE_SHAPE="${VALUE_SHAPE:-random}"
WORKERS="${WORKERS:-4}"
PIPELINE="${PIPELINE:-256}"
SETTLE_SECONDS="${SETTLE_SECONDS:-1}"
CASES="${CASES:-1}"
REPEATS="${REPEATS:-1}"

PIDS=()

cleanup() {
  for pid in "${PIDS[@]:-}"; do
    kill "$pid" 2>/dev/null || true
  done
  wait 2>/dev/null || true
}
trap cleanup EXIT INT TERM

build() {
  cd "$ROOT"
  go build -o "$BENCH_BIN" ./cmd/rediswirebench
}

wait_ping() {
  local port="$1"
  for _ in $(seq 1 100); do
    if redis-cli -p "$port" PING >/dev/null 2>&1; then
      return 0
    fi
    sleep 0.05
  done
  echo "Redis on port $port did not start" >&2
  return 1
}

wait_replica_up() {
  local port="$1"
  for _ in $(seq 1 200); do
    if redis-cli -p "$port" INFO replication 2>/dev/null | grep -q 'master_link_status:up'; then
      return 0
    fi
    sleep 0.05
  done
  echo "Redis replica on port $port did not become ready" >&2
  redis-cli -p "$port" INFO replication >&2 || true
  return 1
}

wait_dbsize() {
  local port="$1"
  local expected="$2"
  local deadline=$((SECONDS + 15))
  local size

  while (( SECONDS < deadline )); do
    size="$(redis-cli -p "$port" DBSIZE 2>/dev/null || echo -1)"
    if [[ "$size" == "$expected" ]]; then
      printf '%s' "$size"
      return 0
    fi
    sleep 0.05
  done

  size="$(redis-cli -p "$port" DBSIZE 2>/dev/null || echo -1)"
  printf '%s' "$size"
  return 1
}

stop_all() {
  cleanup
  PIDS=()
  sleep 0.2
}

start_redis() {
  local port="$1"
  shift
  local logfile="/tmp/redis-replbench-${port}.log"

  "$REDIS_SERVER"     --bind 127.0.0.1     --protected-mode no     --port "$port"     --save ""     --appendonly no     --loglevel warning     --logfile ""     "$@"     >"$logfile" 2>&1 &

  PIDS+=("$!")
  wait_ping "$port"
}

start_star() {
  local replicas="$1"
  start_redis "$PRIMARY_PORT"

  if (( replicas > 0 )); then
    for i in $(seq 0 $((replicas - 1))); do
      local port=$((BASE_REPLICA_PORT + i))
      start_redis "$port" --replicaof 127.0.0.1 "$PRIMARY_PORT"
      wait_replica_up "$port"
    done
  fi
}

run_case() {
  local replicas="$1"
  stop_all
  start_star "$replicas"

  local label="redis-repl-${replicas}"
  echo "=== ${label} ===" >&2

  "$BENCH_BIN"     -server "$label"     -addr "127.0.0.1:${PRIMARY_PORT}"     -workload load     -keys "$KEYS"     -workers "$WORKERS"     -pipeline "$PIPELINE"     -value-bytes "$VALUE_BYTES"     -value-shape "$VALUE_SHAPE"     -reset     | tee -a "$OUT"

  sleep "$SETTLE_SECONDS"

  if (( replicas > 0 )); then
    for i in $(seq 0 $((replicas - 1))); do
      local port=$((BASE_REPLICA_PORT + i))
      local size
      if size="$(wait_dbsize "$port" "$KEYS")"; then
        echo "replica port ${port} dbsize=${size}" >&2
      else
        echo "replica ${port} did not converge: dbsize=${size}, expected=${KEYS}" >&2
        return 1
      fi
    done
  fi
}

: > "$OUT"
build

if ! [[ "$REPEATS" =~ ^[1-9][0-9]*$ ]]; then
  echo "REPEATS must be a positive integer" >&2
  exit 2
fi

for repeat in $(seq 1 "$REPEATS"); do
  for replicas in $CASES; do
    case "$replicas" in
      1|2|4) ;;
      *)
        echo "unknown CASES entry: $replicas (expected 1, 2, or 4)" >&2
        exit 2
        ;;
    esac

    echo "=== repeat ${repeat}/${REPEATS}; case ${replicas} ===" >&2
    run_case "$replicas"
  done
done

echo >&2
echo "Redis replication benchmark results: $OUT" >&2
