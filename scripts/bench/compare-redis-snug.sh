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
SETTLE_STABLE_SAMPLES="${SETTLE_STABLE_SAMPLES:-5}"
SETTLE_POLL_MS="${SETTLE_POLL_MS:-1000}"
SETTLE_MAX_MS="${SETTLE_MAX_MS:-120000}"
FRESH_SERVERS="${FRESH_SERVERS:-1}"
WORKERS="${WORKERS:-4}"
PIPELINE="${PIPELINE:-256}"
RUNS="${RUNS:-3}"

REDIS_CONTAINER="${REDIS_CONTAINER:-snug-bench-redis}"
SNUG_RAW_CONTAINER="${SNUG_RAW_CONTAINER:-snug-bench-snugkv-raw}"
SNUG_OPT_CONTAINER="${SNUG_OPT_CONTAINER:-snug-bench-snugkv-opt}"

PPROF_RAW_URL="${PPROF_RAW_URL:-http://127.0.0.1:6060/debug/pprof/heap}"
PPROF_OPT_URL="${PPROF_OPT_URL:-http://127.0.0.1:6061/debug/pprof/heap}"
CAPTURE_HEAP="${CAPTURE_HEAP:-1}"

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

server_used_memory() {
  local addr="$1"
  redis-cli -h "${addr%:*}" -p "${addr##*:}" --raw INFO memory \
    | awk -F: '/^used_memory:/{gsub(/\r/,"",$2); print $2; exit}'
}

wait_for_memory_stable() {
  local addr="$1"
  local label="$2"
  local stable=0
  local previous=""
  local elapsed=0
  local current=""

  while (( elapsed <= SETTLE_MAX_MS )); do
    current="$(server_used_memory "$addr")"
    if [[ -z "$current" ]]; then
      echo "failed to read used_memory from $label" >&2
      return 1
    fi

    if [[ "$current" == "$previous" ]]; then
      stable=$((stable + 1))
    else
      stable=0
    fi

    if (( stable >= SETTLE_STABLE_SAMPLES )); then
      echo "$current $elapsed"
      return 0
    fi

    previous="$current"
    sleep "$(python3 - "$SETTLE_POLL_MS" <<'PY'
import sys
print(int(sys.argv[1]) / 1000)
PY
)"
    elapsed=$((elapsed + SETTLE_POLL_MS))
  done

  echo "$current $elapsed"
}

annotate_settled_memory() {
  local path="$1"
  local settled="$2"
  local settle_elapsed="$3"

  python3 - "$path" "$settled" "$settle_elapsed" <<'PY'
import json, sys
p=sys.argv[1]
settled=int(sys.argv[2])
elapsed=int(sys.argv[3])
with open(p) as f:
    d=json.load(f)
before=int(d["used_memory_before"])
d["used_memory_after_initial"]=d["used_memory_after"]
d["used_memory_after"]=settled
d["used_memory_delta"]=max(0, settled-before)
if d.get("workload")=="load":
    d["bytes_per_key_delta"]=d["used_memory_delta"]/d["keys"]
d["settle_actual_ms"]=elapsed
with open(p,"w") as f:
    json.dump(d,f,separators=(",",":"))
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

capture_heap_profile() {
  local name="$1"
  local run="$2"
  local url=""

  if [[ "$CAPTURE_HEAP" != "1" ]]; then
    return
  fi

  case "$name" in
    snug_raw) url="$PPROF_RAW_URL" ;;
    snug_opt) url="$PPROF_OPT_URL" ;;
    *) return ;;
  esac

  local profile="$OUT_DIR/${name}-run${run}-heap.pprof"
  local report="$OUT_DIR/${name}-run${run}-heap-top.txt"

  if curl -fsS "$url" -o "$profile"; then
    go tool pprof -top -sample_index=inuse_space -nodecount=40 "$profile" > "$report" 2>&1 || true
  fi
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
    -settle-ms 0 \
    -workers "$WORKERS" \
    -pipeline "$PIPELINE" \
    > "$OUT_DIR/${name}-run${run}-load.json"

  if (( SETTLE_MS > 0 )); then
    sleep "$(python3 - "$SETTLE_MS" <<'PY'
import sys
print(int(sys.argv[1]) / 1000)
PY
)"
  fi

  read -r settled_memory settle_actual_ms < <(wait_for_memory_stable "$addr" "$name")
  annotate_settled_memory "$OUT_DIR/${name}-run${run}-load.json" "$settled_memory" "$settle_actual_ms"

  mem_after="$(container_bytes "$container")"
  annotate_memory "$OUT_DIR/${name}-run${run}-load.json" "$mem_before" "$mem_after"
  capture_heap_profile "$name" "$run"
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
echo "keys=$KEYS ops=$OPS value_bytes=$VALUE_BYTES value_shape=$VALUE_SHAPE settle_ms=$SETTLE_MS stable_samples=$SETTLE_STABLE_SAMPLES settle_max_ms=$SETTLE_MAX_MS fresh_servers=$FRESH_SERVERS workers=$WORKERS pipeline=$PIPELINE runs=$RUNS capture_heap=$CAPTURE_HEAP"
echo "results=$OUT_DIR"

for run in $(seq 1 "$RUNS"); do
  if [[ "$FRESH_SERVERS" == "1" ]]; then
    echo
    echo "===== restarting benchmark servers for run $run/$RUNS ====="
    BUILD_IMAGE=0 bash scripts/bench/start-fair-servers.sh >/dev/null
  fi

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
print("heap reports:", root / "snug_raw-run*-heap-top.txt", "and", root / "snug_opt-run*-heap-top.txt")
PY
