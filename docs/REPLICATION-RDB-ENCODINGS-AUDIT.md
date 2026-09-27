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
- legacy STREAM_LISTPACKS (RDB type 15)
- legacy STREAM_LISTPACKS_2 (RDB type 19)

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


## Legacy stream RDB compatibility

SnugKV now decodes the two historical Redis stream object encodings that precede `STREAM_LISTPACKS_3`:

- `RDB_TYPE_STREAM_LISTPACKS` (type 15)
- `RDB_TYPE_STREAM_LISTPACKS_2` (type 19)

The listpack node payload is shared with the existing type-21 decoder. The compatibility work is in the metadata tail:

- type 15 does not persist first-entry ID, max-deleted ID, or entries-added;
  SnugKV reconstructs first-entry ID from decoded entries, sets max-deleted ID to zero, and initializes entries-added to the live stream length, matching Redis load behavior.
- type 19 persists first-entry ID, max-deleted ID, and entries-added.
- type 15 does not persist consumer-group entries-read; SnugKV reconstructs it from the group's last-delivered ID and decoded stream entries.
- types 15 and 19 do not persist consumer active-time; SnugKV restores active-time from seen-time, matching Redis's legacy load behavior.
- global PEL entries, per-consumer PEL ownership, delivery timestamps, and delivery counts are restored through the existing stream snapshot model.

Validation added:

- direct decoder fixtures for types 15 and 19;
- consumer-group and PEL fixtures for both legacy formats;
- full-sync RDB fixtures covering byte-level RDB decode -> persistence records -> engine restore -> stream snapshot.

Operator-reported passing focused matrices:

- `go test -race ./internal/server -run '^TestDecodeRedisLegacyStreamListpacksType(15|19)$' -count=1 -v`
- `go test -race ./internal/server -run '^TestDecodeRedisLegacyStreamGroupsType(15|19)$' -count=1 -v`
- `go test -race ./internal/server -run '^TestDecodeRedisFullSyncLegacyStreamTypes$' -count=1 -v`
- `go test -race ./internal/server -run '^(TestDecodeRedis|TestReplication|TestKeyRestoreRedis|TestKeyRestoreAcceptsRedis)' -count=1 -v`
- `go test -race ./internal/engine -run '^TestStream' -count=1 -v`


## RDB special opcode compatibility

Redis full-sync RDB parsing now also covers the standard special opcodes that materially affect SnugKV interoperability:

- `RDB_OPCODE_FUNCTION2` (245): function-library source is decoded, compiled before commit, installed into the replica function registry during full sync, and persisted through SnugKV's function sidecar. Read-only imported functions are immediately callable through `FCALL_RO` on the replica.
- `RDB_OPCODE_SLOT_INFO` (244): the three length-prefixed slot metadata values are validated and ignored. SnugKV is single-node and does not reconstruct Redis cluster slot topology from RDB metadata.
- `RDB_OPCODE_FUNCTION_PRE_GA` (246): explicitly rejected as an unsupported pre-GA function-library format.
- `RDB_OPCODE_MODULE_AUX` (247): explicitly rejected because payload semantics are owned by the Redis module implementation and cannot be decoded generically without that module.

The existing Redis RDB full-sync API used by keyspace-only callers remains unchanged; an internal decoded-state form carries both persistence records and function-library source for replication application.

Function-library installation is prepared before the durability critical section. Incoming libraries and rollback libraries are compiled before keyspace replacement, then the registry is replaced only after keyspace restore. The function sidecar is persisted during full-sync commit, and the previous registry is restored if the later persistence step fails.

Operator-reported passing validation:

- `go test -race ./internal/server -run '^(TestDecodeRedisFullSyncRDBFunction2|TestDecodeRedisFullSyncRDBSlotInfoIgnored|TestDecodeRedisFullSyncRDBRejectsUnsupportedSpecialOpcodes)$' -count=1 -v`
- `go test -race ./internal/server -run '^TestRedisFullSyncImportsFunction2Libraries$' -count=1 -v`
- `go test -race ./internal/server -run '^(TestDecodeRedisFullSyncRDB|TestRedisFullSync|TestRedisPartialResync|TestFunction|TestReplication)' -count=1 -v`
- `go test -race ./internal/engine -run '^TestStream' -count=1 -v`
