#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$ROOT"

REDIS_IMAGE="${REDIS_IMAGE:-redis:8.2}"
NAME="${NAME:-snugkv-release-redis82-oracle}"
PORT="${PORT:-6398}"

mkdir -p docs/release

cleanup() {
  docker rm -f "$NAME" >/dev/null 2>&1 || true
}
trap cleanup EXIT
cleanup

docker run -d --rm --name "$NAME" -p "127.0.0.1:$PORT:6379" "$REDIS_IMAGE" redis-server --save "" --appendonly no >/dev/null

for _ in $(seq 1 50); do
  if docker exec "$NAME" redis-cli PING >/dev/null 2>&1; then
    break
  fi
  sleep 0.1
done

docker exec "$NAME" redis-server --version > docs/release/redis82-version.txt
docker exec "$NAME" redis-cli --json COMMAND > docs/release/redis82-command.json

python3 scripts/release/compare-redis82-command-inventory.py \
  --snug docs/release/implemented-commands.json \
  --redis docs/release/redis82-command.json \
  --markdown docs/release/REDIS-COMMAND-GAP-AUDIT.md \
  --json docs/release/redis82-command-gap-audit.json

echo
echo "generated:"
echo "  docs/release/redis82-version.txt"
echo "  docs/release/redis82-command.json"
echo "  docs/release/REDIS-COMMAND-GAP-AUDIT.md"
echo "  docs/release/redis82-command-gap-audit.json"
