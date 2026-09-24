# Replication RDB Encoding Compatibility Audit

Date: 2026-09-24

## Scope

This audit extends Redis primary -> SnugKV replica full-sync interoperability across additional Redis RDB object encodings, with both synthetic decoder tests and a live Redis 8.10.2 differential full sync.

## Added RDB object encodings

SnugKV now decodes:

- plain LIST (RDB type 1)
- plain SET (RDB type 2)
- legacy textual-score ZSET (RDB type 3)
- plain HASH (RDB type 4)
- binary-double ZSET_2 (RDB type 5)
- legacy HASH_ZIPMAP (RDB type 9)
- legacy LIST_ZIPLIST (RDB type 10)
- legacy ZSET_ZIPLIST (RDB type 12)
- legacy HASH_ZIPLIST (RDB type 13)
- legacy LIST_QUICKLIST (RDB type 14)

Existing support retained:

- STRING
- SET_INTSET
- HASH_LISTPACK
- ZSET_LISTPACK
- LIST_QUICKLIST_2
- SET_LISTPACK
- STREAM_LISTPACKS_3

## Shared string encodings

The existing RDB string decoder already supported:

- raw strings
- integer-encoded int8
- integer-encoded int16
- integer-encoded int32
- LZF-compressed strings

This audit verified those paths are exercised correctly inside additional object encodings and during live Redis full sync.

## Legacy ziplist support

A bounded ziplist decoder was added with support for:

- 6-bit string lengths
- 14-bit string lengths
- 32-bit string lengths
- int8
- int16
- int24
- int32
- int64
- immediate integers
- prevlen validation
- header byte-count validation
- entry-count validation
- terminator validation

This decoder is used by legacy list/hash/zset ziplist formats and legacy quicklist nodes.

## Legacy zipmap support

A bounded zipmap decoder was added with:

- short lengths
- 32-bit lengths
- value free-byte handling
- entry-count validation
- terminator validation
- payload bounds checks

## Legacy ZSET score support

RDB type 3 textual scores are decoded using Redis-compatible one-byte length framing.

Special values:

- 254 -> +Inf
- 255 -> -Inf
- 253 -> NaN, rejected

RDB type 5 ZSET_2 uses little-endian IEEE-754 binary64 scores.

## Synthetic validation

Focused tests cover:

- plain LIST/SET/HASH/ZSET_2
- integer-encoded elements inside plain collections
- legacy textual-score ZSET
- NaN rejection
- LIST_ZIPLIST
- HASH_ZIPLIST
- ZSET_ZIPLIST
- old LIST_QUICKLIST with multiple nodes
- HASH_ZIPMAP
- corruption cases for ziplist/zipmap metadata

Focused tests and race tests passed.

## Live Redis 8.10.2 differential

A native Redis 8.10.2 instance was configured to force non-compact collection encodings:

- `hash-max-listpack-entries 0`
- `set-max-intset-entries 0`
- `set-max-listpack-entries 0`
- `zset-max-listpack-entries 0`

Dataset:

- plain HASH
- plain SET
- ZSET_2
- int8-like string value
- int16-like string value
- int32-like string value
- highly compressible string to exercise LZF
- expiring string

A fresh Redis -> SnugKV full sync completed successfully.

Observed after sync:

- HASH fields and values preserved
- SET members preserved
- ZSET ordering and scores preserved
- integer-encoded values restored as their exact string representations
- LZF value restored to the expected 6000-byte length
- TTL value restored with a positive remaining TTL

## Validation gates

The branch passed:

```
go test ./internal/engine ./internal/server -count=1
go test -race ./internal/engine ./internal/server -count=1
go test -race -count=1 ./...
go vet ./...
git diff --check
```

## Remaining RDB work

Not every historical or newly introduced Redis RDB object type is supported.

Future work should be driven by real datasets and Redis-version fixtures, especially:

- newer hash field-expiration encodings
- newer hash template encodings
- any additional Redis object types observed in production RDBs
- broader stream consumer-group / pending-entry edge cases
- exact compatibility audits against older Redis-generated fixtures
