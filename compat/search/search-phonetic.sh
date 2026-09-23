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
for idx in ph phnostem plain dup bad1 bad2 bad3 bad4; do
  run FT.DROPINDEX "$idx"
done

echo
echo "=== seed ==="
run JSON.SET doc:1 '$' '{"name":"Jon Smith","body":"memory guide"}'
run JSON.SET doc:2 '$' '{"name":"John Smyth","body":"server design"}'
run JSON.SET doc:3 '$' '{"name":"Jan Schmidt","body":"memory system"}'
run JSON.SET doc:4 '$' '{"name":"Jane Smith","body":"storage engine"}'
run JSON.SET doc:5 '$' '{"name":"running","body":"execution manual"}'
run JSON.SET doc:6 '$' '{"name":"run","body":"runner system"}'

echo
echo "=== create syntax ==="
run FT.CREATE ph ON JSON PREFIX 1 doc: SCHEMA '$.name' AS name TEXT PHONETIC dm:en '$.body' AS body TEXT
run FT.INFO ph

echo
echo "=== basic phonetic matching ==="
run FT.SEARCH ph '@name:jon' NOCONTENT
run FT.SEARCH ph '@name:john' NOCONTENT
run FT.SEARCH ph '@name:smith' NOCONTENT
run FT.SEARCH ph '@name:smyth' NOCONTENT
run FT.SEARCH ph '@name:schmidt' NOCONTENT
run FT.SEARCH ph '@name:jan' NOCONTENT
run FT.SEARCH ph '@name:jane' NOCONTENT

echo
echo "=== comparison with plain text ==="
run FT.CREATE plain ON JSON PREFIX 1 doc: SCHEMA '$.name' AS name TEXT '$.body' AS body TEXT
run FT.SEARCH plain '@name:jon' NOCONTENT
run FT.SEARCH plain '@name:john' NOCONTENT
run FT.SEARCH plain '@name:smith' NOCONTENT
run FT.SEARCH plain '@name:smyth' NOCONTENT

echo
echo "=== unqualified behavior ==="
run FT.SEARCH ph 'jon' NOCONTENT
run FT.SEARCH ph 'john' NOCONTENT
run FT.SEARCH ph 'smith' NOCONTENT
run FT.SEARCH ph 'memory' NOCONTENT
run FT.SEARCH ph 'jon memory' NOCONTENT

echo
echo "=== prefix fuzzy phrase interaction ==="
run FT.SEARCH ph '@name:jo*' NOCONTENT
run FT.SEARCH ph '@name:%jon%' NOCONTENT
run FT.SEARCH ph '@name:"jon smith"' NOCONTENT
run FT.SEARCH ph '@name:(jon smith)' NOCONTENT

echo
echo "=== stemming and nostem ==="
run FT.SEARCH ph '@name:run' NOCONTENT
run FT.SEARCH ph '@name:running' NOCONTENT
run FT.CREATE phnostem ON JSON PREFIX 1 doc: SCHEMA '$.name' AS name TEXT NOSTEM PHONETIC dm:en
run FT.INFO phnostem
run FT.SEARCH phnostem '@name:run' NOCONTENT
run FT.SEARCH phnostem '@name:running' NOCONTENT

echo
echo "=== modifier ordering ==="
run FT.CREATE dup ON JSON PREFIX 1 doc: SCHEMA '$.name' AS name TEXT PHONETIC dm:en SORTABLE
run FT.INFO dup
run FT.DROPINDEX dup
run FT.CREATE dup ON JSON PREFIX 1 doc: SCHEMA '$.name' AS name TEXT SORTABLE PHONETIC dm:en
run FT.INFO dup
run FT.DROPINDEX dup
run FT.CREATE dup ON JSON PREFIX 1 doc: SCHEMA '$.name' AS name TEXT PHONETIC dm:en PHONETIC dm:en
run FT.INFO dup

echo
echo "=== invalid matcher / arity ==="
run FT.CREATE bad1 ON JSON PREFIX 1 doc: SCHEMA '$.name' AS name TEXT PHONETIC
run FT.CREATE bad2 ON JSON PREFIX 1 doc: SCHEMA '$.name' AS name TEXT PHONETIC bad
run FT.CREATE bad3 ON JSON PREFIX 1 doc: SCHEMA '$.name' AS name TEXT PHONETIC dm:xx
run FT.CREATE bad4 ON JSON PREFIX 1 doc: SCHEMA '$.name' AS name TEXT PHONETIC dm:en extra

echo
echo "=== field types ==="
run FT.DROPINDEX bad1
run FT.DROPINDEX bad2
run FT.DROPINDEX bad3
run FT.DROPINDEX bad4
run FT.CREATE bad1 ON JSON PREFIX 1 doc: SCHEMA '$.body' AS body TAG PHONETIC dm:en
run FT.CREATE bad2 ON JSON PREFIX 1 doc: SCHEMA '$.body' AS body NUMERIC PHONETIC dm:en

echo
echo "=== dialect comparison ==="
run FT.SEARCH ph '@name:jon' NOCONTENT DIALECT 1
run FT.SEARCH ph '@name:jon' NOCONTENT DIALECT 2
run FT.SEARCH ph 'jon' NOCONTENT DIALECT 1
run FT.SEARCH ph 'jon' NOCONTENT DIALECT 2

echo
echo "=== drop ==="
for idx in ph phnostem plain dup bad1 bad2 bad3 bad4; do
  run FT.DROPINDEX "$idx"
done
run FT._LIST
