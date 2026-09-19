#!/usr/bin/env bash
set -euo pipefail

REDIS_ADDR="${REDIS_ADDR:-127.0.0.1:6390}"
SNUG_ADDR="${SNUG_ADDR:-127.0.0.1:6380}"
KEYS="${KEYS:-1000000}"
OPS="${OPS:-1000000}"
VALUE_BYTES="${VALUE_BYTES:-64}"
WORKERS="${WORKERS:-4}"
PIPELINE="${PIPELINE:-256}"
RUNS="${RUNS:-3}"
OUT_DIR="${OUT_DIR:-benchmark-results/redis-vs-snug-$(date +%Y%m%d-%H%M%S)}"

mkdir -p "$OUT_DIR"

echo "Building rediswirebench..."
go build -o /tmp/rediswirebench ./cmd/rediswirebench

run_one() {
  local name="$1"
  local addr="$2"
  local run="$3"

  echo
  echo "===== $name run $run/$RUNS: load ====="
  /tmp/rediswirebench     -server "$name"     -addr "$addr"     -workload load     -keys "$KEYS"     -value-bytes "$VALUE_BYTES"     -workers "$WORKERS"     -pipeline "$PIPELINE"     -reset     > "$OUT_DIR/${name}-run${run}-load.json"
  cat "$OUT_DIR/${name}-run${run}-load.json"

  for workload in get mixed ttl; do
    echo
    echo "===== $name run $run/$RUNS: $workload ====="
    /tmp/rediswirebench       -server "$name"       -addr "$addr"       -workload "$workload"       -keys "$KEYS"       -ops "$OPS"       -value-bytes "$VALUE_BYTES"       -workers "$WORKERS"       -pipeline "$PIPELINE"       > "$OUT_DIR/${name}-run${run}-${workload}.json"
    cat "$OUT_DIR/${name}-run${run}-${workload}.json"
  done

  redis-cli -h "${addr%:*}" -p "${addr##*:}" FLUSHDB >/dev/null
}

echo "Redis:  $REDIS_ADDR"
echo "SnugKV: $SNUG_ADDR"
echo "keys=$KEYS ops=$OPS value_bytes=$VALUE_BYTES workers=$WORKERS pipeline=$PIPELINE runs=$RUNS"
echo "results=$OUT_DIR"

for run in $(seq 1 "$RUNS"); do
  run_one redis "$REDIS_ADDR" "$run"
  run_one snugkv "$SNUG_ADDR" "$run"
done

python3 - "$OUT_DIR" <<'PY'
import json, pathlib, statistics, sys

root = pathlib.Path(sys.argv[1])
rows = []
for path in sorted(root.glob("*.json")):
    with path.open() as f:
        rows.append(json.load(f))

groups = {}
for row in rows:
    groups.setdefault((row["server"], row["workload"]), []).append(row)

summary = []
for (server, workload), items in sorted(groups.items()):
    entry = {
        "server": server,
        "workload": workload,
        "runs": len(items),
        "ops_per_second_median": statistics.median(x["ops_per_second"] for x in items),
        "p50_us_median": statistics.median(x["p50_ns"] for x in items) / 1000,
        "p95_us_median": statistics.median(x["p95_ns"] for x in items) / 1000,
        "p99_us_median": statistics.median(x["p99_ns"] for x in items) / 1000,
    }
    if workload == "load":
        entry["bytes_per_key_delta_median"] = statistics.median(x["bytes_per_key_delta"] for x in items)
        entry["used_memory_after_median"] = statistics.median(x["used_memory_after"] for x in items)
    summary.append(entry)

with (root / "summary.json").open("w") as f:
    json.dump(summary, f, indent=2)

print("\n===== MEDIAN SUMMARY =====")
for row in summary:
    print(row)
print("\nsummary:", root / "summary.json")
PY
