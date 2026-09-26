#!/usr/bin/env bash
set -euo pipefail

ROOT="${ROOT:-$(cd "$(dirname "$0")/.." && pwd)}"
SNUG_BIN="${SNUG_BIN:-/tmp/snugkv-cpuprof}"
BENCH_BIN="${BENCH_BIN:-/tmp/rediswirebench-cpuprof}"

PRIMARY_PORT="${PRIMARY_PORT:-6400}"
BASE_REPLICA_PORT="${BASE_REPLICA_PORT:-6401}"
KEYS="${KEYS:-1000000}"
VALUE_BYTES="${VALUE_BYTES:-256}"
VALUE_SHAPE="${VALUE_SHAPE:-random}"
WORKERS="${WORKERS:-4}"
PIPELINE="${PIPELINE:-256}"

PIDS=()
NAMES=()

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
      echo "$size"
      return 0
    fi
    sleep 0.05
  done
  redis-cli -p "$port" DBSIZE 2>/dev/null || echo -1
  return 1
}

start_server() {
  local name="$1"
  local port="$2"
  "$SNUG_BIN"     -listen "127.0.0.1:${port}"     -admin-listen ""     -metrics-listen ""     -aof ""     -snapshot ""     >"/tmp/snugkv-cpuprof-${port}.log" 2>&1 &
  local pid=$!
  PIDS+=("$pid")
  NAMES+=("$name")
  wait_ping "$port"
}

ticks() {
  local pid="$1"
  awk '{print $14+$15}' "/proc/$pid/stat"
}

ctx_switches() {
  local pid="$1"
  awk '/^(voluntary_ctxt_switches|nonvoluntary_ctxt_switches):/ {sum += $2} END {print sum+0}' "/proc/$pid/status"
}

cd "$ROOT"
go build -o "$SNUG_BIN" ./cmd/snugkv
go build -o "$BENCH_BIN" ./cmd/rediswirebench

for p in 6400 6401 6402 6403 6404; do
  old="$(lsof -ti tcp:$p 2>/dev/null || true)"
  [[ -n "$old" ]] && kill "$old" 2>/dev/null || true
done
sleep 0.5

start_server primary "$PRIMARY_PORT"
for i in 0 1 2 3; do
  port=$((BASE_REPLICA_PORT + i))
  start_server "replica$((i+1))" "$port"
  redis-cli -p "$port" REPLICAOF 127.0.0.1 "$PRIMARY_PORT" >/dev/null
  wait_replica_up "$port"
done

declare -A START_TICKS START_CTX END_TICKS END_CTX
for i in "${!PIDS[@]}"; do
  pid="${PIDS[$i]}"
  START_TICKS["$pid"]="$(ticks "$pid")"
  START_CTX["$pid"]="$(ctx_switches "$pid")"
done

start_ns="$(date +%s%N)"
/usr/bin/time -f 'client_user_sec=%U\\nclient_sys_sec=%S\\nclient_cpu_pct=%P' -o /tmp/snugkv-cpu-profile-client.time \
  "$BENCH_BIN" -server snug-cpu-profile-4rep -addr "127.0.0.1:${PRIMARY_PORT}" -workload load \
  -keys "$KEYS" -workers "$WORKERS" -pipeline "$PIPELINE" -value-bytes "$VALUE_BYTES" \
  -value-shape "$VALUE_SHAPE" -reset > /tmp/snugkv-cpu-profile-bench.json
end_ns="$(date +%s%N)"

for i in 0 1 2 3 4; do
  pid="${PIDS[$i]}"
  END_TICKS["$pid"]="$(ticks "$pid")"
  END_CTX["$pid"]="$(ctx_switches "$pid")"
done

echo
echo "========== BENCHMARK =========="
cat /tmp/snugkv-cpu-profile-bench.json

echo
echo "========== REPLICA CONVERGENCE =========="
for i in 0 1 2 3; do
  port=$((BASE_REPLICA_PORT + i))
  size="$(wait_dbsize "$port" "$KEYS" || true)"
  echo "replica$((i+1)) port=$port dbsize=$size"
done

wall_ns=$((end_ns - start_ns))
wall_s="$(awk -v ns="$wall_ns" 'BEGIN {printf "%.6f", ns/1000000000}')"
hz="$(getconf CLK_TCK)"

echo
echo "========== CPU ACCOUNTING DURING LOAD =========="
echo "wall_seconds=$wall_s  CLK_TCK=$hz"
printf '%-10s %8s %12s %12s %12s\n' "process" "pid" "cpu_sec" "avg_cores" "ctx_switches"

total_ticks=0
for i in 0 1 2 3 4; do
  pid="${PIDS[$i]}"
  name="${NAMES[$i]}"
  delta_ticks=$(( END_TICKS["$pid"] - START_TICKS["$pid"] ))
  delta_ctx=$(( END_CTX["$pid"] - START_CTX["$pid"] ))
  total_ticks=$((total_ticks + delta_ticks))
  cpu_sec="$(awk -v t="$delta_ticks" -v hz="$hz" 'BEGIN {printf "%.3f", t/hz}')"
  avg_cores="$(awk -v t="$delta_ticks" -v hz="$hz" -v wall="$wall_s" 'BEGIN {printf "%.3f", (t/hz)/wall}')"
  printf '%-10s %8s %12s %12s %12s\n' "$name" "$pid" "$cpu_sec" "$avg_cores" "$delta_ctx"
done

total_cpu_sec="$(awk -v t="$total_ticks" -v hz="$hz" 'BEGIN {printf "%.3f", t/hz}')"
total_cores="$(awk -v t="$total_ticks" -v hz="$hz" -v wall="$wall_s" 'BEGIN {printf "%.3f", (t/hz)/wall}')"
echo "server_cpu_seconds=$total_cpu_sec"
echo "server_avg_cores=$total_cores"

echo
echo "========== CLIENT CPU =========="
cat /tmp/snugkv-cpu-profile-client.time
