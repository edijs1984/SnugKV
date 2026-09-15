# Changelog

All notable changes to SnugKV will be documented in this file.

## [Unreleased]

### Added

- Native HASH datatype with packed SH1 storage, adaptive shared field-shape storage,
  numeric operations, scan, random-field support, TTL/rename integration, and
  logical persistence.
- Native SET datatype with canonical packed storage, adaptive singleton/prefix
  physical forms, membership, scan, algebra/store, move, pop, and random-member
  commands.
- Native LIST datatype with packed ordered storage, compatibility mutations,
  atomic `LMOVE`/`RPOPLPUSH`, and blocking `BLPOP`, `BRPOP`, `BLMOVE`, and
  `BRPOPLPUSH` waiter/wakeup support.
- Native ZSET datatype with adaptive integer score delta encoding, member front
  coding, rank/score/lex ranges, algebra/store commands, pop/random/scan commands,
  and `ZRANGESTORE`.
- Mixed SET/ZSET `ZUNION`/`ZINTER` inputs with `WEIGHTS` and
  `AGGREGATE SUM|MIN|MAX|COUNT`.
- `ZPOPMIN`, `ZPOPMAX`, `ZMPOP`, `ZMSCORE`, `ZRANDMEMBER`, `ZSCAN`, and
  `ZRANGESTORE`, including dynamic durability-key handling for multi-key pops.
- Dedicated HASH, SET, LIST, and ZSET benchmark harnesses and documented 100k-key
  memory comparison matrices.
- RESP2 TCP server with bounded protocol parsing.
- Sharded in-memory storage engine.
- Expiration and TTL operations.
- Memory limits and inspection commands.
- Adaptive canonical encodings for selected scalar value types.
- Optional JSON-shape encoding and compression candidates.
- Logical persistence with checksum-protected frames.
- Separate admin listener for `SNUG.*` diagnostics and controls.
- Compatibility smoke tests for ioredis, node-redis, redis-py, and go-redis.
- TCP client soak harness and engine soak workload.
- AGPL-3.0 licensing with separate commercial-license terms available.

### Changed

- Native container formats are now excluded from the generic scalar optimizer.
- HASH, SET, LIST, and ZSET storage designs are frozen for v1; further memory work
  is directed toward shared index/entry/shard/arena overhead.
- Documentation now reflects the implemented native datatype command surface and
  current compatibility boundaries.

### Hardened

- Atomic max-memory rollback for native container mutations and multi-key stores.
- AOF restart coverage for HASH, SET, LIST, and ZSET mutations.
- Blocking LIST commands wait outside the durability mutex.
- Dynamic durability snapshots for `ZMPOP` candidate keys.
- Large arena allocations up to the RESP bulk-size boundary.
- Connection-level panic recovery.
- Exact RESP maximum-bulk boundary behavior.
- Lazy JSON-shape store allocation.
- Persistence restart and corruption recovery tests.

### Verified

- Full race suite, `go vet`, and RESP fuzz are green for the operational ZSET branch.
- Local redis-cli smoke tests validated LIST blocking behavior and ZSET core, range,
  lex, algebra, and store semantics.
- 100k-key native datatype benchmark matrices are recorded in
  `benchmarks/README.md`.
- One-hour engine and TCP/RESP soak runs completed with zero mismatches/client errors
  in the earlier alpha validation cycle.

## [0.1.0-alpha] - 2026-09-14

Initial public alpha release.
