#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
TMP="${TMPDIR:-/tmp}/snugkv-cluster-migrate-diff"
SNUG_BIN="$TMP/snugkv"
REDIS_BASE="${REDIS_BASE_PORT:-7800}"
SNUG_BASE="${SNUG_BASE_PORT:-7900}"

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

expect_ok() {
  local output
  output="$("$@" 2>&1)"
  if [[ "$output" != "OK" ]]; then
    echo "command failed: $*" >&2
    echo "$output" >&2
    exit 1
  fi
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
  candidate="move$i"
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

for base in "$REDIS_BASE" "$SNUG_BASE"; do
  expect_ok redis-cli --raw -p "$base" SET "$K1" one
  expect_ok redis-cli --raw -p "$base" SET "$K2" two
done

redis_src_id="$(redis-cli -p "$REDIS_BASE" CLUSTER NODES | awk -v p="127.0.0.1:$REDIS_BASE@" '$2 ~ p {print $1; exit}')"
redis_dst_id="$(redis-cli -p "$REDIS_BASE" CLUSTER NODES | awk -v p="127.0.0.1:$((REDIS_BASE+1))@" '$2 ~ p {print $1; exit}')"
snug_src_id="$(redis-cli -p "$SNUG_BASE" CLUSTER NODES | awk -v p="127.0.0.1:$SNUG_BASE@" '$2 ~ p {print $1; exit}')"
snug_dst_id="$(redis-cli -p "$SNUG_BASE" CLUSTER NODES | awk -v p="127.0.0.1:$((SNUG_BASE+1))@" '$2 ~ p {print $1; exit}')"

setup_migration() {
  local src="$1" dst="$2" src_id="$3" dst_id="$4"
  expect_ok redis-cli --raw -p "$src" CLUSTER SETSLOT "$SLOT" MIGRATING "$dst_id"
  expect_ok redis-cli --raw -p "$dst" CLUSTER SETSLOT "$SLOT" IMPORTING "$src_id"
}

setup_migration "$REDIS_BASE" "$((REDIS_BASE+1))" "$redis_src_id" "$redis_dst_id"
setup_migration "$SNUG_BASE" "$((SNUG_BASE+1))" "$snug_src_id" "$snug_dst_id"

move_all() {
  local label="$1" src="$2" dst="$3"
  local host="127.0.0.1"
  local round=0

  while true; do
    round=$((round+1))
    if (( round > 20 )); then
      echo "$label migration did not converge after 20 rounds" >&2
      echo "remaining keys:" >&2
      redis-cli --raw -p "$src" CLUSTER GETKEYSINSLOT "$SLOT" 100 >&2 || true
      return 1
    fi

    remaining="$(redis-cli --raw -p "$src" CLUSTER COUNTKEYSINSLOT "$SLOT" | tr -d '\r\n')"
    if [[ "$remaining" == "0" ]]; then
      echo "$label migration complete after $((round-1)) round(s)"
      break
    fi
    if ! [[ "$remaining" =~ ^[0-9]+$ ]]; then
      echo "$label invalid COUNTKEYSINSLOT response: $remaining" >&2
      return 1
    fi

    mapfile -t raw_keys < <(redis-cli --raw -p "$src" CLUSTER GETKEYSINSLOT "$SLOT" 10)
    keys=()
    for key in "${raw_keys[@]}"; do
      [[ -n "$key" ]] && keys+=("$key")
    done
    if (( ${#keys[@]} == 0 )); then
      echo "$label COUNTKEYSINSLOT=$remaining but GETKEYSINSLOT returned no keys" >&2
      return 1
    fi

    echo "$label round $round: remaining=$remaining, batch=${#keys[@]} key(s): ${keys[*]}"
    for key in "${keys[@]}"; do
      echo "$label MIGRATE $key"
      output="$(timeout 8s redis-cli --raw -p "$src" MIGRATE "$host" "$dst" "$key" 0 5000 2>&1)" || {
        status=$?
        echo "$label MIGRATE failed/hung for $key (exit $status): $output" >&2
        return 1
      }
      if [[ "$output" != "OK" ]]; then
        echo "$label MIGRATE unexpected response for $key: $output" >&2
        return 1
      fi
    done
  done
}

move_all "redis" "$REDIS_BASE" "$((REDIS_BASE+1))"
move_all "snug" "$SNUG_BASE" "$((SNUG_BASE+1))"

run_cases() {
  local src="$1" dst="$2" dst_id="$3" out="$4"
  {
    echo "== source-count-after-migrate =="
    redis-cli --raw -p "$src" CLUSTER COUNTKEYSINSLOT "$SLOT"

    echo "== target-k1-with-asking =="
    redis-cli --raw -p "$dst" <<EOF
ASKING
GET $K1
EOF

    echo "== target-k2-with-asking =="
    redis-cli --raw -p "$dst" <<EOF
ASKING
GET $K2
EOF

    echo "== finalize =="
    redis-cli --raw -p "$src" CLUSTER SETSLOT "$SLOT" NODE "$dst_id"
    redis-cli --raw -p "$dst" CLUSTER SETSLOT "$SLOT" NODE "$dst_id"

    echo "== source-after-finalize =="
    redis-cli --raw -p "$src" GET "$K1"

    echo "== target-k1-after-finalize =="
    redis-cli --raw -p "$dst" GET "$K1"

    echo "== target-k2-after-finalize =="
    redis-cli --raw -p "$dst" GET "$K2"
  } >"$out" 2>&1
}

run_cases "$REDIS_BASE" "$((REDIS_BASE+1))" "$redis_dst_id" "$TMP/redis.out"
run_cases "$SNUG_BASE" "$((SNUG_BASE+1))" "$snug_dst_id" "$TMP/snug.out"

normalize() {
  sed -E     -e "s/127\.0\.0\.1:$REDIS_BASE/NODE0/g"     -e "s/127\.0\.0\.1:$((REDIS_BASE+1))/NODE1/g"     -e "s/127\.0\.0\.1:$SNUG_BASE/NODE0/g"     -e "s/127\.0\.0\.1:$((SNUG_BASE+1))/NODE1/g"
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
  echo "cluster MIGRATE resharding parity: PASS"
else
  echo "cluster MIGRATE resharding parity: DIFFERENCES FOUND"
  exit 1
fi
