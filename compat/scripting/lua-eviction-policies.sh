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

# Deterministic high-entropy ASCII payload. This avoids accidentally testing
# SnugKV compression efficiency instead of Redis-compatible eviction semantics.
payload() {
  local bytes="$1"
  local seed="$2"
  local out=""
  local i=0
  while [ "${#out}" -lt "$bytes" ]; do
    out+="$(printf '%s:%d' "$seed" "$i" | sha256sum | cut -d' ' -f1)"
    i=$((i + 1))
  done
  printf '%s' "${out:0:bytes}"
}

victim_count() {
  redis KEYS 'evict:victim-*' | sed '/^$/d' | wc -l
}

report_new() {
  printf 'new_exists=%s\n' "$(redis EXISTS evict:new)"
}

report_allkeys_victims() {
  local count
  count="$(victim_count)"
  if [ "$count" -lt 16 ]; then
    echo "eligible_victim_evicted=yes"
  else
    echo "eligible_victim_evicted=no"
  fi
}

report_volatile() {
  local count
  count="$(victim_count)"
  printf 'persistent_exists=%s\n' "$(redis EXISTS evict:persistent)"
  if [ "$count" -lt 16 ]; then
    echo "eligible_victim_evicted=yes"
  else
    echo "eligible_victim_evicted=no"
  fi
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

  for i in $(seq 1 16); do
    redis SET "evict:victim-$i" "$(payload 4096 "allkeys-$i")" >/dev/null
  done
  redis SET evict:read keep >/dev/null

  local used
  used="$(redis INFO memory | awk -F: '/^used_memory:/{gsub("\r","",$2); print $2}')"
  redis CONFIG SET maxmemory "$((used + 512))" >/dev/null
}

seed_volatile() {
  redis CONFIG SET maxmemory 0 >/dev/null
  redis CONFIG SET maxmemory-policy volatile-lru >/dev/null
  redis FLUSHDB >/dev/null

  redis SET evict:persistent "$(payload 4096 persistent)" >/dev/null
  for i in $(seq 1 16); do
    redis SET "evict:victim-$i" "$(payload 4096 "volatile-$i")" EX 600 >/dev/null
  done
  redis SET evict:read keep >/dev/null

  local used
  used="$(redis INFO memory | awk -F: '/^used_memory:/{gsub("\r","",$2); print $2}')"
  redis CONFIG SET maxmemory "$((used + 512))" >/dev/null
}

seed_volatile_no_victim() {
  redis CONFIG SET maxmemory 0 >/dev/null
  redis CONFIG SET maxmemory-policy volatile-lru >/dev/null
  redis FLUSHDB >/dev/null

  redis SET evict:persistent "$(payload 4096 persistent-no-victim)" >/dev/null
  redis SET evict:read keep >/dev/null

  local used
  used="$(redis INFO memory | awk -F: '/^used_memory:/{gsub("\r","",$2); print $2}')"
  redis CONFIG SET maxmemory "$((used + 128))" >/dev/null
}

WRITE_PAYLOAD="$(payload 4096 write)"
LARGE_WRITE_PAYLOAD="$(payload 8192 large-write)"

echo "Lua eviction differential target=$TARGET addr=$HOST:$PORT"

seed_allkeys
heading "allkeys-lru legacy script growing write"
redis EVAL "return redis.call('SET','evict:new',ARGV[1])" 0 "$WRITE_PAYLOAD" 2>&1 || true
report_new
report_allkeys_victims

seed_allkeys
heading "allkeys-lru shebang default growing write"
redis EVAL $'#!lua\nreturn redis.call("SET","evict:new",ARGV[1])' 0 "$WRITE_PAYLOAD" 2>&1 || true
report_new
report_allkeys_victims

seed_allkeys
heading "allkeys-lru shebang allow-oom growing write"
redis EVAL $'#!lua flags=allow-oom\nreturn redis.call("SET","evict:new",ARGV[1])' 0 "$WRITE_PAYLOAD" 2>&1 || true
report_new
report_allkeys_victims

seed_allkeys
heading "allkeys-lru no-writes read"
redis EVAL $'#!lua flags=no-writes\nreturn redis.call("GET","evict:read")' 0 2>&1 || true
report_allkeys_victims

seed_volatile
heading "volatile-lru legacy script growing write"
redis EVAL "return redis.call('SET','evict:new',ARGV[1])" 0 "$WRITE_PAYLOAD" 2>&1 || true
report_new
report_volatile

seed_volatile
heading "volatile-lru shebang default growing write"
redis EVAL $'#!lua\nreturn redis.call("SET","evict:new",ARGV[1])' 0 "$WRITE_PAYLOAD" 2>&1 || true
report_new
report_volatile

seed_volatile
heading "volatile-lru allow-oom growing write"
redis EVAL $'#!lua flags=allow-oom\nreturn redis.call("SET","evict:new",ARGV[1])' 0 "$WRITE_PAYLOAD" 2>&1 || true
report_new
report_volatile

seed_volatile_no_victim
heading "volatile-lru no eligible victim"
redis EVAL "return redis.call('SET','evict:new',ARGV[1])" 0 "$LARGE_WRITE_PAYLOAD" 2>&1 || true
report_new
printf 'persistent_exists=%s\n' "$(redis EXISTS evict:persistent)"

seed_volatile_no_victim
heading "volatile-lru no eligible victim allow-oom"
redis EVAL $'#!lua flags=allow-oom\nreturn redis.call("SET","evict:new",ARGV[1])' 0 "$LARGE_WRITE_PAYLOAD" 2>&1 || true
report_new
printf 'persistent_exists=%s\n' "$(redis EXISTS evict:persistent)"

echo
echo "Lua eviction differential complete: $TARGET"
