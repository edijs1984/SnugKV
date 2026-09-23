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
for idx in wc wcnostem; do
  run FT.DROPINDEX "$idx"
done

echo
echo "=== seed ==="
run JSON.SET doc:1 '$' '{"text":"memory guide"}'
run JSON.SET doc:2 '$' '{"text":"in-memory engine"}'
run JSON.SET doc:3 '$' '{"text":"memories manual"}'
run JSON.SET doc:4 '$' '{"text":"remember memory"}'
run JSON.SET doc:5 '$' '{"text":"primary storage"}'
run JSON.SET doc:6 '$' '{"text":"memXory token"}'
run JSON.SET doc:7 '$' '{"text":"running runner run"}'

run FT.CREATE wc ON JSON PREFIX 1 doc: SCHEMA '$.text' AS text TEXT
run FT.CREATE wcnostem ON JSON PREFIX 1 doc: SCHEMA '$.text' AS text TEXT NOSTEM

echo
echo "=== fielded wildcard forms ==="
run FT.SEARCH wc '@text:mem*' NOCONTENT
run FT.SEARCH wc '@text:*ory' NOCONTENT
run FT.SEARCH wc '@text:*mor*' NOCONTENT
run FT.SEARCH wc '@text:m*mory' NOCONTENT
run FT.SEARCH wc '@text:me*or*' NOCONTENT
run FT.SEARCH wc '@text:**memory**' NOCONTENT
run FT.SEARCH wc '@text:*' NOCONTENT

echo
echo "=== unqualified wildcard forms ==="
run FT.SEARCH wc 'mem*' NOCONTENT
run FT.SEARCH wc '*ory' NOCONTENT
run FT.SEARCH wc '*mor*' NOCONTENT
run FT.SEARCH wc 'm*mory' NOCONTENT
run FT.SEARCH wc 'me*or*' NOCONTENT
run FT.SEARCH wc '**memory**' NOCONTENT

echo
echo "=== grouped wildcard forms ==="
run FT.SEARCH wc '@text:(mem* guide)' NOCONTENT
run FT.SEARCH wc '@text:(*ory guide)' NOCONTENT
run FT.SEARCH wc '@text:(*mor* guide)' NOCONTENT
run FT.SEARCH wc '@text:(mem* *ide)' NOCONTENT

echo
echo "=== quoted wildcard forms ==="
run FT.SEARCH wc '@text:"mem* guide"' NOCONTENT
run FT.SEARCH wc '"mem* guide"' NOCONTENT
run FT.SEARCH wc '@text:"*ory token"' NOCONTENT
run FT.SEARCH wc '"*ory token"' NOCONTENT

echo
echo "=== stemming interaction ==="
run FT.SEARCH wc '@text:run*' NOCONTENT
run FT.SEARCH wc '@text:*run*' NOCONTENT
run FT.SEARCH wcnostem '@text:run*' NOCONTENT
run FT.SEARCH wcnostem '@text:*run*' NOCONTENT

echo
echo "=== fuzzy interaction ==="
run FT.SEARCH wc '@text:%mem*%' NOCONTENT
run FT.SEARCH wc '%mem*%' NOCONTENT
run FT.SEARCH wc '@text:*%memory%*' NOCONTENT
run FT.SEARCH wc '*%memory%*' NOCONTENT

echo
echo "=== escaping ==="
run FT.SEARCH wc '@text:mem\*' NOCONTENT
run FT.SEARCH wc 'mem\*' NOCONTENT
run FT.SEARCH wc '@text:\*ory' NOCONTENT
run FT.SEARCH wc '\*ory' NOCONTENT
run FT.SEARCH wc '@text:mem\\*' NOCONTENT
run FT.SEARCH wc 'mem\\*' NOCONTENT

echo
echo "=== dialect comparison ==="
for q in '@text:*ory' '@text:*mor*' '@text:m*mory' '*ory' '*mor*' 'm*mory'; do
  run FT.SEARCH wc "$q" NOCONTENT DIALECT 1
  run FT.SEARCH wc "$q" NOCONTENT DIALECT 2
done

echo
echo "=== parser boundaries ==="
run FT.SEARCH wc '@text:**' NOCONTENT
run FT.SEARCH wc '**' NOCONTENT
run FT.SEARCH wc '@text:***' NOCONTENT
run FT.SEARCH wc '***' NOCONTENT
run FT.SEARCH wc '@text:m**y' NOCONTENT
run FT.SEARCH wc 'm**y' NOCONTENT
run FT.SEARCH wc '@text:*m*e*m*' NOCONTENT
run FT.SEARCH wc '*m*e*m*' NOCONTENT

echo
echo "=== drop ==="
run FT.DROPINDEX wc
run FT.DROPINDEX wcnostem
run FT._LIST
