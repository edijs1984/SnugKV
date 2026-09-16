# SnugKV Delivery Plan

This document is the current implementation roadmap. `PROGRESS.md` contains
verification evidence, `COMPATIBILITY.md` contains the public Redis boundary, and
GitHub issue #55 tracks command-family compatibility work.

## Current status

The single-node RESP2 engine, logical persistence, memory accounting, optimizer,
observability, packaging, HASH, SET, LIST, ZSET, broad STREAM/consumer-group
support, classic/sharded Pub/Sub, Redis-style transactions/WATCH, HyperLogLog,
modern GEO, and the common Lua scripting path are implemented.

Streams include core reads/writes, blocking `XREAD`, consumer groups,
`XREADGROUP`, PEL inspection/acknowledgement, claims/autoclaims, XINFO,
MAXLEN/MINID trimming, lifetime `entries-added` / `max-deleted-entry-id`,
separate consumer idle/inactive tracking, Redis 8.2 `KEEPREF` / `DELREF` /
`ACKED` reference policies, `XDELEX`, and `XACKDEL`.

Transactions include `MULTI`, `EXEC`, `DISCARD`, `WATCH`, and `UNWATCH`, with
queue-time EXECABORT semantics, runtime errors preserved inside EXEC arrays,
cross-client WATCH invalidation including change-then-restore, expiration
invalidation, nonblocking execution of blocking commands inside MULTI, and one
logical AOF frame for transaction results.

Lua scripting now includes `EVAL`, `EVALSHA`, `SCRIPT LOAD/EXISTS/FLUSH`,
`KEYS`/`ARGV`, the common `redis.call`/`redis.pcall` bridge, RESP2/Lua reply
conversion, volatile SHA-1 caching, a bounded runtime, MULTI/EXEC integration,
WATCH invalidation, and one-frame logical AOF persistence for script results.
Redis Functions and the remaining scripting-management/read-only commands remain.

HyperLogLog implements `PFADD`, `PFCOUNT`, and `PFMERGE` with Redis-compatible
STRING serialization. The 100,000-member development comparison produced the
same estimate, length, and SHA-256 payload as Redis.

Modern GEO implements `GEOADD`, `GEODIST`, `GEOHASH`, `GEOPOS`, `GEOSEARCH`, and
`GEOSEARCHSTORE` on top of native ZSET storage using Redis-compatible 52-bit
geospatial scores. GEOSEARCH currently favors memory efficiency over a secondary
spatial index and scans the source packed ZSET.

Shared sparse-memory overhead has also been reduced substantially. On the canonical
1,000-key / 256-shard / 16-byte-value benchmark, accounted memory moved from
527,768 bytes to 179,224 bytes (-66.04%). Current sparse layout measurements include
16-byte index slots, 32-byte common entries, 192 bytes of static shard structure,
and 24-byte arena segment descriptors.

## Completed milestones

### Core engine and protocol

- [x] Go module, TCP server, bounded streaming RESP2 parser, pipelining, binary-safe values.
- [x] Sharded open-addressed key index and segmented arenas.
- [x] Exact byte round trips, lazy/active expiration, TTL mutation semantics.
- [x] Cross-key atomic paths for multi-key writes and datatype move/store operations.
- [x] Memory accounting, max-memory admission, rollback on OOM, compaction, sampled LRU eviction.
- [x] Graceful shutdown, connection limits, read/write bounds, panic recovery.
- [x] Per-connection cancellation for blocking LIST/ZSET/STREAM commands.
- [x] Linux TCP peer-disconnect detection for blocked clients using non-consuming socket hangup polling.

### Scalar storage and optimizer

- [x] Raw scalar storage and codec registry.
- [x] Canonical integer, UUID, timestamp, JSON-shape, dictionary, LZ4, and Zstandard candidates.
- [x] Exact reconstruction verification and raw fallback.
- [x] Budgeted background optimizer with stale-rewrite rejection and hysteresis.
- [x] Strict native-container type guards across legacy string/numeric/bitmap commands.
- [x] Native HASH/SET/LIST/ZSET/STREAM optimizer exclusion.

### Persistence and operations

- [x] Checksummed logical AOF and snapshot persistence.
- [x] Restart recovery, truncated-final-frame handling, corruption rejection.
- [x] Online AOF rewrite and append-failure rollback.
- [x] Prometheus metrics and loopback-only administration listener.
- [x] Docker/Compose, Make targets, CI, soak and benchmark harnesses.

