#!/usr/bin/env bash
set -euo pipefail

usage() {
  cat <<'EOF'
Usage:
  bash scripts/bench/bench-one.sh <profile> -p <port> [options]

Examples:
  bash scripts/bench/bench-one.sh uuid -p 6390
  bash scripts/bench/bench-one.sh cache-json -p 6383 -s snug
  bash scripts/bench/bench-one.sh counter -p 6379 -s redis -k 1000000

Profiles:
  Strings:
    session-json api-json cache-json counter uuid ulid text repetitive compressed random
  Native structures:
    hash-small hash-medium hash-large
    list-small list-medium list-large
    set-small set-medium set-large
    zset-small zset-medium zset-large

Options:
  -p, --port PORT          Server port (required)
  -h, --host HOST          Server host (default: 127.0.0.1)
  -s, --server LABEL       Label stored in JSON output (default: server)
  -k, --keys N             Dataset keys (default: 1000000)
  -g, --get-ops N          GET operations (default: 2000000)
  -w, --workers N          Concurrent workers (default: 8)
  -P, --pipeline N         Pipeline depth (default: 256)
      --settle-ms N        Wait after LOAD before convergence check (default: 0)
      --converge-ms N      Max convergence wait; snug defaults to -1 (until complete), others 0
      --seed N             Deterministic seed (default: 1)
  -o, --output DIR         Output directory
      --no-build           Reuse /tmp/rediswirebench instead of rebuilding it
      --help               Show this help

The script NEVER starts, stops, kills, or configures the target server.
You are responsible for starting Redis/SnugKV yourself.
The target database is FLUSHDB'd before the LOAD benchmark.
EOF
}

PROFILE="${1:-}"
if [[ -z "$PROFILE" || "$PROFILE" == "--help" ]]; then
  usage
  [[ "$PROFILE" == "--help" ]] && exit 0
  exit 2
fi
shift

case "$PROFILE" in
  session-json|api-json|cache-json|counter|uuid|ulid|text|repetitive|compressed|random|\
  hash-small|hash-medium|hash-large|list-small|list-medium|list-large|\
  set-small|set-medium|set-large|zset-small|zset-medium|zset-large) ;;
  *)
    echo "unknown profile: $PROFILE" >&2
    usage >&2
    exit 2
    ;;
esac

HOST="127.0.0.1"
PORT=""
SERVER="server"
KEYS="1000000"
GET_OPS="2000000"
WORKERS="8"
PIPELINE="256"
SETTLE_MS="0"
CONVERGE_MS=""
SEED="1"
BUILD=1
ROOT_OUT=""
GO_BIN="${SNUGKV_GO_BIN:-go}"

while [[ $# -gt 0 ]]; do
  case "$1" in
    -p|--port)
      PORT="${2:-}"; shift 2 ;;
    -h|--host)
      HOST="${2:-}"; shift 2 ;;
    -s|--server)
      SERVER="${2:-}"; shift 2 ;;
    -k|--keys)
      KEYS="${2:-}"; shift 2 ;;
    -g|--get-ops)
      GET_OPS="${2:-}"; shift 2 ;;
    -w|--workers)
      WORKERS="${2:-}"; shift 2 ;;
    -P|--pipeline)
      PIPELINE="${2:-}"; shift 2 ;;
    --settle-ms)
      SETTLE_MS="${2:-}"; shift 2 ;;
    --converge-ms)
      CONVERGE_MS="${2:-}"; shift 2 ;;
    --seed)
      SEED="${2:-}"; shift 2 ;;
    -o|--output)
      ROOT_OUT="${2:-}"; shift 2 ;;
    --no-build)
      BUILD=0; shift ;;
    --help)
      usage; exit 0 ;;
    *)
      echo "unknown option: $1" >&2
      usage >&2
      exit 2 ;;
  esac
done

if [[ -z "$PORT" ]]; then
  echo "error: -p/--port is required" >&2
  usage >&2
  exit 2
