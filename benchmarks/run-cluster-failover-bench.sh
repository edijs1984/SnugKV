#!/usr/bin/env bash
set -euo pipefail

ROOT="${ROOT:-$(cd "$(dirname "$0")/.." && pwd)}"
BIN="${BIN:-/tmp/snugkv-cluster-failover-bench-bin}"
TMP="${TMP:-/tmp/snugkv-cluster-failover-bench}"
OUT="${OUT:-/tmp/snugkv-cluster-failover-bench.jsonl}"

P0="${P0:-7220}"
P1="${P1:-7221}"
P2="${P2:-7222}"
A0="127.0.0.1:$P0"
A1="127.0.0.1:$P1"
A2="127.0.0.1:$P2"

PASSWORD="${PASSWORD:-cluster-failover-bench-secret}"
CONTROL_SECRET="${CONTROL_SECRET:-cluster-failover-bench-control-secret}"
GROUP_ID="${GROUP_ID:-cluster-failover-bench}"
KEY_COUNT="${KEY_COUNT:-200}"
REPEATS="${REPEATS:-3}"

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
require_cmd seq
require_cmd date
require_cmd python3

cd "$ROOT"
go build -o "$BIN" ./cmd/snugkv
: >"$OUT"

cli() {
  local port="$1"
  shift
  redis-cli --no-auth-warning --raw -a "$PASSWORD" -p "$port" "$@"
}

wait_ready() {
  local port="$1"
  for _ in $(seq 1 200); do
    if redis-cli --no-auth-warning --raw -a "$PASSWORD" -p "$port" PING 2>/dev/null | grep -qx PONG; then
      return 0
    fi
    sleep 0.05
  done
  echo "node on port $port did not become ready" >&2
  return 1
}

make_config() {
  local name="$1"
  local port="$2"
  local advertise="$3"
  local peers_json="$4"
  local priority="$5"
  cat >"$TMP/$name.json" <<JSON
{
  "listen": "127.0.0.1:$port",
  "admin_listen": "",
  "metrics_listen": "",
  "acl_file": "$TMP/users.acl",
  "aof_path": "$TMP/$name.aof",
  "fsync": "always",
  "masterauth": "$PASSWORD",
  "auto_failover_timeout_ms": 400,
  "failover_peers": $peers_json,
  "failover_quorum": 2,
  "failover_priority": $priority,
  "failover_group_id": "$GROUP_ID",
  "failover_config_epoch": 1,
  "failover_advertise_addr": "$advertise",
  "cleanup_interval_ms": 50,
  "cluster_enabled": true,
  "cluster_node_addr": "$advertise",
  "cluster_control_auth": "$CONTROL_SECRET",
  "cluster_slots": {
    "0-16383": "$A0"
  }
}
JSON
}

start_node() {
  local name="$1"
  "$BIN" -config "$TMP/$name.json" >"$TMP/$name.log" 2>&1 &
  PIDS+=("$!")
  echo $! >"$TMP/$name.pid"
}

stop_hard() {
  local name="$1"
  local pid
  pid="$(cat "$TMP/$name.pid")"
  kill -KILL "$pid"
  wait "$pid" 2>/dev/null || true
}

setup_run() {
  for pid in "${PIDS[@]:-}"; do
    kill "$pid" 2>/dev/null || true
  done
  wait 2>/dev/null || true
  PIDS=()

  rm -rf "$TMP"
  mkdir -p "$TMP"

  cat >"$TMP/users.acl" <<ACL
user default on >$PASSWORD ~* &* +@all
ACL
  chmod 600 "$TMP/users.acl"

  make_config n0 "$P0" "$A0" "[\"$A1\",\"$A2\"]" 100
  make_config n1 "$P1" "$A1" "[\"$A0\",\"$A2\"]" 10
  make_config n2 "$P2" "$A2" "[\"$A0\",\"$A1\"]" 100

  start_node n0
  start_node n1
  start_node n2
  wait_ready "$P0"
  wait_ready "$P1"
  wait_ready "$P2"

  cli "$P1" REPLICAOF 127.0.0.1 "$P0" | grep -qx OK
  cli "$P2" REPLICAOF 127.0.0.1 "$P0" | grep -qx OK

  stable=0
  for _ in $(seq 1 200); do
    r1="$(cli "$P1" INFO replication 2>/dev/null || true)"
    r2="$(cli "$P2" INFO replication 2>/dev/null || true)"
    if grep -q 'master_link_status:up' <<<"$r1" &&
       grep -q 'master_link_status:up' <<<"$r2"; then
      stable=$((stable + 1))
      if (( stable >= 5 )); then
        break
      fi
    else
      stable=0
    fi
    sleep 0.05
  done
  (( stable >= 5 ))

  for _ in $(seq 1 200); do
    out="$(cli "$P0" SET failoverbench:lease-ready yes 2>&1 || true)"
    [[ "$out" == "OK" ]] && break
    sleep 0.05
  done
  [[ "$(cli "$P0" GET failoverbench:lease-ready)" == "yes" ]]

  seed="$TMP/seed.commands"
  : >"$seed"
  for i in $(seq 1 "$KEY_COUNT"); do
    printf 'SET failoverbench:key:%06d value-%06d\n' "$i" "$i" >>"$seed"
  done
  printf 'WAIT 2 15000\n' >>"$seed"
  redis-cli --no-auth-warning --raw -a "$PASSWORD" -p "$P0" <"$seed" >"$TMP/seed.out"
  [[ "$(tail -n 1 "$TMP/seed.out")" == "2" ]]
}

