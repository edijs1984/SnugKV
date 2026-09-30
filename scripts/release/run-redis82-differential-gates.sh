#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
TMP="${TMP:-/tmp/snugkv-g1-differential}"
SNUG_PORT="${SNUG_PORT:-6380}"
REDIS_PORT="${REDIS_PORT:-6390}"
REDIS_DST_PORT="${REDIS_DST_PORT:-6391}"
REDIS_AUTH_PORT="${REDIS_AUTH_PORT:-6392}"
REDIS_IMAGE="${REDIS_IMAGE:-redis:8.2.9}"

SNUG_BIN="$TMP/snugkv"
SNUG_LOG="$TMP/snugkv.log"
SNUG_PID=""

redis_name() {
  printf 'snugkv-g1-redis-%s' "$1"
}

cleanup() {
  if [[ -n "$SNUG_PID" ]]; then
    kill "$SNUG_PID" 2>/dev/null || true
    wait "$SNUG_PID" 2>/dev/null || true
  fi
  for p in "$REDIS_PORT" "$REDIS_DST_PORT" "$REDIS_AUTH_PORT"; do
    docker rm -f "$(redis_name "$p")" >/dev/null 2>&1 || true
  done
}
trap cleanup EXIT

wait_redis() {
  local port="$1"
  for _ in $(seq 1 120); do
    if redis-cli -p "$port" PING 2>/dev/null | grep -q '^PONG

cd "$ROOT"
rm -rf "$TMP"
mkdir -p "$TMP"

command -v docker >/dev/null
command -v redis-cli >/dev/null
command -v python3 >/dev/null
command -v go >/dev/null
command -v npm >/dev/null

echo "[1/8] build and start SnugKV"
go build -buildvcs=false -o "$SNUG_BIN" ./cmd/snugkv
"$SNUG_BIN"   -listen "127.0.0.1:$SNUG_PORT"   -admin-listen ""   -metrics-listen ""   -aof ""   -snapshot ""   >"$SNUG_LOG" 2>&1 &
SNUG_PID=$!
wait_redis "$SNUG_PORT"

echo "[2/8] start Redis 8.2 oracle instances"
for port in "$REDIS_PORT" "$REDIS_DST_PORT" "$REDIS_AUTH_PORT"; do
  docker rm -f "$(redis_name "$port")" >/dev/null 2>&1 || true
  docker run -d --rm     --name "$(redis_name "$port")"     -p "127.0.0.1:$port:6379"     "$REDIS_IMAGE"     redis-server --save "" --appendonly no     >/dev/null
  wait_redis "$port"
done

# Configure an authenticated destination for MIGRATE AUTH/AUTH2.
redis-cli -p "$REDIS_AUTH_PORT" ACL SETUSER migrator on '>secret' '~*' '+@all' >/dev/null
redis-cli -p "$REDIS_AUTH_PORT" ACL SETUSER default off >/dev/null
wait_redis_user "$REDIS_AUTH_PORT" migrator secret

echo "[3/8] scalar DUMP/RESTORE cross-restore"
REDIS_PORT="$REDIS_PORT" SNUG_PORT="$SNUG_PORT"   python3 compat/keyspace/dump-restore-cross.py

echo "[4/8] native datatype DUMP/RESTORE cross-restore"
REDIS_PORT="$REDIS_PORT" SNUG_PORT="$SNUG_PORT"   python3 compat/keyspace/dump-native-cross.py

echo "[5/8] STREAM DUMP/RESTORE cross-restore"
REDIS_PORT="$REDIS_PORT" SNUG_PORT="$SNUG_PORT"   python3 compat/keyspace/dump-stream-cross.py

echo "[6/8] Function RDB cross-restore"
REDIS_PORT="$REDIS_PORT" SNUG_PORT="$SNUG_PORT"   python3 compat/scripting/function-dump-cross-restore.py

echo "[7/8] MIGRATE interoperability"
SNUG_PORT="$SNUG_PORT" REDIS_DST_PORT="$REDIS_DST_PORT" MIGRATE_PORT="$REDIS_DST_PORT"   python3 compat/keyspace/migrate-cross.py

SRC_PORT="$SNUG_PORT" DST_PORT="$REDIS_AUTH_PORT" MIGRATE_PORT="$REDIS_AUTH_PORT" MIGRATE_USER=migrator MIGRATE_PASS=secret   python3 compat/keyspace/migrate-auth-cross.py

echo "[8/8] RESP3 real-client smoke against both servers"
(
  cd compat/node
  [[ -d node_modules ]] || npm ci
)
if [[ ! -x compat/python/.venv/bin/python ]]; then
  python3 -m venv compat/python/.venv
fi
compat/python/.venv/bin/python -c 'import redis' >/dev/null 2>&1 ||   compat/python/.venv/bin/pip install --disable-pip-version-check 'redis==6.4.0'

REDIS_PORT="$SNUG_PORT" TARGET_NAME=snugkv bash compat/resp3/run.sh
REDIS_PORT="$REDIS_PORT" TARGET_NAME=redis82 bash compat/resp3/run.sh

echo
echo "first-release Redis 8.2 differential gates: PASS"
; then
      return 0
    fi
    sleep 0.1
  done
  echo "server on port $port did not become ready" >&2
  return 1
}

