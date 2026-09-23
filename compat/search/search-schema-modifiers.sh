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
for idx in modtxt modtag modnum modmix badw1 badw2 badw3 badw4 badw5 dupsort dupnoindex dupweight order1 order2 order3 order4 order5 order6; do
  run FT.DROPINDEX "$idx"
done

echo
echo "=== seed ==="
run JSON.SET doc:1 '$' '{"title":"memory guide","category":"docs","price":10,"hidden":"alpha"}'
run JSON.SET doc:2 '$' '{"title":"server design","category":"infra","price":20,"hidden":"beta"}'
run JSON.SET doc:3 '$' '{"title":"memory engine","category":"docs","price":15,"hidden":"gamma"}'

echo
echo "=== text modifiers ==="
run FT.CREATE modtxt ON JSON PREFIX 1 doc: SCHEMA '$.title' AS title TEXT WEIGHT 2.5 SORTABLE '$.hidden' AS hidden TEXT NOINDEX
run FT.INFO modtxt
run FT.SEARCH modtxt '@title:memory' NOCONTENT
run FT.SEARCH modtxt '@hidden:alpha' NOCONTENT
run FT.SEARCH modtxt '*' RETURN 2 '$.hidden' AS hidden
run FT.SEARCH modtxt '*' SORTBY title ASC NOCONTENT
run FT.SEARCH modtxt '*' SORTBY hidden ASC NOCONTENT

echo
echo "=== tag sortable ==="
run FT.CREATE modtag ON JSON PREFIX 1 doc: SCHEMA '$.category' AS category TAG SORTABLE
run FT.INFO modtag
run FT.SEARCH modtag '@category:{docs}' NOCONTENT
run FT.SEARCH modtag '*' SORTBY category ASC NOCONTENT

echo
echo "=== numeric sortable ==="
run FT.CREATE modnum ON JSON PREFIX 1 doc: SCHEMA '$.price' AS price NUMERIC SORTABLE
run FT.INFO modnum
run FT.SEARCH modnum '@price:[10 20]' NOCONTENT
run FT.SEARCH modnum '*' SORTBY price ASC NOCONTENT
run FT.SEARCH modnum '*' SORTBY price DESC NOCONTENT

echo
echo "=== mixed modifier ordering ==="
run FT.CREATE modmix ON JSON PREFIX 1 doc: SCHEMA '$.title' AS title TEXT SORTABLE WEIGHT 3 NOINDEX '$.price' AS price NUMERIC NOINDEX SORTABLE '$.category' AS category TAG NOINDEX SORTABLE
run FT.INFO modmix
run FT.SEARCH modmix '@title:memory' NOCONTENT
run FT.SEARCH modmix '@price:[10 20]' NOCONTENT
run FT.SEARCH modmix '@category:{docs}' NOCONTENT
run FT.SEARCH modmix '*' SORTBY title ASC NOCONTENT
run FT.SEARCH modmix '*' SORTBY price ASC NOCONTENT
run FT.SEARCH modmix '*' SORTBY category ASC NOCONTENT

echo
echo "=== weight validation ==="
run FT.CREATE badw1 ON JSON SCHEMA '$.title' AS title TEXT WEIGHT
run FT.INFO badw1
run FT.CREATE badw2 ON JSON SCHEMA '$.title' AS title TEXT WEIGHT nope
run FT.CREATE badw3 ON JSON SCHEMA '$.title' AS title TEXT WEIGHT -1
run FT.CREATE badw4 ON JSON SCHEMA '$.title' AS title TEXT WEIGHT 0
run FT.CREATE badw5 ON JSON SCHEMA '$.title' AS title TEXT WEIGHT 1.5
run FT.INFO badw5

echo
echo "=== duplicate modifiers ==="
run FT.CREATE dupsort ON JSON SCHEMA '$.title' AS title TEXT SORTABLE SORTABLE
run FT.INFO dupsort
run FT.CREATE dupnoindex ON JSON SCHEMA '$.title' AS title TEXT NOINDEX NOINDEX
run FT.INFO dupnoindex
run FT.CREATE dupweight ON JSON SCHEMA '$.title' AS title TEXT WEIGHT 2 WEIGHT 3
run FT.INFO dupweight

echo
echo "=== modifier ordering ==="
run FT.CREATE order1 ON JSON SCHEMA '$.title' AS title TEXT WEIGHT 3 SORTABLE NOINDEX
run FT.INFO order1
run FT.CREATE order2 ON JSON SCHEMA '$.title' AS title TEXT WEIGHT 3 NOINDEX SORTABLE
run FT.INFO order2
run FT.CREATE order3 ON JSON SCHEMA '$.title' AS title TEXT NOINDEX SORTABLE
run FT.INFO order3
run FT.CREATE order4 ON JSON SCHEMA '$.price' AS price NUMERIC NOINDEX SORTABLE
run FT.INFO order4
run FT.CREATE order5 ON JSON SCHEMA '$.category' AS category TAG NOINDEX SORTABLE
run FT.INFO order5
run FT.CREATE order6 ON JSON SCHEMA '$.title' AS title TEXT SORTABLE NOINDEX
run FT.INFO order6

echo
echo "=== drop ==="
for idx in modtxt modtag modnum modmix badw1 badw2 badw3 badw4 badw5 dupsort dupnoindex dupweight order1 order2 order3 order4 order5 order6; do
  run FT.DROPINDEX "$idx"
done
run FT._LIST
