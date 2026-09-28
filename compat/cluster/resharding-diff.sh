#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
TMP="${TMPDIR:-/tmp}/snugkv-cluster-resharding-diff"
SNUG_BIN="$TMP/snugkv"
REDIS_BASE="${REDIS_BASE_PORT:-7400}"
SNUG_BASE="${SNUG_BASE_PORT:-7500}"

cleanup() {
  set +e
  for pidfile in "$TMP"/redis-*.pid "$TMP"/snug-*.pid; do
    [[ -f "$pidfile" ]] || continue
    pid="$(cat "$pidfile")"
    kill "$pid" 2>/dev/null || true
    wait "$pid" 2>/dev/null || true
  done
}

on_exit() {
  local status=$?
  trap - EXIT
  if (( status != 0 )); then
    for log in "$TMP"/redis-*/server.log "$TMP"/snug-*.log; do
      [[ -f "$log" ]] || continue
      echo "===== $log =====" >&2
      tail -n 50 "$log" >&2
    done
  fi
  cleanup
  exit "$status"
}
trap on_exit EXIT

wait_for_pong() {
  local port="$1"
  for _ in $(seq 1 100); do
    if [[ "$(redis-cli -p "$port" PING 2>/dev/null | tr -d '\r\n')" == "PONG" ]]; then
      return 0
    fi
    sleep 0.05
  done
  echo "port $port did not become ready" >&2
  return 1
}

find_key_in_range() {
  local port="$1"
  local min="$2"
  local max="$3"
  local prefix="$4"
  local key slot
  for i in $(seq 1 30000); do
    key="$prefix:$i"
    slot="$(redis-cli -p "$port" CLUSTER KEYSLOT "$key")"
    if (( slot >= min && slot <= max )); then
      printf '%s\n' "$key"
      return 0
    fi
  done
  return 1
}

redis_node_id_for_port() {
  local port="$1"
  redis-cli -p "$REDIS_BASE" CLUSTER NODES |
    awk -v needle="127.0.0.1:$port@" '$2 ~ needle {print $1; exit}'
}

snug_node_id_for_port() {
  local query_port="$1"
  local port="$2"
  redis-cli -p "$query_port" CLUSTER NODES |
    awk -v needle="127.0.0.1:$port@" '$2 ~ needle {print $1; exit}'
}

rm -rf "$TMP"
mkdir -p "$TMP"

command -v redis-server >/dev/null
command -v redis-cli >/dev/null

echo "[1/9] build SnugKV"
cd "$ROOT"
go build -o "$SNUG_BIN" ./cmd/snugkv

start_redis_node() {
  local port="$1"
  local dir="$TMP/redis-$port"
  mkdir -p "$dir"
  cat >"$dir/redis.conf" <<EOF
port $port
bind 127.0.0.1
protected-mode no
daemonize no
save ""
appendonly no
cluster-enabled yes
cluster-config-file nodes.conf
cluster-node-timeout 5000
dir $dir
logfile ""
EOF
  redis-server "$dir/redis.conf" >"$dir/server.log" 2>&1 &
  echo $! >"$TMP/redis-$port.pid"
}

echo "[2/9] start Redis Cluster"
for i in 0 1 2; do start_redis_node "$((REDIS_BASE+i))"; done
for i in 0 1 2; do wait_for_pong "$((REDIS_BASE+i))"; done

echo "[3/9] create Redis Cluster"
redis-cli --cluster create   "127.0.0.1:$REDIS_BASE"   "127.0.0.1:$((REDIS_BASE+1))"   "127.0.0.1:$((REDIS_BASE+2))"   --cluster-replicas 0 --cluster-yes >/dev/null

for _ in $(seq 1 100); do
  if redis-cli -p "$REDIS_BASE" CLUSTER INFO | grep -q 'cluster_state:ok'; then
    break
  fi
  sleep 0.05
done
redis-cli -p "$REDIS_BASE" CLUSTER INFO | grep -q 'cluster_state:ok'

start_snug_node() {
  local port="$1"
  local cfg="$TMP/snug-$port.json"
  cat >"$cfg" <<EOF
{
  "listen": "127.0.0.1:$port",
  "admin_listen": "",
  "metrics_listen": "",
  "cluster_enabled": true,
  "cluster_node_addr": "127.0.0.1:$port",
  "cluster_slots": {
    "0-5460": "127.0.0.1:$SNUG_BASE",
    "5461-10922": "127.0.0.1:$((SNUG_BASE+1))",
    "10923-16383": "127.0.0.1:$((SNUG_BASE+2))"
  }
}
EOF
  "$SNUG_BIN" -config "$cfg" >"$TMP/snug-$port.log" 2>&1 &
  echo $! >"$TMP/snug-$port.pid"
}

echo "[4/9] start SnugKV Cluster"
for i in 0 1 2; do start_snug_node "$((SNUG_BASE+i))"; done
for i in 0 1 2; do wait_for_pong "$((SNUG_BASE+i))"; done

