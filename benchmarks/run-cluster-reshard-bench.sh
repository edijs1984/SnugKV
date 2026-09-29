#!/usr/bin/env bash
set -euo pipefail

ROOT="${ROOT:-$(cd "$(dirname "$0")/.." && pwd)}"
BIN="${BIN:-/tmp/snugkv-cluster-reshard-bench-bin}"
TMP="${TMP:-/tmp/snugkv-cluster-reshard-bench}"
OUT="${OUT:-/tmp/snugkv-cluster-reshard-bench.jsonl}"

SOURCE_PORT="${SOURCE_PORT:-7210}"
TARGET_PORT="${TARGET_PORT:-7211}"
SOURCE_ADDR="127.0.0.1:$SOURCE_PORT"
TARGET_ADDR="127.0.0.1:$TARGET_PORT"

KEYS="${KEYS:-5000}"
VALUE_BYTES="${VALUE_BYTES:-2048}"
REPEATS="${REPEATS:-3}"
SLOT="${SLOT:-8192}"
CONTROL_SECRET="${CONTROL_SECRET:-cluster-reshard-bench-control-secret}"

PIDS=()

cleanup() {
  for pid in "${PIDS[@]:-}"; do
    kill "$pid" 2>/dev/null || true
  done
  wait 2>/dev/null || true
  rm -f "$BIN"
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
require_cmd awk
require_cmd seq
require_cmd python3
require_cmd date

cd "$ROOT"
go build -o "$BIN" ./cmd/snugkv

find_tag() {
  local commands="$TMP/keyslot.commands"
  local slots="$TMP/keyslot.out"
  : >"$commands"
  for i in $(seq 0 50000); do
    printf 'CLUSTER KEYSLOT "reshard:{%d}"\n' "$i" >>"$commands"
  done
  redis-cli --raw -p "$SOURCE_PORT" <"$commands" >"$slots"
  awk -v slot="$SLOT" '$1 == slot { print NR-1; exit }' "$slots"
}

make_config() {
  local port="$1"
  local path="$2"
  cat >"$path" <<JSON
{
  "listen": "127.0.0.1:$port",
  "admin_listen": "",
  "metrics_listen": "",
  "cluster_enabled": true,
  "cluster_node_addr": "127.0.0.1:$port",
  "cluster_control_auth": "$CONTROL_SECRET",
  "cluster_slots": {
    "0-9999": "$SOURCE_ADDR",
    "10000-16383": "$TARGET_ADDR"
  }
}
JSON
}

start_node() {
  local name="$1"
  local cfg="$2"
  "$BIN" -config "$cfg" >"$TMP/$name.log" 2>&1 &
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

reset_nodes() {
  for pid in "${PIDS[@]:-}"; do
    kill "$pid" 2>/dev/null || true
  done
  wait 2>/dev/null || true
  PIDS=()

  rm -rf "$TMP"
  mkdir -p "$TMP"
  make_config "$SOURCE_PORT" "$TMP/source.json"
  make_config "$TARGET_PORT" "$TMP/target.json"

  start_node source "$TMP/source.json"
  start_node target "$TMP/target.json"
  wait_ready "$SOURCE_PORT"
  wait_ready "$TARGET_PORT"
}

: >"$OUT"

echo "SnugKV cluster reshard benchmark"
echo "source=$SOURCE_ADDR target=$TARGET_ADDR slot=$SLOT keys=$KEYS value_bytes=$VALUE_BYTES repeats=$REPEATS"
echo "output=$OUT"

for run in $(seq 1 "$REPEATS"); do
  echo
  echo "===== run $run/$REPEATS ====="
  reset_nodes

  echo "[1/5] locate deterministic hash tag for slot $SLOT"
  tag="$(find_tag)"
  if [[ -z "$tag" ]]; then
    echo "unable to find hash tag for slot $SLOT" >&2
    exit 1
  fi
  echo "tag=$tag"

  echo "[2/5] seed $KEYS keys into source slot"
  value="$(head -c "$VALUE_BYTES" </dev/zero | tr '\0' x)"
  seed="$TMP/seed.commands"
  : >"$seed"
  for i in $(seq 1 "$KEYS"); do
    printf 'SET reshard:{%s}:%06d %s\n' "$tag" "$i" "$value" >>"$seed"
  done

  seed_start_ns="$(date +%s%N)"
  redis-cli --raw -p "$SOURCE_PORT" <"$seed" >"$TMP/seed.out"
  seed_end_ns="$(date +%s%N)"

  if grep -v '^OK$' "$TMP/seed.out" | grep -q .; then
    echo "seed produced unexpected replies" >&2
    exit 1
  fi
  source_before="$(redis-cli --raw -p "$SOURCE_PORT" CLUSTER COUNTKEYSINSLOT "$SLOT")"
  target_before="$(redis-cli --raw -p "$TARGET_PORT" DBSIZE)"
  [[ "$source_before" == "$KEYS" ]]
  [[ "$target_before" == "0" ]]

  echo "[3/5] create rebalance plan"
  plan="$TMP/plan.out"
  redis-cli --raw -p "$SOURCE_PORT" CLUSTER REBALANCE PLAN >"$plan"
  plan_id="$(awk 'NR == 2 {print; exit}' "$plan")"
  if [[ -z "$plan_id" ]]; then
    echo "missing rebalance plan id" >&2
    cat "$plan" >&2
    exit 1
  fi

  echo "[4/5] migrate slot and measure convergence"
  start_ns="$(date +%s%N)"
  redis-cli --raw -p "$SOURCE_PORT" CLUSTER REBALANCE APPLY "$plan_id" ONCE >"$TMP/apply.out"
  command_done_ns="$(date +%s%N)"

  deadline=$((SECONDS + 30))
  converged=0
  while (( SECONDS < deadline )); do
    source_count="$(redis-cli --raw -p "$SOURCE_PORT" DBSIZE 2>/dev/null || echo -1)"
    target_count="$(redis-cli --raw -p "$TARGET_PORT" CLUSTER COUNTKEYSINSLOT "$SLOT" 2>/dev/null || echo -1)"
    moved_reply="$(redis-cli --raw -p "$SOURCE_PORT" GET "reshard:{$tag}:000001" 2>&1 || true)"
    if [[ "$source_count" == "0" &&
          "$target_count" == "$KEYS" &&
          "$moved_reply" == "MOVED $SLOT $TARGET_ADDR" ]]; then
      converged=1
      break
    fi
    sleep 0.01
  done
  end_ns="$(date +%s%N)"

  if (( converged == 0 )); then
    echo "reshard did not converge" >&2
    echo "source_dbsize=$source_count target_slot_count=$target_count moved_reply=$moved_reply" >&2
    cat "$TMP/apply.out" >&2 || true
    exit 1
  fi

  echo "[5/5] verify samples and emit JSON"
  first="reshard:{$tag}:000001"
  last="$(printf 'reshard:{%s}:%06d' "$tag" "$KEYS")"
  [[ "$(redis-cli --raw -p "$TARGET_PORT" STRLEN "$first")" == "$VALUE_BYTES" ]]
  [[ "$(redis-cli --raw -p "$TARGET_PORT" STRLEN "$last")" == "$VALUE_BYTES" ]]

  python3 - "$run" "$KEYS" "$VALUE_BYTES" "$SLOT" "$seed_start_ns" "$seed_end_ns" "$start_ns" "$command_done_ns" "$end_ns" "$OUT" <<'PY'
import json
import sys

run = int(sys.argv[1])
keys = int(sys.argv[2])
value_bytes = int(sys.argv[3])
slot = int(sys.argv[4])
seed_start = int(sys.argv[5])
seed_end = int(sys.argv[6])
start = int(sys.argv[7])
command_done = int(sys.argv[8])
end = int(sys.argv[9])
out_path = sys.argv[10]

seed_ns = seed_end - seed_start
command_ns = command_done - start
converge_ns = end - start
logical_bytes = keys * value_bytes

row = {
    "benchmark": "cluster_reshard",
    "run": run,
    "slot": slot,
    "keys": keys,
    "value_bytes": value_bytes,
    "logical_bytes": logical_bytes,
    "seed_duration_ns": seed_ns,
    "seed_ops_per_second": keys / (seed_ns / 1e9),
    "migration_command_duration_ns": command_ns,
    "convergence_duration_ns": converge_ns,
    "keys_per_second": keys / (converge_ns / 1e9),
    "logical_bytes_per_second": logical_bytes / (converge_ns / 1e9),
    "measurement_note": "black-box two-node single-slot reshard; convergence requires source empty, target owns all slot keys, and source returns MOVED to target",
}

line = json.dumps(row, separators=(",", ":"))
print(line)
with open(out_path, "a", encoding="utf-8") as f:
    f.write(line + "\n")
PY
done

echo
echo "cluster reshard benchmark: PASS"
echo "results=$OUT"
