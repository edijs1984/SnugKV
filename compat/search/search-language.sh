#!/usr/bin/env bash
set -u

PORT="${REDIS_PORT:-6379}"
TARGET="${TARGET_NAME:-target}"
CLI=(redis-cli -p "$PORT" --raw)

run() {
  printf '> '
  printf '%q ' "$@"
  printf '\n'
  "${CLI[@]}" "$@" 2>&1 || true
  printf '\n'
}

echo "target=$TARGET port=$PORT"

echo
echo "=== reset ==="
run FLUSHDB
for idx in langen langde langfield; do
  run FT.DROPINDEX "$idx"
done

echo
echo "=== default english baseline ==="
run JSON.SET lang:en:1 '$' '{"text":"run"}'
run JSON.SET lang:en:2 '$' '{"text":"runs"}'
run JSON.SET lang:en:3 '$' '{"text":"running"}'
run FT.CREATE langen ON JSON PREFIX 1 lang:en: LANGUAGE english SCHEMA '$.text' AS text TEXT
run FT.SEARCH langen '@text:run' NOCONTENT
run FT.SEARCH langen '@text:running' NOCONTENT
run FT.SEARCH langen '@text:run' NOCONTENT LANGUAGE english
run FT.SEARCH langen '@text:run' NOCONTENT LANGUAGE german

echo
echo "=== german index language ==="
run JSON.SET lang:de:1 '$' '{"text":"haus"}'
run JSON.SET lang:de:2 '$' '{"text":"hauses"}'
run JSON.SET lang:de:3 '$' '{"text":"häuser"}'
run JSON.SET lang:de:4 '$' '{"text":"häusern"}'
run FT.CREATE langde ON JSON PREFIX 1 lang:de: LANGUAGE german SCHEMA '$.text' AS text TEXT
run FT.SEARCH langde '@text:haus' NOCONTENT
run FT.SEARCH langde '@text:hauses' NOCONTENT
run FT.SEARCH langde '@text:häuser' NOCONTENT
run FT.SEARCH langde '@text:häusern' NOCONTENT
run FT.SEARCH langde '@text:haus' NOCONTENT LANGUAGE german
run FT.SEARCH langde '@text:haus' NOCONTENT LANGUAGE english

echo
echo "=== language field ==="
run JSON.SET lang:mixed:1 '$' '{"lang":"english","text":"running"}'
run JSON.SET lang:mixed:2 '$' '{"lang":"german","text":"häusern"}'
run JSON.SET lang:mixed:3 '$' '{"lang":"english","text":"studies"}'
run FT.CREATE langfield ON JSON PREFIX 1 lang:mixed: LANGUAGE english LANGUAGE_FIELD '$.lang' SCHEMA '$.text' AS text TEXT
run FT.SEARCH langfield '@text:run' NOCONTENT
run FT.SEARCH langfield '@text:haus' NOCONTENT
run FT.SEARCH langfield '@text:study' NOCONTENT
run FT.INFO langfield

echo
echo "=== language validation ==="
run FT.CREATE badlang ON JSON LANGUAGE klingon SCHEMA '$.text' AS text TEXT
run FT.SEARCH langen '@text:run' NOCONTENT LANGUAGE klingon
run FT.CREATE badlangfield ON JSON LANGUAGE_FIELD SCHEMA '$.text' AS text TEXT

echo
echo "=== drop ==="
run FT.DROPINDEX langen
run FT.DROPINDEX langde
run FT.DROPINDEX langfield
run FT.DROPINDEX badlang
run FT.DROPINDEX badlangfield
run FT._LIST
