#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
BIN=/tmp/snugkv-cluster-chaos-persistence-bin
TMP=/tmp/snugkv-cluster-chaos-persistence

P0="${P0:-7160}"
P1="${P1:-7161}"
P2="${P2:-7162}"
PASSWORD="${PASSWORD:-cluster-persistence-secret}"
CONTROL_SECRET="${CONTROL_SECRET:-cluster-persistence-control-secret}"
GROUP_ID="${GROUP_ID:-cluster-chaos-persistence}"

cleanup() {
  chmod 700 "$TMP/n0-data" 2>/dev/null || true
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
require_cmd chmod

rm -rf "$TMP"
mkdir -p "$TMP/n0-data" "$TMP/n1-data" "$TMP/n2-data"

cd "$ROOT"
go build -o "$BIN" ./cmd/snugkv

cat >"$TMP/users.acl" <<ACL
user default on >$PASSWORD ~* &* +@all
ACL
chmod 600 "$TMP/users.acl"

make_config() {
  local name="$1"
  local port="$2"
  local peer1="$3"
  local peer2="$4"

  cat >"$TMP/$name.json" <<JSON
{
  "listen": "127.0.0.1:$port",
  "admin_listen": "",
  "metrics_listen": "",
  "acl_file": "$TMP/users.acl",
  "aof_path": "$TMP/$name-data/append.aof",
  "fsync": "always",
  "masterauth": "$PASSWORD",
  "auto_failover_timeout_ms": 2000,
  "failover_peers": ["$peer1", "$peer2"],
  "failover_quorum": 2,
  "failover_group_id": "$GROUP_ID",
  "failover_config_epoch": 1,
  "failover_advertise_addr": "127.0.0.1:$port",
  "cluster_enabled": true,
  "cluster_node_addr": "127.0.0.1:$port",
  "cluster_control_auth": "$CONTROL_SECRET",
  "cluster_slots": {
    "0-16383": "127.0.0.1:$P0"
  }
}
JSON
}

make_config n0 "$P0" "127.0.0.1:$P1" "127.0.0.1:$P2"
make_config n1 "$P1" "127.0.0.1:$P0" "127.0.0.1:$P2"
make_config n2 "$P2" "127.0.0.1:$P0" "127.0.0.1:$P1"

start_node() {
  local name="$1"
  "$BIN" -config "$TMP/$name.json" >"$TMP/$name.log" 2>&1 &
  echo $! >"$TMP/$name.pid"
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

wait_ready() {
  local port="$1"
  for _ in $(seq 1 200); do
    if cli "$port" PING 2>/dev/null | grep -qx PONG; then
      return 0
    fi
    sleep 0.05
  done
  echo "node on port $port did not become ready" >&2
  return 1
}

persistence_field() {
  local port="$1"
  local field="$2"
  cli "$port" INFO persistence 2>/dev/null |
    tr -d '\r' |
    awk -F: -v key="$field" '$1 == key {print $2}'
}

wait_rewrite_done() {
  local port="$1"
  for _ in $(seq 1 200); do
    running="$(persistence_field "$port" aof_rewrite_in_progress)"
    [[ "$running" == "0" || -z "$running" ]] && return 0
    sleep 0.05
  done
  return 1
}

start_node n0
start_node n1
start_node n2
wait_ready "$P0"
wait_ready "$P1"
wait_ready "$P2"

echo "[1/8] attach replicas and establish durable baseline"
cli "$P1" REPLICAOF 127.0.0.1 "$P0" | grep -qx OK
cli "$P2" REPLICAOF 127.0.0.1 "$P0" | grep -qx OK

for _ in $(seq 1 200); do
  i1="$(cli "$P1" INFO replication 2>/dev/null || true)"
  i2="$(cli "$P2" INFO replication 2>/dev/null || true)"
  if grep -q '^master_link_status:up' <<<"$i1" &&
     grep -q '^master_link_status:up' <<<"$i2"; then
    break
  fi
  sleep 0.05
done
grep -q '^master_link_status:up' <<<"$i1"
grep -q '^master_link_status:up' <<<"$i2"

seed="$TMP/seed.commands"
: >"$seed"
for i in $(seq 1 100); do
  printf 'SET persist:key:%03d before-%03d\n' "$i" "$i" >>"$seed"
done
printf 'WAIT 2 15000\n' >>"$seed"
redis-cli --no-auth-warning --raw -a "$PASSWORD" -p "$P0" <"$seed" >"$TMP/seed.out"
seed_wait="$(tail -n 1 "$TMP/seed.out")"
if [[ "$seed_wait" != "2" ]]; then
  echo "baseline did not replicate to both replicas: WAIT=$seed_wait" >&2
  echo "--- primary INFO replication ---" >&2
  cli "$P0" INFO replication >&2 || true
  echo "--- replica n1 INFO replication ---" >&2
  cli "$P1" INFO replication >&2 || true
  echo "--- replica n2 INFO replication ---" >&2
  cli "$P2" INFO replication >&2 || true
  exit 1
fi

echo "[2/8] deny primary AOF directory writes"
chmod 500 "$TMP/n0-data"

echo "[3/8] trigger BGREWRITEAOF and require filesystem failure"
rewrite_reply="$(cli "$P0" BGREWRITEAOF 2>&1 || true)"
echo "BGREWRITEAOF reply: $rewrite_reply"
wait_rewrite_done "$P0" || {
  echo "rewrite did not finish" >&2
  cli "$P0" INFO persistence >&2 || true
  exit 1
}

rewrite_status="$(persistence_field "$P0" aof_last_bgrewrite_status)"
failed_rewrite_status="$rewrite_status"
if [[ "$rewrite_status" != "err" ]]; then
  echo "expected failed rewrite, got aof_last_bgrewrite_status=$rewrite_status" >&2
  cli "$P0" INFO persistence >&2 || true
  echo "--- n0 log ---" >&2
  tail -n 120 "$TMP/n0.log" >&2 || true
  exit 1
fi
echo "rewrite failure observed: $rewrite_status"

echo "[4/8] prove live AOF remains writable after rewrite failure"
post="$TMP/post-failure.commands"
cat >"$post" <<EOF
SET persist:after-failure durable-after-failure
WAIT 2 15000
EOF
redis-cli --no-auth-warning --raw -a "$PASSWORD" -p "$P0" <"$post" >"$TMP/post-failure.out" 2>"$TMP/post-failure.err" || true
post_set="$(head -n 1 "$TMP/post-failure.out" | tr -d '\r')"
post_wait="$(tail -n 1 "$TMP/post-failure.out" | tr -d '\r')"
if [[ "$post_set" != "OK" || "$post_wait" != "2" ]]; then
  echo "post-failure live-AOF check failed: SET=$post_set WAIT=$post_wait" >&2
  if [[ -s "$TMP/post-failure.err" ]]; then
    echo "--- redis-cli stderr ---" >&2
    cat "$TMP/post-failure.err" >&2 || true
  fi
  echo "--- raw command output ---" >&2
  cat "$TMP/post-failure.out" >&2 || true
  echo "--- primary INFO persistence ---" >&2
  cli "$P0" INFO persistence >&2 || true
  echo "--- primary INFO replication ---" >&2
  cli "$P0" INFO replication >&2 || true
  echo "--- primary FAILOVER HEALTH ---" >&2
  cli "$P0" FAILOVER HEALTH >&2 || true
  echo "--- replica n1 INFO replication ---" >&2
  cli "$P1" INFO replication >&2 || true
  echo "--- replica n2 INFO replication ---" >&2
  cli "$P2" INFO replication >&2 || true
  echo "--- primary log tail ---" >&2
  tail -n 160 "$TMP/n0.log" >&2 || true
  exit 1
fi
[[ "$(cli "$P0" GET persist:after-failure)" == "durable-after-failure" ]]

echo "[5/8] restore storage permissions and retry rewrite"
chmod 700 "$TMP/n0-data"
retry_reply="$(cli "$P0" BGREWRITEAOF 2>&1 || true)"
echo "BGREWRITEAOF retry reply: $retry_reply"
wait_rewrite_done "$P0" || {
  echo "rewrite retry did not finish" >&2
  cli "$P0" INFO persistence >&2 || true
  exit 1
}

rewrite_status="$(persistence_field "$P0" aof_last_bgrewrite_status)"
if [[ "$rewrite_status" != "ok" ]]; then
  echo "rewrite retry did not recover: status=$rewrite_status" >&2
  cli "$P0" INFO persistence >&2 || true
  tail -n 120 "$TMP/n0.log" >&2 || true
  exit 1
fi
echo "rewrite retry status: $rewrite_status"

echo "[6/8] add durable write after successful rewrite"
cli "$P0" SET persist:after-rewrite durable-after-rewrite | grep -qx OK
[[ "$(cli "$P0" GET persist:after-rewrite)" == "durable-after-rewrite" ]]

echo "[7/8] hard-restart primary from rewritten AOF"
stop_hard n0
start_node n0
wait_ready "$P0"

echo "[8/8] verify baseline and post-failure data survived restart"
[[ "$(cli "$P0" GET persist:key:001)" == "before-001" ]]
[[ "$(cli "$P0" GET persist:key:100)" == "before-100" ]]
[[ "$(cli "$P0" GET persist:after-failure)" == "durable-after-failure" ]]
[[ "$(cli "$P0" GET persist:after-rewrite)" == "durable-after-rewrite" ]]

echo "cluster persistence failure/restart matrix: PASS"
echo "failed_rewrite=$failed_rewrite_status recovered_rewrite=$rewrite_status recovered_primary=127.0.0.1:$P0"
