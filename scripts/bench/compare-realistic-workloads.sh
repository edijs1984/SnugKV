#!/usr/bin/env bash
set -euo pipefail

# Fast realistic comparison.
#
# Defaults are intentionally modest:
# - one server process/container at a time;
# - Redis vs optimized SnugKV only;
# - one run per profile;
# - LOAD + GET only;
# - fixed post-load settle instead of waiting up to 120s for "stability".
#
# Opt into the expensive cases when needed:
#   SERVERS="redis snug-opt snug-raw"
#   WORKLOADS="load get mixed ttl"
#   RUNS=3
#   MIXED_OPS=1000000 TTL_OPS=1000000

KEYS="${KEYS:-1000000}"
GET_OPS="${GET_OPS:-2000000}"
MIXED_OPS="${MIXED_OPS:-250000}"
TTL_OPS="${TTL_OPS:-250000}"
WORKERS="${WORKERS:-8}"
PIPELINE="${PIPELINE:-256}"
RUNS="${RUNS:-1}"
SETTLE_MS="${SETTLE_MS:-10000}"
SERVERS="${SERVERS:-redis snug-opt}"
WORKLOADS="${WORKLOADS:-load get}"
ROOT_OUT="${ROOT_OUT:-benchmark-results/realistic-$(date +%Y%m%d-%H%M%S)}"

REDIS_ADDR="${REDIS_ADDR:-127.0.0.1:6390}"
SNUG_RAW_ADDR="${SNUG_RAW_ADDR:-127.0.0.1:6382}"
SNUG_OPT_ADDR="${SNUG_OPT_ADDR:-127.0.0.1:6383}"

REDIS_CONTAINER="${REDIS_CONTAINER:-snug-bench-redis}"
SNUG_RAW_CONTAINER="${SNUG_RAW_CONTAINER:-snug-bench-snugkv-raw}"
SNUG_OPT_CONTAINER="${SNUG_OPT_CONTAINER:-snug-bench-snugkv-opt}"

profiles=(
  "session-json:384"
  "api-json:768"
  "counter:10"
  "uuid:36"
  "text:256"
  "repetitive:256"
  "compressed:256"
  "random:256"
)

mkdir -p "$ROOT_OUT"
go build -o /tmp/rediswirebench ./cmd/rediswirebench

server_addr() {
  case "$1" in
    redis) echo "$REDIS_ADDR" ;;
    snug-raw) echo "$SNUG_RAW_ADDR" ;;
    snug-opt) echo "$SNUG_OPT_ADDR" ;;
    *) echo "unknown server $1" >&2; exit 2 ;;
  esac
}

server_label() {
  case "$1" in
    redis) echo "redis" ;;
    snug-raw) echo "snug_raw" ;;
    snug-opt) echo "snug_opt" ;;
  esac
}

container_name() {
  case "$1" in
    redis) echo "$REDIS_CONTAINER" ;;
    snug-raw) echo "$SNUG_RAW_CONTAINER" ;;
    snug-opt) echo "$SNUG_OPT_CONTAINER" ;;
  esac
}

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
    print(0); raise SystemExit
n=float(m.group(1)); u=m.group(2)
mult={"B":1,"KB":1000,"MB":1000**2,"GB":1000**3,"TB":1000**4,
      "KiB":1024,"MiB":1024**2,"GiB":1024**3,"TiB":1024**4}
print(int(n*mult[u]))
PY
}

annotate_container_memory() {
  local path="$1" bytes="$2"
  python3 - "$path" "$bytes" <<'PY'
import json, sys
p=sys.argv[1]
with open(p) as f: d=json.load(f)
d["container_memory_after"]=int(sys.argv[2])
with open(p,"w") as f: json.dump(d,f,separators=(",",":"))
PY
}

run_workload() {
  local server="$1" profile="$2" bytes="$3" workload="$4" run="$5"
  local addr label ops out
  addr="$(server_addr "$server")"
  label="$(server_label "$server")"
  out="$ROOT_OUT/$profile/${label}-run${run}-${workload}.json"
  mkdir -p "$ROOT_OUT/$profile"

  case "$workload" in
    load) ops="$KEYS" ;;
    get) ops="$GET_OPS" ;;
    mixed) ops="$MIXED_OPS" ;;
    ttl) ops="$TTL_OPS" ;;
    *) echo "unknown workload $workload" >&2; exit 2 ;;
  esac

  echo "===== $profile | $label | run $run/$RUNS | $workload ====="

  args=(
    -server "$label"
    -addr "$addr"
    -workload "$workload"
    -keys "$KEYS"
    -workers "$WORKERS"
    -pipeline "$PIPELINE"
    -value-bytes "$bytes"
    -value-shape "$profile"
  )

  if [[ "$workload" == "load" ]]; then
    args+=( -reset -settle-ms "$SETTLE_MS" )
  else
    args+=( -ops "$ops" )
  fi

  /tmp/rediswirebench "${args[@]}" > "$out"

  if [[ "$workload" == "load" ]]; then
    annotate_container_memory "$out" "$(container_bytes "$(container_name "$server")")"
  fi

  cat "$out"
}