echo "[5/9] select source-slot fixtures"
EXISTING="$(find_key_in_range "$REDIS_BASE" 0 5460 reshard-existing)"
SLOT="$(redis-cli -p "$REDIS_BASE" CLUSTER KEYSLOT "$EXISTING")"
MISSING=""
for i in $(seq 1 30000); do
  candidate="reshard-missing:$i"
  if [[ "$(redis-cli -p "$REDIS_BASE" CLUSTER KEYSLOT "$candidate")" == "$SLOT" ]]; then
    MISSING="$candidate"
    break
  fi
done
[[ -n "$MISSING" ]]

REDIS_SRC_ID="$(redis_node_id_for_port "$REDIS_BASE")"
REDIS_DST_ID="$(redis_node_id_for_port "$((REDIS_BASE+1))")"
SNUG_SRC_ID="$(snug_node_id_for_port "$SNUG_BASE" "$SNUG_BASE")"
SNUG_DST_ID="$(snug_node_id_for_port "$SNUG_BASE" "$((SNUG_BASE+1))")"

[[ -n "$REDIS_SRC_ID" && -n "$REDIS_DST_ID" && -n "$SNUG_SRC_ID" && -n "$SNUG_DST_ID" ]]

setup_redis() {
  redis-cli --raw -p "$REDIS_BASE" SET "$EXISTING" source >/dev/null
  redis-cli --raw -p "$REDIS_BASE" CLUSTER SETSLOT "$SLOT" MIGRATING "$REDIS_DST_ID" >/dev/null
  redis-cli --raw -p "$((REDIS_BASE+1))" CLUSTER SETSLOT "$SLOT" IMPORTING "$REDIS_SRC_ID" >/dev/null
}

setup_snug() {
  redis-cli --raw -p "$SNUG_BASE" SET "$EXISTING" source >/dev/null
  redis-cli --raw -p "$SNUG_BASE" CLUSTER SETSLOT "$SLOT" MIGRATING "$SNUG_DST_ID" >/dev/null
  redis-cli --raw -p "$((SNUG_BASE+1))" CLUSTER SETSLOT "$SLOT" IMPORTING "$SNUG_SRC_ID" >/dev/null
}

run_cases() {
  local src="$1"
  local dst="$2"
  local dst_id="$3"
  local out="$4"

  {
    echo "== source-existing =="
    redis-cli --raw -p "$src" GET "$EXISTING"

    echo "== source-missing-ask =="
    redis-cli --raw -p "$src" GET "$MISSING"

    echo "== target-without-asking =="
    redis-cli --raw -p "$dst" GET "$MISSING"

    echo "== target-asking-set =="
    redis-cli --raw -p "$dst" <<EOF
ASKING
SET $MISSING imported
GET $MISSING
ASKING
GET $MISSING
EOF

    echo "== source-existing-after-import =="
    redis-cli --raw -p "$src" GET "$EXISTING"

    echo "== finalize-owner =="
    redis-cli --raw -p "$src" CLUSTER SETSLOT "$SLOT" NODE "$dst_id"
    redis-cli --raw -p "$dst" CLUSTER SETSLOT "$SLOT" NODE "$dst_id"

    echo "== source-after-node =="
    redis-cli --raw -p "$src" GET "$MISSING"

    echo "== target-after-node =="
    redis-cli --raw -p "$dst" GET "$MISSING"
  } >"$out" 2>&1
}

normalize() {
  local input="$1"
  sed -E     -e "s/127\.0\.0\.1:$REDIS_BASE/NODE0/g"     -e "s/127\.0\.0\.1:$((REDIS_BASE+1))/NODE1/g"     -e "s/127\.0\.0\.1:$((REDIS_BASE+2))/NODE2/g"     -e "s/127\.0\.0\.1:$SNUG_BASE/NODE0/g"     -e "s/127\.0\.0\.1:$((SNUG_BASE+1))/NODE1/g"     -e "s/127\.0\.0\.1:$((SNUG_BASE+2))/NODE2/g"     "$input"
}

echo "[6/9] configure migration state"
setup_redis
setup_snug

echo "[7/9] run resharding cases"
run_cases "$REDIS_BASE" "$((REDIS_BASE+1))" "$REDIS_DST_ID" "$TMP/redis.out"
run_cases "$SNUG_BASE" "$((SNUG_BASE+1))" "$SNUG_DST_ID" "$TMP/snug.out"

normalize "$TMP/redis.out" >"$TMP/redis.normalized"
normalize "$TMP/snug.out" >"$TMP/snug.normalized"

echo "[8/9] compare outputs"
echo "===== Redis Cluster resharding ====="
cat "$TMP/redis.normalized"
echo
echo "===== SnugKV Cluster resharding ====="
cat "$TMP/snug.normalized"
echo
echo "===== Diff ====="

if diff -u "$TMP/redis.normalized" "$TMP/snug.normalized"; then
  echo "[9/9] resharding cluster parity: PASS"
else
  echo "[9/9] resharding cluster parity: DIFFERENCES FOUND"
  exit 1
fi
