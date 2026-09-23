#!/usr/bin/env bash
set -u

PORT="${REDIS_PORT:-6379}"
TARGET="${TARGET_NAME:-target}"
CLI=(redis-cli -p "$PORT" --raw)

run() {
  printf '> '
  printf '%q ' "$@"
  printf '\n'
  "${CLI[@]}" "$@" 2>&1 || true
  printf '\n'
}

echo "target=$TARGET port=$PORT"

echo
echo "=== reset ==="
run FLUSHDB
run FT.DROPINDEX phraseidx

echo
echo "=== seed ==="
run JSON.SET phrase:1 '$' '{"text":"memory guide"}'
run JSON.SET phrase:2 '$' '{"text":"memory fast guide"}'
run JSON.SET phrase:3 '$' '{"text":"guide memory"}'
run JSON.SET phrase:4 '$' '{"text":"memory very fast guide"}'
run JSON.SET phrase:5 '$' '{"text":"memory search guide"}'
run FT.CREATE phraseidx ON JSON PREFIX 1 phrase: SCHEMA '$.text' AS text TEXT

echo
echo "=== exact phrase baseline ==="
run FT.SEARCH phraseidx '@text:"memory guide"' NOCONTENT

echo
echo "=== slop ==="
run FT.SEARCH phraseidx '@text:"memory guide"' NOCONTENT SLOP 0
run FT.SEARCH phraseidx '@text:"memory guide"' NOCONTENT SLOP 1
run FT.SEARCH phraseidx '@text:"memory guide"' NOCONTENT SLOP 2
run FT.SEARCH phraseidx '@text:"memory guide"' NOCONTENT SLOP 3

echo
echo "=== inorder ==="
run FT.SEARCH phraseidx '@text:"memory guide"' NOCONTENT INORDER
run FT.SEARCH phraseidx '@text:"memory guide"' NOCONTENT SLOP 1 INORDER
run FT.SEARCH phraseidx '@text:"memory guide"' NOCONTENT SLOP 2 INORDER

echo
echo "=== reverse order ==="
run FT.SEARCH phraseidx '@text:"guide memory"' NOCONTENT SLOP 0
run FT.SEARCH phraseidx '@text:"guide memory"' NOCONTENT SLOP 1
run FT.SEARCH phraseidx '@text:"guide memory"' NOCONTENT SLOP 1 INORDER

echo
echo "=== grouped terms with slop/inorder ==="
run FT.SEARCH phraseidx '@text:(memory guide)' NOCONTENT SLOP 0
run FT.SEARCH phraseidx '@text:(memory guide)' NOCONTENT SLOP 1
run FT.SEARCH phraseidx '@text:(memory guide)' NOCONTENT SLOP 1 INORDER

echo
echo "=== validation ==="
run FT.SEARCH phraseidx '@text:"memory guide"' NOCONTENT SLOP
run FT.SEARCH phraseidx '@text:"memory guide"' NOCONTENT SLOP -1
run FT.SEARCH phraseidx '@text:"memory guide"' NOCONTENT SLOP nope
run FT.SEARCH phraseidx '@text:"memory guide"' NOCONTENT INORDER extra
run FT.SEARCH phraseidx '@text:"memory guide"' NOCONTENT SLOP 1 SLOP 2

echo
echo "=== drop ==="
run FT.DROPINDEX phraseidx
run FT._LIST
