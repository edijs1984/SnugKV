#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

tests=(
  scripts/cluster-chaos-rebalance-restart.sh
  scripts/cluster-chaos-target-restart.sh
  scripts/cluster-chaos-failover-restart.sh
  scripts/cluster-chaos-repeated-recovery.sh
  scripts/cluster-chaos-majority-partition.sh
  scripts/cluster-chaos-persistence-failure.sh
  scripts/cluster-chaos-corrupt-replica.sh
)

echo "SnugKV cluster restart/failure recovery matrix"
echo "cases=${#tests[@]}"

for test_script in "${tests[@]}"; do
  echo
  echo "===== $test_script ====="
  bash -n "$test_script"
  bash "$test_script"
done

echo
echo "cluster restart/failure recovery matrix: PASS"
