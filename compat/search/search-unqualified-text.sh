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
for idx in unq nostem notext stop0 stopcustom; do
  run FT.DROPINDEX "$idx"
done

echo
echo "=== seed ==="
run JSON.SET doc:1 '$' '{"title":"memory guide","body":"fast storage engine","tag":"docs","price":10}'
run JSON.SET doc:2 '$' '{"title":"server design","body":"memory system guide","tag":"infra","price":20}'
run JSON.SET doc:3 '$' '{"title":"running system","body":"execution manual","tag":"docs","price":30}'
run JSON.SET doc:4 '$' '{"title":"memry handbook","body":"search memory","tag":"misc","price":40}'
run JSON.SET doc:5 '$' '{"title":"the memory guide","body":"the fast engine","tag":"docs","price":50}'

run FT.CREATE unq ON JSON PREFIX 1 doc: SCHEMA '$.title' AS title TEXT '$.body' AS body TEXT '$.tag' AS tag TAG '$.price' AS price NUMERIC

echo
echo "=== single unqualified terms ==="
run FT.SEARCH unq memory NOCONTENT
run FT.SEARCH unq guide NOCONTENT
run FT.SEARCH unq system NOCONTENT
run FT.SEARCH unq running NOCONTENT

echo
echo "=== multiple unqualified terms ==="
run FT.SEARCH unq 'memory guide' NOCONTENT
run FT.SEARCH unq 'memory engine' NOCONTENT
run FT.SEARCH unq 'memory system guide' NOCONTENT

echo
echo "=== fielded plus unqualified ==="
run FT.SEARCH unq '@title:memory guide' NOCONTENT
run FT.SEARCH unq 'memory @body:guide' NOCONTENT
run FT.SEARCH unq '@tag:{docs} memory' NOCONTENT
run FT.SEARCH unq 'memory @price:[10 30]' NOCONTENT

echo
echo "=== prefix and fuzzy ==="
run FT.SEARCH unq 'mem*' NOCONTENT
run FT.SEARCH unq '%memory%' NOCONTENT
run FT.SEARCH unq '%memry%' NOCONTENT
run FT.SEARCH unq 'memory mem*' NOCONTENT

echo
echo "=== quoted phrase ==="
run FT.SEARCH unq '"memory guide"' NOCONTENT
run FT.SEARCH unq '"guide memory"' NOCONTENT
run FT.SEARCH unq '"memory system"' NOCONTENT

echo
echo "=== stopwords ==="
run FT.SEARCH unq the NOCONTENT
run FT.SEARCH unq 'the memory' NOCONTENT
run FT.SEARCH unq '"the memory guide"' NOCONTENT

run FT.CREATE stop0 ON JSON PREFIX 1 doc: STOPWORDS 0 SCHEMA '$.title' AS title TEXT '$.body' AS body TEXT
run FT.SEARCH stop0 the NOCONTENT
run FT.SEARCH stop0 'the memory' NOCONTENT
run FT.SEARCH stop0 '"the memory guide"' NOCONTENT

run FT.CREATE stopcustom ON JSON PREFIX 1 doc: STOPWORDS 1 memory SCHEMA '$.title' AS title TEXT '$.body' AS body TEXT
run FT.SEARCH stopcustom memory NOCONTENT
run FT.SEARCH stopcustom guide NOCONTENT
run FT.SEARCH stopcustom 'memory guide' NOCONTENT

echo
echo "=== stemming and NOSTEM ==="
run FT.SEARCH unq run NOCONTENT
run FT.SEARCH unq running NOCONTENT
run FT.CREATE nostem ON JSON PREFIX 1 doc: SCHEMA '$.title' AS title TEXT NOSTEM '$.body' AS body TEXT NOSTEM
run FT.SEARCH nostem run NOCONTENT
run FT.SEARCH nostem running NOCONTENT

echo
echo "=== no text fields ==="
run FT.CREATE notext ON JSON PREFIX 1 doc: SCHEMA '$.tag' AS tag TAG '$.price' AS price NUMERIC
run FT.SEARCH notext memory NOCONTENT
run FT.SEARCH notext 'mem*' NOCONTENT
run FT.SEARCH notext '%memory%' NOCONTENT
run FT.SEARCH notext '"memory guide"' NOCONTENT

echo
echo "=== dialect comparison ==="
run FT.SEARCH unq 'memory guide' NOCONTENT DIALECT 1
run FT.SEARCH unq 'memory guide' NOCONTENT DIALECT 2
run FT.SEARCH unq '@title:memory guide' NOCONTENT DIALECT 1
run FT.SEARCH unq '@title:memory guide' NOCONTENT DIALECT 2
run FT.SEARCH unq '"memory guide"' NOCONTENT DIALECT 1
run FT.SEARCH unq '"memory guide"' NOCONTENT DIALECT 2

echo
echo "=== syntax / boundaries ==="
run FT.SEARCH unq '*' NOCONTENT
run FT.SEARCH unq '' NOCONTENT
run FT.SEARCH unq '%' NOCONTENT
run FT.SEARCH unq '%%' NOCONTENT
run FT.SEARCH unq '%%%%memory%%%%' NOCONTENT
run FT.SEARCH unq '*mem' NOCONTENT
run FT.SEARCH unq 'me*m' NOCONTENT

echo
echo "=== drop ==="
for idx in unq nostem notext stop0 stopcustom; do
  run FT.DROPINDEX "$idx"
done
run FT._LIST
