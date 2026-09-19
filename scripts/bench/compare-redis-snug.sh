#!/usr/bin/env bash
set -euo pipefail

REDIS_ADDR="${REDIS_ADDR:-127.0.0.1:6390}"
SNUG_RAW_ADDR="${SNUG_RAW_ADDR:-127.0.0.1:6382}"
SNUG_OPT_ADDR="${SNUG_OPT_ADDR:-127.0.0.1:6383}"

KEYS="${KEYS:-1000000}"
OPS="${OPS:-1000000}"
VALUE_BYTES="${VALUE_BYTES:-64}"
VALUE_SHAPE="${VALUE_SHAPE:-repetitive}"
SETTLE_MS="${SETTLE_MS:-0}"
WORKERS="${WORKERS:-4}"
PIPELINE="${PIPELINE:-256}"
RUNS="${RUNS:-3}"

REDIS_CONTAINER="${REDIS_CONTAINER:-snug-bench-redis}"
SNUG_RAW_CONTAINER="${SNUG_RAW_CONTAINER:-snug-bench-snugkv-raw}"
SNUG_OPT_CONTAINER="${SNUG_OPT_CONTAINER:-snug-bench-snugkv-opt}"

OUT_DIR="${OUT_DIR:-benchmark-results/redis-vs-snug-modes-$(date +%Y%m%d-%H%M%S)}"

mkdir -p "$OUT_DIR"

echo "Building rediswirebench..."
go build -o /tmp/rediswirebench ./cmd/rediswirebench

container_bytes() {
  local container="$1"
  if ! docker inspect "$container" >/dev/null 2>&1; then
    echo 0
    return
  fi

  local raw
  raw="$(docker stats --no-stream --format '{{.MemUsage}}' "$container" | awk -F/ '{gsub(/^ +| +$/, "", $1); print $1}')"

  python3 - "$raw" <<'PY'
import re, sys
s=sys.argv[1].strip()
m=re.fullmatch(r"([0-9.]+)([KMGTP]?i?B)", s)
if not m:
    print(0)
    raise SystemExit
n=float(m.group(1))
u=m.group(2)
mult={
    "B":1,
    "KB":1000,
    "MB":1000**2,
    "GB":1000**3,
    "TB":1000**4,
    "KiB":1024,
    "MiB":1024**2,
    "GiB":1024**3,
    "TiB":1024**4,
}
print(int(n*mult[u]))
PY
}

annotate_memory() {
  local path="$1" before="$2" after="$3"

  python3 - "$path" "$before" "$after" <<'PY'
import json, sys
p=sys.argv[1]
with open(p) as f:
    d=json.load(f)
b=int(sys.argv[2])
a=int(sys.argv[3])
d["container_memory_before"]=b
d["container_memory_after"]=a
d["container_memory_delta"]=max(0,a-b)
with open(p,"w") as f:
    json.dump(d,f,separators=(",",":"))
PY
}

run_one() {
  local name="$1"
  local addr="$2"
  local run="$3"
  local container="$4"

  redis-cli -h "${addr%:*}" -p "${addr##*:}" FLUSHDB >/dev/null
  sleep 0.2

  local mem_before mem_after
  mem_before="$(container_bytes "$container")"

  echo
  echo "===== $name run $run/$RUNS: load ====="
  /tmp/rediswirebench \
    -server "$name" \
    -addr "$addr" \
    -workload load \
    -keys "$KEYS" \
    -value-bytes "$VALUE_BYTES" \
    -value-shape "$VALUE_SHAPE" \
    -settle-ms "$SETTLE_MS" \
    -workers "$WORKERS" \
    -pipeline "$PIPELINE" \
    > "$OUT_DIR/${name}-run${run}-load.json"

  mem_after="$(container_bytes "$container")"
  annotate_memory "$OUT_DIR/${name}-run${run}-load.json" "$mem_before" "$mem_after"
  cat "$OUT_DIR/${name}-run${run}-load.json"

  for workload in get mixed ttl; do
    echo
    echo "===== $name run $run/$RUNS: $workload ====="
    /tmp/rediswirebench \
      -server "$name" \
      -addr "$addr" \
      -workload "$workload" \
      -keys "$KEYS" \
      -ops "$OPS" \
      -value-bytes "$VALUE_BYTES" \
      -value-shape "$VALUE_SHAPE" \
      -settle-ms 0 \
      -workers "$WORKERS" \
      -pipeline "$PIPELINE" \
      > "$OUT_DIR/${name}-run${run}-${workload}.json"
    cat "$OUT_DIR/${name}-run${run}-${workload}.json"
  done

  redis-cli -h "${addr%:*}" -p "${addr##*:}" FLUSHDB >/dev/null
}

echo "Redis:       $REDIS_ADDR"
echo "SnugKV raw:  $SNUG_RAW_ADDR"
echo "SnugKV opt:  $SNUG_OPT_ADDR"
echo "keys=$KEYS ops=$OPS value_bytes=$VALUE_BYTES value_shape=$VALUE_SHAPE settle_ms=$SETTLE_MS workers=$WORKERS pipeline=$PIPELINE runs=$RUNS"
echo "results=$OUT_DIR"

for run in $(seq 1 "$RUNS"); do
  run_one redis "$REDIS_ADDR" "$run" "$REDIS_CONTAINER"
  run_one snug_raw "$SNUG_RAW_ADDR" "$run" "$SNUG_RAW_CONTAINER"
  run_one snug_opt "$SNUG_OPT_ADDR" "$run" "$SNUG_OPT_CONTAINER"
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
        entry["container_memory_after_median"] = statistics.median(x.get("container_memory_after",0) for x in items)
        entry["container_memory_delta_median"] = statistics.median(x.get("container_memory_delta",0) for x in items)
    summary.append(entry)

with (root / "summary.json").open("w") as f:
    json.dump(summary, f, indent=2)

print("\n===== MEDIAN SUMMARY =====")
for row in summary:
    print(row)
print("\nsummary:", root / "summary.json")
PY
