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
run FT.DROPINDEX fuzzyidx

echo
echo "=== seed ==="
run JSON.SET fuzzy:1 '$' '{"text":"memory guide"}'
run JSON.SET fuzzy:2 '$' '{"text":"memori guide"}'
run JSON.SET fuzzy:3 '$' '{"text":"memry manual"}'
run JSON.SET fuzzy:4 '$' '{"text":"server design"}'
run JSON.SET fuzzy:5 '$' '{"text":"running system"}'
run JSON.SET fuzzy:6 '$' '{"text":"run system"}'
run FT.CREATE fuzzyidx ON JSON PREFIX 1 fuzzy: SCHEMA '$.text' AS text TEXT

echo
echo "=== exact baseline ==="
run FT.SEARCH fuzzyidx '@text:memory' NOCONTENT
run FT.SEARCH fuzzyidx '@text:memori' NOCONTENT
run FT.SEARCH fuzzyidx '@text:run' NOCONTENT
run FT.SEARCH fuzzyidx '@text:memri' NOCONTENT
run FT.SEARCH fuzzyidx '@text:memry' NOCONTENT

echo
echo "=== fuzzy tiers ==="
run FT.SEARCH fuzzyidx '@text:%memory%' NOCONTENT
run FT.SEARCH fuzzyidx '@text:%%memory%%' NOCONTENT
run FT.SEARCH fuzzyidx '@text:%%%memory%%%' NOCONTENT
run FT.SEARCH fuzzyidx '@text:%memori%' NOCONTENT
run FT.SEARCH fuzzyidx '@text:%memry%' NOCONTENT

echo
echo "=== fuzzy stemming interaction ==="
run FT.SEARCH fuzzyidx '@text:%run%' NOCONTENT
run FT.SEARCH fuzzyidx '@text:%running%' NOCONTENT

echo
echo "=== grouped fuzzy ==="
run FT.SEARCH fuzzyidx '@text:(%memory% guide)' NOCONTENT
run FT.SEARCH fuzzyidx '@text:(%memory% %guide%)' NOCONTENT

echo
echo "=== mixed prefix/fuzzy ==="
run FT.SEARCH fuzzyidx '@text:%mem*%' NOCONTENT
run FT.SEARCH fuzzyidx '@text:mem*' NOCONTENT

echo
echo "=== validation ==="
run FT.SEARCH fuzzyidx '@text:%' NOCONTENT
run FT.SEARCH fuzzyidx '@text:%%' NOCONTENT
run FT.SEARCH fuzzyidx '@text:%%%%memory%%%%' NOCONTENT
run FT.SEARCH fuzzyidx '@text:%memory%%' NOCONTENT
run FT.SEARCH fuzzyidx '@text:memory%' NOCONTENT
run FT.SEARCH fuzzyidx '@text:%memory' NOCONTENT

echo
echo "=== unknown field / non-text ==="
run FT.SEARCH fuzzyidx '@missing:%memory%' NOCONTENT

echo
echo "=== drop ==="
run FT.DROPINDEX fuzzyidx
run FT._LIST
