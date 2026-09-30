#!/usr/bin/env bash
set -euo pipefail

ROOT="${ROOT:-$(cd "$(dirname "$0")/.." && pwd)}"
BIN="${BIN:-/tmp/snugkv-cluster-tls-reshard-bench-bin}"
TMP="${TMP:-/tmp/snugkv-cluster-tls-reshard-bench}"
OUT="${OUT:-/tmp/snugkv-cluster-tls-reshard-bench.jsonl}"

SOURCE_PORT="${SOURCE_PORT:-7240}"
TARGET_PORT="${TARGET_PORT:-7241}"
TARGET_BACKEND_PORT="${TARGET_BACKEND_PORT:-7242}"
SOURCE_ADDR="127.0.0.1:$SOURCE_PORT"
TARGET_ADDR="127.0.0.1:$TARGET_PORT"
TARGET_BACKEND_ADDR="127.0.0.1:$TARGET_BACKEND_PORT"

KEYS="${KEYS:-1000}"
VALUE_BYTES="${VALUE_BYTES:-2048}"
REPEATS="${REPEATS:-3}"
SLOT="${SLOT:-8192}"
CONTROL_SECRET="${CONTROL_SECRET:-cluster-tls-bench-control-secret}"

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
require_cmd openssl
require_cmd awk
require_cmd seq
require_cmd date

cd "$ROOT"
go build -o "$BIN" ./cmd/snugkv

rm -rf "$TMP"
mkdir -p "$TMP"
: >"$OUT"

openssl req -x509 -newkey rsa:2048 -nodes   -keyout "$TMP/server.key"   -out "$TMP/server.crt"   -days 1   -subj "/CN=localhost"   -addext "subjectAltName=DNS:localhost,IP:127.0.0.1"   >/dev/null 2>&1

cat >"$TMP/tls_proxy.py" <<'PY'
import selectors
import socket
import ssl
import sys
import threading

listen_host = sys.argv[1]
listen_port = int(sys.argv[2])
backend_host = sys.argv[3]
backend_port = int(sys.argv[4])
cert = sys.argv[5]
key = sys.argv[6]

ctx = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
ctx.minimum_version = ssl.TLSVersion.TLSv1_2
ctx.load_cert_chain(cert, key)

def pump(src, dst):
    try:
        while True:
            data = src.recv(65536)
            if not data:
                break
            dst.sendall(data)
    except (OSError, ssl.SSLError):
        pass
    finally:
        try:
            dst.shutdown(socket.SHUT_WR)
        except OSError:
            pass

with socket.create_server((listen_host, listen_port), reuse_port=False) as listener:
    while True:
        raw, _ = listener.accept()
        try:
            client = ctx.wrap_socket(raw, server_side=True)
            backend = socket.create_connection((backend_host, backend_port), timeout=5)
        except Exception:
            raw.close()
            continue
        threading.Thread(target=pump, args=(client, backend), daemon=True).start()
        threading.Thread(target=pump, args=(backend, client), daemon=True).start()
PY

wait_ready() {
  local port="$1"
  for _ in $(seq 1 200); do
    if redis-cli --raw -p "$port" PING 2>/dev/null | grep -qx PONG; then
      return 0
    fi
    sleep 0.05
  done
  echo "node on port $port did not become ready" >&2
  return 1
}

stop_all() {
  for pid in "${PIDS[@]:-}"; do
    kill "$pid" 2>/dev/null || true
  done
  wait 2>/dev/null || true
  PIDS=()
}

find_tag() {
  local commands="$TMP/keyslot.commands"
  local slots="$TMP/keyslot.out"
  : >"$commands"
  for i in $(seq 0 50000); do
    printf 'CLUSTER KEYSLOT "tlsbench:{%d}"\n' "$i" >>"$commands"
  done
  redis-cli --raw -p "$SOURCE_PORT" <"$commands" >"$slots"
  awk -v slot="$SLOT" '$1 == slot { print NR-1; exit }' "$slots"
}