fi

STRUCTURE_TYPE=""
CARDINALITY=""

case "$PROFILE" in
  session-json) VALUE_BYTES=384 ;;
  api-json) VALUE_BYTES=768 ;;
  cache-json) VALUE_BYTES=1024 ;;
  counter) VALUE_BYTES=10 ;;
  uuid) VALUE_BYTES=36 ;;
  ulid) VALUE_BYTES=26 ;;
  text|repetitive|compressed|random) VALUE_BYTES=256 ;;
  hash-small) STRUCTURE_TYPE=hash; CARDINALITY=10; VALUE_BYTES=64 ;;
  hash-medium) STRUCTURE_TYPE=hash; CARDINALITY=100; VALUE_BYTES=64 ;;
  hash-large) STRUCTURE_TYPE=hash; CARDINALITY=1000; VALUE_BYTES=64 ;;
  list-small) STRUCTURE_TYPE=list; CARDINALITY=10; VALUE_BYTES=64 ;;
  list-medium) STRUCTURE_TYPE=list; CARDINALITY=100; VALUE_BYTES=64 ;;
  list-large) STRUCTURE_TYPE=list; CARDINALITY=1000; VALUE_BYTES=64 ;;
  set-small) STRUCTURE_TYPE=set; CARDINALITY=10; VALUE_BYTES=24 ;;
  set-medium) STRUCTURE_TYPE=set; CARDINALITY=100; VALUE_BYTES=24 ;;
  set-large) STRUCTURE_TYPE=set; CARDINALITY=1000; VALUE_BYTES=24 ;;
  zset-small) STRUCTURE_TYPE=zset; CARDINALITY=10; VALUE_BYTES=24 ;;
  zset-medium) STRUCTURE_TYPE=zset; CARDINALITY=100; VALUE_BYTES=24 ;;
  zset-large) STRUCTURE_TYPE=zset; CARDINALITY=1000; VALUE_BYTES=24 ;;
esac

if [[ -z "$CONVERGE_MS" ]]; then
  if [[ "$SERVER" == "snug" ]]; then
    CONVERGE_MS="-1"
  else
    CONVERGE_MS="0"
  fi
fi

ADDR="$HOST:$PORT"
if [[ -z "$ROOT_OUT" ]]; then
  ROOT_OUT="benchmark-results/${PROFILE}-${SERVER}-${PORT}-$(date +%Y%m%d-%H%M%S)"
fi
mkdir -p "$ROOT_OUT"

if [[ "$BUILD" == "1" ]]; then
  "$GO_BIN" build -o /tmp/rediswirebench ./cmd/rediswirebench
  "$GO_BIN" build -o /tmp/redisstructurebench ./cmd/redisstructurebench
elif [[ ! -x /tmp/rediswirebench || ! -x /tmp/redisstructurebench ]]; then
  echo "error: benchmark binaries do not exist; remove --no-build" >&2
  exit 2
fi

echo "Single-profile benchmark"
echo "  profile:    $PROFILE"
echo "  server:     $SERVER"
echo "  address:    $ADDR"
echo "  keys:       $KEYS"
echo "  get_ops:    $GET_OPS"
echo "  workers:    $WORKERS"
echo "  pipeline:   $PIPELINE"
echo "  value:      $VALUE_BYTES bytes"
echo "  settle_ms:  $SETTLE_MS"
echo "  converge_ms:$CONVERGE_MS"
echo "  output:     $ROOT_OUT"
echo
echo "WARNING: this benchmark will FLUSHDB on $ADDR"
echo

LOAD_OUT="$ROOT_OUT/load.json"
GET_OUT="$ROOT_OUT/get.json"

