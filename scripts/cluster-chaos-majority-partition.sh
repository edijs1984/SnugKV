#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
BIN=/tmp/snugkv-cluster-chaos-partition-bin
TMP=/tmp/snugkv-cluster-chaos-partition

B0="${B0:-7140}"
B1="${B1:-7141}"
B2="${B2:-7142}"
P0="${P0:-7150}"
P1="${P1:-7151}"
P2="${P2:-7152}"

A0="127.0.0.1:$P0"
A1="127.0.0.1:$P1"
A2="127.0.0.1:$P2"

PASSWORD="${PASSWORD:-cluster-partition-secret}"
CONTROL_SECRET="${CONTROL_SECRET:-cluster-partition-control-secret}"
GROUP_ID="${GROUP_ID:-cluster-chaos-partition}"
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
require_cmd python3
require_cmd seq

rm -rf "$TMP"
mkdir -p "$TMP"
cd "$ROOT"
go build -o "$BIN" ./cmd/snugkv

cat >"$TMP/users.acl" <<EOF
user default on >$PASSWORD ~* &* +@all
user n0 on >$PASSWORD ~* &* +@all
user n1 on >$PASSWORD ~* &* +@all
user n2 on >$PASSWORD ~* &* +@all
EOF

make_config() {
  local backend="$1"
  local advertised="$2"
  local user="$3"
  local priority="$4"
  local peer1="$5"
  local peer2="$6"
  local name="$7"

  cat >"$TMP/$name.json" <<JSON
{
  "listen": "127.0.0.1:$backend",
  "admin_listen": "",
  "metrics_listen": "",
  "acl_file": "$TMP/users.acl",
  "aof_path": "$TMP/$name.aof",
  "fsync": "always",
  "masteruser": "$user",
  "masterauth": "$PASSWORD",
  "auto_failover_timeout_ms": 400,
  "failover_peers": ["$peer1", "$peer2"],
  "failover_quorum": 2,
  "failover_priority": $priority,
  "failover_group_id": "$GROUP_ID",
  "failover_config_epoch": 1,
  "failover_advertise_addr": "$advertised",
  "cluster_enabled": true,
  "cluster_node_addr": "$advertised",
  "cluster_control_auth": "$CONTROL_SECRET",
  "cluster_slots": {
    "0-16383": "$A0"
  }
}
JSON
}

make_config "$B0" "$A0" n0 100 "$A1" "$A2" n0
make_config "$B1" "$A1" n1 10  "$A0" "$A2" n1
make_config "$B2" "$A2" n2 100 "$A0" "$A1" n2

start_proxy() {
  local name="$1"
  local proxy_port="$2"
  local backend_port="$3"
  : >"$TMP/$name.block"
  python3 "$ROOT/scripts/cluster-chaos-proxy.py"     --listen-port "$proxy_port"     --target-port "$backend_port"     --block-file "$TMP/$name.block"     >"$TMP/$name-proxy.log" 2>&1 &
  echo $! >"$TMP/$name-proxy.pid"
}

start_node() {
  local name="$1"
  "$BIN" -config "$TMP/$name.json" >"$TMP/$name.log" 2>&1 &
  echo $! >"$TMP/$name.pid"
}

cli() {
  local port="$1"
  shift
  redis-cli --no-auth-warning --raw -a "$PASSWORD" -p "$port" "$@"
}

wait_ready() {
  local port="$1"
  for _ in $(seq 1 200); do
    if cli "$port" PING 2>/dev/null | grep -qx PONG; then
      return 0
    fi
    sleep 0.05
  done
  echo "node proxy on port $port did not become ready" >&2
  return 1
}

role_of() {
  cli "$1" ROLE 2>/dev/null | head -n 1
}

