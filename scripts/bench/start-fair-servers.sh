#!/usr/bin/env bash
set -euo pipefail

NETWORK="${NETWORK:-snug-bench-net}"
REDIS_NAME="${REDIS_NAME:-snug-bench-redis}"
SNUG_NAME="${SNUG_NAME:-snug-bench-snugkv}"
REDIS_PORT="${REDIS_PORT:-6390}"
SNUG_PORT="${SNUG_PORT:-6382}"
PPROF_PORT="${PPROF_PORT:-6060}"
IMAGE="${SNUG_IMAGE:-snugkv-bench:local}"

docker network create "$NETWORK" >/dev/null 2>&1 || true

docker rm -f "$REDIS_NAME" "$SNUG_NAME" >/dev/null 2>&1 || true

echo "Building SnugKV benchmark image..."
docker build -t "$IMAGE" .

echo "Starting Redis 8.2 benchmark container..."
docker run -d --rm   --name "$REDIS_NAME"   --network "$NETWORK"   -p "127.0.0.1:${REDIS_PORT}:6379"   redis:8.2   redis-server     --save ""     --appendonly no     --protected-mode no   >/dev/null

echo "Starting SnugKV benchmark container..."
docker run -d --rm   --name "$SNUG_NAME"   --network "$NETWORK"   -p "127.0.0.1:${SNUG_PORT}:6380"   -p "127.0.0.1:${PPROF_PORT}:6060"   "$IMAGE"   -listen 0.0.0.0:6380   -admin-listen ""   -pprof-listen 0.0.0.0:6060   >/dev/null

for port in "$REDIS_PORT" "$SNUG_PORT"; do
  for _ in $(seq 1 100); do
    if redis-cli -p "$port" PING >/dev/null 2>&1; then
      break
    fi
    sleep 0.05
  done
  redis-cli -p "$port" PING
done

echo
echo "Fair benchmark endpoints:"
echo "  Redis:  127.0.0.1:$REDIS_PORT  container=$REDIS_NAME"
echo "  SnugKV: 127.0.0.1:$SNUG_PORT   container=$SNUG_NAME"
echo "  pprof:  http://127.0.0.1:$PPROF_PORT/debug/pprof/"
echo
echo "Persistence is disabled on both benchmark servers."
