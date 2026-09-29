#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
BIN=/tmp/snugkv-cluster-chaos-failover-bin
TMP=/tmp/snugkv-cluster-chaos-failover
P0="${P0:-7120}"
P1="${P1:-7121}"
P2="${P2:-7122}"
A0="127.0.0.1:$P0"
A1="127.0.0.1:$P1"
A2="127.0.0.1:$P2"
PASSWORD="${PASSWORD:-cluster-failover-secret}"
CONTROL_SECRET="${CONTROL_SECRET:-cluster-failover-control-secret}"
GROUP_ID="${GROUP_ID:-cluster-chaos-failover}"
KEY_COUNT="${KEY_COUNT:-200}"

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
require_cmd seq
require_cmd awk

rm -rf "$TMP"
mkdir -p "$TMP"

cd "$ROOT"
go build -o "$BIN" ./cmd/snugkv

cat >"$TMP/users.acl" <<ACL
user default on >$PASSWORD ~* &* +@all
ACL
chmod 600 "$TMP/users.acl"

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

make_config n0 "$P0" "$A0" "[\"$A1\",\"$A2\"]" 100
make_config n1 "$P1" "$A1" "[\"$A0\",\"$A2\"]" 10
make_config n2 "$P2" "$A2" "[\"$A0\",\"$A1\"]" 100

start_node() {
  local name="$1"
  "$BIN" -config "$TMP/$name.json" >"$TMP/$name.log" 2>&1 &
  echo $! >"$TMP/$name.pid"
}

wait_ready() {
  local port="$1"
  for _ in $(seq 1 200); do
    if redis-cli --no-auth-warning -a "$PASSWORD" -p "$port" PING 2>/dev/null | grep -qx PONG; then
      return 0
    fi
    sleep 0.05
  done
  echo "node on port $port did not become ready" >&2
  return 1
}

stop_hard() {
  local name="$1"
  local pid
  pid="$(cat "$TMP/$name.pid")"
  kill -KILL "$pid"
  wait "$pid" 2>/dev/null || true
  rm -f "$TMP/$name.pid"
}

cli() {
  local port="$1"
  shift
  redis-cli --no-auth-warning --raw -a "$PASSWORD" -p "$port" "$@"
}

start_node n0
start_node n1
start_node n2
wait_ready "$P0"
wait_ready "$P1"
wait_ready "$P2"

attach_replica() {
  local replica_port="$1"
  local label="$2"
  local reply=""

  for _ in $(seq 1 100); do
    reply="$(cli "$replica_port" REPLICAOF 127.0.0.1 "$P0" 2>&1 || true)"
    if [[ "$reply" == "OK" ]]; then
      return 0
    fi
    sleep 0.05
  done

  echo "$label failed to accept REPLICAOF after retries: $reply" >&2
  echo "--- primary ROLE ---" >&2
  cli "$P0" ROLE >&2 || true
  echo "--- $label ROLE ---" >&2
  cli "$replica_port" ROLE >&2 || true
  echo "--- primary failover health ---" >&2
  cli "$P0" SNUG.FAILOVER HEALTH >&2 || true
  echo "--- $label failover health ---" >&2
  cli "$replica_port" SNUG.FAILOVER HEALTH >&2 || true
  echo "--- primary log tail ---" >&2
  tail -n 120 "$TMP/n0.log" >&2 || true
  if [[ "$replica_port" == "$P1" ]]; then
    echo "--- n1 log tail ---" >&2
    tail -n 120 "$TMP/n1.log" >&2 || true
  else
    echo "--- n2 log tail ---" >&2
    tail -n 120 "$TMP/n2.log" >&2 || true
  fi
  return 1
}

echo "[1/9] attach two replicas to primary"
attach_replica "$P1" n1
attach_replica "$P2" n2

for _ in $(seq 1 200); do
  r1="$(cli "$P1" INFO replication 2>/dev/null || true)"
  r2="$(cli "$P2" INFO replication 2>/dev/null || true)"
  if grep -q 'role:slave' <<<"$r1" &&
     grep -q 'master_link_status:up' <<<"$r1" &&
     grep -q 'role:slave' <<<"$r2" &&
     grep -q 'master_link_status:up' <<<"$r2"; then
    break
  fi
  sleep 0.05
done
grep -q 'master_link_status:up' <<<"$(cli "$P1" INFO replication)"
grep -q 'master_link_status:up' <<<"$(cli "$P2" INFO replication)"

