#!/usr/bin/env bash
set -euo pipefail

PORT="${REDIS_PORT:-6390}"
TARGET="${TARGET_NAME:-redis82}"
CLI=(redis-cli -p "$PORT" --raw)

section() {
  printf '\n=== %s ===\n' "$1"
}

run() {
  printf '> '
  printf '%q ' "$@"
  printf '\n'
  "${CLI[@]}" "$@" 2>&1 || true
}

printf 'target=%s port=%s\n' "$TARGET" "$PORT"

section "setup"
run FLUSHDB
run JSON.SET json:core '$' '{"user":{"name":"Edijs","profile":{"age":42},"weird.key":"dot"},"items":[{"name":"a"},{"name":"b"},3],"empty":[],"nil":null}'

section "root aliases"
run JSON.GET json:core '$'
run JSON.GET json:core '.'
run JSON.TYPE json:core '$'
run JSON.TYPE json:core '.'

section "object member paths"
run JSON.GET json:core '$.user.name'
run JSON.TYPE json:core '$.user.profile.age'
run JSON.GET json:core '$.user.missing'
run JSON.TYPE json:core '$.user.missing'

section "bracket member paths"
run JSON.GET json:core '$["user"]["name"]'
run JSON.GET json:core '$["user"]["weird.key"]'
run JSON.TYPE json:core '$["nil"]'

section "array index paths"
run JSON.GET json:core '$.items[0]'
run JSON.GET json:core '$.items[1].name'
run JSON.TYPE json:core '$.items[2]'
run JSON.GET json:core '$.items[99]'
run JSON.GET json:core '$.items[-1]'

section "JSON.SET exact paths"
run JSON.SET json:core '$.user.name' '"Alice"'
run JSON.GET json:core '$.user.name'
run JSON.SET json:core '$.items[1].name' '"Bee"'
run JSON.GET json:core '$.items[1].name'
run JSON.SET json:core '$.items[1].newField' 'true'
run JSON.GET json:core '$.items[1]'
run JSON.SET json:core '$.missing.child' '1'

section "JSON.SET NX XX"
run JSON.SET json:core '$.user.name' '"NX"' NX
run JSON.SET json:core '$.user.name' '"XX"' XX
run JSON.GET json:core '$.user.name'
run JSON.SET json:core '$.user.newName' '"created"' NX
run JSON.SET json:core '$.user.missingXX' '"nope"' XX
run JSON.GET json:core '$.user.newName'

section "JSON.DEL exact paths"
run JSON.SET json:del '$' '{"obj":{"a":1,"b":2},"arr":[10,20,30]}'
run JSON.DEL json:del '$.obj.a'
run JSON.GET json:del '$'
run JSON.DEL json:del '$.arr[1]'
run JSON.GET json:del '$'
run JSON.DEL json:del '$.doesNotExist'
run JSON.DEL json:del '$'
run JSON.GET json:del '$'

section "invalid path syntax"
run JSON.GET json:core 'user.name'
run JSON.GET json:core '$.'
run JSON.GET json:core '$..name'
run JSON.GET json:core '$['
run JSON.SET json:core '$.items[' '1'
run JSON.DEL json:core '$.items['
