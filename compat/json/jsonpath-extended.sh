#!/usr/bin/env bash
set -euo pipefail

PORT="${REDIS_PORT:-6390}"
TARGET="${TARGET_NAME:-redis}"
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
run JSON.SET json:path '$' '{"store":{"books":[{"title":"A","price":8,"tags":["go","db"]},{"title":"B","price":12,"tags":["js"]},{"title":"C","price":20,"tags":["go","systems"]},{"title":"D","price":5,"tags":[]}],"nested":{"name":"outer","child":{"name":"inner"}}},"nums":[0,1,2,3,4,5]}'

section "wildcards"
run JSON.GET json:path '$.store.books[*].title'
run JSON.GET json:path '$.store.books.*.title'
run JSON.GET json:path '$.store.*'
run JSON.TYPE json:path '$.store.books[*].price'

section "recursive descent"
run JSON.GET json:path '$..name'
run JSON.GET json:path '$.store..name'
run JSON.GET json:path '$..title'
run JSON.GET json:path '$..*'

section "array slices"
run JSON.GET json:path '$.nums[1:4]'
run JSON.GET json:path '$.nums[:3]'
run JSON.GET json:path '$.nums[::2]'
run JSON.GET json:path '$.nums[-3:]'
run JSON.GET json:path '$.nums[::-1]'

section "array unions"
run JSON.GET json:path '$.nums[0,2,4]'
run JSON.GET json:path '$.nums[4,2,0]'
run JSON.GET json:path '$.nums[0,0,2]'

section "scalar filters"
run JSON.GET json:path '$.store.books[?(@.price < 10)].title'
run JSON.GET json:path '$.store.books[?(@.price <= 8)].title'
run JSON.GET json:path '$.store.books[?(@.price > 10)].title'
run JSON.GET json:path '$.store.books[?(@.price >= 12)].title'
run JSON.GET json:path '$.store.books[?(@.title == "B")].price'
run JSON.GET json:path '$.store.books[?(@.title != "B")].title'

section "logical filters"
run JSON.GET json:path '$.store.books[?(@.price >= 8 && @.price <= 12)].title'
run JSON.GET json:path '$.store.books[?(@.price < 8 || @.price > 15)].title'
run JSON.GET json:path '$.store.books[?(!(@.price < 10))].title'

section "regex and membership"
run JSON.GET json:path '$.store.books[?(@.title =~ "^A|C$")].title'
run JSON.GET json:path '$.store.books[?(@.title in ["A","C"])].title'
run JSON.GET json:path '$.store.books[?(@.title nin ["A","C"])].title'

section "array relations and size"
run JSON.GET json:path '$.store.books[?(@.tags subsetof ["go","db","systems"])].title'
run JSON.GET json:path '$.store.books[?(@.tags anyof ["systems"])].title'
run JSON.GET json:path '$.store.books[?(@.tags noneof ["js"])].title'
run JSON.GET json:path '$.store.books[?(@.tags size 2)].title'
run JSON.GET json:path '$.store.books[?(@.tags empty true)].title'

section "arithmetic filters"
run JSON.GET json:path '$.store.books[?(@.price + 2 == 10)].title'
run JSON.GET json:path '$.store.books[?(@.price * 2 >= 24)].title'
run JSON.GET json:path '$.store.books[?((@.price - 2) / 2 == 5)].title'

section "multi-match mutation"
run JSON.SET json:path '$.store.books[?(@.price >= 12)].expensive' 'true'
run JSON.GET json:path '$.store.books[*].expensive'
run JSON.DEL json:path '$.store.books[?(@.price < 10)]'
run JSON.GET json:path '$.store.books[*].title'

section "no match"
run JSON.GET json:path '$.store.books[?(@.price > 999)].title'
run JSON.TYPE json:path '$.store.books[?(@.price > 999)].title'
