#!/usr/bin/env bash
set -euo pipefail

ROOT="${ROOT:-$(cd "$(dirname "$0")/.." && pwd)}"
BIN="${BIN:-/tmp/snugkv-cluster-topology-bench-bin}"
TMP="${TMP:-/tmp/snugkv-cluster-topology-bench}"
OUT="${OUT:-/tmp/snugkv-cluster-topology-bench.jsonl}"

P0="${P0:-7230}"
P1="${P1:-7231}"
P2="${P2:-7232}"
A0="127.0.0.1:$P0"
A1="127.0.0.1:$P1"
A2="127.0.0.1:$P2"

PASSWORD="${PASSWORD:-cluster-topology-bench-secret}"
CONTROL_SECRET="${CONTROL_SECRET:-cluster-topology-bench-control-secret}"
GROUP_ID="${GROUP_ID:-cluster-topology-bench}"
OPS="${OPS:-100}"
REPEATS="${REPEATS:-3}"

PIDS=()

cleanup() {
  for pid in "${PIDS[@]:-}"; do
    kill "$pid" 2>/dev/null || true
  done
  wait 2>/dev/null || true
  rm -f "$BIN"
}
trap cleanup EXIT INT TERM

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
: >"$OUT"

cd "$ROOT"
go build -o "$BIN" ./cmd/snugkv

cat >"$TMP/users.acl" <<ACL
user default on >$PASSWORD ~* &* +@all
ACL
chmod 600 "$TMP/users.acl"

make_config() {
  local name="$1"
  local port="$2"
  local advertise="$3"
  local peers_json="$4"
  local priority="$5"
  cat >"$TMP/$name.json" <<JSON
{
  "listen": "127.0.0.1:$port",
  "admin_listen": "",
  "metrics_listen": "",
  "acl_file": "$TMP/users.acl",
  "masterauth": "$PASSWORD",
  "failover_peers": $peers_json,
  "failover_quorum": 2,
  "failover_priority": $priority,
  "failover_group_id": "$GROUP_ID",
  "failover_config_epoch": 1,
  "failover_advertise_addr": "$advertise",
  "cluster_enabled": true,
  "cluster_node_addr": "$advertise",
  "cluster_control_auth": "$CONTROL_SECRET",
  "cluster_slots": {
    "0-16383": "$A0"
  }
}
JSON
}

make_config n0 "$P0" "$A0" "[\"$A1\",\"$A2\"]" 100
make_config n1 "$P1" "$A1" "[\"$A0\",\"$A2\"]" 10
make_config n2 "$P2" "$A2" "[\"$A0\",\"$A1\"]" 100

start_node() {
  local name="$1"
  "$BIN" -config "$TMP/$name.json" >"$TMP/$name.log" 2>&1 &
  PIDS+=("$!")
}

wait_ready() {
  local port="$1"
  for _ in $(seq 1 200); do
    if redis-cli --no-auth-warning --raw -a "$PASSWORD" -p "$port" PING 2>/dev/null | grep -qx PONG; then
      return 0
    fi
    sleep 0.05
  done
  echo "node on port $port did not become ready" >&2
  return 1
}

start_node n0
start_node n1
start_node n2
wait_ready "$P0"
wait_ready "$P1"
wait_ready "$P2"

redis-cli --no-auth-warning --raw -a "$PASSWORD" -p "$P1" REPLICAOF 127.0.0.1 "$P0" | grep -qx OK
redis-cli --no-auth-warning --raw -a "$PASSWORD" -p "$P2" REPLICAOF 127.0.0.1 "$P0" | grep -qx OK

stable=0
for _ in $(seq 1 200); do
  r1="$(redis-cli --no-auth-warning --raw -a "$PASSWORD" -p "$P1" INFO replication 2>/dev/null || true)"
  r2="$(redis-cli --no-auth-warning --raw -a "$PASSWORD" -p "$P2" INFO replication 2>/dev/null || true)"
  if grep -q 'master_link_status:up' <<<"$r1" &&
     grep -q 'master_link_status:up' <<<"$r2"; then
    stable=$((stable + 1))
    if (( stable >= 5 )); then
      break
    fi
  else
    stable=0
  fi
  sleep 0.05
done
(( stable >= 5 ))