dump_all() {
  for pair in "n0:$P0" "n1:$P1" "n2:$P2"; do
    name="${pair%%:*}"
    port="${pair##*:}"
    echo "--- $name ROLE ---" >&2
    cli "$port" ROLE >&2 || true
    echo "--- $name HEALTH ---" >&2
    cli "$port" SNUG.FAILOVER HEALTH >&2 || true
    echo "--- $name CLUSTER NODES ---" >&2
    cli "$port" CLUSTER NODES >&2 || true
    echo "--- $name log tail ---" >&2
    tail -n 100 "$TMP/$name.log" >&2 || true
  done
}

start_proxy p0 "$P0" "$B0"
start_proxy p1 "$P1" "$B1"
start_proxy p2 "$P2" "$B2"

start_node n0
start_node n1
start_node n2

wait_ready "$P0"
wait_ready "$P1"
wait_ready "$P2"

echo "[1/10] attach replicas through advertised proxies"
cli "$P1" REPLICAOF 127.0.0.1 "$P0" >/dev/null
cli "$P2" REPLICAOF 127.0.0.1 "$P0" >/dev/null

for _ in $(seq 1 200); do
  s1="$(cli "$P1" INFO replication 2>/dev/null || true)"
  s2="$(cli "$P2" INFO replication 2>/dev/null || true)"
  if grep -q '^master_link_status:up' <<<"$s1" &&
     grep -q '^master_link_status:up' <<<"$s2"; then
    break
  fi
  sleep 0.05
done
grep -q '^master_link_status:up' <<<"$s1"
grep -q '^master_link_status:up' <<<"$s2"

echo "[2/10] wait for primary quorum lease and seed replicated data"
for _ in $(seq 1 200); do
  if cli "$P0" SET partition:lease-ready yes 2>/dev/null | grep -qx OK; then
    break
  fi
  sleep 0.05
done
[[ "$(cli "$P0" GET partition:lease-ready)" == "yes" ]]

seed="$TMP/seed.commands"
: >"$seed"
for i in $(seq 1 "$KEY_COUNT"); do
  printf 'SET partition:key:%06d value-%06d\n' "$i" "$i" >>"$seed"
done
printf 'WAIT 2 5000\n' >>"$seed"
redis-cli --no-auth-warning --raw -a "$PASSWORD" -p "$P0" <"$seed" >"$TMP/seed.out"
wait_reply="$(tail -n 1 "$TMP/seed.out")"
if [[ "$wait_reply" != "2" ]]; then
  echo "seed did not replicate to both replicas: WAIT=$wait_reply" >&2
  dump_all
  exit 1
fi
echo "replication acknowledgements: WAIT=$wait_reply"

echo "[3/10] partition old primary from majority while keeping client access"
printf 'n1\nn2\n' >"$TMP/p0.block"
printf 'n0\n' >"$TMP/p1.block"
printf 'n0\n' >"$TMP/p2.block"

echo "[4/10] prove old primary fences after quorum lease expiry"
old_rejection=""
for _ in $(seq 1 160); do
  old_rejection="$(cli "$P0" SET partition:old-primary blocked 2>&1 || true)"
  if grep -q '^READONLY ' <<<"$old_rejection"; then
    break
  fi
  sleep 0.05
done
if ! grep -q '^READONLY ' <<<"$old_rejection"; then
  echo "isolated old primary did not fence writes" >&2
  echo "last SET reply: $old_rejection" >&2
  dump_all
  exit 1
fi
echo "old primary rejection: $old_rejection"
old_read="$(cli "$P0" GET partition:key:000001 2>&1 || true)"
if [[ "$old_read" != "value-000001" ]]; then
  echo "fenced old primary read check failed: GET=$old_read" >&2
  echo "--- old primary INFO replication ---" >&2
  cli "$P0" INFO replication >&2 || true
  echo "--- old primary HEALTH ---" >&2
  cli "$P0" SNUG.FAILOVER HEALTH >&2 || true
  echo "--- old primary CLUSTER NODES ---" >&2
  cli "$P0" CLUSTER NODES >&2 || true
  echo "--- p0 proxy block list ---" >&2
  cat "$TMP/p0.block" >&2 || true
  echo "--- p0 proxy log ---" >&2
  tail -n 100 "$TMP/p0-proxy.log" >&2 || true
  echo "--- n0 log tail ---" >&2
  tail -n 100 "$TMP/n0.log" >&2 || true
  exit 1
