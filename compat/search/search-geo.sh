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
for idx in geo geosort bad1 bad2 bad3; do
  run FT.DROPINDEX "$idx"
done

echo
echo "=== seed string coordinates ==="
run JSON.SET place:1 '$' '{"name":"Riga","loc":"24.1052,56.9496","rank":30}'
run JSON.SET place:2 '$' '{"name":"Jurmala","loc":"23.7704,56.9680","rank":20}'
run JSON.SET place:3 '$' '{"name":"Sigulda","loc":"24.8595,57.1537","rank":40}'
run JSON.SET place:4 '$' '{"name":"Jelgava","loc":"23.7128,56.6511","rank":10}'
run JSON.SET place:5 '$' '{"name":"No location","rank":50}'
run JSON.SET place:6 '$' '{"name":"Bad location","loc":"not-a-coordinate","rank":60}'

echo
echo "=== create / info ==="
run FT.CREATE geo ON JSON PREFIX 1 place: SCHEMA '$.name' AS name TEXT '$.loc' AS loc GEO '$.rank' AS rank NUMERIC SORTABLE
run FT.INFO geo

echo
echo "=== basic radius ==="
run FT.SEARCH geo '@loc:[24.1052 56.9496 1 km]' NOCONTENT
run FT.SEARCH geo '@loc:[24.1052 56.9496 30 km]' NOCONTENT
run FT.SEARCH geo '@loc:[24.1052 56.9496 60 km]' NOCONTENT
run FT.SEARCH geo '@loc:[24.1052 56.9496 100 km]' NOCONTENT

echo
echo "=== units ==="
run FT.SEARCH geo '@loc:[24.1052 56.9496 1000 m]' NOCONTENT
run FT.SEARCH geo '@loc:[24.1052 56.9496 30 km]' NOCONTENT
run FT.SEARCH geo '@loc:[24.1052 56.9496 20 mi]' NOCONTENT
run FT.SEARCH geo '@loc:[24.1052 56.9496 100000 ft]' NOCONTENT

echo
echo "=== boolean composition ==="
run FT.SEARCH geo '@loc:[24.1052 56.9496 60 km] @rank:[0 25]' NOCONTENT
run FT.SEARCH geo '(@loc:[24.1052 56.9496 60 km]) | (@rank:[40 40])' NOCONTENT DIALECT 2
run FT.SEARCH geo '-@loc:[24.1052 56.9496 30 km]' NOCONTENT DIALECT 2

echo
echo "=== SORTBY / LIMIT ==="
run FT.SEARCH geo '@loc:[24.1052 56.9496 100 km]' NOCONTENT SORTBY rank ASC
run FT.SEARCH geo '@loc:[24.1052 56.9496 100 km]' NOCONTENT SORTBY rank DESC
run FT.SEARCH geo '@loc:[24.1052 56.9496 100 km]' NOCONTENT LIMIT 1 2

echo
echo "=== mutation visibility ==="
run JSON.SET place:2 '$.loc' '"26.0000,56.9680"'
run FT.SEARCH geo '@loc:[24.1052 56.9496 30 km]' NOCONTENT
run JSON.SET place:2 '$.loc' '"23.7704,56.9680"'
run FT.SEARCH geo '@loc:[24.1052 56.9496 30 km]' NOCONTENT
run JSON.DEL place:2 '$.loc'
run FT.SEARCH geo '@loc:[24.1052 56.9496 30 km]' NOCONTENT
run JSON.SET place:2 '$.loc' '"23.7704,56.9680"'

echo
echo "=== alternate JSON value shapes ==="
run JSON.SET alt:1 '$' '{"loc":[24.1052,56.9496]}'
run JSON.SET alt:2 '$' '{"loc":{"lon":24.1052,"lat":56.9496}}'
run FT.CREATE geosort ON JSON PREFIX 1 alt: SCHEMA '$.loc' AS loc GEO
run FT.SEARCH geosort '@loc:[24.1052 56.9496 1 km]' NOCONTENT

echo
echo "=== coordinate boundaries ==="
run JSON.SET edge:1 '$' '{"loc":"180,85"}'
run JSON.SET edge:2 '$' '{"loc":"-180,-85"}'
run JSON.SET edge:3 '$' '{"loc":"181,0"}'
run JSON.SET edge:4 '$' '{"loc":"0,86"}'
run FT.CREATE bad1 ON JSON PREFIX 1 edge: SCHEMA '$.loc' AS loc GEO
run FT.SEARCH bad1 '@loc:[180 85 1 km]' NOCONTENT
run FT.SEARCH bad1 '@loc:[-180 -85 1 km]' NOCONTENT

echo
echo "=== parser / invalid query boundaries ==="
run FT.SEARCH geo '@loc:[24.1052 56.9496]' NOCONTENT
run FT.SEARCH geo '@loc:[24.1052 56.9496 30]' NOCONTENT
run FT.SEARCH geo '@loc:[x 56.9496 30 km]' NOCONTENT
run FT.SEARCH geo '@loc:[24.1052 y 30 km]' NOCONTENT
run FT.SEARCH geo '@loc:[24.1052 56.9496 x km]' NOCONTENT
run FT.SEARCH geo '@loc:[24.1052 56.9496 -1 km]' NOCONTENT
run FT.SEARCH geo '@loc:[24.1052 56.9496 30 bad]' NOCONTENT
run FT.SEARCH geo '@missing:[24.1052 56.9496 30 km]' NOCONTENT

echo
echo "=== schema boundaries ==="
run FT.CREATE bad2 ON JSON PREFIX 1 place: SCHEMA '$.loc' AS loc GEO SORTABLE
run FT.CREATE bad3 ON JSON PREFIX 1 place: SCHEMA '$.loc' AS loc GEO NOINDEX
run FT.INFO bad2
run FT.INFO bad3

echo
echo "=== dialect comparison ==="
run FT.SEARCH geo '@loc:[24.1052 56.9496 30 km]' NOCONTENT DIALECT 1
run FT.SEARCH geo '@loc:[24.1052 56.9496 30 km]' NOCONTENT DIALECT 2

echo
echo "=== cleanup ==="
for idx in geo geosort bad1 bad2 bad3; do
  run FT.DROPINDEX "$idx"
done
run FLUSHDB