### Native HASH / SET / LIST / ZSET

- [x] Native packed HASH with broad command coverage.
- [x] Native packed SET with algebra/store/move/pop/random/scan operations.
- [x] Native packed LIST with moves and blocking waiter/wakeup operations.
- [x] Native packed ZSET with rank/score/lex ranges, algebra, multipops and blocking pops.
- [x] TTL, persistence, WRONGTYPE, OOM rollback, race and RESP coverage across the implemented surfaces.

### HyperLogLog

- [x] `PFADD`, `PFCOUNT`, and `PFMERGE`.
- [x] Redis-compatible sparse/dense serialized HLL STRING values.
- [x] Duplicate-add, union/merge, TTL, invalid-object, and large-cardinality coverage.
- [x] Manual Redis comparison at 100,000 members: identical `PFCOUNT` (99,471), `STRLEN` (12,304), and payload SHA-256.

### GEO

- [x] Redis-compatible 52-bit longitude/latitude score encoding on native ZSET storage.
- [x] `GEOADD` with `NX`, `XX`, and `CH`, preserving an existing destination TTL.
- [x] `GEODIST`, `GEOHASH`, and `GEOPOS`.
- [x] `GEOSEARCH` with `FROMMEMBER` / `FROMLONLAT`, `BYRADIUS` / `BYBOX`, ordering, COUNT/ANY, and WITH* result options.
- [x] `GEOSEARCHSTORE` with replacement semantics, TTL clearing, and `STOREDIST`.
- [x] OOM retry protection keeps both GEOSEARCHSTORE source and destination from eviction.
- [ ] Optional legacy `GEORADIUS*` aliases if real client compatibility requires them.
- [ ] Dedicated large geospatial performance benchmark and potential score-range pruning/indexing if O(N) search becomes a measured bottleneck.

### Lua scripting core

- [x] `EVAL` and `EVALSHA` with Redis-style key/argument splitting.
- [x] `SCRIPT LOAD`, `SCRIPT EXISTS`, and `SCRIPT FLUSH [SYNC|ASYNC]`.
- [x] Volatile SHA-1 cache populated by `SCRIPT LOAD` and successfully compiled `EVAL` scripts.
- [x] Lua 5.1-compatible runtime with `KEYS`, `ARGV`, `redis.call`, `redis.pcall`, `redis.error_reply`, `redis.status_reply`, and `redis.sha1hex`.
- [x] RESP2/Lua conversions for integers, strings, arrays, null/false, status replies, and error replies.
- [x] Five-second execution limit and no filesystem/process Lua libraries.
- [x] Atomic client-visible execution under the existing command-serialization mutex.
- [x] MULTI/EXEC execution and transient WATCH invalidation across script writes.
- [x] One logical AOF frame for direct script results; writes before a later runtime error remain durable.
- [ ] `EVAL_RO` / `EVALSHA_RO`.
- [ ] `SCRIPT KILL`, `SCRIPT DEBUG`, and broader SCRIPT subcommand parity.
- [ ] Redis Functions (`FUNCTION`, `FCALL`, `FCALL_RO`).
- [ ] Differential Redis audit of Lua edge cases, command flags, ACL semantics, and OOM/eviction behavior.

### Native STREAM

- [x] Versioned packed stream format with backward decode.
- [x] `XADD`, `XLEN`, `XRANGE`, `XREVRANGE`, `XDEL`, `XTRIM`.
- [x] `MAXLEN` and `MINID` trimming, including XADD trimming options and LIMIT caps.
- [x] Redis 8.2 `KEEPREF`, `DELREF`, and `ACKED` trimming/deletion reference policies.
- [x] `XDELEX` and `XACKDEL` per-ID status semantics across consumer groups.
- [x] `XREAD` including BLOCK and disconnect/shutdown cancellation.
- [x] `XGROUP CREATE/DESTROY/SETID/CREATECONSUMER/DELCONSUMER`.
- [x] `XREADGROUP`, `XACK`, `XPENDING`.
- [x] `XCLAIM`, `XAUTOCLAIM` including deleted-PEL cleanup and retry metadata.
- [x] `XINFO STREAM/GROUPS/CONSUMERS/HELP`.
- [x] Durable PEL/group state and restart/export-restore coverage.
- [x] Lifetime `entries-added` and `max-deleted-entry-id` metadata.
- [x] Distinct consumer attempted/successful timestamps for `idle` vs `inactive`.
- [x] Production optimizer regression: STREAM is never rewritten by scalar codecs.