echo "===== $PROFILE | $SERVER | LOAD ====="
if [[ -n "$STRUCTURE_TYPE" ]]; then
  /tmp/redisstructurebench \
    -server "$SERVER" \
    -addr "$ADDR" \
    -mode load \
    -type "$STRUCTURE_TYPE" \
    -items "$KEYS" \
    -cardinality "$CARDINALITY" \
    -workers "$WORKERS" \
    -pipeline "$PIPELINE" \
    -value-bytes "$VALUE_BYTES" \
    -seed "$SEED" \
    -converge-ms "$CONVERGE_MS" \
    -reset \
    | tee "$LOAD_OUT"
else
  /tmp/rediswirebench \
    -server "$SERVER" \
    -addr "$ADDR" \
    -workload load \
    -keys "$KEYS" \
    -workers "$WORKERS" \
    -pipeline "$PIPELINE" \
    -value-bytes "$VALUE_BYTES" \
    -value-shape "$PROFILE" \
    -seed "$SEED" \
    -settle-ms "$SETTLE_MS" \
    -converge-ms "$CONVERGE_MS" \
    -reset \
    | tee "$LOAD_OUT"
fi

echo
echo "===== $PROFILE | $SERVER | READ ====="
if [[ -n "$STRUCTURE_TYPE" ]]; then
  /tmp/redisstructurebench \
    -server "$SERVER" \
    -addr "$ADDR" \
    -mode read \
    -type "$STRUCTURE_TYPE" \
    -items "$KEYS" \
    -cardinality "$CARDINALITY" \
    -ops "$GET_OPS" \
    -workers "$WORKERS" \
    -pipeline "$PIPELINE" \
    -value-bytes "$VALUE_BYTES" \
    -seed "$SEED" \
    | tee "$GET_OUT"
else
  /tmp/rediswirebench \
    -server "$SERVER" \
    -addr "$ADDR" \
    -workload get \
    -keys "$KEYS" \
    -ops "$GET_OPS" \
    -workers "$WORKERS" \
    -pipeline "$PIPELINE" \
    -value-bytes "$VALUE_BYTES" \
    -value-shape "$PROFILE" \
    -seed "$SEED" \
    | tee "$GET_OUT"
fi

python3 - "$LOAD_OUT" "$GET_OUT" <<'PY'
import json, sys
with open(sys.argv[1]) as f:
    load=json.load(f)
with open(sys.argv[2]) as f:
    get=json.load(f)

print()
print("===== RESULT =====")
print(f"profile:       {load['value_shape']}")
print(f"server:        {load['server']}")
print(f"address:       {load['addr']}")
print(f"keys:          {load['keys']:,}")
print(f"value_bytes:   {load['value_bytes']}")
write_label = "WRITE" if load.get("logical_unit") == "item" else "SET"
read_label = "READ" if load.get("logical_unit") == "item" else "GET"
unit = load.get("logical_unit", "key")
print(f"{write_label}:           {load['ops_per_second']:,.0f}/s")
print(f"{write_label} p95:       {load['p95_ns']/1000:.2f} us")
print(f"{read_label}:           {get['ops_per_second']:,.0f}/s")
print(f"{read_label} p95:       {get['p95_ns']/1000:.2f} us")
print(f"bytes/{unit} hot: {load.get('bytes_per_key_post_workload', load['bytes_per_key_delta']):.2f}")
print(f"bytes/{unit} final:{load['bytes_per_key_delta']:.2f}")
if load.get("logical_unit") == "item":
    print(f"container keys: {load.get('container_keys', 0):,}")
    print(f"cardinality:    {load.get('cardinality', 0):,}")
print(f"memory hot:    {load.get('used_memory_post_workload_delta', load['used_memory_delta']):,} B")
print(f"memory final:  {load['used_memory_delta']:,} B")
if load.get('converge_ms', 0):
    state = "yes" if load.get('converged') else "no"
    print(f"converged:     {state} in {load.get('convergence_elapsed_ms', 0):,} ms")
PY

echo
echo "saved:"
echo "  $LOAD_OUT"
echo "  $GET_OUT"
