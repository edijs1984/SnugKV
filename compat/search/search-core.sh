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
run FT.DROPINDEX products

echo
echo "=== seed ==="
run JSON.SET product:1 '$' '{"category":"books","price":10,"title":"A"}'
run JSON.SET product:2 '$' '{"category":"games","price":20,"title":"B"}'
run JSON.SET product:3 '$' '{"category":"books","price":30,"title":"C"}'
run JSON.SET ignored:1 '$' '{"category":"books","price":15,"title":"ignored"}'

echo
echo "=== create ==="
run FT.CREATE products ON JSON PREFIX 1 product: SCHEMA   '$.category' AS category TAG   '$.price' AS price NUMERIC

echo
echo "=== list ==="
run FT._LIST

echo
echo "=== match all ==="
run FT.SEARCH products '*' NOCONTENT

echo
echo "=== tag ==="
run FT.SEARCH products '@category:{books}' NOCONTENT

echo
echo "=== numeric ==="
run FT.SEARCH products '@price:[10 20]' NOCONTENT

echo
echo "=== numeric infinities ==="
run FT.SEARCH products '@price:[-inf 20]' NOCONTENT
run FT.SEARCH products '@price:[20 +inf]' NOCONTENT

echo
echo "=== implicit AND ==="
run FT.SEARCH products '@category:{books} @price:[20 40]' NOCONTENT

echo
echo "=== limit ==="
run FT.SEARCH products '*' NOCONTENT LIMIT 1 1

echo
echo "=== mutation visibility ==="
run JSON.SET product:1 '$.category' '"games"'
run FT.SEARCH products '@category:{books}' NOCONTENT
run FT.SEARCH products '@category:{games}' NOCONTENT

echo
echo "=== deletion visibility ==="
run JSON.DEL product:2 '$'
run FT.SEARCH products '*' NOCONTENT

echo
echo "=== expiry visibility ==="
run PEXPIRE product:3 1
sleep 0.02
run FT.SEARCH products '*' NOCONTENT

echo
echo "=== default content shape ==="
run FT.SEARCH products '@category:{games}' LIMIT 0 1

echo
echo "=== return projections ==="
run FT.SEARCH products '@category:{games}' RETURN 1 '$.title'
run FT.SEARCH products '@category:{games}' RETURN 3 '$.title' AS title
run FT.SEARCH products '@category:{games}' RETURN 4 '$.title' '$.price' AS cost
run FT.SEARCH products '@category:{games}' RETURN 1 '$.missing'
run FT.SEARCH products '*' RETURN 0 LIMIT 0 1

echo
echo "=== drop ==="
run FT.DROPINDEX products
run FT._LIST
