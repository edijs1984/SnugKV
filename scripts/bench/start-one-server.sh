#!/usr/bin/env bash
set -euo pipefail

SERVER="${1:?usage: start-one-server.sh redis|snug}"

REDIS_NAME="${REDIS_NAME:-snug-bench-redis}"
SNUG_NAME="${SNUG_NAME:-snug-bench-snugkv}"

REDIS_PORT="${REDIS_PORT:-6390}"
SNUG_PORT="${SNUG_PORT:-6383}"
PPROF_PORT="${PPROF_PORT:-6060}"

IMAGE="${SNUG_IMAGE:-snugkv-bench:local}"
BUILD_IMAGE="${BUILD_IMAGE:-0}"

docker rm -f "$REDIS_NAME" "$SNUG_NAME" >/dev/null 2>&1 || true

if [[ "$SERVER" == "snug" && "$BUILD_IMAGE" == "1" ]]; then
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
  snug)
    docker run -d --rm \
      --name "$SNUG_NAME" \
      -p "127.0.0.1:${SNUG_PORT}:6380" \
      -p "127.0.0.1:${PPROF_PORT}:6060" \
      "$IMAGE" \
      -listen 0.0.0.0:6380 \
      -admin-listen "" \
      -pprof-listen 0.0.0.0:6060 >/dev/null
    port="$SNUG_PORT"
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
