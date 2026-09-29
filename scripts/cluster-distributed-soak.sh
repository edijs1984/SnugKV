#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

DURATION_SECONDS="${DURATION_SECONDS:-3600}"
MAX_CYCLES="${MAX_CYCLES:-0}"
CASE_TIMEOUT_SECONDS="${CASE_TIMEOUT_SECONDS:-240}"
OUT="${OUT:-/tmp/snugkv-cluster-distributed-soak.jsonl}"
LOG_DIR="${LOG_DIR:-/tmp/snugkv-cluster-distributed-soak-logs}"

cases=(
  scripts/cluster-chaos-rebalance-restart.sh
  scripts/cluster-chaos-target-restart.sh
  scripts/cluster-chaos-failover-restart.sh
  scripts/cluster-chaos-repeated-recovery.sh
  scripts/cluster-chaos-majority-partition.sh
  scripts/cluster-chaos-persistence-failure.sh
  scripts/cluster-chaos-corrupt-replica.sh
)

require_cmd() {
  command -v "$1" >/dev/null 2>&1 || {
    echo "missing required command: $1" >&2
    exit 1
  }
}

require_cmd bash
require_cmd timeout
require_cmd python3
require_cmd date

if ! [[ "$DURATION_SECONDS" =~ ^[0-9]+$ ]] || (( DURATION_SECONDS <= 0 )); then
  echo "DURATION_SECONDS must be a positive integer" >&2
  exit 1
fi
if ! [[ "$MAX_CYCLES" =~ ^[0-9]+$ ]]; then
  echo "MAX_CYCLES must be a non-negative integer" >&2
  exit 1
fi
if ! [[ "$CASE_TIMEOUT_SECONDS" =~ ^[0-9]+$ ]] || (( CASE_TIMEOUT_SECONDS <= 0 )); then
  echo "CASE_TIMEOUT_SECONDS must be a positive integer" >&2
  exit 1
fi

rm -rf "$LOG_DIR"
mkdir -p "$LOG_DIR"
: >"$OUT"

start_epoch="$(date +%s)"
deadline=$((start_epoch + DURATION_SECONDS))
cycle=0
passed=0
failed=0
timed_out=0

emit_row() {
  local cycle_no="$1"
  local case_name="$2"
  local status="$3"
  local start_ns="$4"
  local end_ns="$5"
  local log_path="$6"

  python3 - "$cycle_no" "$case_name" "$status" "$start_ns" "$end_ns" "$log_path" "$OUT" <<'PY'
import json
import sys

cycle = int(sys.argv[1])
case = sys.argv[2]
status = sys.argv[3]
start_ns = int(sys.argv[4])
end_ns = int(sys.argv[5])
log_path = sys.argv[6]
out_path = sys.argv[7]

row = {
    "benchmark": "cluster_distributed_soak",
    "cycle": cycle,
    "case": case,
    "status": status,
    "duration_ns": end_ns - start_ns,
    "log": log_path,
}
line = json.dumps(row, separators=(",", ":"))
print(line)
with open(out_path, "a", encoding="utf-8") as f:
    f.write(line + "\n")
PY
}

echo "SnugKV distributed cluster soak"
echo "duration_seconds=$DURATION_SECONDS max_cycles=$MAX_CYCLES case_timeout_seconds=$CASE_TIMEOUT_SECONDS"
echo "cases=${#cases[@]}"
echo "results=$OUT"
echo "logs=$LOG_DIR"

while :; do
  now="$(date +%s)"
  if (( now >= deadline )); then
    break
  fi
  if (( MAX_CYCLES > 0 && cycle >= MAX_CYCLES )); then
    break
  fi

  cycle=$((cycle + 1))
  echo
  echo "===== cycle $cycle ====="

  for test_script in "${cases[@]}"; do
    now="$(date +%s)"
    if (( now >= deadline )); then
      break 2
    fi

    base="$(basename "$test_script" .sh)"
    log_path="$LOG_DIR/cycle-$(printf '%04d' "$cycle")-$base.log"
    echo "--- $test_script ---"

    bash -n "$test_script"

    start_ns="$(date +%s%N)"
    set +e
    timeout --signal=TERM --kill-after=10s "${CASE_TIMEOUT_SECONDS}s"       bash "$test_script" >"$log_path" 2>&1
    rc=$?
    set -e
    end_ns="$(date +%s%N)"

    if (( rc == 0 )); then
      status="pass"
      passed=$((passed + 1))
    elif (( rc == 124 || rc == 137 )); then
      status="timeout"
      timed_out=$((timed_out + 1))
      failed=$((failed + 1))
    else
      status="fail"
      failed=$((failed + 1))
    fi

    emit_row "$cycle" "$test_script" "$status" "$start_ns" "$end_ns" "$log_path"

    if [[ "$status" != "pass" ]]; then
      echo "soak case failed: cycle=$cycle case=$test_script status=$status rc=$rc" >&2
      echo "--- tail: $log_path ---" >&2
      tail -n 120 "$log_path" >&2 || true
      echo "distributed soak: FAIL" >&2
      exit 1
    fi
  done
done

end_epoch="$(date +%s)"
elapsed=$((end_epoch - start_epoch))

python3 - "$cycle" "$passed" "$failed" "$timed_out" "$elapsed" "$OUT" <<'PY'
import json
import sys

row = {
    "benchmark": "cluster_distributed_soak_summary",
    "cycles_started": int(sys.argv[1]),
    "cases_passed": int(sys.argv[2]),
    "cases_failed": int(sys.argv[3]),
    "cases_timed_out": int(sys.argv[4]),
    "elapsed_seconds": int(sys.argv[5]),
    "status": "pass" if int(sys.argv[3]) == 0 else "fail",
}
line = json.dumps(row, separators=(",", ":"))
print(line)
with open(sys.argv[6], "a", encoding="utf-8") as f:
    f.write(line + "\n")
PY

echo
echo "distributed soak: PASS"
echo "cycles_started=$cycle cases_passed=$passed elapsed_seconds=$elapsed"
echo "results=$OUT"
