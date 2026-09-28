#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
TMP="${TMPDIR:-/tmp}/snugkv-cluster-slot-management-diff"
SNUG_BIN="$TMP/snugkv"
REDIS_PORT="${REDIS_PORT:-8000}"
SNUG_PORT="${SNUG_PORT:-8100}"

cleanup() {
  set +e
  for pidfile in "$TMP"/redis.pid "$TMP"/snug.pid; do
    [[ -f "$pidfile" ]] || continue
    pid="$(cat "$pidfile")"
    kill "$pid" 2>/dev/null || true
    wait "$pid" 2>/dev/null || true
  done
}
trap cleanup EXIT
trap 'echo "slot-management differential failed at line $LINENO" >&2' ERR

wait_for_pong() {
  local port="$1"
  for _ in $(seq 1 100); do
    if [[ "$(redis-cli -p "$port" PING 2>/dev/null | tr -d '\r\n')" == "PONG" ]]; then
      return 0
    fi
    sleep 0.05
  done
  return 1
}

rm -rf "$TMP"
mkdir -p "$TMP"
cd "$ROOT"
echo "[1/5] build SnugKV"
go build -o "$SNUG_BIN" ./cmd/snugkv

echo "[2/5] start Redis cluster node"
redis_dir="$TMP/redis"
mkdir -p "$redis_dir"
cat >"$redis_dir/redis.conf" <<EOF
port $REDIS_PORT
bind 127.0.0.1
protected-mode no
daemonize no
save ""
appendonly no
cluster-enabled yes
cluster-config-file nodes.conf
cluster-node-timeout 5000
dir $redis_dir
logfile ""
EOF
redis-server "$redis_dir/redis.conf" >"$redis_dir/server.log" 2>&1 &
echo $! >"$TMP/redis.pid"
wait_for_pong "$REDIS_PORT"

echo "[3/5] start SnugKV cluster node"
snug_cfg="$TMP/snug.json"
cat >"$snug_cfg" <<EOF
{
  "listen": "127.0.0.1:$SNUG_PORT",
  "admin_listen": "",
  "metrics_listen": "",
  "cluster_enabled": true,
  "cluster_node_addr": "127.0.0.1:$SNUG_PORT",
  "cluster_slots": {
    "0": "127.0.0.1:$SNUG_PORT"
  }
}
EOF
"$SNUG_BIN" -config "$snug_cfg" >"$TMP/snug.log" 2>&1 &
echo $! >"$TMP/snug.pid"
if ! wait_for_pong "$SNUG_PORT"; then
  echo "SnugKV failed to start; log follows:" >&2
  cat "$TMP/snug.log" >&2 || true
  exit 1
fi

bootstrap="$(redis-cli --raw -p "$SNUG_PORT" CLUSTER FLUSHSLOTS 2>&1)"
if [[ "$bootstrap" != "OK" ]]; then
  echo "failed to bootstrap SnugKV to empty slot map: $bootstrap" >&2
  exit 1
fi

run_cases() {
  local port="$1"
  local out="$2"
  {
    echo "== myid =="
    redis-cli --raw -p "$port" CLUSTER MYID

    echo "== addslots =="
    redis-cli --raw -p "$port" CLUSTER ADDSLOTS 1 2

    echo "== slots-after-add =="
    redis-cli --raw -p "$port" CLUSTER SLOTS

    echo "== add-busy =="
    redis-cli --raw -p "$port" CLUSTER ADDSLOTS 1

    echo "== add-duplicate =="
    redis-cli --raw -p "$port" CLUSTER ADDSLOTS 3 3

    echo "== delslot =="
    redis-cli --raw -p "$port" CLUSTER DELSLOTS 1

    echo "== del-unassigned =="
    redis-cli --raw -p "$port" CLUSTER DELSLOTS 1

    echo "== invalid-slot =="
    redis-cli --raw -p "$port" CLUSTER ADDSLOTS 16384

    echo "== flushslots-nonempty =="
    redis-cli --raw -p "$port" SET foo value
    redis-cli --raw -p "$port" CLUSTER FLUSHSLOTS

    echo "== flushslots-empty =="
    redis-cli --raw -p "$port" FLUSHDB
    redis-cli --raw -p "$port" CLUSTER FLUSHSLOTS

    echo "== slots-after-flush =="
    redis-cli --raw -p "$port" CLUSTER SLOTS
  } >"$out" 2>&1
}

echo "[4/5] run command cases"
run_cases "$REDIS_PORT" "$TMP/redis.out"
run_cases "$SNUG_PORT" "$TMP/snug.out"

normalize() {
  sed -E     -e "s/127\.0\.0\.1:$REDIS_PORT/NODE/g"     -e "s/127\.0\.0\.1:$SNUG_PORT/NODE/g"     -e '/^[0-9a-f]{40}$/s/.*/NODEID/'
}

normalize <"$TMP/redis.out" >"$TMP/redis.normalized"
normalize <"$TMP/snug.out" >"$TMP/snug.normalized"

echo "[5/5] compare outputs"
echo "===== Redis ====="
cat "$TMP/redis.normalized"
echo
echo "===== SnugKV ====="
cat "$TMP/snug.normalized"
echo
echo "===== Diff ====="

if diff -u "$TMP/redis.normalized" "$TMP/snug.normalized"; then
  echo "slot management cluster parity: PASS"
else
  echo "slot management cluster parity: DIFFERENCES FOUND"
  exit 1
fi
