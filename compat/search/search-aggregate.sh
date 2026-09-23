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
run FT.DROPINDEX agg

echo
echo "=== seed ==="
run JSON.SET doc:1 '$' '{"title":"memory guide","category":"db","price":10,"rank":30,"active":true}'
run JSON.SET doc:2 '$' '{"title":"memory engine","category":"db","price":20,"rank":10,"active":true}'
run JSON.SET doc:3 '$' '{"title":"server guide","category":"infra","price":15,"rank":20,"active":false}'
run JSON.SET doc:4 '$' '{"title":"memory server","category":"infra","price":25,"rank":40,"active":true}'
run JSON.SET doc:5 '$' '{"title":"cache guide","category":"db","price":30,"rank":50,"active":false}'

run FT.CREATE agg ON JSON PREFIX 1 doc: SCHEMA   '$.title' AS title TEXT   '$.category' AS category TAG SORTABLE   '$.price' AS price NUMERIC SORTABLE   '$.rank' AS rank NUMERIC SORTABLE   '$.active' AS active TAG

echo
echo "=== baseline ==="
run FT.AGGREGATE agg '*'
run FT.AGGREGATE agg 'memory'
run FT.AGGREGATE agg '@category:{db}'
run FT.AGGREGATE agg '@price:[10 20]'

echo
echo "=== LOAD ==="
run FT.AGGREGATE agg '*' LOAD 1 '@title'
run FT.AGGREGATE agg '*' LOAD 2 '@title' '@price'
run FT.AGGREGATE agg '*' LOAD 4 '@title' AS name '@price' AS cost
run FT.AGGREGATE agg '*' LOAD 1 '$.title'
run FT.AGGREGATE agg '*' LOAD 1 '@missing'

echo
echo "=== FILTER ==="
run FT.AGGREGATE agg '*' LOAD 2 '@title' '@price' FILTER '@price > 15'
run FT.AGGREGATE agg '*' LOAD 2 '@title' '@category' FILTER '@category == "db"'
run FT.AGGREGATE agg '*' LOAD 2 '@title' '@active' FILTER '@active == "true"'
run FT.AGGREGATE agg '*' FILTER '@price >= 15 && @price <= 25'
run FT.AGGREGATE agg '*' FILTER '@missing > 1'

echo
echo "=== GROUPBY / REDUCE COUNT ==="
run FT.AGGREGATE agg '*' GROUPBY 1 '@category' REDUCE COUNT 0 AS count
run FT.AGGREGATE agg '*' GROUPBY 1 '@active' REDUCE COUNT 0 AS count
run FT.AGGREGATE agg '@title:memory' GROUPBY 1 '@category' REDUCE COUNT 0 AS count
run FT.AGGREGATE agg '*' GROUPBY 0 REDUCE COUNT 0 AS count

echo
echo "=== REDUCE SUM / MIN / MAX / AVG ==="
run FT.AGGREGATE agg '*' GROUPBY 1 '@category' REDUCE SUM 1 '@price' AS total
run FT.AGGREGATE agg '*' GROUPBY 1 '@category' REDUCE MIN 1 '@price' AS min
run FT.AGGREGATE agg '*' GROUPBY 1 '@category' REDUCE MAX 1 '@price' AS max
run FT.AGGREGATE agg '*' GROUPBY 1 '@category' REDUCE AVG 1 '@price' AS avg

echo
echo "=== SORTBY ==="
run FT.AGGREGATE agg '*' LOAD 2 '@title' '@rank' SORTBY 2 '@rank' ASC
run FT.AGGREGATE agg '*' LOAD 2 '@title' '@rank' SORTBY 2 '@rank' DESC
run FT.AGGREGATE agg '*' GROUPBY 1 '@category' REDUCE COUNT 0 AS count SORTBY 2 '@count' DESC
run FT.AGGREGATE agg '*' LOAD 2 '@title' '@rank' SORTBY 4 '@rank' ASC '@title' DESC

echo
echo "=== LIMIT ==="
run FT.AGGREGATE agg '*' LOAD 1 '@title' LIMIT 0 2
run FT.AGGREGATE agg '*' LOAD 1 '@title' LIMIT 2 2
run FT.AGGREGATE agg '*' LOAD 2 '@title' '@rank' SORTBY 2 '@rank' ASC LIMIT 1 2

echo
echo "=== pipeline ordering ==="
run FT.AGGREGATE agg '*' LOAD 2 '@title' '@price' FILTER '@price > 10' SORTBY 2 '@price' DESC LIMIT 0 2
run FT.AGGREGATE agg '*' GROUPBY 1 '@category' REDUCE COUNT 0 AS count FILTER '@count > 1'
run FT.AGGREGATE agg '*' FILTER '@price > 10' LOAD 1 '@title'

echo
echo "=== invalid grammar ==="
run FT.AGGREGATE
run FT.AGGREGATE agg
run FT.AGGREGATE missing '*'
run FT.AGGREGATE agg '*' LOAD
run FT.AGGREGATE agg '*' LOAD x
run FT.AGGREGATE agg '*' GROUPBY
run FT.AGGREGATE agg '*' GROUPBY x
run FT.AGGREGATE agg '*' GROUPBY 1
run FT.AGGREGATE agg '*' GROUPBY 1 '@category' REDUCE
run FT.AGGREGATE agg '*' GROUPBY 1 '@category' REDUCE COUNT
run FT.AGGREGATE agg '*' GROUPBY 1 '@category' REDUCE COUNT 1
run FT.AGGREGATE agg '*' SORTBY
run FT.AGGREGATE agg '*' SORTBY 1 '@rank'
run FT.AGGREGATE agg '*' SORTBY 2 '@rank' SIDEWAYS
run FT.AGGREGATE agg '*' LIMIT
run FT.AGGREGATE agg '*' LIMIT -1 2
run FT.AGGREGATE agg '*' LIMIT 0 -1
run FT.AGGREGATE agg '*' FILTER
run FT.AGGREGATE agg '*' FILTER 'garbage'

echo
echo "=== dialect ==="
run FT.AGGREGATE agg 'memory' LOAD 1 '@title' DIALECT 1
run FT.AGGREGATE agg 'memory' LOAD 1 '@title' DIALECT 2

echo
echo "=== cleanup ==="
run FT.DROPINDEX agg
run FLUSHDB