echo "SnugKV cluster failover benchmark"
echo "nodes=$A0,$A1,$A2 keys=$KEY_COUNT repeats=$REPEATS"
echo "output=$OUT"

for run in $(seq 1 "$REPEATS"); do
  echo
  echo "===== run $run/$REPEATS ====="
  setup_run

  echo "[1/4] crash primary"
  crash_ns="$(date +%s%N)"
  stop_hard n0

  echo "[2/4] wait for elected leader"
  leader_port=""
  follower_port=""
  for _ in $(seq 1 400); do
    role1="$(cli "$P1" ROLE 2>/dev/null | head -n1 || true)"
    role2="$(cli "$P2" ROLE 2>/dev/null | head -n1 || true)"
    if [[ "$role1" == "master" && "$role2" == "slave" ]]; then
      leader_port="$P1"
      follower_port="$P2"
      break
    fi
    if [[ "$role2" == "master" && "$role1" == "slave" ]]; then
      leader_port="$P2"
      follower_port="$P1"
      break
    fi
    sleep 0.01
  done
  [[ -n "$leader_port" ]]
  elected_ns="$(date +%s%N)"

  if [[ "$leader_port" == "$P1" ]]; then
    leader_addr="$A1"
  else
    leader_addr="$A2"
  fi
  echo "leader=$leader_addr"

  echo "[3/4] wait for first successful write"
  write_reply=""
  for _ in $(seq 1 400); do
    write_reply="$(cli "$leader_port" SET failoverbench:after-election value 2>&1 || true)"
    [[ "$write_reply" == "OK" ]] && break
    sleep 0.01
  done
  [[ "$write_reply" == "OK" ]]
  writable_ns="$(date +%s%N)"

  echo "[4/4] wait for cluster ownership convergence"
  converged=0
  for _ in $(seq 1 400); do
    nodes_leader="$(cli "$leader_port" CLUSTER NODES 2>/dev/null || true)"
    nodes_follower="$(cli "$follower_port" CLUSTER NODES 2>/dev/null || true)"
    if grep -F "$leader_addr@0" <<<"$nodes_leader" | grep -q '0-16383' &&
       grep -F "$leader_addr@0" <<<"$nodes_follower" | grep -q '0-16383'; then
      converged=1
      break
    fi
    sleep 0.01
  done
  (( converged == 1 ))
  converged_ns="$(date +%s%N)"

  [[ "$(redis-cli --no-auth-warning --raw -a "$PASSWORD" -c -p "$follower_port" GET failoverbench:after-election)" == "value" ]]
  [[ "$(redis-cli --no-auth-warning --raw -a "$PASSWORD" -c -p "$follower_port" GET failoverbench:key:000001)" == "value-000001" ]]

  python3 - "$run" "$crash_ns" "$elected_ns" "$writable_ns" "$converged_ns" "$leader_addr" "$OUT" <<'PY'
import json
import sys

run = int(sys.argv[1])
crash = int(sys.argv[2])
elected = int(sys.argv[3])
writable = int(sys.argv[4])
converged = int(sys.argv[5])
leader = sys.argv[6]
out_path = sys.argv[7]

row = {
    "benchmark": "cluster_failover",
    "run": run,
    "leader": leader,
    "election_duration_ns": elected - crash,
    "writable_duration_ns": writable - crash,
    "ownership_convergence_duration_ns": converged - crash,
    "post_election_write_delay_ns": writable - elected,
    "post_write_ownership_delay_ns": converged - writable,
    "measurement_note": "black-box three-node failover benchmark; time origin is successful SIGKILL of primary, election requires exactly one replica master, writable requires SET=OK, convergence requires leader ownership on leader and follower views",
}
line = json.dumps(row, separators=(",", ":"))
print(line)
with open(out_path, "a", encoding="utf-8") as f:
    f.write(line + "\n")
PY
done

echo
echo "cluster failover benchmark: PASS"
echo "results=$OUT"