run_case() {
  local mode="$1"
  local run="$2"

  stop_all
  rm -f "$TMP/source.log" "$TMP/target.log" "$TMP/proxy.log"

  local target_listen="$TARGET_ADDR"
  if [[ "$mode" == "tls" ]]; then
    target_listen="$TARGET_BACKEND_ADDR"
  fi

  cat >"$TMP/source.json" <<JSON
{
  "listen": "$SOURCE_ADDR",
  "admin_listen": "",
  "metrics_listen": "",
  "cluster_enabled": true,
  "cluster_node_addr": "$SOURCE_ADDR",
  "cluster_control_auth": "$CONTROL_SECRET",
  "cluster_slots": {
    "0-9999": "$SOURCE_ADDR",
    "10000-16383": "$TARGET_ADDR"
  },
  "mastertls": $([[ "$mode" == "tls" ]] && echo true || echo false),
  "mastertls_ca_cert": "$([[ "$mode" == "tls" ]] && echo "$TMP/server.crt" || true)",
  "mastertls_server_name": "$([[ "$mode" == "tls" ]] && echo localhost || true)"
}
JSON

  cat >"$TMP/target.json" <<JSON
{
  "listen": "$target_listen",
  "admin_listen": "",
  "metrics_listen": "",
  "cluster_enabled": true,
  "cluster_node_addr": "$TARGET_ADDR",
  "cluster_control_auth": "$CONTROL_SECRET",
  "cluster_slots": {
    "0-9999": "$SOURCE_ADDR",
    "10000-16383": "$TARGET_ADDR"
  }
}
JSON

  "$BIN" -config "$TMP/source.json" >"$TMP/source.log" 2>&1 &
  PIDS+=("$!")
  "$BIN" -config "$TMP/target.json" >"$TMP/target.log" 2>&1 &
  PIDS+=("$!")

  wait_ready "$SOURCE_PORT"
  if [[ "$mode" == "plain" ]]; then
    wait_ready "$TARGET_PORT"
  else
    wait_ready "$TARGET_BACKEND_PORT"
    python3 "$TMP/tls_proxy.py"       127.0.0.1 "$TARGET_PORT"       127.0.0.1 "$TARGET_BACKEND_PORT"       "$TMP/server.crt" "$TMP/server.key"       >"$TMP/proxy.log" 2>&1 &
    PIDS+=("$!")
    tls_ready=0
    for _ in $(seq 1 100); do
      if python3 - "$TARGET_PORT" "$TMP/server.crt" <<'PY' >/dev/null 2>&1
import socket
import ssl
import sys

port = int(sys.argv[1])
cafile = sys.argv[2]
ctx = ssl.create_default_context(cafile=cafile)
with socket.create_connection(("127.0.0.1", port), timeout=0.5) as raw:
    with ctx.wrap_socket(raw, server_hostname="localhost"):
        pass
PY
      then
        tls_ready=1
        break
      fi
      sleep 0.05
    done
    if (( tls_ready == 0 )); then
      echo "TLS proxy on $TARGET_ADDR did not become ready" >&2
      tail -n 80 "$TMP/proxy.log" >&2 || true
      exit 1
    fi
  fi

  tag="$(find_tag)"
  [[ -n "$tag" ]]

  value="$(head -c "$VALUE_BYTES" </dev/zero | tr '\0' x)"
  seed="$TMP/seed.commands"
  : >"$seed"
  for i in $(seq 1 "$KEYS"); do
    printf 'SET tlsbench:{%s}:%06d %s\n' "$tag" "$i" "$value" >>"$seed"
  done
  redis-cli --raw -p "$SOURCE_PORT" <"$seed" >"$TMP/seed.out"
  if grep -v '^OK$' "$TMP/seed.out" | grep -q .; then
    echo "seed produced unexpected replies" >&2
    exit 1
  fi

  plan="$TMP/plan.out"
  redis-cli --raw -p "$SOURCE_PORT" CLUSTER REBALANCE PLAN >"$plan"
  plan_id="$(awk 'NR == 2 {print; exit}' "$plan")"
  [[ -n "$plan_id" ]]

  start_ns="$(date +%s%N)"
  redis-cli --raw -p "$SOURCE_PORT" CLUSTER REBALANCE APPLY "$plan_id" ONCE >"$TMP/apply.out"
  command_done_ns="$(date +%s%N)"

  local target_query_port="$TARGET_PORT"
  if [[ "$mode" == "tls" ]]; then
    target_query_port="$TARGET_BACKEND_PORT"
  fi

  deadline=$((SECONDS + 60))
  converged=0
  while (( SECONDS < deadline )); do
    source_count="$(redis-cli --raw -p "$SOURCE_PORT" DBSIZE 2>/dev/null || echo -1)"
    target_count="$(redis-cli --raw -p "$target_query_port" CLUSTER COUNTKEYSINSLOT "$SLOT" 2>/dev/null || echo -1)"
    moved_reply="$(redis-cli --raw -p "$SOURCE_PORT" GET "tlsbench:{$tag}:000001" 2>&1 || true)"
    if [[ "$source_count" == "0" &&
          "$target_count" == "$KEYS" &&
          "$moved_reply" == "MOVED $SLOT $TARGET_ADDR" ]]; then
      converged=1
      break
    fi
    sleep 0.01
  done
  end_ns="$(date +%s%N)"
  (( converged == 1 ))

  first="tlsbench:{$tag}:000001"
  last="$(printf 'tlsbench:{%s}:%06d' "$tag" "$KEYS")"
  [[ "$(redis-cli --raw -p "$target_query_port" STRLEN "$first")" == "$VALUE_BYTES" ]]
  [[ "$(redis-cli --raw -p "$target_query_port" STRLEN "$last")" == "$VALUE_BYTES" ]]

  python3 - "$mode" "$run" "$KEYS" "$VALUE_BYTES" "$start_ns" "$command_done_ns" "$end_ns" "$OUT" <<'PY'
import json
import sys

mode = sys.argv[1]
run = int(sys.argv[2])
keys = int(sys.argv[3])
value_bytes = int(sys.argv[4])
start = int(sys.argv[5])
command_done = int(sys.argv[6])
end = int(sys.argv[7])
out_path = sys.argv[8]

command_ns = command_done - start
converge_ns = end - start
logical_bytes = keys * value_bytes

row = {
    "benchmark": "cluster_reshard_transport",
    "transport": mode,
    "run": run,
    "keys": keys,
    "value_bytes": value_bytes,
    "logical_bytes": logical_bytes,
    "migration_command_duration_ns": command_ns,
    "convergence_duration_ns": converge_ns,
    "keys_per_second": keys / (converge_ns / 1e9),
    "logical_bytes_per_second": logical_bytes / (converge_ns / 1e9),
    "measurement_note": "plain compares direct TCP target; tls compares the same SnugKV migration protocol through a local TLS 1.2+ forwarding proxy because SnugKV currently provides outbound internal TLS but no native inbound TLS listener",
}
line = json.dumps(row, separators=(",", ":"))
print(line)
with open(out_path, "a", encoding="utf-8") as f:
    f.write(line + "\n")
PY
}

echo "SnugKV cluster migration transport benchmark"
echo "source=$SOURCE_ADDR target=$TARGET_ADDR keys=$KEYS value_bytes=$VALUE_BYTES repeats=$REPEATS"
echo "output=$OUT"

for run in $(seq 1 "$REPEATS"); do
  echo
  echo "===== plain run $run/$REPEATS ====="
  run_case plain "$run"
done

for run in $(seq 1 "$REPEATS"); do
  echo
  echo "===== tls run $run/$REPEATS ====="
  run_case tls "$run"
done

echo
echo "cluster migration transport benchmark: PASS"
echo "results=$OUT"
