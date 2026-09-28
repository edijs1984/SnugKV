#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
TMP="${TMPDIR:-/tmp}/snugkv-cluster-scripting-diff"
SNUG_BIN="$TMP/snugkv"
REDIS_BASE="${REDIS_BASE_PORT:-7200}"
SNUG_BASE="${SNUG_BASE_PORT:-7300}"

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
  echo "scripting differential failed with exit $status" >&2
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
  echo "port $port did not become ready" >&2
  return 1
}

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

rm -rf "$TMP"
mkdir -p "$TMP"

command -v redis-server >/dev/null
command -v redis-cli >/dev/null

REDIS_VERSION="$(redis-server --version | sed -E 's/.*v=([0-9]+)\.([0-9]+)\..*/\1.\2/')"
REDIS_MAJOR="${REDIS_VERSION%%.*}"

echo "[1/8] build SnugKV"
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

echo "[2/8] start Redis Cluster ($REDIS_VERSION)"
for i in 0 1 2; do
  start_redis_node "$((REDIS_BASE+i))"
done
for i in 0 1 2; do
  wait_for_pong "$((REDIS_BASE+i))"
done

echo "[3/8] create Redis Cluster"
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
(( cluster_ok == 1 ))

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

echo "[4/8] start SnugKV Cluster"
for i in 0 1 2; do
  start_snug_node "$((SNUG_BASE+i))"
done
for i in 0 1 2; do
  wait_for_pong "$((SNUG_BASE+i))"
done

echo "[5/8] select slot fixtures"
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
TAG_A="eval:{$LOCAL_TAG}:a"
TAG_B="eval:{$LOCAL_TAG}:b"

run_legacy_cases() {
  local port="$1"
  local out="$2"
  {
    echo "== declared-same-slot =="
    redis-cli --raw -p "$port" EVAL "return {KEYS[1],KEYS[2]}" 2 "$TAG_A" "$TAG_B"

    echo "== declared-cross-slot =="
    redis-cli --raw -p "$port" EVAL "return 1" 2 "$LOCAL_A" "$LOCAL_B"

    echo "== declared-remote =="
    redis-cli --raw -p "$port" EVAL "return redis.call('GET',KEYS[1])" 1 "$REMOTE_KEY"

    echo "== nested-local =="
    redis-cli --raw -p "$port" EVAL "return redis.call('GET','$LOCAL_A')" 0

    echo "== nested-remote =="
    redis-cli --raw -p "$port" EVAL "return redis.call('GET','$REMOTE_KEY')" 0

    echo "== nested-local-cross-slot =="
    redis-cli --raw -p "$port" EVAL "return {redis.call('GET','$LOCAL_A'),redis.call('GET','$LOCAL_B')}" 0
  } >"$out" 2>&1
}

normalize() {
  local input="$1"
  sed -E \
    -e "s/127\.0\.0\.1:$REDIS_BASE/NODE0/g" \
    -e "s/127\.0\.0\.1:$((REDIS_BASE+1))/NODE1/g" \
    -e "s/127\.0\.0\.1:$((REDIS_BASE+2))/NODE2/g" \
    -e "s/127\.0\.0\.1:$SNUG_BASE/NODE0/g" \
    -e "s/127\.0\.0\.1:$((SNUG_BASE+1))/NODE1/g" \
    -e "s/127\.0\.0\.1:$((SNUG_BASE+2))/NODE2/g" \
    -e 's#ERR Error running script \(call to f_([0-9a-f]+)\): .*non local key in a cluster node.*#ERR Error running script (call to f_\1): non local key in a cluster node#' \
    "$input"
}

echo "[6/8] run legacy EVAL cases"
run_legacy_cases "$REDIS_BASE" "$TMP/redis-legacy.out"
run_legacy_cases "$SNUG_BASE" "$TMP/snug-legacy.out"
normalize "$TMP/redis-legacy.out" >"$TMP/redis-legacy.normalized"
normalize "$TMP/snug-legacy.out" >"$TMP/snug-legacy.normalized"

echo "[7/8] compare legacy EVAL behavior"
echo "===== Redis Cluster legacy EVAL ====="
cat "$TMP/redis-legacy.normalized"
echo
echo "===== SnugKV Cluster legacy EVAL ====="
cat "$TMP/snug-legacy.normalized"
echo
echo "===== Diff ====="
legacy_status=0
diff -u "$TMP/redis-legacy.normalized" "$TMP/snug-legacy.normalized" || legacy_status=$?

echo "[8/8] Redis 7/8 scripting/function capability"
if (( REDIS_MAJOR >= 7 )); then
  echo "Redis $REDIS_VERSION supports shebang script flags and Functions."
  echo "Run the Redis 7/8 extended cases in a follow-up harness revision."
else
  echo "Redis $REDIS_VERSION detected: extended Redis 7/8 shebang/FCALL differential skipped."
  echo "SnugKV extended cluster scripting/function behavior is covered by Go race tests."
fi

if (( legacy_status == 0 )); then
  echo "legacy scripting cluster parity: PASS"
else
  echo "legacy scripting cluster parity: DIFFERENCES FOUND"
  exit 1
fi
