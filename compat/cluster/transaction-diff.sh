#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
TMP="${TMPDIR:-/tmp}/snugkv-cluster-transaction-diff"
SNUG_BIN="$TMP/snugkv"
REDIS_BASE="${REDIS_BASE_PORT:-7100}"
SNUG_BASE="${SNUG_BASE_PORT:-7000}"

cleanup() {
  set +e
  for pidfile in "$TMP"/redis-*.pid "$TMP"/snug-*.pid; do
    [[ -f "$pidfile" ]] || continue
    pid="$(cat "$pidfile")"
    kill "$pid" 2>/dev/null || true
    wait "$pid" 2>/dev/null || true
  done
}

dump_failure() {
  local status="$1"
  echo "transaction differential failed with exit $status" >&2
  for log in "$TMP"/redis-*/server.log "$TMP"/snug-*.log; do
    [[ -f "$log" ]] || continue
    echo "===== $log =====" >&2
    tail -n 40 "$log" >&2
  done
}

on_exit() {
  local status=$?
  trap - EXIT
  if (( status != 0 )); then
    dump_failure "$status"
  fi
  cleanup
  exit "$status"
}
trap on_exit EXIT

wait_for_pong() {
  local port="$1"
  local pong=""
  for _ in $(seq 1 100); do
    pong="$(redis-cli -p "$port" PING 2>/dev/null || true)"
    pong="${pong//$'\r'/}"
    pong="${pong//$'\n'/}"
    if [[ "$pong" == "PONG" ]]; then
      return 0
    fi
    sleep 0.05
  done
  echo "port $port did not become ready; last PING response: $pong" >&2
  return 1
}

rm -rf "$TMP"
mkdir -p "$TMP"

command -v redis-server >/dev/null
command -v redis-cli >/dev/null

echo "[1/7] build SnugKV"
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

echo "[2/7] start Redis Cluster nodes"
for i in 0 1 2; do
  start_redis_node "$((REDIS_BASE+i))"
done
for i in 0 1 2; do
  wait_for_pong "$((REDIS_BASE+i))"
done

echo "[3/7] create Redis Cluster"
redis-cli --cluster create \
  "127.0.0.1:$REDIS_BASE" \
  "127.0.0.1:$((REDIS_BASE+1))" \
  "127.0.0.1:$((REDIS_BASE+2))" \
  --cluster-replicas 0 \
  --cluster-yes >/dev/null

cluster_ok=0
for _ in $(seq 1 100); do
  state="$(redis-cli -p "$REDIS_BASE" CLUSTER INFO 2>/dev/null || true)"
  if [[ "$state" == *"cluster_state:ok"* ]]; then
    cluster_ok=1
    break
  fi
  sleep 0.05
done
if (( cluster_ok != 1 )); then
  echo "Redis Cluster did not reach cluster_state:ok" >&2
  exit 1
fi

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

echo "[4/7] start SnugKV Cluster nodes"
for i in 0 1 2; do
  start_snug_node "$((SNUG_BASE+i))"
done
for i in 0 1 2; do
  wait_for_pong "$((SNUG_BASE+i))"
done

echo "[5/7] select slot fixtures"

find_key_in_range() {
  local min="$1"
  local max="$2"
  local prefix="$3"
  local key slot
  for i in $(seq 1 20000); do
    key="$prefix:$i"
    slot="$(redis-cli -p "$REDIS_BASE" CLUSTER KEYSLOT "$key")"
    if (( slot >= min && slot <= max )); then
      printf '%s\n' "$key"
      return 0
    fi
  done
  return 1
}

LOCAL_A="$(find_key_in_range 0 5460 local-a)"
LOCAL_A_SLOT="$(redis-cli -p "$REDIS_BASE" CLUSTER KEYSLOT "$LOCAL_A")"

LOCAL_B=""
for i in $(seq 1 20000); do
  candidate="local-b:$i"
  slot="$(redis-cli -p "$REDIS_BASE" CLUSTER KEYSLOT "$candidate")"
  if (( slot >= 0 && slot <= 5460 && slot != LOCAL_A_SLOT )); then
    LOCAL_B="$candidate"
    break
  fi
done
[[ -n "$LOCAL_B" ]]

REMOTE_KEY="$(find_key_in_range 10923 16383 remote)"

LOCAL_TAG=""
for i in $(seq 1 20000); do
  slot="$(redis-cli -p "$REDIS_BASE" CLUSTER KEYSLOT "{$i}")"
  if (( slot >= 0 && slot <= 5460 )); then
    LOCAL_TAG="$i"
    break
  fi
done
[[ -n "$LOCAL_TAG" ]]

TAG_A="acct:{$LOCAL_TAG}:a"
TAG_B="acct:{$LOCAL_TAG}:b"

run_cases() {
  local port="$1"
  local out="$2"

  {
    echo "== same-slot =="
    redis-cli --raw -p "$port" <<EOF
MULTI
SET $TAG_A A
SET $TAG_B B
EXEC
EOF

    echo "== cross-slot =="
    redis-cli --raw -p "$port" <<EOF
MULTI
SET $LOCAL_A 1
SET $LOCAL_B 2
EXEC
EOF

    echo "== moved-queue =="
    redis-cli --raw -p "$port" <<EOF
MULTI
SET $REMOTE_KEY 2
EXEC
EOF

    echo "== watch-local =="
    redis-cli --raw -p "$port" <<EOF
WATCH $LOCAL_A
MULTI
SET $LOCAL_A 3
EXEC
EOF

    echo "== watch-remote =="
    redis-cli --raw -p "$port" <<EOF
WATCH $REMOTE_KEY
EOF

    echo "== discard-reset =="
    redis-cli --raw -p "$port" <<EOF
MULTI
SET $LOCAL_A 4
DISCARD
MULTI
SET $LOCAL_B 5
EXEC
EOF

    echo "== exec-reset =="
    redis-cli --raw -p "$port" <<EOF
MULTI
SET $LOCAL_A 6
EXEC
MULTI
SET $LOCAL_B 7
EXEC
EOF
  } >"$out" 2>&1
}

echo "[6/7] run transaction cases"
run_cases "$REDIS_BASE" "$TMP/redis.out"
run_cases "$SNUG_BASE" "$TMP/snug.out"

normalize() {
  local input="$1"
  sed -E \
    -e "s/127\.0\.0\.1:$REDIS_BASE/NODE0/g" \
    -e "s/127\.0\.0\.1:$((REDIS_BASE+1))/NODE1/g" \
    -e "s/127\.0\.0\.1:$((REDIS_BASE+2))/NODE2/g" \
    -e "s/127\.0\.0\.1:$SNUG_BASE/NODE0/g" \
    -e "s/127\.0\.0\.1:$((SNUG_BASE+1))/NODE1/g" \
    -e "s/127\.0\.0\.1:$((SNUG_BASE+2))/NODE2/g" \
    "$input"
}

normalize "$TMP/redis.out" >"$TMP/redis.normalized"
normalize "$TMP/snug.out" >"$TMP/snug.normalized"

echo "[7/7] compare outputs"
echo "===== Redis Cluster ====="
cat "$TMP/redis.normalized"
echo
echo "===== SnugKV Cluster ====="
cat "$TMP/snug.normalized"
echo
echo "===== Diff ====="

if diff -u "$TMP/redis.normalized" "$TMP/snug.normalized"; then
  echo "transaction cluster parity: PASS"
else
  echo "transaction cluster parity: DIFFERENCES FOUND"
  exit 1
fi
