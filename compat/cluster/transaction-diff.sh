#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
TMP="${TMPDIR:-/tmp}/snugkv-cluster-transaction-diff"
SNUG_BIN="$TMP/snugkv"
REDIS_BASE="${REDIS_BASE_PORT:-7100}"
SNUG_BASE="${SNUG_BASE_PORT:-7000}"

dump_failure() {
  status=$?
  if (( status != 0 )); then
    echo "transaction differential failed with exit $status" >&2
    for log in "$TMP"/redis-*/server.log "$TMP"/snug-*.log; do
      [[ -f "$log" ]] || continue
      echo "===== $log =====" >&2
      tail -n 40 "$log" >&2
    done
  fi
  return "$status"
}

cleanup() {
  set +e
  for pidfile in "$TMP"/redis-*.pid "$TMP"/snug-*.pid; do
    [[ -f "$pidfile" ]] || continue
    pid="$(cat "$pidfile")"
    kill "$pid" 2>/dev/null || true
    wait "$pid" 2>/dev/null || true
  done
}
trap 'status=$?; if (( status != 0 )); then dump_failure || true; fi; cleanup; exit $status' EXIT

rm -rf "$TMP"
mkdir -p "$TMP"

command -v redis-server >/dev/null || { echo "redis-server not found"; exit 1; }
command -v redis-cli >/dev/null || { echo "redis-cli not found"; exit 1; }

cd "$ROOT"
go build -o "$SNUG_BIN" ./cmd/snugkv

slot_ranges=("0-5460" "5461-10922" "10923-16383")

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

for i in 0 1 2; do
  start_redis_node "$((REDIS_BASE+i))"
done

for i in 0 1 2; do
  port="$((REDIS_BASE+i))"
  for _ in $(seq 1 100); do
    redis-cli -p "$port" PING >/dev/null 2>&1 && break
    sleep 0.05
  done
  redis-cli -p "$port" PING | grep -qx PONG
done

yes yes | redis-cli --cluster create   "127.0.0.1:$REDIS_BASE"   "127.0.0.1:$((REDIS_BASE+1))"   "127.0.0.1:$((REDIS_BASE+2))"   --cluster-replicas 0 >/dev/null

for _ in $(seq 1 100); do
  state="$(redis-cli -p "$REDIS_BASE" CLUSTER INFO 2>/dev/null | tr -d '\r' | grep '^cluster_state:' || true)"
  [[ "$state" == "cluster_state:ok" ]] && break
  sleep 0.05
done

start_snug_node() {
  local port="$1"
  local idx="$2"
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

for i in 0 1 2; do
  start_snug_node "$((SNUG_BASE+i))" "$i"
done

for i in 0 1 2; do
  port="$((SNUG_BASE+i))"
  for _ in $(seq 1 100); do
    redis-cli -p "$port" PING >/dev/null 2>&1 && break
    sleep 0.05
  done
  redis-cli -p "$port" PING | grep -qx PONG
done

# Find one local key for node 0 and one remote key, avoiding assumptions about
# exact CRC16 fixtures while keeping both systems on the same logical keys.
find_key_in_range() {
  local min="$1"
  local max="$2"
  local prefix="$3"
  local k slot
  for i in $(seq 1 10000); do
    k="$prefix:$i"
    slot="$(redis-cli -p "$REDIS_BASE" CLUSTER KEYSLOT "$k")"
    if (( slot >= min && slot <= max )); then
      printf '%s' "$k"
      return
    fi
  done
  echo "could not find key in slot range $min-$max" >&2
  exit 1
}

LOCAL_KEY="$(find_key_in_range 0 5460 local)"
REMOTE_KEY="$(find_key_in_range 10923 16383 remote)"
TAG_A="acct:{42}:a"
TAG_B="acct:{42}:b"

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
SET $LOCAL_KEY 1
SET $REMOTE_KEY 2
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
WATCH $LOCAL_KEY
MULTI
SET $LOCAL_KEY 3
EXEC
EOF

    echo "== watch-remote =="
    redis-cli --raw -p "$port" <<EOF
WATCH $REMOTE_KEY
EOF

    echo "== discard-reset =="
    redis-cli --raw -p "$port" <<EOF
MULTI
SET $LOCAL_KEY 4
DISCARD
MULTI
SET $REMOTE_KEY 5
EXEC
EOF
  } >"$out" 2>&1
}

run_cases "$REDIS_BASE" "$TMP/redis.out"
run_cases "$SNUG_BASE" "$TMP/snug.out"

normalize() {
  sed -E     -e "s/127\.0\.0\.1:$REDIS_BASE/NODE0/g"     -e "s/127\.0\.0\.1:$((REDIS_BASE+1))/NODE1/g"     -e "s/127\.0\.0\.1:$((REDIS_BASE+2))/NODE2/g"     -e "s/127\.0\.0\.1:$SNUG_BASE/NODE0/g"     -e "s/127\.0\.0\.1:$((SNUG_BASE+1))/NODE1/g"     -e "s/127\.0\.0\.1:$((SNUG_BASE+2))/NODE2/g"     "$1"
}

normalize "$TMP/redis.out" >"$TMP/redis.normalized"
normalize "$TMP/snug.out" >"$TMP/snug.normalized"

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