# Full sync completion and master_link_status=up can be observed immediately
# before the replica has settled into steady-state streaming on a busy machine.
# Require both links to remain up for a short consecutive window before the
# fsync=always seed burst.
stable=0
for _ in $(seq 1 100); do
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
if (( stable < 5 )); then
  echo "replication links did not stabilize before seed" >&2
  cli "$P1" INFO replication >&2 || true
  cli "$P2" INFO replication >&2 || true
  exit 1
fi

echo "[2/9] wait for primary quorum lease, then seed replicated data"
for _ in $(seq 1 200); do
  out="$(cli "$P0" SET failover:lease-ready yes 2>&1 || true)"
  [[ "$out" == "OK" ]] && break
  sleep 0.05
done
[[ "$(cli "$P0" GET failover:lease-ready)" == "yes" ]]

seed="$TMP/seed.commands"
: >"$seed"
for i in $(seq 1 "$KEY_COUNT"); do
  printf 'SET failover:key:%06d value-%06d\n' "$i" "$i" >>"$seed"
done
# WAIT is scoped to the connection's last successful write offset. Keep it on
# the same redis-cli session as the seed writes so we wait for these writes,
# not for offset zero from a fresh connection.
# fsync=always intentionally exercises the slow durability path. In the full
# recovery matrix, allow enough time for a transient replica reconnect and
# partial resynchronization instead of treating a slow workstation as a
# replication correctness failure.
printf 'WAIT 2 15000\n' >>"$seed"
redis-cli --no-auth-warning --raw -a "$PASSWORD" -p "$P0" <"$seed" >"$TMP/seed.out"

seed_lines="$(wc -l <"$TMP/seed.out")"
if (( seed_lines != KEY_COUNT + 1 )); then
  echo "unexpected seed reply count: got=$seed_lines want=$((KEY_COUNT + 1))" >&2
  cat "$TMP/seed.out" >&2
  exit 1
fi

if head -n "$KEY_COUNT" "$TMP/seed.out" | grep -v '^OK$' | grep -q .; then
  echo "seed produced unexpected SET replies" >&2
  exit 1
fi

wait_reply="$(tail -n 1 "$TMP/seed.out")"
if [[ ! "$wait_reply" =~ ^[0-9]+$ ]] || (( wait_reply < 2 )); then
  echo "replicas did not acknowledge seed: WAIT=$wait_reply" >&2
  echo "--- primary INFO replication ---" >&2
  cli "$P0" INFO replication >&2 || true
  echo "--- replica n1 INFO replication ---" >&2
  cli "$P1" INFO replication >&2 || true
  echo "--- replica n2 INFO replication ---" >&2
  cli "$P2" INFO replication >&2 || true
  echo "--- primary log tail ---" >&2
  tail -n 120 "$TMP/n0.log" >&2 || true
  echo "--- replica n1 log tail ---" >&2
  tail -n 120 "$TMP/n1.log" >&2 || true
  echo "--- replica n2 log tail ---" >&2
  tail -n 120 "$TMP/n2.log" >&2 || true
  exit 1
fi
echo "replication acknowledgements: WAIT=$wait_reply"

echo "[3/9] SIGKILL primary"
stop_hard n0

echo "[4/9] wait for exactly one replica to become writable leader"
leader_port=""
follower_port=""
for _ in $(seq 1 300); do
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
  sleep 0.05
done

if [[ -z "$leader_port" ]]; then
  echo "no single failover leader emerged" >&2
  echo "n1 ROLE:" >&2
  cli "$P1" ROLE >&2 || true
  echo "n2 ROLE:" >&2
  cli "$P2" ROLE >&2 || true
  echo "n1 health:" >&2
  cli "$P1" SNUG.FAILOVER HEALTH >&2 || true
  echo "n2 health:" >&2
  cli "$P2" SNUG.FAILOVER HEALTH >&2 || true
  exit 1
fi

if [[ "$leader_port" == "$P1" ]]; then
  leader_addr="$A1"
  follower_addr="$A2"
else
  leader_addr="$A2"
  follower_addr="$A1"
fi
echo "leader=$leader_addr follower=$follower_addr"

echo "[5/9] verify only leader accepts writes"
leader_write=""
for _ in $(seq 1 100); do
  leader_write="$(cli "$leader_port" SET failover:after-election leader-write 2>&1 || true)"
  [[ "$leader_write" == "OK" ]] && break
  sleep 0.05
done
if [[ "$leader_write" != "OK" ]]; then
  echo "promoted leader never became writable: $leader_write" >&2
  cli "$leader_port" SNUG.FAILOVER HEALTH >&2 || true
  exit 1
fi

follower_write="$(cli "$follower_port" SET failover:forbidden should-not-write 2>&1 || true)"
if [[ "$follower_write" == "OK" ]]; then
  echo "follower incorrectly accepted a write" >&2
  exit 1