echo "Realistic benchmark"
echo "  servers:   $SERVERS"
echo "  workloads: $WORKLOADS"
echo "  profiles:  ${#profiles[@]}"
echo "  runs:      $RUNS"
echo "  keys:      $KEYS"
echo "  get_ops:   $GET_OPS"
echo "  mixed_ops: $MIXED_OPS"
echo "  ttl_ops:   $TTL_OPS"
echo "  settle_ms: $SETTLE_MS"
echo "  output:    $ROOT_OUT"

# Strict isolation model:
#   for each profile/run:
#     kill all benchmark servers
#     start Redis -> run -> kill
#     start Snug raw -> run -> kill
#     start Snug optimized -> run -> kill
#
# Only the servers selected in $SERVERS are executed, but every selected server
# gets a completely fresh process/container. No database process is left resident
# while another database is benchmarked.
for spec in "${profiles[@]}"; do
  profile="${spec%%:*}"
  bytes="${spec##*:}"

  echo
  echo "################################################################"
  echo "PROFILE: $profile value_bytes=$bytes"
  echo "################################################################"

  for run in $(seq 1 "$RUNS"); do
    for server in $SERVERS; do
      echo
      echo "---- fresh $server | profile=$profile | run $run/$RUNS ----"

      # start-one-server.sh begins by removing all Redis/Snug benchmark containers.
      BUILD_IMAGE=0 bash scripts/bench/start-one-server.sh "$server"

      # LOAD must precede reads/mutations. If LOAD is omitted explicitly,
      # preload once so GET/mixed/ttl still have a dataset.
      if [[ " $WORKLOADS " != *" load "* ]]; then
        run_workload "$server" "$profile" "$bytes" load "$run" >/dev/null
      fi

      for workload in $WORKLOADS; do
        run_workload "$server" "$profile" "$bytes" "$workload" "$run"
      done

      docker rm -f "$(container_name "$server")" >/dev/null 2>&1 || true
    done
  done
done

# Leave the machine clean even if the selected server list changes later.
docker rm -f "$REDIS_CONTAINER" "$SNUG_RAW_CONTAINER" "$SNUG_OPT_CONTAINER" >/dev/null 2>&1 || true

python3 - "$ROOT_OUT" <<'PY'
import json, pathlib, statistics, sys

root=pathlib.Path(sys.argv[1])
rows=[]
for path in sorted(root.glob("*/*.json")):
    with path.open() as f:
        d=json.load(f)
    d["profile"]=path.parent.name
    rows.append(d)

groups={}
for row in rows:
    groups.setdefault((row["profile"],row["server"],row["workload"]),[]).append(row)

summary=[]
for (profile,server,workload),items in sorted(groups.items()):
    e={
        "profile":profile,
        "server":server,
        "workload":workload,
        "runs":len(items),
        "ops_per_second_median":statistics.median(x["ops_per_second"] for x in items),
        "p50_us_median":statistics.median(x["p50_ns"] for x in items)/1000,
        "p95_us_median":statistics.median(x["p95_ns"] for x in items)/1000,
        "p99_us_median":statistics.median(x["p99_ns"] for x in items)/1000,
    }
    if workload=="load":
        e["bytes_per_key_delta_median"]=statistics.median(x["bytes_per_key_delta"] for x in items)
        e["used_memory_after_median"]=statistics.median(x["used_memory_after"] for x in items)
        e["container_memory_after_median"]=statistics.median(x.get("container_memory_after",0) for x in items)
    summary.append(e)

out=root/"matrix-summary.json"
with out.open("w") as f: json.dump(summary,f,indent=2)

print("\n===== SUMMARY =====")
for r in summary:
    extra=""
    if r["workload"]=="load":
        extra=f' bpk={r["bytes_per_key_delta_median"]:.2f}'
    print(f'{r["profile"]:14} {r["server"]:9} {r["workload"]:5} '
          f'{r["ops_per_second_median"]:10.0f}/s p95={r["p95_us_median"]:8.2f}us{extra}')
print("\ncombined summary:",out)
PY
