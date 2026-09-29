#!/usr/bin/env bash
set -euo pipefail

ROOT="${ROOT:-$(cd "$(dirname "$0")/.." && pwd)}"
SNUG_BIN="${SNUG_BIN:-/tmp/snugkv-clusterbench}"
BENCH_BIN="${BENCH_BIN:-/tmp/clusterbench}"
TMP="${TMP:-/tmp/snugkv-cluster-routing-bench}"
OUT="${OUT:-/tmp/snugkv-cluster-routing-bench.jsonl}"

P0="${P0:-7200}"
P1="${P1:-7201}"
P2="${P2:-7202}"
A0="127.0.0.1:$P0"
A1="127.0.0.1:$P1"
A2="127.0.0.1:$P2"
NODES="$A0,$A1,$A2"

KEYS="${KEYS:-100000}"
OPS="${OPS:-500000}"
WORKERS="${WORKERS:-4}"
PIPELINE="${PIPELINE:-256}"
VALUE_BYTES="${VALUE_BYTES:-256}"
REPEATS="${REPEATS:-3}"
CONTROL_SECRET="${CONTROL_SECRET:-cluster-bench-control-secret}"

PIDS=()

cleanup() {
  for pid in "${PIDS[@]:-}"; do
    kill "$pid" 2>/dev/null || true
  done
  wait 2>/dev/null || true
  rm -f "$SNUG_BIN" "$BENCH_BIN"
}
trap cleanup EXIT INT TERM

require_cmd() {
  command -v "$1" >/dev/null 2>&1 || {
    echo "missing required command: $1" >&2
    exit 1
  }
}

require_cmd go
require_cmd redis-cli
require_cmd seq

rm -rf "$TMP"
mkdir -p "$TMP"
: >"$OUT"

cd "$ROOT"
go build -o "$SNUG_BIN" ./cmd/snugkv
go build -o "$BENCH_BIN" ./cmd/clusterbench

cat >"$TMP/node.json.template" <<'JSON'
{
  "admin_listen": "",
  "metrics_listen": "",
  "cluster_enabled": true,
  "cluster_control_auth": "__CONTROL__",
  "cluster_slots": {
    "0-5460": "__A0__",
    "5461-10922": "__A1__",
    "10923-16383": "__A2__"
  }
}
JSON

make_config() {
  local port="$1"
  local addr="$2"
  local path="$3"
  sed     -e "s|__CONTROL__|$CONTROL_SECRET|g"     -e "s|__A0__|$A0|g"     -e "s|__A1__|$A1|g"     -e "s|__A2__|$A2|g"     "$TMP/node.json.template" |
  sed "s|^{|{\n  \"listen\": \"127.0.0.1:$port\",\n  \"cluster_node_addr\": \"$addr\",|"     >"$path"
}

make_config "$P0" "$A0" "$TMP/n0.json"
make_config "$P1" "$A1" "$TMP/n1.json"
make_config "$P2" "$A2" "$TMP/n2.json"

start_node() {
  local name="$1"
  local cfg="$2"
  "$SNUG_BIN" -config "$cfg" >"$TMP/$name.log" 2>&1 &
  PIDS+=("$!")
}

wait_ready() {
  local port="$1"
  for _ in $(seq 1 200); do
    if redis-cli --raw -p "$port" PING 2>/dev/null | grep -qx PONG; then
      return 0
    fi
    sleep 0.05
  done
  echo "node on port $port did not become ready" >&2
  return 1
}

start_node n0 "$TMP/n0.json"
start_node n1 "$TMP/n1.json"
start_node n2 "$TMP/n2.json"
wait_ready "$P0"
wait_ready "$P1"
wait_ready "$P2"

echo "SnugKV cluster routing benchmark"
echo "nodes=$NODES keys=$KEYS ops=$OPS workers=$WORKERS pipeline=$PIPELINE value_bytes=$VALUE_BYTES repeats=$REPEATS"
echo "output=$OUT"

echo
echo "[1/4] load distributed dataset through known slot owners"
"$BENCH_BIN"   -mode direct   -workload set   -seed-addr "$A0"   -nodes "$NODES"   -keys "$KEYS"   -ops "$KEYS"   -workers "$WORKERS"   -value-bytes "$VALUE_BYTES" |
  tee -a "$OUT"

for port in "$P0" "$P1" "$P2"; do
  echo "node $port dbsize=$(redis-cli --raw -p "$port" DBSIZE)"
done

echo
echo "[2/4] direct owner GET"
for run in $(seq 1 "$REPEATS"); do
  echo "run=$run mode=direct"
  "$BENCH_BIN"     -mode direct     -workload get     -seed-addr "$A0"     -nodes "$NODES"     -keys "$KEYS"     -ops "$OPS"     -workers "$WORKERS"     -value-bytes "$VALUE_BYTES" |
    tee -a "$OUT"
done

echo
echo "[3/4] seed-first GET with MOVED follow on every non-local slot"
for run in $(seq 1 "$REPEATS"); do
  echo "run=$run mode=redirect"
  "$BENCH_BIN"     -mode redirect     -workload get     -seed-addr "$A0"     -nodes "$NODES"     -keys "$KEYS"     -ops "$OPS"     -workers "$WORKERS"     -value-bytes "$VALUE_BYTES" |
    tee -a "$OUT"
done

echo
echo "[4/4] seed discovery with per-slot MOVED cache"
for run in $(seq 1 "$REPEATS"); do
  echo "run=$run mode=cache"
  "$BENCH_BIN"     -mode cache     -workload get     -seed-addr "$A0"     -nodes "$NODES"     -keys "$KEYS"     -ops "$OPS"     -workers "$WORKERS"     -value-bytes "$VALUE_BYTES" |
    tee -a "$OUT"
done

echo
echo "cluster routing benchmark: PASS"
echo "results=$OUT"
