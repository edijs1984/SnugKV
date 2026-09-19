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

tmp="$(mktemp)"
trap 'rm -f "$tmp"' EXIT

cat >"$tmp" <<'LUA'
redis.call('SET','debug:key','written')
redis.call('INCR','debug:counter')
return redis.call('GET','debug:key')
LUA

echo "SCRIPT DEBUG differential target=$TARGET addr=$HOST:$PORT"

heading "command syntax"
redis SCRIPT DEBUG NO 2>&1 || true
redis SCRIPT DEBUG YES 2>&1 || true
redis SCRIPT DEBUG SYNC 2>&1 || true

heading "invalid mode"
redis SCRIPT DEBUG MAYBE 2>&1 || true

heading "missing mode"
redis SCRIPT DEBUG 2>&1 || true

heading "extra argument"
redis SCRIPT DEBUG NO EXTRA 2>&1 || true

heading "async ldb session"
redis DEL debug:key debug:counter >/dev/null
printf 'continue\n' | timeout 10s redis-cli -h "$HOST" -p "$PORT" --ldb --eval "$tmp" 2>&1 || true
printf 'key_exists_after_async=%s\n' "$(redis EXISTS debug:key)"
printf 'counter_exists_after_async=%s\n' "$(redis EXISTS debug:counter)"

heading "sync ldb session"
redis DEL debug:key debug:counter >/dev/null
printf 'continue\n' | timeout 10s redis-cli -h "$HOST" -p "$PORT" --ldb-sync-mode --eval "$tmp" 2>&1 || true
printf 'key_exists_after_sync=%s\n' "$(redis EXISTS debug:key)"
printf 'counter_after_sync=%s\n' "$(redis GET debug:counter)"

heading "normal eval after debug sessions"
redis EVAL "return redis.call('PING')" 0 2>&1 || true

echo
echo "SCRIPT DEBUG differential complete: $TARGET"
