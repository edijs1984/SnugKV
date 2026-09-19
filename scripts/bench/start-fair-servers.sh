#!/usr/bin/env bash
set -euo pipefail

NETWORK="${NETWORK:-snug-bench-net}"
REDIS_NAME="${REDIS_NAME:-snug-bench-redis}"
SNUG_RAW_NAME="${SNUG_RAW_NAME:-snug-bench-snugkv-raw}"
SNUG_OPT_NAME="${SNUG_OPT_NAME:-snug-bench-snugkv-opt}"

REDIS_PORT="${REDIS_PORT:-6390}"
SNUG_RAW_PORT="${SNUG_RAW_PORT:-6382}"
SNUG_OPT_PORT="${SNUG_OPT_PORT:-6383}"
PPROF_RAW_PORT="${PPROF_RAW_PORT:-6060}"
PPROF_OPT_PORT="${PPROF_OPT_PORT:-6061}"

IMAGE="${SNUG_IMAGE:-snugkv-bench:local}"

docker network create "$NETWORK" >/dev/null 2>&1 || true

docker rm -f "$REDIS_NAME" "$SNUG_RAW_NAME" "$SNUG_OPT_NAME" snug-bench-snugkv >/dev/null 2>&1 || true

echo "Building SnugKV benchmark image..."
docker build -t "$IMAGE" .

echo "Starting Redis 8.2 benchmark container..."
docker run -d --rm \
  --name "$REDIS_NAME" \
  --network "$NETWORK" \
  -p "127.0.0.1:${REDIS_PORT}:6379" \
  redis:8.2 \
  redis-server \
    --save "" \
    --appendonly no \
    --protected-mode no \
  >/dev/null

echo "Starting SnugKV raw benchmark container..."
docker run -d --rm \
  --name "$SNUG_RAW_NAME" \
  --network "$NETWORK" \
  -p "127.0.0.1:${SNUG_RAW_PORT}:6380" \
  -p "127.0.0.1:${PPROF_RAW_PORT}:6060" \
  "$IMAGE" \
  -listen 0.0.0.0:6380 \
  -admin-listen "" \
  -pprof-listen 0.0.0.0:6060 \
  -encoding=false \
  -compression=false \
  -json-shape=false \
  >/dev/null

echo "Starting SnugKV optimized benchmark container..."
docker run -d --rm \
  --name "$SNUG_OPT_NAME" \
  --network "$NETWORK" \
  -p "127.0.0.1:${SNUG_OPT_PORT}:6380" \
  -p "127.0.0.1:${PPROF_OPT_PORT}:6060" \
  "$IMAGE" \
  -listen 0.0.0.0:6380 \
  -admin-listen "" \
  -pprof-listen 0.0.0.0:6060 \
  -encoding=true \
  -compression=true \
  -json-shape=true \
  >/dev/null

for port in "$REDIS_PORT" "$SNUG_RAW_PORT" "$SNUG_OPT_PORT"; do
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
echo "  Redis:         127.0.0.1:$REDIS_PORT     container=$REDIS_NAME"
echo "  SnugKV raw:    127.0.0.1:$SNUG_RAW_PORT  container=$SNUG_RAW_NAME"
echo "  SnugKV opt:    127.0.0.1:$SNUG_OPT_PORT  container=$SNUG_OPT_NAME"
echo "  pprof raw:     http://127.0.0.1:$PPROF_RAW_PORT/debug/pprof/"
echo "  pprof opt:     http://127.0.0.1:$PPROF_OPT_PORT/debug/pprof/"
echo
echo "Persistence is disabled on all benchmark servers."
echo "Raw mode: encoding=false compression=false json_shape=false"
echo "Opt mode: encoding=true compression=true json_shape=true"
