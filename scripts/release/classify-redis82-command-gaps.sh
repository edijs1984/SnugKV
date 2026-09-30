#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$ROOT"

python3 scripts/release/classify-redis82-command-gaps.py \
  --input docs/release/redis82-command-gap-audit.json \
  --markdown docs/release/REDIS-COMMAND-GAP-CLASSIFICATION.md \
  --json docs/release/redis82-command-gap-classification.json
