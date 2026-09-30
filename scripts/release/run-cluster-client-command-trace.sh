#!/usr/bin/env bash
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$ROOT"
OUT="${OUT:-docs/release/client-traces}"
mkdir -p "$OUT"
rm -f "$OUT"/cluster-*.monitor "$OUT"/cluster.jsonl
TRACE_DIR="$OUT" bash scripts/cluster-client-smoke.sh
python3 scripts/release/monitor-to-command-trace.py \
  --input "$OUT/cluster-7000.monitor" \
  --input "$OUT/cluster-7001.monitor" \
  --input "$OUT/cluster-7002.monitor" \
  --output "$OUT/cluster.jsonl"
python3 scripts/release/summarize-client-command-traces.py \
  --trace "$OUT/standalone.jsonl" \
  --trace "$OUT/cluster.jsonl" \
  --candidates docs/release/redis82-command-gap-classification.json \
  --markdown docs/release/CLIENT-COMMAND-TRACE.md
