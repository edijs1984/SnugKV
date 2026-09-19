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

run_case() {
  local title="$1"
  shift
  heading "$title"
  "$@" 2>&1 || true
}

original_maxmemory="$(redis CONFIG GET maxmemory | tail -n1)"
original_policy="$(redis CONFIG GET maxmemory-policy | tail -n1)"

restore() {
  redis CONFIG SET maxmemory 0 >/dev/null 2>&1 || true
  redis CONFIG SET maxmemory-policy "$original_policy" >/dev/null 2>&1 || true
  redis CONFIG SET maxmemory "$original_maxmemory" >/dev/null 2>&1 || true
}
trap restore EXIT

prepare_oom() {
  redis CONFIG SET maxmemory 0 >/dev/null
  redis CONFIG SET maxmemory-policy noeviction >/dev/null
  redis FLUSHDB >/dev/null

  redis SET oom:read value >/dev/null
  redis SET oom:delete value >/dev/null
  redis SET oom:existing old >/dev/null

  redis CONFIG SET maxmemory 1 >/dev/null
}

echo "Lua OOM flags differential target=$TARGET addr=$HOST:$PORT"

prepare_oom

run_case "legacy EVAL read while OOM" \
  redis EVAL "return redis.call('GET','oom:read')" 0

prepare_oom
run_case "legacy EVAL growing write first while OOM" \
  redis EVAL "return redis.call('SET','oom:new','x')" 0

prepare_oom
run_case "legacy EVAL shrinking write first, then growing write" \
  redis EVAL "redis.call('DEL','oom:delete'); return redis.call('SET','oom:new','x')" 0
run_case "legacy post-script new key" redis GET oom:new

prepare_oom
run_case "shebang with no flags read while OOM" \
  redis EVAL $'#!lua\nreturn redis.call("GET","oom:read")' 0

prepare_oom
run_case "shebang allow-oom read while OOM" \
  redis EVAL $'#!lua flags=allow-oom\nreturn redis.call("GET","oom:read")' 0

prepare_oom
run_case "shebang allow-oom growing write while OOM" \
  redis EVAL $'#!lua flags=allow-oom\nreturn redis.call("SET","oom:new","x")' 0
run_case "allow-oom post-script new key" redis GET oom:new

prepare_oom
run_case "shebang no-writes read while OOM" \
  redis EVAL $'#!lua flags=no-writes\nreturn redis.call("GET","oom:read")' 0

prepare_oom
run_case "shebang no-writes write attempt" \
  redis EVAL $'#!lua flags=no-writes\nreturn redis.call("SET","oom:new","x")' 0

prepare_oom
run_case "EVAL_RO legacy read while OOM" \
  redis EVAL_RO "return redis.call('GET','oom:read')" 0

prepare_oom
run_case "EVAL_RO shebang default read while OOM" \
  redis EVAL_RO $'#!lua\nreturn redis.call("GET","oom:read")' 0

prepare_oom
run_case "EVAL_RO allow-oom write attempt" \
  redis EVAL_RO $'#!lua flags=allow-oom\nreturn redis.call("SET","oom:new","x")' 0

prepare_oom
run_case "unknown shebang flag" \
  redis EVAL $'#!lua flags=definitely-not-a-real-flag\nreturn 1' 0

prepare_oom
run_case "SCRIPT LOAD allow-oom" \
  redis SCRIPT LOAD $'#!lua flags=allow-oom\nreturn redis.call("GET","oom:read")'

prepare_oom
sha="$(redis SCRIPT LOAD $'#!lua flags=allow-oom\nreturn redis.call("SET","oom:sha","x")' 2>&1 || true)"
heading "EVALSHA allow-oom growing write while OOM"
if [[ "$sha" == ERR* || "$sha" == OOM* ]]; then
  echo "$sha"
else
  redis EVALSHA "$sha" 0 2>&1 || true
  redis GET oom:sha 2>&1 || true
fi

echo
echo "Lua OOM flags differential complete: $TARGET"
