#!/usr/bin/env bash
set -euo pipefail

# Isolated native Redis vs adaptive SnugKV benchmark.
# One database process is resident at a time.

KEYS="${KEYS:-1000000}"
GET_OPS="${GET_OPS:-2000000}"
MIXED_OPS="${MIXED_OPS:-250000}"
TTL_OPS="${TTL_OPS:-250000}"
WORKERS="${WORKERS:-8}"
PIPELINE="${PIPELINE:-256}"
RUNS="${RUNS:-1}"
SETTLE_MS="${SETTLE_MS:-10000}"
SERVERS="${SERVERS:-redis snug}"
WORKLOADS="${WORKLOADS:-load get}"
ROOT_OUT="${ROOT_OUT:-benchmark-results/realistic-$(date +%Y%m%d-%H%M%S)}"
BUILD_SNUG="${BUILD_SNUG:-1}"
PROFILE="${PROFILE:-}"

REDIS_ADDR="${REDIS_ADDR:-127.0.0.1:6390}"
SNUG_ADDR="${SNUG_ADDR:-127.0.0.1:6383}"
REDIS_PIDFILE="${REDIS_PIDFILE:-/tmp/snug-bench-redis.pid}"
SNUG_PIDFILE="${SNUG_PIDFILE:-/tmp/snug-bench-snug.pid}"
SNUG_BIN="${SNUG_BIN:-/tmp/snugkv-bench}"

profiles=(
  "session-json:384"
  "api-json:768"
  "cache-json:1024"
  "counter:10"
  "uuid:36"
  "text:256"
  "repetitive:256"
  "compressed:256"
  "random:256"
)

if [[ -n "$PROFILE" ]]; then
  selected=""
  for spec in "${profiles[@]}"; do
    if [[ "${spec%%:*}" == "$PROFILE" ]]; then
      selected="$spec"
      break
    fi
  done
  if [[ -z "$selected" ]]; then
    echo "unknown PROFILE: $PROFILE" >&2
    echo "available: ${profiles[*]}" >&2
    exit 2
  fi
  profiles=("$selected")
fi

mkdir -p "$ROOT_OUT"
go build -o /tmp/rediswirebench ./cmd/rediswirebench

if [[ "$BUILD_SNUG" == "1" ]]; then
  echo "Building fresh native SnugKV benchmark binary..."
  go build -trimpath -o "$SNUG_BIN" ./cmd/snugkv
fi

server_addr() {
  case "$1" in
    redis) echo "$REDIS_ADDR" ;;
    snug) echo "$SNUG_ADDR" ;;
    *) echo "unknown server $1" >&2; exit 2 ;;
  esac
}

server_pidfile() {
  case "$1" in
    redis) echo "$REDIS_PIDFILE" ;;
    snug) echo "$SNUG_PIDFILE" ;;
    *) echo "unknown server $1" >&2; exit 2 ;;
  esac
}

process_rss_bytes() {
  local pidfile="$1"
  if [[ ! -f "$pidfile" ]]; then
    echo 0
    return
  fi
  local pid
  pid="$(cat "$pidfile" 2>/dev/null || true)"
  if [[ -z "$pid" || ! -r "/proc/$pid/status" ]]; then
    echo 0
    return
  fi
  awk '/^VmRSS:/{print $2 * 1024; exit}' "/proc/$pid/status"
}

stop_server() {
  local server="$1"
  local pidfile
  pidfile="$(server_pidfile "$server")"
  if [[ ! -f "$pidfile" ]]; then
    return
  fi
  local pid
  pid="$(cat "$pidfile" 2>/dev/null || true)"
  if [[ -n "$pid" ]] && kill -0 "$pid" 2>/dev/null; then
    kill "$pid" 2>/dev/null || true
    for _ in $(seq 1 50); do
      kill -0 "$pid" 2>/dev/null || break
      sleep 0.02
    done
    kill -9 "$pid" 2>/dev/null || true
  fi
  rm -f "$pidfile"
}

annotate_process_memory() {
  local path="$1" bytes="$2"
  python3 - "$path" "$bytes" <<'PY'
import json, sys
p=sys.argv[1]
with open(p) as f:
    d=json.load(f)
d["process_rss_after"]=int(float(sys.argv[2]))
with open(p,"w") as f:
    json.dump(d,f,separators=(",",":"))
PY
}

run_workload() {
  local server="$1" profile="$2" bytes="$3" workload="$4" run="$5"
  local addr ops out
  addr="$(server_addr "$server")"
  out="$ROOT_OUT/$profile/${server}-run${run}-${workload}.json"
  mkdir -p "$ROOT_OUT/$profile"

  case "$workload" in
    load) ops="$KEYS" ;;
    get) ops="$GET_OPS" ;;
    mixed) ops="$MIXED_OPS" ;;
    ttl) ops="$TTL_OPS" ;;
    *) echo "unknown workload $workload" >&2; exit 2 ;;
  esac

  echo "===== $profile | $server | run $run/$RUNS | $workload ====="

  args=(
    -server "$server"
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
    annotate_process_memory "$out" "$(process_rss_bytes "$(server_pidfile "$server")")"
  fi

  cat "$out"
}

cleanup() {
  stop_server redis
  stop_server snug
}
trap cleanup EXIT INT TERM

echo "Native realistic benchmark"
echo "  servers:    $SERVERS"
echo "  workloads:  $WORKLOADS"
echo "  profiles:   ${#profiles[@]}"
[[ -n "$PROFILE" ]] && echo "  profile:    $PROFILE"
echo "  runs:       $RUNS"
echo "  keys:       $KEYS"
echo "  get_ops:    $GET_OPS"
echo "  workers:    $WORKERS"
echo "  pipeline:   $PIPELINE"
echo "  settle_ms:  $SETTLE_MS"
echo "  output:     $ROOT_OUT"
echo "  snug_bin:   $SNUG_BIN"

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
      echo "---- fresh native $server | profile=$profile | run $run/$RUNS ----"

      BUILD_SNUG=0 SNUG_BIN="$SNUG_BIN" bash scripts/bench/start-one-server.sh "$server"

      if [[ " $WORKLOADS " != *" load "* ]]; then
        run_workload "$server" "$profile" "$bytes" load "$run" >/dev/null
      fi

      for workload in $WORKLOADS; do
        run_workload "$server" "$profile" "$bytes" "$workload" "$run"
      done

      stop_server "$server"
    done
  done
done

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
        e["process_rss_after_median"]=statistics.median(x.get("process_rss_after",0) for x in items)
    summary.append(e)

out=root/"matrix-summary.json"
with out.open("w") as f:
    json.dump(summary,f,indent=2)

print("\n===== SUMMARY =====")
for r in summary:
    extra=""
    if r["workload"]=="load":
        extra=f' bpk={r["bytes_per_key_delta_median"]:.2f} rss={r["process_rss_after_median"]/1024/1024:.1f}MiB'
    print(f'{r["profile"]:14} {r["server"]:9} {r["workload"]:5} '
          f'{r["ops_per_second_median"]:10.0f}/s p95={r["p95_us_median"]:8.2f}us{extra}')
print("\ncombined summary:",out)
PY
