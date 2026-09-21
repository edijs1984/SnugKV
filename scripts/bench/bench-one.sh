#!/usr/bin/env bash
set -euo pipefail

PROFILE="${1:-}"
if [[ -z "$PROFILE" ]]; then
  echo "usage: $0 <profile>" >&2
  echo "profiles: session-json api-json cache-json counter uuid text repetitive compressed random" >&2
  exit 2
fi

case "$PROFILE" in
  session-json|api-json|cache-json|counter|uuid|text|repetitive|compressed|random) ;;
  *)
    echo "unknown profile: $PROFILE" >&2
    exit 2
    ;;
esac

ROOT_OUT="${ROOT_OUT:-benchmark-results/manual-${PROFILE}-$(date +%Y%m%d-%H%M%S)}"

PROFILE="$PROFILE" \
ROOT_OUT="$ROOT_OUT" \
KEYS="${KEYS:-1000000}" \
GET_OPS="${GET_OPS:-2000000}" \
WORKERS="${WORKERS:-8}" \
PIPELINE="${PIPELINE:-256}" \
RUNS="${RUNS:-1}" \
SERVERS="${SERVERS:-redis snug-raw snug-opt}" \
WORKLOADS="${WORKLOADS:-load get}" \
BUILD_IMAGE="${BUILD_IMAGE:-1}" \
bash scripts/bench/compare-realistic-workloads.sh

if [[ "${UPDATE_TABLE:-1}" == "1" ]]; then
  python3 scripts/bench/update-realistic-results.py "$ROOT_OUT"
fi

echo
echo "profile result: $ROOT_OUT"
echo "scoreboard: benchmarks/REALISTIC_RESULTS.md"