cat >"$TMP/bench.py" <<'PY'
import json
import socket
import statistics
import sys
import time

host = sys.argv[1]
port = int(sys.argv[2])
password = sys.argv[3]
ops = int(sys.argv[4])
repeats = int(sys.argv[5])
out_path = sys.argv[6]

commands = [
    ("cluster_slots", ["CLUSTER", "SLOTS"]),
    ("cluster_nodes", ["CLUSTER", "NODES"]),
    ("cluster_shards", ["CLUSTER", "SHARDS"]),
    ("cluster_info", ["CLUSTER", "INFO"]),
    ("cluster_health", ["CLUSTER", "HEALTH"]),
]

def encode(parts):
    out = [f"*{len(parts)}\r\n".encode()]
    for part in parts:
        b = part.encode()
        out.append(f"${len(b)}\r\n".encode())
        out.append(b)
        out.append(b"\r\n")
    return b"".join(out)

class Resp:
    def __init__(self, sock):
        self.sock = sock
        self.buf = bytearray()

    def _fill(self):
        data = self.sock.recv(65536)
        if not data:
            raise EOFError("socket closed")
        self.buf.extend(data)

    def read_exact(self, n):
        while len(self.buf) < n:
            self._fill()
        out = bytes(self.buf[:n])
        del self.buf[:n]
        return out

    def read_line(self):
        while True:
            idx = self.buf.find(b"\r\n")
            if idx >= 0:
                out = bytes(self.buf[:idx])
                del self.buf[:idx+2]
                return out
            self._fill()

    def read(self):
        prefix = self.read_exact(1)
        line = self.read_line()
        if prefix == b"+":
            return line.decode(errors="replace")
        if prefix == b"-":
            raise RuntimeError(line.decode(errors="replace"))
        if prefix == b":":
            return int(line)
        if prefix == b"$":
            n = int(line)
            if n < 0:
                return None
            payload = self.read_exact(n)
            if self.read_exact(2) != b"\r\n":
                raise RuntimeError("invalid bulk terminator")
            return payload
        if prefix == b"*":
            n = int(line)
            if n < 0:
                return None
            return [self.read() for _ in range(n)]
        raise RuntimeError(f"unsupported RESP prefix {prefix!r}")

def pct(vals, p):
    vals = sorted(vals)
    if not vals:
        return 0
    idx = int((len(vals)-1) * p)
    return vals[idx]

with socket.create_connection((host, port), timeout=5) as sock:
    sock.settimeout(5)
    resp = Resp(sock)
    sock.sendall(encode(["AUTH", password]))
    resp.read()

    for name, command in commands:
        for run in range(1, repeats + 1):
            samples = []
            errors = 0
            total_start = time.perf_counter_ns()
            for _ in range(ops):
                t0 = time.perf_counter_ns()
                try:
                    sock.sendall(encode(command))
                    resp.read()
                except Exception:
                    errors += 1
                samples.append(time.perf_counter_ns() - t0)
            total_ns = time.perf_counter_ns() - total_start

            row = {
                "benchmark": "cluster_topology_observation",
                "command": name,
                "run": run,
                "ops": ops,
                "duration_ns": total_ns,
                "ops_per_second": ops / (total_ns / 1e9),
                "p50_ns": pct(samples, 0.50),
                "p95_ns": pct(samples, 0.95),
                "p99_ns": pct(samples, 0.99),
                "max_ns": max(samples) if samples else 0,
                "errors": errors,
                "measurement_note": "persistent RESP2/TCP connection to primary; each observation command is executed synchronously and includes server-side peer observation where the command requires it",
            }
            line = json.dumps(row, separators=(",", ":"))
            print(line)
            with open(out_path, "a", encoding="utf-8") as f:
                f.write(line + "\n")
PY

echo "SnugKV cluster topology observation benchmark"
echo "primary=$A0 replicas=$A1,$A2 ops=$OPS repeats=$REPEATS"
echo "output=$OUT"

python3 "$TMP/bench.py" 127.0.0.1 "$P0" "$PASSWORD" "$OPS" "$REPEATS" "$OUT"

if grep -q '"errors":[1-9]' "$OUT"; then
  echo "topology benchmark recorded command errors" >&2
  exit 1
fi

echo
echo "cluster topology observation benchmark: PASS"
echo "results=$OUT"
