# SnugKV Delivery Plan

This document is the current implementation roadmap. Completed work is kept here
only at milestone level; `PROGRESS.md` contains verification evidence and
`COMPATIBILITY.md` contains the public command/protocol boundary.

## Current status

The single-node RESP2 engine, persistence, memory accounting, optimizer,
observability, packaging, and the main Redis-style native container types are
implemented. HASH, SET, LIST, and the intended ZSET v1 command/storage surface are
implemented, including non-polling blocking operations for LIST and ZSET.

The packed storage formats for HASH, SET, LIST, and ZSET are frozen for v1. The
legacy scalar/native-container WRONGTYPE audit is complete for the current command
surface. Linux TCP builds now proactively cancel blocked LIST/ZSET waiters when a
client disconnects without consuming queued protocol bytes. Shared-memory work is
now targeting engine-wide fixed overhead: sparse shards use small adaptive entry
pools and compact first arena segments before scaling to the existing dense-store
policies.

## Completed milestones

### Core engine and protocol

- [x] Go module, TCP server, bounded streaming RESP2 parser, pipelining, binary-safe values.
- [x] Sharded open-addressed key index and segmented arenas.
- [x] Exact byte round trips, lazy/active expiration, TTL mutation semantics.
- [x] Cross-key atomic paths for multi-key writes and datatype move/store operations.
- [x] Memory accounting, max-memory admission, rollback on OOM, compaction, sampled LRU eviction.
- [x] Graceful shutdown, connection limits, read/write bounds, panic recovery.
- [x] Per-connection cancellation for blocking LIST/ZSET commands.
- [x] Linux TCP peer-disconnect detection for blocked clients using non-consuming socket hangup polling.

### Scalar storage and optimizer

- [x] Raw scalar storage and codec registry.
- [x] Canonical integer, UUID, timestamp, JSON-shape, dictionary, LZ4, and Zstandard candidates.
- [x] Exact reconstruction verification and raw fallback.
- [x] Budgeted background optimizer with stale-rewrite rejection and hysteresis.
- [x] Strict native-container type guards across legacy string/numeric/bitmap commands.
- [x] Redis-compatible non-string exceptions for `MGET` and `GETDEL`.
- [x] RESP `-WRONGTYPE` prefix preservation and cross-datatype regression coverage.

### Persistence and operations

- [x] Checksummed logical AOF and snapshot persistence.
- [x] AOF restart recovery, truncated-final-frame handling, corruption rejection.
- [x] Online AOF rewrite and append-failure rollback.
- [x] Prometheus metrics and loopback-only administration listener.
- [x] Docker/Compose, Make targets, CI, soak and benchmark harnesses.

### Native HASH

- [x] Canonical SH1 packed hashes.
- [x] Adaptive shared field-shape physical representation (SH2) with logical SH1 persistence.
- [x] Core, numeric, random, and scan command coverage.
- [x] TTL, rename, persistence, race, and benchmark coverage.

### Native SET

- [x] Canonical SS1 packed sets.
- [x] Adaptive singleton and prefix-coded physical storage.
- [x] Membership, scan, algebra/store, move, pop, and random-member commands.
- [x] TTL, persistence, atomicity, race, and benchmark coverage.

### Native LIST

- [x] Canonical ordered SL1 packed lists.
- [x] Push/pop/index/range and compatibility mutation commands.
- [x] `LMOVE` / `RPOPLPUSH` atomic cross-key operations.
- [x] `BLPOP`, `BRPOP`, `BLMOVE`, `BRPOPLPUSH` waiter/wakeup architecture.
- [x] AOF-safe blocking behavior: waits do not hold the durability mutex.
- [x] TTL, OOM rollback, restart, race, and benchmark coverage.

### Native ZSET

