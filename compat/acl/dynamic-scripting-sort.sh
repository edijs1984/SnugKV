#!/usr/bin/env bash
set -euo pipefail

HOST="${REDIS_HOST:-127.0.0.1}"
PORT="${REDIS_PORT:?set REDIS_PORT}"
TARGET="${TARGET_NAME:-target}"

redis() {
  redis-cli -h "$HOST" -p "$PORT" --raw "$@"
}

user_redis() {
  local user="$1"
  shift
  redis-cli -h "$HOST" -p "$PORT" --raw --no-auth-warning --user "$user" --pass "" "$@"
}

section() {
  printf '\n== %s ==\n' "$1"
}

reset_acl_user() {
  local user="$1"
  redis ACL DELUSER "$user" >/dev/null 2>&1 || true
}

cleanup() {
  redis ACL DELUSER acl_script_cmd acl_script_key acl_script_ro acl_fn_key acl_sort_split acl_sort_ok acl_script_sort >/dev/null 2>&1 || true
  redis DEL acl:src acl:weight_1 acl:weight_2 acl:name_1 acl:name_2 >/dev/null 2>&1 || true
  redis FUNCTION FLUSH >/dev/null 2>&1 || true
}
trap cleanup EXIT

echo "dynamic ACL differential target=$TARGET addr=$HOST:$PORT"

cleanup

section "nested command denial"
redis ACL SETUSER acl_script_cmd on nopass resetkeys '~*' nocommands +eval
user_redis acl_script_cmd EVAL "return redis.call('GET','allowed:key')" 0 2>&1 || true

section "nested key denial"
redis ACL SETUSER acl_script_key on nopass resetkeys '~allowed:*' nocommands +eval +get
user_redis acl_script_key EVAL "return redis.call('GET','denied:key')" 0 2>&1 || true

section "read-only nested key denial"
redis ACL SETUSER acl_script_ro on nopass resetkeys '~allowed:*' nocommands +eval_ro +get
user_redis acl_script_ro EVAL_RO "return redis.call('GET','denied:key')" 0 2>&1 || true

section "function nested key denial"
redis FUNCTION LOAD "#!lua name=acltest
redis.register_function('read_denied', function(keys,args) return redis.call('GET','denied:key') end)" >/dev/null
redis ACL SETUSER acl_fn_key on nopass resetkeys '~allowed:*' nocommands +fcall +get
user_redis acl_fn_key FCALL read_denied 0 2>&1 || true

section "SORT split-selector denial"
redis RPUSH acl:src 1 2 >/dev/null
redis SET acl:weight_1 2 >/dev/null
redis SET acl:weight_2 1 >/dev/null
redis ACL SETUSER acl_sort_split on nopass resetkeys '~acl:src' nocommands +sort '(~acl:weight_* +sort)'
user_redis acl_sort_split SORT acl:src BY 'acl:weight_*' 2>&1 || true

section "SORT complete-selector allow"
redis SET acl:name_1 one >/dev/null
redis SET acl:name_2 two >/dev/null
redis ACL SETUSER acl_sort_ok on nopass resetkeys nocommands '(~acl:src ~acl:weight_* ~acl:name_* +sort)'
user_redis acl_sort_ok SORT acl:src BY 'acl:weight_*' GET 'acl:name_*' 2>&1 || true

section "nested SORT dynamic-key denial"
redis ACL SETUSER acl_script_sort on nopass resetkeys '~acl:src' nocommands +eval +sort
user_redis acl_script_sort EVAL "return redis.call('SORT','acl:src','BY','acl:weight_*')" 0 2>&1 || true

echo
echo "dynamic ACL differential complete: $TARGET"