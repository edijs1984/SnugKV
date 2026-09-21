#!/usr/bin/env bash
set -euo pipefail

SERVER="${1:?usage: start-one-server.sh redis|snug-opt|snug-raw}"

REDIS_NAME="${REDIS_NAME:-snug-bench-redis}"
SNUG_RAW_NAME="${SNUG_RAW_NAME:-snug-bench-snugkv-raw}"
SNUG_OPT_NAME="${SNUG_OPT_NAME:-snug-bench-snugkv-opt}"

REDIS_PORT="${REDIS_PORT:-6390}"
SNUG_RAW_PORT="${SNUG_RAW_PORT:-6382}"
SNUG_OPT_PORT="${SNUG_OPT_PORT:-6383}"
PPROF_RAW_PORT="${PPROF_RAW_PORT:-6060}"
PPROF_OPT_PORT="${PPROF_OPT_PORT:-6061}"

IMAGE="${SNUG_IMAGE:-snugkv-bench:local}"
BUILD_IMAGE="${BUILD_IMAGE:-0}"

docker rm -f "$REDIS_NAME" "$SNUG_RAW_NAME" "$SNUG_OPT_NAME" snug-bench-snugkv >/dev/null 2>&1 || true

if [[ "$SERVER" != "redis" && "$BUILD_IMAGE" == "1" ]]; then
  docker build -t "$IMAGE" .
fi

case "$SERVER" in
  redis)
    docker run -d --rm \
      --name "$REDIS_NAME" \
      -p "127.0.0.1:${REDIS_PORT}:6379" \
      redis:8.2 \
      redis-server --save "" --appendonly no --protected-mode no >/dev/null
    port="$REDIS_PORT"
    ;;
  snug-raw)
    docker run -d --rm \
      --name "$SNUG_RAW_NAME" \
      -p "127.0.0.1:${SNUG_RAW_PORT}:6380" \
      -p "127.0.0.1:${PPROF_RAW_PORT}:6060" \
      "$IMAGE" \
      -listen 0.0.0.0:6380 \
      -admin-listen "" \
      -pprof-listen 0.0.0.0:6060 \
      -encoding=false -compression=false -json-shape=false >/dev/null
    port="$SNUG_RAW_PORT"
    ;;
  snug-opt)
    docker run -d --rm \
      --name "$SNUG_OPT_NAME" \
      -p "127.0.0.1:${SNUG_OPT_PORT}:6380" \
      -p "127.0.0.1:${PPROF_OPT_PORT}:6060" \
      "$IMAGE" \
      -listen 0.0.0.0:6380 \
      -admin-listen "" \
      -pprof-listen 0.0.0.0:6060 \
      -encoding=true -compression=true -json-shape=true >/dev/null
    port="$SNUG_OPT_PORT"
    ;;
  *)
    echo "unknown server: $SERVER" >&2
    exit 2
    ;;
esac

for _ in $(seq 1 100); do
  if redis-cli -p "$port" PING >/dev/null 2>&1; then
    echo "$SERVER ready on 127.0.0.1:$port"
    exit 0
  fi
  sleep 0.05
done

echo "$SERVER failed to become ready" >&2
exit 1
