#!/usr/bin/env bash
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$ROOT"
OUT="${OUT:-docs/release/client-traces}"
STATE="${STATE:-/tmp/snugkv-a3-failover-client}"
mkdir -p "$OUT"
rm -rf "$STATE"
mkdir -p "$STATE"
rm -f "$OUT"/failover-*.monitor "$OUT"/failover.jsonl

PRE_HOOK=$(cat <<'EOF'
cd "$ROOT/compat/node"
[[ -d node_modules ]] || npm ci
CLIENT_STATE_DIR="$STATE" P0="$P0" PASSWORD="$PASSWORD" node failover-trace.cjs >"$STATE/client.log" 2>&1 &
echo $! >"$STATE/client.pid"
for _ in $(seq 1 300); do
  [[ -f "$STATE/ready" ]] && exit 0
  [[ -f "$STATE/failed" ]] && { cat "$STATE/failed" >&2; exit 1; }
  sleep 0.05
done
echo "failover client did not become ready" >&2
cat "$STATE/client.log" >&2 || true
exit 1
EOF
)

POST_HOOK=$(cat <<'EOF'
touch "$STATE/post-failover"
for _ in $(seq 1 400); do
  [[ -f "$STATE/passed" ]] && { cat "$STATE/client.log"; exit 0; }
  [[ -f "$STATE/failed" ]] && { cat "$STATE/failed" >&2; cat "$STATE/client.log" >&2 || true; exit 1; }
  sleep 0.05
done
echo "failover client did not recover" >&2
cat "$STATE/client.log" >&2 || true
exit 1
EOF
)

export ROOT STATE
TRACE_DIR="$OUT" PRE_FAILOVER_HOOK="$PRE_HOOK" POST_FAILOVER_HOOK="$POST_HOOK" \
  bash scripts/cluster-chaos-failover-restart.sh

python3 scripts/release/monitor-to-command-trace.py \
  --input "$OUT/failover-7120.monitor" \
  --input "$OUT/failover-7121.monitor" \
  --input "$OUT/failover-7122.monitor" \
  --output "$OUT/failover.jsonl"

python3 scripts/release/summarize-client-command-traces.py \
  --trace "$OUT/standalone.jsonl" \
  --trace "$OUT/cluster.jsonl" \
  --trace "$OUT/failover.jsonl" \
  --candidates docs/release/redis82-command-gap-classification.json \
  --markdown docs/release/CLIENT-COMMAND-TRACE.md
