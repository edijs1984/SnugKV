#!/usr/bin/env bash
set -euo pipefail

ROOT="${ROOT:-$(cd "$(dirname "$0")/.." && pwd)}"
SNUG_BIN="${SNUG_BIN:-/tmp/snugkv-replbench}"
BENCH_BIN="${BENCH_BIN:-/tmp/rediswirebench-replbench}"
OUT="${OUT:-/tmp/snugkv-replication-bench.jsonl}"

PRIMARY_PORT="${PRIMARY_PORT:-6400}"
BASE_REPLICA_PORT="${BASE_REPLICA_PORT:-6401}"
KEYS="${KEYS:-200000}"
VALUE_BYTES="${VALUE_BYTES:-256}"
VALUE_SHAPE="${VALUE_SHAPE:-random}"
WORKERS="${WORKERS:-4}"
PIPELINE="${PIPELINE:-256}"
FSYNC="${FSYNC:-everysec}"
AOF_MODE="${AOF_MODE:-off}"
SETTLE_SECONDS="${SETTLE_SECONDS:-1}"

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
  go build -o "$SNUG_BIN" ./cmd/snugkv
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
  echo "server on port $port did not start" >&2
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
  echo "replica on port $port did not become ready" >&2
  redis-cli -p "$port" INFO replication >&2 || true
  return 1
}

stop_all() {
  cleanup
  PIDS=()
  sleep 0.2
}

aof_args() {
  local port="$1"
  if [[ "$AOF_MODE" == "on" ]]; then
    printf '%s
' "-aof" "/tmp/snugkv-replbench-${port}.aof" "-fsync" "$FSYNC"
  else
    printf '%s
' "-aof" ""
  fi
}

start_snug() {
  local port="$1"
  rm -f "/tmp/snugkv-replbench-${port}.aof" "/tmp/snugkv-replbench-${port}.aof.replication"
  mapfile -t AOF_ARGS < <(aof_args "$port")
  "$SNUG_BIN"     -listen "127.0.0.1:${port}"     -admin-listen ""     -metrics-listen ""     "${AOF_ARGS[@]}"     -snapshot ""     >"/tmp/snugkv-replbench-${port}.log" 2>&1 &
  PIDS+=("$!")
  wait_ping "$port"
}

start_star() {
  local replicas="$1"
  start_snug "$PRIMARY_PORT"
  if (( replicas > 0 )); then
    for i in $(seq 0 $((replicas - 1))); do
      local port=$((BASE_REPLICA_PORT + i))
      start_snug "$port"
      redis-cli -p "$port" REPLICAOF 127.0.0.1 "$PRIMARY_PORT" >/dev/null
      wait_replica_up "$port"
    done
  fi
}

run_star_case() {
  local replicas="$1"
  stop_all
  start_star "$replicas"

  local label="snug-repl-${replicas}-aof-${AOF_MODE}-${FSYNC}"
  echo "=== ${label} ===" >&2

  "$BENCH_BIN"     -server "$label"     -addr "127.0.0.1:${PRIMARY_PORT}"     -workload load     -keys "$KEYS"     -workers "$WORKERS"     -pipeline "$PIPELINE"     -value-bytes "$VALUE_BYTES"     -value-shape "$VALUE_SHAPE"     -reset     | tee -a "$OUT"

  if (( replicas > 0 )); then
    local wait_reply
    wait_reply="$(redis-cli -p "$PRIMARY_PORT" WAIT "$replicas" 5000)"
    echo "WAIT direct replicas: ${wait_reply}/${replicas}" >&2
  fi

  sleep "$SETTLE_SECONDS"

  if (( replicas > 0 )); then
    for i in $(seq 0 $((replicas - 1))); do
      local port=$((BASE_REPLICA_PORT + i))
      local size
      size="$(redis-cli -p "$port" DBSIZE)"
      echo "replica port ${port} dbsize=${size}" >&2
      if [[ "$size" != "$KEYS" ]]; then
        echo "replica ${port} did not converge: dbsize=${size}, expected=${KEYS}" >&2
        return 1
      fi
    done
  fi
}

run_chain_case() {
  stop_all
  local middle="$BASE_REPLICA_PORT"
  local leaf=$((BASE_REPLICA_PORT + 1))

  start_snug "$PRIMARY_PORT"
  start_snug "$middle"
  start_snug "$leaf"

  redis-cli -p "$middle" REPLICAOF 127.0.0.1 "$PRIMARY_PORT" >/dev/null
  wait_replica_up "$middle"
  redis-cli -p "$leaf" REPLICAOF 127.0.0.1 "$middle" >/dev/null
  wait_replica_up "$leaf"

  local label="snug-repl-chain-aof-${AOF_MODE}-${FSYNC}"
  echo "=== ${label} ===" >&2

  "$BENCH_BIN"     -server "$label"     -addr "127.0.0.1:${PRIMARY_PORT}"     -workload load     -keys "$KEYS"     -workers "$WORKERS"     -pipeline "$PIPELINE"     -value-bytes "$VALUE_BYTES"     -value-shape "$VALUE_SHAPE"     -reset     | tee -a "$OUT"

  redis-cli -p "$PRIMARY_PORT" WAIT 1 5000 >/dev/null
  redis-cli -p "$middle" WAIT 1 5000 >/dev/null
  sleep "$SETTLE_SECONDS"

  local middle_size leaf_size
  middle_size="$(redis-cli -p "$middle" DBSIZE)"
  leaf_size="$(redis-cli -p "$leaf" DBSIZE)"
  echo "chain middle dbsize=${middle_size}; leaf dbsize=${leaf_size}" >&2

  if [[ "$middle_size" != "$KEYS" || "$leaf_size" != "$KEYS" ]]; then
    echo "chain did not converge: middle=${middle_size} leaf=${leaf_size} expected=${KEYS}" >&2
    return 1
  fi
}

: > "$OUT"
build

for replicas in 0 1 2 4; do
  run_star_case "$replicas"
done

run_chain_case

echo >&2
echo "Replication benchmark results: $OUT" >&2
