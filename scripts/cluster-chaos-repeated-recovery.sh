#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
BIN=/tmp/snugkv-cluster-chaos-repeat-bin
TMP=/tmp/snugkv-cluster-chaos-repeat
SOURCE_PORT="${SOURCE_PORT:-7130}"
TARGET_PORT="${TARGET_PORT:-7131}"
SOURCE_ADDR="127.0.0.1:$SOURCE_PORT"
TARGET_ADDR="127.0.0.1:$TARGET_PORT"
CONTROL_SECRET="${CONTROL_SECRET:-cluster-chaos-repeat-control-secret}"
KEY_COUNT="${KEY_COUNT:-5000}"
VALUE_BYTES="${VALUE_BYTES:-4096}"
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
  for _ in $(seq 1 200); do
    if redis-cli --raw -p "$port" PING 2>/dev/null | grep -qx PONG; then
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

dump_state() {
  echo "--- source CLUSTER NODES ---" >&2
  redis-cli --raw -p "$SOURCE_PORT" CLUSTER NODES >&2 || true
  echo "--- target CLUSTER NODES ---" >&2
  redis-cli --raw -p "$TARGET_PORT" CLUSTER NODES >&2 || true
  echo "--- source log tail ---" >&2
  tail -n 100 "$TMP/source.log" >&2 || true
  echo "--- target log tail ---" >&2
  tail -n 100 "$TMP/target.log" >&2 || true
}

start_node source "$TMP/source.json"
start_node target "$TMP/target.json"
wait_ready "$SOURCE_PORT"
wait_ready "$TARGET_PORT"

echo "[1/9] locate hash tag for slot $SLOT"
commands="$TMP/keyslot.commands"
slots="$TMP/keyslot.out"
: >"$commands"
for i in $(seq 0 50000); do
  printf 'CLUSTER KEYSLOT "chaos-repeat:{%d}"\n' "$i" >>"$commands"
done
redis-cli --raw -p "$SOURCE_PORT" <"$commands" >"$slots"
tag="$(awk -v slot="$SLOT" '$1 == slot { print NR-1; exit }' "$slots")"
if [[ -z "$tag" ]]; then
  echo "unable to find hash tag for slot $SLOT" >&2
  exit 1
fi
echo "slot tag: $tag"

echo "[2/9] seed $KEY_COUNT durable keys"
value="$(head -c "$VALUE_BYTES" </dev/zero | tr '\0' z)"
seed="$TMP/seed.commands"
: >"$seed"
for i in $(seq 1 "$KEY_COUNT"); do
  printf 'SET chaos-repeat:{%s}:%06d %s\n' "$tag" "$i" "$value" >>"$seed"
done
redis-cli --raw -p "$SOURCE_PORT" <"$seed" >"$TMP/seed.out"
if grep -v '^OK$' "$TMP/seed.out" | grep -q .; then
  echo "seed produced unexpected replies" >&2
  exit 1
fi
[[ "$(redis-cli --raw -p "$SOURCE_PORT" CLUSTER COUNTKEYSINSLOT "$SLOT")" == "$KEY_COUNT" ]]

echo "[3/9] start rebalance and crash target during initial migration"
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

first_partial=0
for _ in $(seq 1 2000); do
  n="$(redis-cli --raw -p "$TARGET_PORT" DBSIZE 2>/dev/null || echo 0)"
  if [[ "$n" =~ ^[0-9]+$ ]] && (( n > 0 && n < KEY_COUNT )); then
    first_partial="$n"
    break
  fi
  sleep 0.002
done
if (( first_partial == 0 )); then
  echo "failed to observe first partial migration" >&2
  cat "$TMP/apply.out" >&2 || true
  cat "$TMP/apply.err" >&2 || true
  exit 1
fi
echo "first interruption: target had $first_partial/$KEY_COUNT keys"
stop_node_hard target
wait "$apply_pid" 2>/dev/null || true

# Batched MIGRATE creates an at-least-once crash window: the target can have
# restored part of a batch while the source has not yet received all replies
# and durably recorded deletions. Intermediate recovery counts may therefore
# range all the way from zero/full retention to partial progress. The strict
# correctness requirement is final convergence after RECOVER RESUME.

source_after_target_crash="$(redis-cli --raw -p "$SOURCE_PORT" CLUSTER COUNTKEYSINSLOT "$SLOT" 2>/dev/null || true)"
if [[ ! "$source_after_target_crash" =~ ^[0-9]+$ ]] ||
   (( source_after_target_crash <= 0 || source_after_target_crash > KEY_COUNT )); then
  echo "unexpected source count after target crash: $source_after_target_crash" >&2
  dump_state
  exit 1
fi

echo "[4/9] restart target and verify resumable recovery"
start_node target "$TMP/target.json"
wait_ready "$TARGET_PORT"

target_recovered="$(redis-cli --raw -p "$TARGET_PORT" DBSIZE)"
if [[ ! "$target_recovered" =~ ^[0-9]+$ ]] ||
   (( target_recovered < 0 || target_recovered > KEY_COUNT )); then
  echo "unexpected target recovered count: $target_recovered" >&2
  dump_state
  exit 1
fi

recovery1="$TMP/recovery1.out"
redis-cli --raw -p "$SOURCE_PORT" CLUSTER REBALANCE RECOVER PLAN >"$recovery1"
if ! grep -qx "$SLOT" "$recovery1" || ! grep -qx "resume" "$recovery1"; then
  echo "first recovery plan is not resumable" >&2
  cat "$recovery1" >&2
  dump_state
  exit 1