wait_redis_user() {
  local port="$1"
  local user="$2"
  local pass="$3"
  for _ in $(seq 1 120); do
    if redis-cli -p "$port" --user "$user" -a "$pass" --no-auth-warning PING 2>/dev/null | grep -q '^PONG

cd "$ROOT"
rm -rf "$TMP"
mkdir -p "$TMP"

command -v docker >/dev/null
command -v redis-cli >/dev/null
command -v python3 >/dev/null
command -v go >/dev/null

echo "[1/8] build and start SnugKV"
go build -buildvcs=false -o "$SNUG_BIN" ./cmd/snugkv
"$SNUG_BIN"   -listen "127.0.0.1:$SNUG_PORT"   -admin-listen ""   -metrics-listen ""   -aof ""   -snapshot ""   >"$SNUG_LOG" 2>&1 &
SNUG_PID=$!
wait_redis "$SNUG_PORT"

echo "[2/8] start Redis 8.2 oracle instances"
for port in "$REDIS_PORT" "$REDIS_DST_PORT" "$REDIS_AUTH_PORT"; do
  docker rm -f "$(redis_name "$port")" >/dev/null 2>&1 || true
  docker run -d --rm     --name "$(redis_name "$port")"     -p "127.0.0.1:$port:6379"     "$REDIS_IMAGE"     redis-server --save "" --appendonly no     >/dev/null
  wait_redis "$port"
done

# Configure an authenticated destination for MIGRATE AUTH/AUTH2.
redis-cli -p "$REDIS_AUTH_PORT" ACL SETUSER migrator on '>secret' '~*' '+@all' >/dev/null
redis-cli -p "$REDIS_AUTH_PORT" ACL SETUSER default off >/dev/null
wait_redis "$REDIS_AUTH_PORT" secret

echo "[3/8] scalar DUMP/RESTORE cross-restore"
REDIS_PORT="$REDIS_PORT" SNUG_PORT="$SNUG_PORT"   python3 compat/keyspace/dump-restore-cross.py

echo "[4/8] native datatype DUMP/RESTORE cross-restore"
REDIS_PORT="$REDIS_PORT" SNUG_PORT="$SNUG_PORT"   python3 compat/keyspace/dump-native-cross.py

echo "[5/8] STREAM DUMP/RESTORE cross-restore"
REDIS_PORT="$REDIS_PORT" SNUG_PORT="$SNUG_PORT"   python3 compat/keyspace/dump-stream-cross.py

echo "[6/8] Function RDB cross-restore"
REDIS_PORT="$REDIS_PORT" SNUG_PORT="$SNUG_PORT"   python3 compat/scripting/function-dump-cross-restore.py

echo "[7/8] MIGRATE interoperability"
SNUG_PORT="$SNUG_PORT" REDIS_DST_PORT="$REDIS_DST_PORT" MIGRATE_PORT="$REDIS_DST_PORT"   python3 compat/keyspace/migrate-cross.py

SRC_PORT="$SNUG_PORT" DST_PORT="$REDIS_AUTH_PORT" MIGRATE_PORT="$REDIS_AUTH_PORT" MIGRATE_USER=migrator MIGRATE_PASS=secret   python3 compat/keyspace/migrate-auth-cross.py

echo "[8/8] RESP3 real-client smoke against both servers"
REDIS_PORT="$SNUG_PORT" TARGET_NAME=snugkv bash compat/resp3/run.sh
REDIS_PORT="$REDIS_PORT" TARGET_NAME=redis82 bash compat/resp3/run.sh

echo
echo "first-release Redis 8.2 differential gates: PASS"
; then
      return 0
    fi
    sleep 0.1
  done
  echo "authenticated server on port $port did not become ready" >&2
  return 1
}

