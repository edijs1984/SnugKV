#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
BIN=/tmp/snugkv-cluster-chaos-corrupt-replica-bin
TMP=/tmp/snugkv-cluster-chaos-corrupt-replica

P0="${P0:-7170}"
P1="${P1:-7171}"
P2="${P2:-7172}"
PASSWORD="${PASSWORD:-cluster-corrupt-secret}"
CONTROL_SECRET="${CONTROL_SECRET:-cluster-corrupt-control-secret}"
GROUP_ID="${GROUP_ID:-cluster-chaos-corrupt-replica}"

cleanup() {
  for pidfile in "$TMP"/*.pid; do
    [[ -f "$pidfile" ]] || continue
    pid="$(cat "$pidfile")"
    kill "$pid" 2>/dev/null || true
    wait "$pid" 2>/dev/null || true
  done
  rm -f "$BIN"
}
trap cleanup EXIT

require_cmd() {
  command -v "$1" >/dev/null 2>&1 || {
    echo "missing required command: $1" >&2
    exit 1
  }
}

require_cmd go
require_cmd redis-cli
require_cmd python3
require_cmd seq

rm -rf "$TMP"
mkdir -p "$TMP"
cd "$ROOT"
go build -o "$BIN" ./cmd/snugkv

cat >"$TMP/users.acl" <<ACL
user default on >$PASSWORD ~* &* +@all
ACL
chmod 600 "$TMP/users.acl"

make_config() {
  local name="$1"
  local port="$2"
  local peer1="$3"
  local peer2="$4"
  cat >"$TMP/$name.json" <<JSON
{
  "listen": "127.0.0.1:$port",
  "admin_listen": "",
  "metrics_listen": "",
  "acl_file": "$TMP/users.acl",
  "aof_path": "$TMP/$name.aof",
  "fsync": "always",
  "masterauth": "$PASSWORD",
  "auto_failover_timeout_ms": 2000,
  "failover_peers": ["$peer1", "$peer2"],
  "failover_quorum": 2,
  "failover_group_id": "$GROUP_ID",
  "failover_config_epoch": 1,
  "failover_advertise_addr": "127.0.0.1:$port",
  "cluster_enabled": true,
  "cluster_node_addr": "127.0.0.1:$port",
  "cluster_control_auth": "$CONTROL_SECRET",
  "cluster_slots": {
    "0-16383": "127.0.0.1:$P0"
  }
}
JSON
}

make_config n0 "$P0" "127.0.0.1:$P1" "127.0.0.1:$P2"
make_config n1 "$P1" "127.0.0.1:$P0" "127.0.0.1:$P2"
make_config n2 "$P2" "127.0.0.1:$P0" "127.0.0.1:$P1"

start_node() {
  local name="$1"
  "$BIN" -config "$TMP/$name.json" >"$TMP/$name.log" 2>&1 &
  echo $! >"$TMP/$name.pid"
}

wait_ready() {
  local port="$1"
  for _ in $(seq 1 200); do
    if redis-cli --no-auth-warning --raw -a "$PASSWORD" -p "$port" PING 2>/dev/null | grep -qx PONG; then
      return 0
    fi
    sleep 0.05
  done
  return 1
}

stop_hard() {
  local name="$1"
  local pid
  pid="$(cat "$TMP/$name.pid")"
  kill -KILL "$pid"
  wait "$pid" 2>/dev/null || true
  rm -f "$TMP/$name.pid"
}

cli() {
  local port="$1"
  shift
  redis-cli --no-auth-warning --raw -a "$PASSWORD" -p "$port" "$@"
}

start_node n0
start_node n1
start_node n2
wait_ready "$P0"
wait_ready "$P1"
wait_ready "$P2"

echo "[1/8] attach replicas and seed durable data"
cli "$P1" REPLICAOF 127.0.0.1 "$P0" | grep -qx OK
cli "$P2" REPLICAOF 127.0.0.1 "$P0" | grep -qx OK

for _ in $(seq 1 200); do
  i1="$(cli "$P1" INFO replication 2>/dev/null || true)"
  i2="$(cli "$P2" INFO replication 2>/dev/null || true)"
  if grep -q '^master_link_status:up' <<<"$i1" &&
     grep -q '^master_link_status:up' <<<"$i2"; then
    break
  fi
  sleep 0.05
done
grep -q '^master_link_status:up' <<<"$i1"
grep -q '^master_link_status:up' <<<"$i2"

seed="$TMP/seed.commands"
: >"$seed"
for i in $(seq 1 200); do
  printf 'SET corrupt:key:%03d value-%03d\n' "$i" "$i" >>"$seed"
done
printf 'WAIT 2 5000\n' >>"$seed"
redis-cli --no-auth-warning --raw -a "$PASSWORD" -p "$P0" <"$seed" >"$TMP/seed.out"
[[ "$(tail -n 1 "$TMP/seed.out")" == "2" ]]

echo "[2/8] hard-stop replica n2"
stop_hard n2

echo "[3/8] corrupt a durable byte inside n2 AOF"
python3 - "$TMP/n2.aof" <<'PY'
import os
import sys

path = sys.argv[1]
size = os.path.getsize(path)
if size < 32:
    raise SystemExit(f"AOF unexpectedly small: {size}")

# Corrupt a byte well inside a persisted frame rather than truncating the tail.
offset = max(16, size // 2)
with open(path, "r+b") as f:
    f.seek(offset)
    b = f.read(1)
    if not b:
        raise SystemExit("unable to read corruption byte")
    f.seek(offset)
    f.write(bytes([b[0] ^ 0x5A]))
    f.flush()
    os.fsync(f.fileno())

print(f"corrupted offset={offset} size={size}")
PY

echo "[4/8] require corrupted replica restart to fail closed"
start_node n2
if wait_ready "$P2"; then
  echo "corrupted replica unexpectedly started" >&2
  cli "$P2" INFO persistence >&2 || true
  tail -n 120 "$TMP/n2.log" >&2 || true
  exit 1
fi

pid="$(cat "$TMP/n2.pid")"
if kill -0 "$pid" 2>/dev/null; then
  echo "corrupted replica process remained alive without becoming ready" >&2
  tail -n 120 "$TMP/n2.log" >&2 || true
  kill "$pid" 2>/dev/null || true
  wait "$pid" 2>/dev/null || true
else
  wait "$pid" 2>/dev/null || true
fi
rm -f "$TMP/n2.pid"

if ! grep -Ei 'checksum|corrupt|persistence|replay|invalid' "$TMP/n2.log" >/dev/null; then
  echo "replica failed as expected, but log lacked a persistence-corruption diagnostic" >&2
  tail -n 120 "$TMP/n2.log" >&2 || true
  exit 1
fi
echo "corrupted replica failed closed as expected"

echo "[5/8] remove damaged local AOF and restart empty replica"
rm -f "$TMP/n2.aof" "$TMP/n2.aof.lock"
start_node n2
wait_ready "$P2" || {
  echo "clean replica restart failed" >&2
  tail -n 120 "$TMP/n2.log" >&2 || true
  exit 1
}

echo "[6/8] reattach n2 and require full recovery from healthy primary"
cli "$P2" REPLICAOF 127.0.0.1 "$P0" | grep -qx OK
for _ in $(seq 1 300); do
  info="$(cli "$P2" INFO replication 2>/dev/null || true)"
  if grep -q '^role:slave' <<<"$info" &&
     grep -q '^master_link_status:up' <<<"$info"; then
    break
  fi
  sleep 0.05
done
grep -q '^master_link_status:up' <<<"$info"

echo "[7/8] verify recovered replica contains full dataset"
for n in 001 100 200; do
  got="$(cli "$P2" GET "corrupt:key:$n")"
  [[ "$got" == "value-$n" ]] || {
    echo "recovered replica mismatch for key $n: $got" >&2
    exit 1
  }
done

echo "[8/8] prove rebuilt replica remains durable across another hard restart"
stop_hard n2
start_node n2
wait_ready "$P2"
for n in 001 100 200; do
  got="$(cli "$P2" GET "corrupt:key:$n")"
  [[ "$got" == "value-$n" ]] || {
    echo "post-restart replica mismatch for key $n: $got" >&2
    exit 1
  }
done

echo "cluster corrupted-replica recovery matrix: PASS"
echo "replica=127.0.0.1:$P2 fail_closed=yes rebuilt_from_primary=yes restart_recovered=yes"
