#!/usr/bin/env bash
set -euo pipefail

ROOT="${ROOT:-$(cd "$(dirname "$0")/.." && pwd)}"
SNUG_BIN="${SNUG_BIN:-/tmp/snugkv-allocprof}"
BENCH_BIN="${BENCH_BIN:-/tmp/rediswirebench-allocprof}"

KEYS="${KEYS:-1000000}"
VALUE_BYTES="${VALUE_BYTES:-256}"
VALUE_SHAPE="${VALUE_SHAPE:-random}"
WORKERS="${WORKERS:-4}"
PIPELINE="${PIPELINE:-256}"

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
  for _ in $(seq 1 100); do
    if redis-cli -p "$port" PING >/dev/null 2>&1; then return 0; fi
    sleep 0.05
  done
  echo "server on port $port did not start" >&2
  return 1
}

wait_replica_up() {
  local port="$1"
  for _ in $(seq 1 200); do
    if redis-cli -p "$port" INFO replication 2>/dev/null | grep -q 'master_link_status:up'; then return 0; fi
    sleep 0.05
  done
  echo "replica on port $port did not become ready" >&2
  return 1
}

start_server() {
  local port="$1"
  local pprof="$2"
  "$SNUG_BIN"     -listen "127.0.0.1:${port}"     -admin-listen ""     -metrics-listen ""     -pprof-listen "127.0.0.1:${pprof}"     -aof ""     -snapshot ""     >"/tmp/snugkv-allocprof-${port}.log" 2>&1 &
  PIDS+=("$!")
  wait_ping "$port"
}

cd "$ROOT"
go build -o "$SNUG_BIN" ./cmd/snugkv
go build -o "$BENCH_BIN" ./cmd/rediswirebench

for p in 6400 6401 6402 6403 6404 6060 6061 6062 6063 6064; do
  old="$(lsof -ti tcp:$p 2>/dev/null || true)"
  [[ -n "$old" ]] && kill "$old" 2>/dev/null || true
done
sleep 0.5

start_server 6400 6060
for i in 1 2 3 4; do
  start_server $((6400+i)) $((6060+i))
  redis-cli -p $((6400+i)) REPLICAOF 127.0.0.1 6400 >/dev/null
  wait_replica_up $((6400+i))
done

"$BENCH_BIN"   -server snug-alloc-profile-4rep   -addr 127.0.0.1:6400   -workload load   -keys "$KEYS"   -workers "$WORKERS"   -pipeline "$PIPELINE"   -value-bytes "$VALUE_BYTES"   -value-shape "$VALUE_SHAPE"   -reset   > /tmp/snugkv-alloc-profile-bench.json

sleep 1

curl -s http://127.0.0.1:6060/debug/pprof/allocs -o /tmp/snug-primary-allocs.pprof
curl -s http://127.0.0.1:6061/debug/pprof/allocs -o /tmp/snug-replica1-allocs.pprof

echo
echo "========== BENCHMARK =========="
cat /tmp/snugkv-alloc-profile-bench.json

echo
echo "========== REPLICA1 ALLOC SPACE =========="
go tool pprof -top -nodecount=30 -sample_index=alloc_space   "$SNUG_BIN" /tmp/snug-replica1-allocs.pprof

echo
echo "========== REPLICA1 ALLOC OBJECTS =========="
go tool pprof -top -nodecount=30 -sample_index=alloc_objects   "$SNUG_BIN" /tmp/snug-replica1-allocs.pprof

echo
echo "========== PRIMARY ALLOC SPACE =========="
go tool pprof -top -nodecount=20 -sample_index=alloc_space   "$SNUG_BIN" /tmp/snug-primary-allocs.pprof