cd "$ROOT"
rm -rf "$TMP"
mkdir -p "$TMP"

command -v docker >/dev/null
command -v redis-cli >/dev/null
command -v python3 >/dev/null
command -v go >/dev/null

echo "[1/8] build and start SnugKV"
go build -buildvcs=false -o "$SNUG_BIN" ./cmd/snugkv
"$SNUG_BIN"   -listen "127.0.0.1:$SNUG_PORT"   -admin-listen ""   -metrics-listen ""   -aof ""   -snapshot ""   >"$SNUG_LOG" 2>&1 &
SNUG_PID=$!
wait_redis "$SNUG_PORT"

echo "[2/8] start Redis 8.2 oracle instances"
for port in "$REDIS_PORT" "$REDIS_DST_PORT" "$REDIS_AUTH_PORT"; do
  docker rm -f "$(redis_name "$port")" >/dev/null 2>&1 || true
  docker run -d --rm     --name "$(redis_name "$port")"     -p "127.0.0.1:$port:6379"     "$REDIS_IMAGE"     redis-server --save "" --appendonly no     >/dev/null
  wait_redis "$port"
done

# Configure an authenticated destination for MIGRATE AUTH/AUTH2.
redis-cli -p "$REDIS_AUTH_PORT" ACL SETUSER migrator on '>secret' '~*' '+@all' >/dev/null
redis-cli -p "$REDIS_AUTH_PORT" ACL SETUSER default off >/dev/null
wait_redis "$REDIS_AUTH_PORT" secret

echo "[3/8] scalar DUMP/RESTORE cross-restore"
REDIS_PORT="$REDIS_PORT" SNUG_PORT="$SNUG_PORT"   python3 compat/keyspace/dump-restore-cross.py

echo "[4/8] native datatype DUMP/RESTORE cross-restore"
REDIS_PORT="$REDIS_PORT" SNUG_PORT="$SNUG_PORT"   python3 compat/keyspace/dump-native-cross.py

echo "[5/8] STREAM DUMP/RESTORE cross-restore"
REDIS_PORT="$REDIS_PORT" SNUG_PORT="$SNUG_PORT"   python3 compat/keyspace/dump-stream-cross.py

echo "[6/8] Function RDB cross-restore"
REDIS_PORT="$REDIS_PORT" SNUG_PORT="$SNUG_PORT"   python3 compat/scripting/function-dump-cross-restore.py

echo "[7/8] MIGRATE interoperability"
SNUG_PORT="$SNUG_PORT" REDIS_DST_PORT="$REDIS_DST_PORT" MIGRATE_PORT="$REDIS_DST_PORT"   python3 compat/keyspace/migrate-cross.py

SRC_PORT="$SNUG_PORT" DST_PORT="$REDIS_AUTH_PORT" MIGRATE_PORT="$REDIS_AUTH_PORT" MIGRATE_USER=migrator MIGRATE_PASS=secret   python3 compat/keyspace/migrate-auth-cross.py

echo "[8/8] RESP3 real-client smoke against both servers"
REDIS_PORT="$SNUG_PORT" TARGET_NAME=snugkv bash compat/resp3/run.sh
REDIS_PORT="$REDIS_PORT" TARGET_NAME=redis82 bash compat/resp3/run.sh

echo
echo "first-release Redis 8.2 differential gates: PASS"
