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

cleanup() {
  redis CONFIG SET maxmemory "$original_maxmemory" >/dev/null 2>&1 || true
  redis CONFIG SET maxmemory-policy "$original_policy" >/dev/null 2>&1 || true
  redis FUNCTION FLUSH >/dev/null 2>&1 || true
  redis DEL oom:seed oom:plain oom:allow oom:ro oom:delete oom:after >/dev/null 2>&1 || true
}
trap cleanup EXIT

echo "allow-oom differential target=$TARGET addr=$HOST:$PORT"

cleanup

cat >/tmp/snugkv_allow_oom_lib.lua <<'EOF'
#!lua name=oomaudit
redis.register_function('plain_read', function(keys,args)
  return redis.call('GET', keys[1])
end)
redis.register_function('plain_write', function(keys,args)
  return redis.call('SET', keys[1], args[1])
end)
redis.register_function{
  function_name='allow_read',
  callback=function(keys,args)
    return redis.call('GET', keys[1])
  end,
  flags={'allow-oom'}
}
redis.register_function{
  function_name='allow_write',
  callback=function(keys,args)
    return redis.call('SET', keys[1], args[1])
  end,
  flags={'allow-oom'}
}
redis.register_function{
  function_name='readonly',
  callback=function(keys,args)
    return redis.call('GET', keys[1])
  end,
  flags={'no-writes'}
}
redis.register_function{
  function_name='readonly_badwrite',
  callback=function(keys,args)
    return redis.call('SET', keys[1], args[1])
  end,
  flags={'no-writes'}
}
redis.register_function{
  function_name='allow_delete',
  callback=function(keys,args)
    return redis.call('DEL', keys[1])
  end,
  flags={'allow-oom'}
}
EOF

echo
echo "== function load =="
redis -x FUNCTION LOAD < /tmp/snugkv_allow_oom_lib.lua 2>&1 || true

echo
echo "== seed before OOM =="
redis SET oom:seed "$(printf 'x%.0s' {1..4096})" 2>&1 || true

# Force current usage above the configured maxmemory without evicting existing data.
redis CONFIG SET maxmemory-policy noeviction >/dev/null
redis CONFIG SET maxmemory 1 >/dev/null

echo
echo "== plain FCALL read while OOM =="
redis FCALL plain_read 1 oom:seed 2>&1 || true

echo
echo "== plain FCALL write while OOM =="
redis FCALL plain_write 1 oom:plain value 2>&1 || true

echo
echo "== allow-oom read while OOM =="
redis FCALL allow_read 1 oom:seed 2>&1 || true

echo
echo "== allow-oom write while OOM =="
redis FCALL allow_write 1 oom:allow value 2>&1 || true

echo
echo "== no-writes FCALL read while OOM =="
redis FCALL readonly 1 oom:seed 2>&1 || true

echo
echo "== no-writes FCALL attempted write while OOM =="
redis FCALL readonly_badwrite 1 oom:ro value 2>&1 || true

echo
echo "== FCALL_RO read while OOM =="
redis FCALL_RO plain_read 1 oom:seed 2>&1 || true

echo
echo "== FCALL_RO attempted write while OOM =="
redis FCALL_RO plain_write 1 oom:ro value 2>&1 || true

echo
echo "== allow-oom delete while OOM =="
redis SET oom:delete keep >/dev/null 2>&1 || true
# SET above is expected to fail while maxmemory=1, so temporarily restore to seed deletion target.
redis CONFIG SET maxmemory "$original_maxmemory" >/dev/null
redis SET oom:delete keep >/dev/null
redis CONFIG SET maxmemory 1 >/dev/null
redis FCALL allow_delete 1 oom:delete 2>&1 || true
redis EXISTS oom:delete 2>&1 || true

echo
echo "== bypass is scoped to function invocation =="
redis SET oom:after value 2>&1 || true

echo
echo "allow-oom differential complete: $TARGET"