### Pub/Sub

- [x] Classic `SUBSCRIBE` / `UNSUBSCRIBE` / `PSUBSCRIBE` / `PUNSUBSCRIBE` / `PUBLISH`.
- [x] Sharded `SSUBSCRIBE` / `SUNSUBSCRIBE` / `SPUBLISH`.
- [x] `PUBSUB` classic/sharded introspection.
- [x] RESP2 subscribed-mode restrictions, subscribed `PING`, `RESET`, asynchronous push delivery, disconnect cleanup, and serialized socket writes.

### Transactions / optimistic locking

- [x] `MULTI`, `EXEC`, `DISCARD`, `WATCH`, `UNWATCH`.
- [x] Queue-time errors abort EXEC; runtime errors remain individual EXEC array elements.
- [x] Atomic EXEC relative to other client commands.
- [x] Cross-client WATCH invalidation, including change-then-restore and key expiration.
- [x] Blocking commands execute nonblockingly inside MULTI/EXEC.
- [x] One-frame logical AOF persistence for transaction results with rollback on append failure.
- [x] Two-client TCP, race, full-suite, vet, fuzz, and live Python smoke validation.

### SCAN family compatibility

- [x] Binary-safe MATCH implementation for keyspace/HASH/SET/ZSET scans.
- [x] Redis-style glob syntax and empty-pattern semantics.
- [x] COUNT-as-work-hint behavior.
- [x] Global `SCAN ... TYPE` filtering.

### Shared memory overhead

- [x] Compact index slot to 16 bytes.
- [x] Compact common entry representation to 32 bytes using sidecars where needed.
- [x] Reduce sparse index initial capacity and allow full occupancy for 4-slot tables.
- [x] Add tiny full-table negative lookup filter without increasing table struct size.
- [x] Stage sparse entry capacity growth through 4/6/8 slots.
- [x] Reduce tiny arena first segment size and make freelists lazy.
- [x] Compact arena segment descriptor to 24 bytes.
- [x] Correct structural accounting and embed/compact index table metadata.
- [x] Verify sparse benchmark reduction from 527,768 to 179,224 accounted bytes (-66.04%).

## Remaining work / TODO

### P0 — Streams differential hardening

- [ ] Differential Redis edge-case audit for stream ID/trimming/group/claim/XINFO/reference-policy semantics.

### P1 — major Redis command families

- [x] HyperLogLog: `PFADD`, `PFCOUNT`, `PFMERGE`.
- [x] Modern GEO: `GEOADD`, `GEODIST`, `GEOHASH`, `GEOPOS`, `GEOSEARCH`, `GEOSEARCHSTORE`.
- [x] Lua scripting core: `EVAL`, `EVALSHA`, `SCRIPT LOAD/EXISTS/FLUSH` and common `redis.*` bridge.
- [ ] Redis Functions and remaining scripting parity/hardening.
- [ ] `SORT` / `SORT_RO`.
- [ ] `COPY` and migration scope decision.

### P2 — client/tooling compatibility

- [ ] RESP3.
- [ ] CLIENT subcommands needed by major Redis clients.
- [ ] CONFIG compatibility needed by common tooling.
- [ ] ACL/authentication scope.
- [ ] COMMAND metadata completeness.
- [ ] Equivalent proactive blocked-client disconnect detection for non-Linux server builds if cross-platform parity is required.

### P3 — memory/performance validation

The large sparse fixed-overhead wins are complete; further memory work must justify
its complexity with measurements.

- [ ] Re-evaluate remaining index/entry/arena slack using representative workloads rather than structural size alone.
- [ ] Fresh dedicated Redis baselines for public comparison claims.
- [ ] Multi-run variance rather than single-run latency snapshots.
- [ ] Million-record datasets on dedicated hardware.
- [ ] Retain evidence from a 24-hour mixed workload soak.
- [ ] Expand third-party client compatibility tests.
- [ ] Benchmark large GEO sets before adding permanent geospatial indexing.
- [ ] Benchmark script compile/execute overhead and cache-hit behavior before pooling Lua states or compiled chunks.

### P4 — distributed features (outside current single-node target)

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
- add OOM/rollback coverage for atomic multi-key writes where applicable;
- test production configuration interactions for native datatypes;
- document observable compatibility differences;
- publish performance or memory claims only with reproducible benchmark details.
