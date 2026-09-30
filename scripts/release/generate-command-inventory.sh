#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$ROOT"

mkdir -p docs/release

go test ./internal/server -run '^TestCommandInventory' -count=1

tmp_md="$(mktemp)"
tmp_json="$(mktemp)"
trap 'rm -f "$tmp_md" "$tmp_json"' EXIT

go run ./cmd/commandinventory -format markdown > "$tmp_md"
go run ./cmd/commandinventory -format json > "$tmp_json"

mv "$tmp_md" docs/release/IMPLEMENTED-COMMANDS.md
mv "$tmp_json" docs/release/implemented-commands.json
trap - EXIT

echo "generated:"
echo "  docs/release/IMPLEMENTED-COMMANDS.md"
echo "  docs/release/implemented-commands.json"
echo
head -n 8 docs/release/IMPLEMENTED-COMMANDS.md