- [x] Canonical score/member ordering with packed adaptive storage.
- [x] Integer score delta-varints with float64 fallback.
- [x] Adaptive member prefix/front coding with raw-member fallback.
- [x] Rank, score-range, lex-range, removal, and modern `ZRANGE` modes.
- [x] `ZUNION`, `ZINTER`, `ZDIFF` and STORE variants, including mixed SET/ZSET sources.
- [x] `WEIGHTS`, `AGGREGATE SUM|MIN|MAX|COUNT`, `ZINTERCARD`.
- [x] `ZPOPMIN`, `ZPOPMAX`, `ZMPOP`, `ZMSCORE`, `ZRANDMEMBER`, `ZSCAN`, `ZRANGESTORE`.
- [x] `BZPOPMIN`, `BZPOPMAX`, `BZMPOP` with per-key waiter/wakeup handling.
- [x] Blocking ZSET waits execute the eventual pop through the normal durable path without holding `durableMu` while sleeping.
- [x] Dynamic durability key discovery for `ZMPOP` and destination-as-source safety for stores.
- [x] TTL, OOM rollback, persistence, race, RESP, and benchmark coverage.

## Remaining work / TODO

### P0 — harden compatibility of the completed datatype surface

- [ ] Review scan compatibility edge cases (`MATCH`, `COUNT`, glob character classes, cursor semantics) across `SCAN`, `HSCAN`, `SSCAN`, and `ZSCAN`.
- [ ] Add equivalent proactive blocked-client disconnect detection for non-Linux server builds if cross-platform server parity is required for v1.

### P1 — shared memory overhead

Current 100k-key container benchmarks show roughly 31.5 B/key of index reservation
plus roughly 54–55 B/key of entry/key accounting before the container payload.
Tiny containers therefore remain weaker than Redis even when the packed payload is
smaller. Sparse 1k-key workloads previously paid an additional multiplier from a
64-entry first reservation and an 8 KiB first arena segment in every active shard.

- [x] Replace the 64-entry first reservation with an adaptive 8→16→32→64 entry-pool ramp, then 25% growth.
- [x] Use a roughly 1 KiB exact-multiple first arena segment for small allocations; retain 8 KiB subsequent segments for dense workloads.
- [x] Add sparse-layout diagnostics and a dedicated `cmd/sparsebench` harness.
- [ ] Evaluate reducing the 24-byte index slot while preserving collision safety.
- [ ] Evaluate reducing the 40-byte common entry representation or moving more fields into optional sidecars.
- [ ] Measure mutation throughput/reallocation cost after the adaptive entry-pool change and tune thresholds only with evidence.
- [ ] Evaluate lazy/shared arena segment pools if the smaller first segment does not sufficiently address very sparse stores.
- [ ] Re-run STRING/HASH/SET/LIST/ZSET 100k-key benchmarks to verify dense-workload memory remains stable.
- [ ] Re-run 1k/10k sparse matrices with `cmd/sparsebench` and record before/after deltas.

### P2 — release validation

- [ ] Run fresh dedicated Redis baselines for public comparison claims.
- [ ] Run multi-run variance rather than single-run memory/latency snapshots.
- [ ] Run million-record datasets on dedicated hardware.
- [ ] Retain evidence from a 24-hour mixed workload soak.
- [ ] Expand third-party client compatibility tests beyond the current smoke matrix.
- [ ] Publish a versioned container image when registry/credentials are chosen.

### P3 — protocol/application compatibility beyond v1

- [ ] RESP3.
- [ ] Transactions: `MULTI`, `EXEC`, `WATCH`, `UNWATCH`, `DISCARD`.
- [ ] Streams.
- [ ] Pub/Sub.
- [ ] Lua scripting / Redis Functions or an explicitly different extension model.

### P4 — distributed features (not part of current single-node v1)

- [ ] Replication.
- [ ] Automatic failover / Sentinel-like behavior.
- [ ] Cluster/sharding protocol.
- [ ] Multi-node consistency and recovery model.

## Release discipline

Before treating a feature as complete:

- run `go test -race -count=1 ./...`;
- run `go vet ./...`;
- keep RESP fuzz green;
- add persistence/restart coverage for durable writes;
- add OOM/rollback coverage for atomic multi-key writes;
- document observable compatibility differences;
- publish performance or memory claims only with reproducible benchmark details.