fi
echo "target recovered $target_recovered keys; source retains $source_after_target_crash"

echo "[5/9] resume migration and SIGKILL source during recovery"
(
  redis-cli --raw -p "$SOURCE_PORT" CLUSTER REBALANCE RECOVER RESUME "$SLOT"     >"$TMP/resume1.out" 2>"$TMP/resume1.err"
) &
resume_pid=$!

second_partial=0
for _ in $(seq 1 3000); do
  n="$(redis-cli --raw -p "$TARGET_PORT" DBSIZE 2>/dev/null || echo 0)"
  if [[ "$n" =~ ^[0-9]+$ ]] &&
     (( n > target_recovered && n < KEY_COUNT )); then
    second_partial="$n"
    break
  fi
  sleep 0.001
done

if (( second_partial == 0 )); then
  echo "failed to observe second in-flight partial migration" >&2
  cat "$TMP/resume1.out" >&2 || true
  cat "$TMP/resume1.err" >&2 || true
  dump_state
  exit 1
fi

echo "second interruption: target advanced to $second_partial/$KEY_COUNT keys"
stop_node_hard source
wait "$resume_pid" 2>/dev/null || true

target_after_source_crash="$(redis-cli --raw -p "$TARGET_PORT" DBSIZE)"
if [[ ! "$target_after_source_crash" =~ ^[0-9]+$ ]] ||
   (( target_after_source_crash <= target_recovered || target_after_source_crash > KEY_COUNT )); then
  echo "unexpected target count after source crash: $target_after_source_crash" >&2
  dump_state
  exit 1
fi

echo "[6/9] restart source and require a second recovery plan"
start_node source "$TMP/source.json"
wait_ready "$SOURCE_PORT"

source_recovered="$(redis-cli --raw -p "$SOURCE_PORT" CLUSTER COUNTKEYSINSLOT "$SLOT" 2>/dev/null || true)"
if [[ ! "$source_recovered" =~ ^[0-9]+$ ]] ||
   (( source_recovered <= 0 || source_recovered > KEY_COUNT )); then
  echo "unexpected source recovered count after second crash: $source_recovered" >&2
  dump_state
  exit 1
fi

recovery2="$TMP/recovery2.out"
redis-cli --raw -p "$SOURCE_PORT" CLUSTER REBALANCE RECOVER PLAN >"$recovery2"
if ! grep -qx "$SLOT" "$recovery2" || ! grep -qx "resume" "$recovery2"; then
  echo "second recovery plan is not resumable" >&2
  cat "$recovery2" >&2
  dump_state
  exit 1
fi
echo "source recovered $source_recovered keys after second interruption"

echo "[7/9] resume a second time and converge"
if ! redis-cli --raw -p "$SOURCE_PORT" CLUSTER REBALANCE RECOVER RESUME "$SLOT"   >"$TMP/resume2.out" 2>"$TMP/resume2.err"; then
  echo "second recovery resume failed" >&2
  cat "$TMP/resume2.out" >&2 || true
  cat "$TMP/resume2.err" >&2 || true
  dump_state
  exit 1
fi
cat "$TMP/resume2.out"

target_slot_count="$(redis-cli --raw -p "$TARGET_PORT" CLUSTER COUNTKEYSINSLOT "$SLOT" 2>&1 || true)"
target_dbsize="$(redis-cli --raw -p "$TARGET_PORT" DBSIZE 2>&1 || true)"
source_dbsize="$(redis-cli --raw -p "$SOURCE_PORT" DBSIZE 2>&1 || true)"
printf 'final target_slot_count=%s target_dbsize=%s source_dbsize=%s\n'   "$target_slot_count" "$target_dbsize" "$source_dbsize"

if [[ "$target_slot_count" != "$KEY_COUNT" ||
      "$target_dbsize" != "$KEY_COUNT" ||
      "$source_dbsize" != "0" ]]; then
  echo "final repeated-recovery convergence failed" >&2
  dump_state
  exit 1
fi

echo "[8/9] verify recovery state is cleared"
post="$TMP/post-recovery.out"
redis-cli --raw -p "$SOURCE_PORT" CLUSTER REBALANCE RECOVER PLAN >"$post"
if grep -qx "$SLOT" "$post"; then
  echo "slot $SLOT still appears in recovery plan" >&2
  cat "$post" >&2
  exit 1
fi

echo "[9/9] verify redirect and data samples"
first="chaos-repeat:{$tag}:000001"
middle="$(printf 'chaos-repeat:{%s}:%06d' "$tag" "$((KEY_COUNT / 2))")"
last="$(printf 'chaos-repeat:{%s}:%06d' "$tag" "$KEY_COUNT")"

moved="$(redis-cli --raw -p "$SOURCE_PORT" GET "$first" 2>&1 || true)"
grep -Fq "MOVED $SLOT $TARGET_ADDR" <<<"$moved"
[[ "$(redis-cli --raw -p "$TARGET_PORT" STRLEN "$first")" == "$VALUE_BYTES" ]]
[[ "$(redis-cli --raw -p "$TARGET_PORT" STRLEN "$middle")" == "$VALUE_BYTES" ]]
[[ "$(redis-cli --raw -p "$TARGET_PORT" STRLEN "$last")" == "$VALUE_BYTES" ]]

echo "cluster repeated crash/recovery chaos: PASS"
echo "first_partial=$first_partial target_recovered=$target_recovered second_partial=$second_partial source_recovered=$source_recovered final_target_keys=$KEY_COUNT"
