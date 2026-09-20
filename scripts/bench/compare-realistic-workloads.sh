#!/usr/bin/env bash
set -euo pipefail

# Runs a workload matrix intended to look more like common Redis application
# usage than a single uniform random-blob benchmark. Each profile is still
# synthetic and deterministic; keep the random/compressed cases as controls.

KEYS="${KEYS:-1000000}"
OPS="${OPS:-5000000}"
WORKERS="${WORKERS:-8}"
PIPELINE="${PIPELINE:-256}"
RUNS="${RUNS:-3}"
FRESH_SERVERS="${FRESH_SERVERS:-1}"
CAPTURE_HEAP="${CAPTURE_HEAP:-0}"
ROOT_OUT="${ROOT_OUT:-benchmark-results/realistic-$(date +%Y%m%d-%H%M%S)}"

mkdir -p "$ROOT_OUT"

profiles=(
  "session-json:384"
  "api-json:768"
  "counter:10"
  "uuid:36"
  "text:256"
  "repetitive:256"
  "compressed:256"
  "random:256"
)

for spec in "${profiles[@]}"; do
  shape="${spec%%:*}"
  bytes="${spec##*:}"

  echo
  echo "################################################################"
  echo "profile=$shape value_bytes=$bytes"
  echo "################################################################"

  KEYS="$KEYS" \
  OPS="$OPS" \
  WORKERS="$WORKERS" \
  PIPELINE="$PIPELINE" \
  RUNS="$RUNS" \
  FRESH_SERVERS="$FRESH_SERVERS" \
  CAPTURE_HEAP="$CAPTURE_HEAP" \
  VALUE_SHAPE="$shape" \
  VALUE_BYTES="$bytes" \
  OUT_DIR="$ROOT_OUT/$shape" \
    bash scripts/bench/compare-redis-snug.sh
done

python3 - "$ROOT_OUT" <<'PY'
import json
import pathlib
import sys

root = pathlib.Path(sys.argv[1])
rows = []

for summary_path in sorted(root.glob("*/summary.json")):
    profile = summary_path.parent.name
    with summary_path.open() as f:
        summary = json.load(f)
    for row in summary:
        rows.append({"profile": profile, **row})

out = root / "matrix-summary.json"
with out.open("w") as f:
    json.dump(rows, f, indent=2)

print("\n===== REALISTIC WORKLOAD MATRIX =====")
for row in rows:
    if row["workload"] == "load":
        print(
            f'{row["profile"]:14} {row["server"]:8} '
            f'load={row["ops_per_second_median"]:10.0f}/s '
            f'bpk={row.get("bytes_per_key_delta_median", 0):8.2f}'
        )
    elif row["workload"] in ("get", "mixed"):
        print(
            f'{row["profile"]:14} {row["server"]:8} '
            f'{row["workload"]:5}={row["ops_per_second_median"]:10.0f}/s '
            f'p95={row["p95_us_median"]:8.2f}us'
        )

print("\ncombined summary:", out)
PY
