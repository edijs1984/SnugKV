#!/usr/bin/env bash
set -euo pipefail

HOST="${REDIS_HOST:-127.0.0.1}"
PORT="${REDIS_PORT:?set REDIS_PORT}"
TARGET="${TARGET_NAME:-target}"

redis() {
  redis-cli -h "$HOST" -p "$PORT" --raw "$@"
}

original_maxmemory="$(redis CONFIG GET maxmemory | tail -n1)"
original_policy="$(redis CONFIG GET maxmemory-policy | tail -n1)"

restore_limits() {
  redis CONFIG SET maxmemory "$original_maxmemory" >/dev/null
  redis CONFIG SET maxmemory-policy "$original_policy" >/dev/null
}

cleanup() {
  restore_limits >/dev/null 2>&1 || true
  redis FLUSHDB >/dev/null 2>&1 || true
}
trap cleanup EXIT

expect_raw() {
  local expected="$1"
  shift
  local got
  got="$(redis "$@" 2>&1 || true)"
  if [[ "$got" != "$expected" ]]; then
    echo "HARNESS SETUP FAILURE: redis $*" >&2
    echo "  got:      $got" >&2
    echo "  expected: $expected" >&2
    exit 1
  fi
}

prepare() {
  # Seed from a known non-OOM state regardless of the server's incoming
  # maxmemory setting. Previous OOM audits may intentionally leave a tiny
  # runtime maxmemory configured.
  expect_raw "OK" CONFIG SET maxmemory 0
  expect_raw "0" CONFIG GET maxmemory
  expect_raw "OK" CONFIG SET maxmemory-policy noeviction
  expect_raw "OK" FLUSHDB

  expect_raw "OK" SET oom:str value
  expect_raw "OK" SET oom:counter 1
  expect_raw "OK" SET oom:expire value EX 3600
  expect_raw "1" HSET oom:hash field value
  expect_raw "1" SADD oom:set member
  expect_raw "2" RPUSH oom:list a b
  expect_raw "1" ZADD oom:zset 1 member
  redis XADD oom:stream 1-0 field value >/dev/null
  expect_raw "1" PFADD oom:hll member
  expect_raw "2" RPUSH oom:sort 2 1
  expect_raw "OK" SET oom:copy-src value

  expect_raw "1" EXISTS oom:str
  expect_raw "1" EXISTS oom:hash
  expect_raw "1" EXISTS oom:list
  expect_raw "1" EXISTS oom:stream
}

enter_oom() {
  redis CONFIG SET maxmemory-policy noeviction >/dev/null
  redis CONFIG SET maxmemory 1 >/dev/null
}

case_cmd() {
  local title="$1"
  shift
  echo
  echo "== $title =="
  prepare
  enter_oom
  redis "$@" 2>&1 || true
}

echo "command OOM differential target=$TARGET addr=$HOST:$PORT"

echo
echo "== Redis command flags =="
for cmd in GET SET DEL EXPIRE PERSIST HSET HDEL SADD SREM LPUSH LPOP ZADD ZREM XADD XDEL PFADD PFCOUNT SORT SORT_RO COPY RENAME TOUCH; do
  printf '%s: ' "$cmd"
  redis COMMAND INFO "$cmd" 2>/dev/null |
    awk '
      BEGIN { first=1 }
      /^write$|^denyoom$|^readonly$|^fast$|^movablekeys$/ {
        if (!first) printf ","
        printf "%s", $0
        first=0
      }
      END { printf "\n" }
    '
done

case_cmd "GET while OOM" GET oom:str
case_cmd "EXISTS while OOM" EXISTS oom:str
case_cmd "TOUCH while OOM" TOUCH oom:str
case_cmd "DEL while OOM" DEL oom:str
case_cmd "EXPIRE while OOM" EXPIRE oom:str 120
case_cmd "PERSIST while OOM" PERSIST oom:expire
case_cmd "HDEL while OOM" HDEL oom:hash field
case_cmd "SREM while OOM" SREM oom:set member
case_cmd "LPOP while OOM" LPOP oom:list
case_cmd "ZREM while OOM" ZREM oom:zset member
case_cmd "XDEL while OOM" XDEL oom:stream 1-0
case_cmd "PFCOUNT while OOM" PFCOUNT oom:hll

case_cmd "SET new key while OOM" SET oom:new value
case_cmd "SET existing key while OOM" SET oom:str changed
case_cmd "APPEND while OOM" APPEND oom:str x
case_cmd "INCR while OOM" INCR oom:counter
case_cmd "HSET new field while OOM" HSET oom:hash other value
case_cmd "SADD new member while OOM" SADD oom:set other
case_cmd "LPUSH while OOM" LPUSH oom:list c
case_cmd "ZADD new member while OOM" ZADD oom:zset 2 other
case_cmd "XADD while OOM" XADD oom:stream '*' other value
case_cmd "PFADD while OOM" PFADD oom:hll other
case_cmd "SORT without STORE while OOM" SORT oom:sort
case_cmd "SORT_RO while OOM" SORT_RO oom:sort
case_cmd "COPY while OOM" COPY oom:copy-src oom:copy-dst
case_cmd "RENAME while OOM" RENAME oom:str oom:renamed

echo
echo "command OOM differential complete: $TARGET"