#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
BIN=/tmp/snugkv-cluster-chaos-target-bin
TMP=/tmp/snugkv-cluster-chaos-target
SOURCE_PORT="${SOURCE_PORT:-7110}"
TARGET_PORT="${TARGET_PORT:-7111}"
SOURCE_ADDR="127.0.0.1:$SOURCE_PORT"
TARGET_ADDR="127.0.0.1:$TARGET_PORT"
CONTROL_SECRET="${CONTROL_SECRET:-cluster-chaos-target-control-secret}"
KEY_COUNT="${KEY_COUNT:-2000}"
VALUE_BYTES="${VALUE_BYTES:-2048}"
SLOT="${SLOT:-8192}"

cleanup() {
  for pidfile in "$TMP"/*.pid; do
    [[ -f "$pidfile" ]] || continue
    pid="$(cat "$pidfile")"
    kill "$pid" 2>/dev/null || true
    wait "$pid" 2>/dev/null || true
  done
  rm -f "$BIN"
}
trap cleanup EXIT

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

rm -rf "$TMP"
mkdir -p "$TMP"

cd "$ROOT"
go build -o "$BIN" ./cmd/snugkv

make_config() {
  local port="$1"
  local path="$2"
  local aof="$3"
  cat >"$path" <<JSON
{
  "listen": "127.0.0.1:$port",
  "admin_listen": "",
  "metrics_listen": "",
  "aof_path": "$aof",
  "fsync": "always",
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

make_config "$SOURCE_PORT" "$TMP/source.json" "$TMP/source.aof"
make_config "$TARGET_PORT" "$TMP/target.json" "$TMP/target.aof"

start_node() {
  local name="$1"
  local cfg="$2"
  "$BIN" -config "$cfg" >"$TMP/$name.log" 2>&1 &
  echo $! >"$TMP/$name.pid"
}

wait_ready() {
  local port="$1"
  for _ in $(seq 1 100); do
    if redis-cli -h 127.0.0.1 -p "$port" PING 2>/dev/null | grep -qx PONG; then
      return 0
    fi
    sleep 0.05
  done
  echo "node on port $port did not become ready" >&2
  return 1
}

stop_node_hard() {
  local name="$1"
  local pid
  pid="$(cat "$TMP/$name.pid")"
  kill -KILL "$pid"
  wait "$pid" 2>/dev/null || true
  rm -f "$TMP/$name.pid"
}

start_node source "$TMP/source.json"
start_node target "$TMP/target.json"
wait_ready "$SOURCE_PORT"
wait_ready "$TARGET_PORT"

echo "[1/8] locate hash tag for slot $SLOT"
commands="$TMP/keyslot.commands"
slots="$TMP/keyslot.out"
: >"$commands"
for i in $(seq 0 50000); do
  printf 'CLUSTER KEYSLOT "chaos-target:{%d}"\n' "$i" >>"$commands"
done
redis-cli --raw -p "$SOURCE_PORT" <"$commands" >"$slots"
tag="$(awk -v slot="$SLOT" '$1 == slot { print NR-1; exit }' "$slots")"
if [[ -z "$tag" ]]; then
  echo "unable to find hash tag for slot $SLOT" >&2
  exit 1
fi
echo "slot tag: $tag"

echo "[2/8] seed $KEY_COUNT durable keys"
value="$(head -c "$VALUE_BYTES" </dev/zero | tr '\0' y)"
seed="$TMP/seed.commands"
: >"$seed"
for i in $(seq 1 "$KEY_COUNT"); do
  printf 'SET chaos-target:{%s}:%06d %s\n' "$tag" "$i" "$value" >>"$seed"
done
redis-cli --raw -p "$SOURCE_PORT" <"$seed" >"$TMP/seed.out"
if grep -v '^OK$' "$TMP/seed.out" | grep -q .; then
  echo "seed produced unexpected replies" >&2
  exit 1
fi
[[ "$(redis-cli --raw -p "$SOURCE_PORT" CLUSTER COUNTKEYSINSLOT "$SLOT")" == "$KEY_COUNT" ]]

echo "[3/8] create and start rebalance plan"
plan="$TMP/plan.out"
redis-cli --raw -p "$SOURCE_PORT" CLUSTER REBALANCE PLAN >"$plan"
plan_id="$(awk 'NR == 2 {print; exit}' "$plan")"
if [[ -z "$plan_id" ]]; then
  echo "missing rebalance plan id" >&2
  cat "$plan" >&2
  exit 1
fi

(
  redis-cli --raw -p "$SOURCE_PORT" CLUSTER REBALANCE APPLY "$plan_id" ONCE     >"$TMP/apply.out" 2>"$TMP/apply.err"
) &
apply_pid=$!

echo "[4/8] wait for partial migration, then SIGKILL target"
partial=0
for _ in $(seq 1 1000); do
  target_count="$(redis-cli --raw -p "$TARGET_PORT" DBSIZE 2>/dev/null || echo 0)"
  if [[ "$target_count" =~ ^[0-9]+$ ]] &&
     (( target_count > 0 && target_count < KEY_COUNT )); then
    partial="$target_count"
    break
  fi
  sleep 0.005
done

if (( partial == 0 )); then
  echo "failed to observe an in-flight partial migration" >&2
  cat "$TMP/apply.out" >&2 || true
  cat "$TMP/apply.err" >&2 || true
  exit 1
fi

echo "observed $partial/$KEY_COUNT keys on target before crash"
stop_node_hard target
wait "$apply_pid" 2>/dev/null || true

source_remaining="$(redis-cli --raw -p "$SOURCE_PORT" CLUSTER COUNTKEYSINSLOT "$SLOT" 2>/dev/null || true)"
if [[ ! "$source_remaining" =~ ^[0-9]+$ ]] ||
   (( source_remaining <= 0 || source_remaining > KEY_COUNT )); then
  echo "unexpected source key count after target crash: $source_remaining" >&2
  echo "--- apply.out ---" >&2
  cat "$TMP/apply.out" >&2 || true
  echo "--- apply.err ---" >&2
  cat "$TMP/apply.err" >&2 || true
  exit 1
fi
# Batched MIGRATE can restore several keys on the target before the source
# receives the complete batch replies. If the target crashes in that window,
# the source may still retain all KEY_COUNT keys. Recovery must tolerate that
# at-least-once state and later converge with REPLACE.
echo "source retained $source_remaining keys after target crash"

echo "[5/8] verify source remains in recoverable MIGRATING state"
recovery="$TMP/recovery-plan-before-target-restart.out"
redis-cli --raw -p "$SOURCE_PORT" CLUSTER REBALANCE RECOVER PLAN >"$recovery"
if ! grep -qx "$SLOT" "$recovery" || ! grep -qx "resume" "$recovery"; then
  echo "source did not expose a resumable recovery plan" >&2
  cat "$recovery" >&2
  exit 1
fi

echo "[6/8] restart target from AOF + topology sidecar"
start_node target "$TMP/target.json"
wait_ready "$TARGET_PORT"

target_recovered="$(redis-cli --raw -p "$TARGET_PORT" DBSIZE)"
if [[ ! "$target_recovered" =~ ^[0-9]+$ ]] ||
   (( target_recovered < 0 || target_recovered > KEY_COUNT )); then
  echo "unexpected target recovered key count: $target_recovered" >&2
  exit 1
fi
# A hard target crash may lose an in-flight batch that had not reached durable
# AOF state yet, so zero recovered target keys is also a valid recovery point.
# The source-side MIGRATING marker plus RECOVER RESUME is the correctness gate.
echo "target recovered $target_recovered keys"

echo "[7/8] resume migration and converge ownership"
if ! redis-cli --raw -p "$SOURCE_PORT" CLUSTER REBALANCE RECOVER RESUME "$SLOT"   >"$TMP/resume.out" 2>"$TMP/resume.err"; then
  echo "resume command failed" >&2
  cat "$TMP/resume.out" >&2 || true
  cat "$TMP/resume.err" >&2 || true
  exit 1
fi
cat "$TMP/resume.out"

target_slot_count="$(redis-cli --raw -p "$TARGET_PORT" CLUSTER COUNTKEYSINSLOT "$SLOT" 2>&1 || true)"
target_dbsize="$(redis-cli --raw -p "$TARGET_PORT" DBSIZE 2>&1 || true)"
source_dbsize="$(redis-cli --raw -p "$SOURCE_PORT" DBSIZE 2>&1 || true)"

printf 'post-resume target_slot_count=%s target_dbsize=%s source_dbsize=%s\n' \
  "$target_slot_count" "$target_dbsize" "$source_dbsize"

if [[ "$target_slot_count" != "$KEY_COUNT" ||
      "$target_dbsize" != "$KEY_COUNT" ||
      "$source_dbsize" != "0" ]]; then
  echo "post-resume convergence assertion failed" >&2
  echo "--- source CLUSTER NODES ---" >&2
  redis-cli --raw -p "$SOURCE_PORT" CLUSTER NODES >&2 || true
  echo "--- target CLUSTER NODES ---" >&2
  redis-cli --raw -p "$TARGET_PORT" CLUSTER NODES >&2 || true
  echo "--- source log tail ---" >&2
  tail -n 80 "$TMP/source.log" >&2 || true
  echo "--- target log tail ---" >&2
  tail -n 80 "$TMP/target.log" >&2 || true
  exit 1
fi

echo "[8/8] verify redirects and sample values"
first="chaos-target:{$tag}:000001"
last="$(printf 'chaos-target:{%s}:%06d' "$tag" "$KEY_COUNT")"
moved="$(redis-cli --raw -p "$SOURCE_PORT" GET "$first" 2>&1 || true)"
grep -Fq "MOVED $SLOT $TARGET_ADDR" <<<"$moved"
[[ "$(redis-cli --raw -p "$TARGET_PORT" STRLEN "$first")" == "$VALUE_BYTES" ]]
[[ "$(redis-cli --raw -p "$TARGET_PORT" STRLEN "$last")" == "$VALUE_BYTES" ]]

echo "cluster target-crash recovery chaos: PASS"
echo "partial_before_kill=$partial source_remaining=$source_remaining target_recovered=$target_recovered final_target_keys=$KEY_COUNT"
