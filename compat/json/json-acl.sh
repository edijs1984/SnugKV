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
run ACL DELUSER jsoncat jsonread jsonwrite jsonkeys

echo
echo "=== category ==="
run ACL CAT json

echo
echo "=== +@json command grant ==="
run ACL SETUSER jsoncat on nopass -@all +@json allkeys
run ACL DRYRUN jsoncat JSON.GET doc '$'
run ACL DRYRUN jsoncat JSON.SET doc '$' '{}'
run ACL DRYRUN jsoncat GET doc
run ACL DRYRUN jsoncat SET doc x

echo
echo "=== +@read / +@write split ==="
run ACL SETUSER jsonread on nopass -@all +@read allkeys
run ACL DRYRUN jsonread JSON.GET doc '$'
run ACL DRYRUN jsonread JSON.SET doc '$' '{}'

run ACL SETUSER jsonwrite on nopass -@all +@write allkeys
run ACL DRYRUN jsonwrite JSON.SET doc '$' '{}'
run ACL DRYRUN jsonwrite JSON.GET doc '$'

echo
echo "=== key patterns: single-key JSON ==="
run ACL SETUSER jsonkeys on nopass -@all +@json resetkeys '~allowed:*'
run ACL DRYRUN jsonkeys JSON.GET allowed:doc '$'
run ACL DRYRUN jsonkeys JSON.GET denied:doc '$'
run ACL DRYRUN jsonkeys JSON.SET allowed:doc '$' '{}'
run ACL DRYRUN jsonkeys JSON.SET denied:doc '$' '{}'

echo
echo "=== key patterns: JSON.MGET ==="
run ACL DRYRUN jsonkeys JSON.MGET allowed:1 allowed:2 '$'
run ACL DRYRUN jsonkeys JSON.MGET allowed:1 denied:2 '$'

echo
echo "=== key patterns: JSON.MSET ==="
run ACL DRYRUN jsonkeys JSON.MSET allowed:1 '$' '{"v":1}' allowed:2 '$' '{"v":2}'
run ACL DRYRUN jsonkeys JSON.MSET allowed:1 '$' '{"v":1}' denied:2 '$' '{"v":2}'

echo
echo "=== cleanup ==="
run ACL DELUSER jsoncat jsonread jsonwrite jsonkeys