fi
echo "follower rejection: $follower_write"

for _ in $(seq 1 100); do
  got="$(redis-cli --no-auth-warning --raw -a "$PASSWORD" -c -p "$follower_port" GET failover:after-election 2>/dev/null || true)"
  [[ "$got" == "leader-write" ]] && break
  sleep 0.05
done
[[ "$got" == "leader-write" ]]

echo "[6/9] verify cluster ownership converged to elected leader"
ownership_converged=0
for _ in $(seq 1 100); do
  nodes_leader="$(cli "$leader_port" CLUSTER NODES 2>/dev/null || true)"
  nodes_follower="$(cli "$follower_port" CLUSTER NODES 2>/dev/null || true)"
  if grep -F "$leader_addr@0" <<<"$nodes_leader" | grep -q '0-16383' &&
     grep -F "$leader_addr@0" <<<"$nodes_follower" | grep -q '0-16383'; then
    ownership_converged=1
    break
  fi
  sleep 0.05
done

if (( ownership_converged == 0 )); then
  echo "cluster ownership did not converge to elected leader $leader_addr" >&2
  echo "--- leader CLUSTER NODES ---" >&2
  printf '%s\n' "$nodes_leader" >&2
  echo "--- follower CLUSTER NODES ---" >&2
  printf '%s\n' "$nodes_follower" >&2
  echo "--- leader failover topology ---" >&2
  cli "$leader_port" SNUG.FAILOVER TOPOLOGY >&2 || true
  echo "--- follower failover topology ---" >&2
  cli "$follower_port" SNUG.FAILOVER TOPOLOGY >&2 || true
  echo "--- leader health ---" >&2
  cli "$leader_port" SNUG.FAILOVER HEALTH >&2 || true
  echo "--- follower health ---" >&2
  cli "$follower_port" SNUG.FAILOVER HEALTH >&2 || true
  echo "--- leader log tail ---" >&2
  if [[ "$leader_port" == "$P1" ]]; then
    tail -n 120 "$TMP/n1.log" >&2 || true
  else
    tail -n 120 "$TMP/n2.log" >&2 || true
  fi
  echo "--- follower log tail ---" >&2
  if [[ "$follower_port" == "$P1" ]]; then
    tail -n 120 "$TMP/n1.log" >&2 || true
  else
    tail -n 120 "$TMP/n2.log" >&2 || true
  fi
  exit 1
fi
echo "cluster ownership converged to $leader_addr"

echo "[7/9] restart old primary"
start_node n0
wait_ready "$P0"

echo "[8/9] wait for old primary demotion/reparent and stale-owner repair"
old_role=""
for _ in $(seq 1 300); do
  old_role="$(cli "$P0" ROLE 2>/dev/null | head -n1 || true)"
  old_nodes="$(cli "$P0" CLUSTER NODES 2>/dev/null || true)"
  if [[ "$old_role" == "slave" ]] &&
     grep -F "$leader_addr@0" <<<"$old_nodes" | grep -q '0-16383'; then
    break
  fi
  sleep 0.05
done

if [[ "$old_role" != "slave" ]]; then
  echo "old primary was not demoted after restart" >&2
  cli "$P0" ROLE >&2 || true
  cli "$P0" SNUG.FAILOVER HEALTH >&2 || true
  cli "$P0" CLUSTER NODES >&2 || true
  exit 1
fi
grep -F "$leader_addr@0" <<<"$old_nodes" | grep -q '0-16383'

old_write="$(cli "$P0" SET failover:stale-primary forbidden 2>&1 || true)"
if [[ "$old_write" == "OK" ]]; then
  echo "restarted stale primary incorrectly accepted a write" >&2
  exit 1
fi
echo "old primary rejection: $old_write"

echo "[9/9] verify data and routing after full convergence"
for key in failover:key:000001 failover:key:000100 "failover:key:$(printf '%06d' "$KEY_COUNT")" failover:after-election; do
  got="$(redis-cli --no-auth-warning --raw -a "$PASSWORD" -c -p "$P0" GET "$key" 2>/dev/null || true)"
  if [[ -z "$got" ]]; then
    echo "missing value for $key after failover convergence" >&2
    exit 1
  fi
done

leader_health="$(cli "$leader_port" SNUG.FAILOVER HEALTH)"
old_health="$(cli "$P0" SNUG.FAILOVER HEALTH)"
echo "leader health: $leader_health"
echo "old primary health: $old_health"

echo "cluster multi-process failover crash/restart chaos: PASS"
echo "leader=$leader_addr follower=$follower_addr old_primary_role=$old_role"
