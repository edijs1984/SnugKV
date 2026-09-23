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
for idx in score weight phrase phon fuzzy sortidx; do
  run FT.DROPINDEX "$idx"
done

echo
echo "=== seed ==="
run JSON.SET doc:1 '$' '{"title":"memory guide","body":"fast storage engine memory","rank":30}'
run JSON.SET doc:2 '$' '{"title":"memory memory","body":"server design","rank":10}'
run JSON.SET doc:3 '$' '{"title":"server design","body":"memory system guide","rank":20}'
run JSON.SET doc:4 '$' '{"title":"running system","body":"run memory","rank":40}'
run JSON.SET doc:5 '$' '{"title":"memori guide","body":"memory guide","rank":50}'
run JSON.SET doc:6 '$' '{"title":"Jon Smith","body":"memory","rank":60}'

echo
echo "=== create baseline ==="
run FT.CREATE score ON JSON PREFIX 1 doc: SCHEMA '$.title' AS title TEXT '$.body' AS body TEXT '$.rank' AS rank NUMERIC SORTABLE

echo
echo "=== WITHSCORES shape ==="
run FT.SEARCH score 'memory' WITHSCORES NOCONTENT
run FT.SEARCH score 'memory' WITHSCORES
run FT.SEARCH score '@title:memory' WITHSCORES NOCONTENT
run FT.SEARCH score 'memory guide' WITHSCORES NOCONTENT
run FT.SEARCH score '*' WITHSCORES NOCONTENT

echo
echo "=== repeated term / field distribution ==="
run FT.SEARCH score '@title:memory' WITHSCORES NOCONTENT
run FT.SEARCH score '@body:memory' WITHSCORES NOCONTENT
run FT.SEARCH score 'memory' WITHSCORES NOCONTENT
run FT.SEARCH score '@title:memory @body:memory' WITHSCORES NOCONTENT

echo
echo "=== field WEIGHT ==="
run FT.CREATE weight ON JSON PREFIX 1 doc: SCHEMA '$.title' AS title TEXT WEIGHT 5 '$.body' AS body TEXT WEIGHT 1
run FT.SEARCH weight 'memory' WITHSCORES NOCONTENT
run FT.SEARCH weight '@title:memory' WITHSCORES NOCONTENT
run FT.SEARCH weight '@body:memory' WITHSCORES NOCONTENT
run FT.SEARCH weight 'guide' WITHSCORES NOCONTENT

echo
echo "=== exact vs stem ==="
run FT.SEARCH score '@title:run' WITHSCORES NOCONTENT
run FT.SEARCH score '@title:running' WITHSCORES NOCONTENT
run FT.SEARCH score '@title:memory' WITHSCORES NOCONTENT

echo
echo "=== fuzzy ==="
run FT.CREATE fuzzy ON JSON PREFIX 1 doc: SCHEMA '$.title' AS title TEXT '$.body' AS body TEXT
run FT.SEARCH fuzzy '@title:%memory%' WITHSCORES NOCONTENT
run FT.SEARCH fuzzy '@title:%memori%' WITHSCORES NOCONTENT
run FT.SEARCH fuzzy '%memory%' WITHSCORES NOCONTENT

echo
echo "=== phonetic ==="
run FT.CREATE phon ON JSON PREFIX 1 doc: SCHEMA '$.title' AS title TEXT PHONETIC dm:en '$.body' AS body TEXT
run FT.SEARCH phon '@title:jon' WITHSCORES NOCONTENT
run FT.SEARCH phon '@title:john' WITHSCORES NOCONTENT
run FT.SEARCH phon 'jon' WITHSCORES NOCONTENT

echo
echo "=== phrase and proximity ==="
run FT.CREATE phrase ON JSON PREFIX 1 doc: SCHEMA '$.title' AS title TEXT '$.body' AS body TEXT
run FT.SEARCH phrase '@body:"memory guide"' WITHSCORES NOCONTENT
run FT.SEARCH phrase '"memory guide"' WITHSCORES NOCONTENT
run FT.SEARCH phrase '@body:(memory guide)' WITHSCORES NOCONTENT
run FT.SEARCH phrase '@body:(memory guide)' SLOP 0 WITHSCORES NOCONTENT
run FT.SEARCH phrase '@body:(memory guide)' SLOP 2 WITHSCORES NOCONTENT
run FT.SEARCH phrase '@body:(memory guide)' SLOP 2 INORDER WITHSCORES NOCONTENT

echo
echo "=== SORTBY precedence ==="
run FT.CREATE sortidx ON JSON PREFIX 1 doc: SCHEMA '$.title' AS title TEXT '$.body' AS body TEXT '$.rank' AS rank NUMERIC SORTABLE
run FT.SEARCH sortidx 'memory' WITHSCORES NOCONTENT SORTBY rank ASC
run FT.SEARCH sortidx 'memory' WITHSCORES NOCONTENT SORTBY rank DESC

echo
echo "=== LIMIT ==="
run FT.SEARCH score 'memory' WITHSCORES NOCONTENT LIMIT 0 2
run FT.SEARCH score 'memory' WITHSCORES NOCONTENT LIMIT 1 2

echo
echo "=== DIALECT ==="
run FT.SEARCH score 'memory guide' WITHSCORES NOCONTENT DIALECT 1
run FT.SEARCH score 'memory guide' WITHSCORES NOCONTENT DIALECT 2
run FT.SEARCH score '@title:memory' WITHSCORES NOCONTENT DIALECT 1
run FT.SEARCH score '@title:memory' WITHSCORES NOCONTENT DIALECT 2

echo
echo "=== parser / option boundaries ==="
run FT.SEARCH score 'memory' WITHSCORES WITHSCORES NOCONTENT
run FT.SEARCH score 'memory' NOCONTENT WITHSCORES
run FT.SEARCH score 'memory' WITHSCORES LIMIT 0 1 NOCONTENT
run FT.SEARCH score 'memory' WITHSCORES RETURN 1 '$.title'
run FT.SEARCH score 'memory' WITHSCORES SORTBY rank ASC LIMIT 0 3 NOCONTENT

echo
echo "=== drop ==="
for idx in score weight phrase phon fuzzy sortidx; do
  run FT.DROPINDEX "$idx"
done
run FT._LIST
