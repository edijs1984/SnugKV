#!/usr/bin/env bash
set -euo pipefail

HOST="${REDIS_HOST:-127.0.0.1}"
PORT="${REDIS_PORT:-6390}"
TARGET="${TARGET_NAME:-redis82}"

redis() {
  redis-cli --raw -h "$HOST" -p "$PORT" "$@"
}

heading() {
  printf '\n== %s ==\n' "$1"
}

show_keys() {
  redis KEYS 'evict:*' | sort
}

original_maxmemory="$(redis CONFIG GET maxmemory | tail -n1)"
original_policy="$(redis CONFIG GET maxmemory-policy | tail -n1)"

restore() {
  redis CONFIG SET maxmemory 0 >/dev/null 2>&1 || true
  redis CONFIG SET maxmemory-policy "$original_policy" >/dev/null 2>&1 || true
  redis CONFIG SET maxmemory "$original_maxmemory" >/dev/null 2>&1 || true
}
trap restore EXIT

seed_allkeys() {
  redis CONFIG SET maxmemory 0 >/dev/null
  redis CONFIG SET maxmemory-policy allkeys-lru >/dev/null
  redis FLUSHDB >/dev/null

  redis SET evict:victim-a "$(printf 'A%.0s' {1..512})" >/dev/null
  redis SET evict:victim-b "$(printf 'B%.0s' {1..512})" >/dev/null
  redis SET evict:read keep >/dev/null

  local used
  used="$(redis INFO memory | awk -F: '/^used_memory:/{gsub("\r","",$2); print $2}')"
  redis CONFIG SET maxmemory "$((used + 256))" >/dev/null
}

seed_volatile() {
  redis CONFIG SET maxmemory 0 >/dev/null
  redis CONFIG SET maxmemory-policy volatile-lru >/dev/null
  redis FLUSHDB >/dev/null

  redis SET evict:persistent "$(printf 'P%.0s' {1..512})" >/dev/null
  redis SET evict:volatile-a "$(printf 'A%.0s' {1..512})" EX 600 >/dev/null
  redis SET evict:volatile-b "$(printf 'B%.0s' {1..512})" EX 600 >/dev/null
  redis SET evict:read keep >/dev/null

  local used
  used="$(redis INFO memory | awk -F: '/^used_memory:/{gsub("\r","",$2); print $2}')"
  redis CONFIG SET maxmemory "$((used + 256))" >/dev/null
}

seed_volatile_no_victim() {
  redis CONFIG SET maxmemory 0 >/dev/null
  redis CONFIG SET maxmemory-policy volatile-lru >/dev/null
  redis FLUSHDB >/dev/null

  redis SET evict:persistent "$(printf 'P%.0s' {1..1024})" >/dev/null
  redis SET evict:read keep >/dev/null

  local used
  used="$(redis INFO memory | awk -F: '/^used_memory:/{gsub("\r","",$2); print $2}')"
  redis CONFIG SET maxmemory "$((used + 64))" >/dev/null
}

echo "Lua eviction differential target=$TARGET addr=$HOST:$PORT"

seed_allkeys
heading "allkeys-lru legacy script growing write"
redis EVAL "return redis.call('SET','evict:new','$(printf 'N%.0s' {1..1024})')" 0 2>&1 || true
show_keys

seed_allkeys
heading "allkeys-lru shebang default growing write"
redis EVAL $'#!lua\nreturn redis.call("SET","evict:new","'$(printf 'N%.0s' {1..1024})'")' 0 2>&1 || true
show_keys

seed_allkeys
heading "allkeys-lru shebang allow-oom growing write"
redis EVAL $'#!lua flags=allow-oom\nreturn redis.call("SET","evict:new","'$(printf 'N%.0s' {1..1024})'")' 0 2>&1 || true
show_keys

seed_allkeys
heading "allkeys-lru no-writes read"
redis EVAL $'#!lua flags=no-writes\nreturn redis.call("GET","evict:read")' 0 2>&1 || true
show_keys

seed_volatile
heading "volatile-lru legacy script growing write"
redis EVAL "return redis.call('SET','evict:new','$(printf 'N%.0s' {1..1024})')" 0 2>&1 || true
show_keys
redis EXISTS evict:persistent

seed_volatile
heading "volatile-lru shebang default growing write"
redis EVAL $'#!lua\nreturn redis.call("SET","evict:new","'$(printf 'N%.0s' {1..1024})'")' 0 2>&1 || true
show_keys
redis EXISTS evict:persistent

seed_volatile
heading "volatile-lru allow-oom growing write"
redis EVAL $'#!lua flags=allow-oom\nreturn redis.call("SET","evict:new","'$(printf 'N%.0s' {1..1024})'")' 0 2>&1 || true
show_keys
redis EXISTS evict:persistent

seed_volatile_no_victim
heading "volatile-lru no eligible victim"
redis EVAL "return redis.call('SET','evict:new','$(printf 'N%.0s' {1..2048})')" 0 2>&1 || true
show_keys

seed_volatile_no_victim
heading "volatile-lru no eligible victim allow-oom"
redis EVAL $'#!lua flags=allow-oom\nreturn redis.call("SET","evict:new","'$(printf 'N%.0s' {1..2048})'")' 0 2>&1 || true
show_keys

echo
echo "Lua eviction differential complete: $TARGET"
