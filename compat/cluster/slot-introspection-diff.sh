#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
TMP="${TMPDIR:-/tmp}/snugkv-cluster-slot-introspection-diff"
SNUG_BIN="$TMP/snugkv"
REDIS_BASE="${REDIS_BASE_PORT:-7600}"
SNUG_BASE="${SNUG_BASE_PORT:-7700}"

cleanup() {
  set +e
  for pidfile in "$TMP"/redis-*.pid "$TMP"/snug-*.pid; do
    [[ -f "$pidfile" ]] || continue
    pid="$(cat "$pidfile")"
    kill "$pid" 2>/dev/null || true
    wait "$pid" 2>/dev/null || true
  done
}
trap cleanup EXIT

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

for i in 0 1 2; do start_redis_node "$((REDIS_BASE+i))"; done
for i in 0 1 2; do wait_for_pong "$((REDIS_BASE+i))"; done

redis-cli --cluster create   "127.0.0.1:$REDIS_BASE"   "127.0.0.1:$((REDIS_BASE+1))"   "127.0.0.1:$((REDIS_BASE+2))"   --cluster-replicas 0 --cluster-yes >/dev/null

for _ in $(seq 1 100); do
  if redis-cli -p "$REDIS_BASE" CLUSTER INFO | grep -q 'cluster_state:ok'; then break; fi
  sleep 0.05
done

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

for i in 0 1 2; do start_snug_node "$((SNUG_BASE+i))"; done
for i in 0 1 2; do wait_for_pong "$((SNUG_BASE+i))"; done

TAG=""
for i in $(seq 1 30000); do
  candidate="slot$i"
  slot="$(redis-cli -p "$REDIS_BASE" CLUSTER KEYSLOT "{$candidate}")"
  if (( slot >= 0 && slot <= 5460 )); then
    TAG="$candidate"
    SLOT="$slot"
    break
  fi
done
[[ -n "$TAG" ]]

K1="a{$TAG}"
K2="b{$TAG}"

expect_ok() {
  local output
  output="$("$@" 2>&1)"
  if [[ "$output" != "OK" ]]; then
    echo "command failed: $*" >&2
    echo "$output" >&2
    exit 1
  fi
}

expect_ok redis-cli --raw -p "$REDIS_BASE" SET "$K1" one
expect_ok redis-cli --raw -p "$REDIS_BASE" SET "$K2" two
expect_ok redis-cli --raw -p "$SNUG_BASE" SET "$K1" one
expect_ok redis-cli --raw -p "$SNUG_BASE" SET "$K2" two

redis_dst_id="$(redis-cli -p "$REDIS_BASE" CLUSTER NODES | awk -v p="127.0.0.1:$((REDIS_BASE+1))@" '$2 ~ p {print $1; exit}')"
redis_src_id="$(redis-cli -p "$REDIS_BASE" CLUSTER NODES | awk -v p="127.0.0.1:$REDIS_BASE@" '$2 ~ p {print $1; exit}')"
snug_dst_id="$(redis-cli -p "$SNUG_BASE" CLUSTER NODES | awk -v p="127.0.0.1:$((SNUG_BASE+1))@" '$2 ~ p {print $1; exit}')"
snug_src_id="$(redis-cli -p "$SNUG_BASE" CLUSTER NODES | awk -v p="127.0.0.1:$SNUG_BASE@" '$2 ~ p {print $1; exit}')"

expect_ok redis-cli --raw -p "$REDIS_BASE" CLUSTER SETSLOT "$SLOT" MIGRATING "$redis_dst_id"
expect_ok redis-cli --raw -p "$((REDIS_BASE+1))" CLUSTER SETSLOT "$SLOT" IMPORTING "$redis_src_id"
expect_ok redis-cli --raw -p "$SNUG_BASE" CLUSTER SETSLOT "$SLOT" MIGRATING "$snug_dst_id"
expect_ok redis-cli --raw -p "$((SNUG_BASE+1))" CLUSTER SETSLOT "$SLOT" IMPORTING "$snug_src_id"

run_cases() {
  local src="$1"
  local dst="$2"
  local out="$3"
  {
    echo "== count =="
    redis-cli --raw -p "$src" CLUSTER COUNTKEYSINSLOT "$SLOT"

    echo "== get-1 =="
    redis-cli --raw -p "$src" CLUSTER GETKEYSINSLOT "$SLOT" 1

    echo "== get-10 =="
    redis-cli --raw -p "$src" CLUSTER GETKEYSINSLOT "$SLOT" 10 | sort

    echo "== get-0 =="
    redis-cli --raw -p "$src" CLUSTER GETKEYSINSLOT "$SLOT" 0

    echo "== remote-count =="
    redis-cli --raw -p "$dst" CLUSTER COUNTKEYSINSLOT "$SLOT"

    echo "== source-nodes-marker =="
    redis-cli --raw -p "$src" CLUSTER NODES | grep 'myself'

    echo "== target-nodes-marker =="
    redis-cli --raw -p "$dst" CLUSTER NODES | grep 'myself'
  } >"$out" 2>&1
}

run_cases "$REDIS_BASE" "$((REDIS_BASE+1))" "$TMP/redis.out"
run_cases "$SNUG_BASE" "$((SNUG_BASE+1))" "$TMP/snug.out"

normalize() {
  sed -E \
    -e "s/127\\.0\\.0\\.1:$REDIS_BASE@[0-9]+/NODE0@BUS/g" \
    -e "s/127\\.0\\.0\\.1:$((REDIS_BASE+1))@[0-9]+/NODE1@BUS/g" \
    -e "s/127\\.0\\.0\\.1:$((REDIS_BASE+2))@[0-9]+/NODE2@BUS/g" \
    -e "s/127\\.0\\.0\\.1:$SNUG_BASE@0/NODE0@BUS/g" \
    -e "s/127\\.0\\.0\\.1:$((SNUG_BASE+1))@0/NODE1@BUS/g" \
    -e "s/127\\.0\\.0\\.1:$((SNUG_BASE+2))@0/NODE2@BUS/g" \
    -e "s/$redis_src_id/SRCID/g" \
    -e "s/$redis_dst_id/DSTID/g" \
    -e "s/$snug_src_id/SRCID/g" \
    -e "s/$snug_dst_id/DSTID/g" \
    -e 's/(myself,master -) [0-9]+ [0-9]+ [0-9]+ (connected)/\\1 EPOCH EPOCH EPOCH \\2/'
}

normalize <"$TMP/redis.out" >"$TMP/redis.normalized"
normalize <"$TMP/snug.out" >"$TMP/snug.normalized"

echo "===== Redis ====="
cat "$TMP/redis.normalized"
echo
echo "===== SnugKV ====="
cat "$TMP/snug.normalized"
echo
echo "===== Diff ====="

if diff -u "$TMP/redis.normalized" "$TMP/snug.normalized"; then
  echo "slot introspection cluster parity: PASS"
else
  echo "slot introspection cluster parity: DIFFERENCES FOUND"
  exit 1
fi
