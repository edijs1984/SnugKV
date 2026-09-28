#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
BIN=/tmp/snugkv-cluster-smoke-bin
TMP=/tmp/snugkv-cluster-smoke

cleanup() {
  if [[ "${KEEP_CLUSTER:-0}" == "1" ]]; then
    echo "cluster left running on ports 7000, 7001, 7002"
    echo "logs: $TMP/node-7000.log $TMP/node-7001.log $TMP/node-7002.log"
    return
  fi

  for pidfile in "$TMP"/node*.pid; do
    [[ -f "$pidfile" ]] || continue
    pid="$(cat "$pidfile")"
    kill "$pid" 2>/dev/null || true
    wait "$pid" 2>/dev/null || true
  done
  rm -f "$BIN"
}
trap cleanup EXIT

rm -rf "$TMP"
mkdir -p "$TMP"

cd "$ROOT"
go build -o "$BIN" ./cmd/snugkv

make_config() {
  local port="$1"
  local path="$2"
  cat >"$path" <<JSON
{
  "listen": "127.0.0.1:$port",
  "admin_listen": "",
  "metrics_listen": "",
  "cluster_enabled": true,
  "cluster_node_addr": "127.0.0.1:$port",
  "cluster_slots": {
    "0-5460": "127.0.0.1:7000",
    "5461-10922": "127.0.0.1:7001",
    "10923-16383": "127.0.0.1:7002"
  }
}
JSON
}

for port in 7000 7001 7002; do
  cfg="$TMP/node-$port.json"
  make_config "$port" "$cfg"
  "$BIN" -config "$cfg" >"$TMP/node-$port.log" 2>&1 &
  echo $! >"$TMP/node-$port.pid"
done

for port in 7000 7001 7002; do
  for _ in $(seq 1 50); do
    if redis-cli -h 127.0.0.1 -p "$port" PING >/dev/null 2>&1; then
      break
    fi
    sleep 0.1
  done
  redis-cli -h 127.0.0.1 -p "$port" PING | grep -qx PONG
done

find_key() {
  local start="$1"
  local end="$2"
  local prefix="$3"

  for i in $(seq 0 5000); do
    local key="$prefix:$i"
    local slot
    slot="$(redis-cli -h 127.0.0.1 -p 7000 CLUSTER KEYSLOT "$key")"
    if (( slot >= start && slot <= end )); then
      printf '%s\n' "$key"
      return 0
    fi
  done

  echo "unable to find key for slot range $start-$end" >&2
  return 1
}

k1="$(find_key 0 5460 n1)"
k2="$(find_key 5461 10922 n2)"
k3="$(find_key 10923 16383 n3)"

s1="$(redis-cli -p 7000 CLUSTER KEYSLOT "$k1")"
s2="$(redis-cli -p 7000 CLUSTER KEYSLOT "$k2")"
s3="$(redis-cli -p 7000 CLUSTER KEYSLOT "$k3")"

echo "keys: $k1->$s1 $k2->$s2 $k3->$s3"

echo "[1/4] direct MOVED"
moved="$(redis-cli --raw -p 7000 GET "$k2" 2>&1 || true)"
echo "MOVED reply: $moved"
grep -Fq "MOVED $s2 127.0.0.1:7001" <<<"$moved"

echo "[2/4] redis-cli -c redirect routing"

set1="$(redis-cli --raw -c -p 7000 SET "$k1" one 2>&1)"
set2="$(redis-cli --raw -c -p 7000 SET "$k2" two 2>&1)"
set3="$(redis-cli --raw -c -p 7000 SET "$k3" three 2>&1)"

printf 'SET1: %s\n' "$set1"
printf 'SET2: %s\n' "$set2"
printf 'SET3: %s\n' "$set3"

grep -q '^OK$' <<<"$set1"
grep -q '^OK$' <<<"$set2"
grep -q '^OK$' <<<"$set3"

direct1="$(redis-cli --raw -p 7000 GET "$k1" 2>&1)"
direct2="$(redis-cli --raw -p 7001 GET "$k2" 2>&1)"
direct3="$(redis-cli --raw -p 7002 GET "$k3" 2>&1)"

printf 'DIRECT1@7000: %s\n' "$direct1"
printf 'DIRECT2@7001: %s\n' "$direct2"
printf 'DIRECT3@7002: %s\n' "$direct3"

grep -q '^one$' <<<"$direct1"
grep -q '^two$' <<<"$direct2"
grep -q '^three$' <<<"$direct3"

get1="$(redis-cli --raw -c -p 7000 GET "$k1" 2>&1)"
get2="$(redis-cli --raw -c -p 7000 GET "$k2" 2>&1)"
get3="$(redis-cli --raw -c -p 7000 GET "$k3" 2>&1)"

printf 'GET1: %s\n' "$get1"
printf 'GET2: %s\n' "$get2"
printf 'GET3: %s\n' "$get3"

grep -q '^one$' <<<"$get1"
grep -q '^two$' <<<"$get2"
grep -q '^three$' <<<"$get3"

echo "[3/4] hash-tag and CROSSSLOT"

tagged="$(redis-cli --raw -c -p 7000 MSET "acct:{42}:a" A "acct:{42}:b" B 2>&1)"
printf 'MSET tagged: %s\n' "$tagged"
grep -q '^OK$' <<<"$tagged"

cross="$(redis-cli --raw -p 7000 MGET "$k1" "$k2" 2>&1 || true)"
echo "CROSSSLOT reply: $cross"
grep -Fq "CROSSSLOT Keys in request don't hash to the same slot" <<<"$cross"

echo "[4/4] topology discovery"

info="$(redis-cli --raw -p 7000 CLUSTER INFO)"
nodes="$(redis-cli --raw -p 7000 CLUSTER NODES)"

grep -q "cluster_state:ok" <<<"$info"
[[ "$(wc -l <<<"$nodes")" -eq 3 ]]

redis-cli --raw -p 7000 CLUSTER SLOTS >/dev/null
redis-cli --raw -p 7000 CLUSTER SHARDS >/dev/null

echo "cluster static smoke: PASS"