fi
echo "fenced old primary read remains available"

echo "[5/10] wait for majority side to elect one writable leader"
leader_port=""
follower_port=""
for _ in $(seq 1 300); do
  r1="$(role_of "$P1" || true)"
  r2="$(role_of "$P2" || true)"
  if [[ "$r1" == "master" && "$r2" == "slave" ]]; then
    leader_port="$P1"; follower_port="$P2"; break
  fi
  if [[ "$r2" == "master" && "$r1" == "slave" ]]; then
    leader_port="$P2"; follower_port="$P1"; break
  fi
  sleep 0.05
done
if [[ -z "$leader_port" ]]; then
  echo "majority side did not elect exactly one leader" >&2
  dump_all
  exit 1
fi
leader_addr="127.0.0.1:$leader_port"
follower_addr="127.0.0.1:$follower_port"
echo "leader=$leader_addr follower=$follower_addr"

echo "[6/10] verify majority leader writes while old primary stays fenced"
for _ in $(seq 1 100); do
  if cli "$leader_port" SET partition:new-leader ok 2>/dev/null | grep -qx OK; then
    break
  fi
  sleep 0.05
done
[[ "$(cli "$leader_port" GET partition:new-leader)" == "ok" ]]
again="$(cli "$P0" SET partition:still-old no 2>&1 || true)"
grep -q '^READONLY ' <<<"$again"

echo "[7/10] verify majority cluster ownership converged"
for _ in $(seq 1 100); do
  nodes1="$(cli "$leader_port" CLUSTER NODES 2>/dev/null || true)"
  nodes2="$(cli "$follower_port" CLUSTER NODES 2>/dev/null || true)"
  if grep -F "$leader_addr@0" <<<"$nodes1" | grep -q '0-16383' &&
     grep -F "$leader_addr@0" <<<"$nodes2" | grep -q '0-16383'; then
    break
  fi
  sleep 0.05
done
grep -F "$leader_addr@0" <<<"$nodes1" | grep -q '0-16383'
grep -F "$leader_addr@0" <<<"$nodes2" | grep -q '0-16383'

echo "[8/10] heal partition"
: >"$TMP/p0.block"
: >"$TMP/p1.block"
: >"$TMP/p2.block"

echo "[9/10] wait for stale primary demotion/reparent"
for _ in $(seq 1 300); do
  old_role="$(role_of "$P0" || true)"
  old_info="$(cli "$P0" INFO replication 2>/dev/null || true)"
  old_nodes="$(cli "$P0" CLUSTER NODES 2>/dev/null || true)"
  if [[ "$old_role" == "slave" ]] &&
     grep -q '^master_link_status:up' <<<"$old_info" &&
     grep -F "$leader_addr@0" <<<"$old_nodes" | grep -q '0-16383'; then
    break
  fi
  sleep 0.05
done
if [[ "$old_role" != "slave" ]]; then
  echo "stale primary was not demoted after heal" >&2
  dump_all
  exit 1
fi
echo "old primary role after heal: $old_role"

echo "[10/10] verify data, routing, and healthy quorum"
[[ "$(cli "$leader_port" GET partition:key:000001)" == "value-000001" ]]
[[ "$(cli "$P0" GET partition:key:000001 2>&1 || true)" =~ MOVED ]]
health="$(cli "$leader_port" SNUG.FAILOVER HEALTH)"
grep -q '"status":"healthy"' <<<"$health"
grep -q '"quorum_reachable":true' <<<"$health"

echo "leader health: $health"
echo "cluster majority-partition fencing chaos: PASS"
echo "leader=$leader_addr follower=$follower_addr old_primary_role=$old_role"
