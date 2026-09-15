# Progress

## Current milestone

SnugKV now has a broad single-node RESP2 command surface with native HASH, SET,
LIST, and ZSET types, logical durability, memory accounting, adaptive scalar
encoding, observability, and operational tooling. The current branch includes the
operational ZSET tranche plus blocking ZSET commands (`BZPOPMIN`, `BZPOPMAX`,
`BZMPOP`).

The immediate engineering focus is no longer datatype storage design. HASH, SET,
LIST, and ZSET packed formats are frozen for v1. Remaining work is compatibility
hardening, shared small-key overhead, blocking-client disconnect cleanup, and
release-scale validation.

## Completed

- Bounded streaming RESP2 parsing with fragmentation, pipelining, binary payloads,
  request limits, deadlines, connection limits, and graceful shutdown.
- Sharded collision-safe indexing, segmented arenas, expiration, compaction,
  explicit max-memory accounting, OOM rollback, and sampled LRU eviction.
- String, numeric, bit, expiration, key, JSON, and administration commands.
- Native HASH with packed SH1 plus shared field-shape optimization and full
  hash command coverage for the v1 scope.
- Native SET with canonical packed storage, adaptive singleton/prefix storage,
  algebra/store, move, pop, random-member, and scan commands.
- Native LIST with packed ordered storage, compatibility mutations, cross-key
  moves, and blocking waiter/wakeup commands.
- Native ZSET with adaptive score/member packing, score/lex/rank ranges, algebra,
  pop/random/scan/range-store operations, and blocking pop commands.
- Mixed SET/ZSET algebra with `WEIGHTS` and `AGGREGATE SUM|MIN|MAX|COUNT`.
- Blocking LIST and ZSET waits register before readiness checks, use per-key wakeup
  signaling, and do not hold the AOF durability mutex while sleeping.
- Logical AOF and snapshot persistence with checksums, restart recovery,
  truncated-final-frame handling, corruption rejection, and online AOF rewrite.
- Prometheus metrics and a separate loopback-only administration listener.
- Strict configuration, container files, Make targets, CI, benchmark harnesses,
  and soak tooling.

## Current verification

- Go 1.27.1 is used locally and in CI.
- `go test -race -count=1 ./...` is green for the blocking ZSET implementation.
- `go vet ./...` and RESP fuzz remain part of the required CI gate.
- Blocking ZSET tests cover immediate replies, fractional timeout, key priority,
  wake-on-`ZADD`, `BZMPOP COUNT`, nested RESP2 replies, shutdown cancellation, and
  validation errors.
- A dedicated durability test verifies a sleeping `BZPOPMIN` does not retain
  `durableMu`; only the producer write and eventual pop append durable state.
- AOF restart tests cover generic recovery plus native HASH/SET/LIST/ZSET writes.
- Atomic OOM rollback tests cover multi-key LIST moves, ZSET algebra stores, and
  ZSET range-store/multi-pop paths.
- Local redis-cli smoke tests have validated LIST blocking behavior, ZSET core,
  score ranges, lex ranges, ZSET algebra/store behavior, and non-blocking ZSET pops.

## Native datatype benchmark results

The following are single-run engineering measurements from the local Linux test
machine using 100,000 keys and Redis `used_memory` deltas. They are not RSS
comparisons or universal claims. Exact command/workload details are recorded in
`benchmarks/README.md`.

### HASH — 100k hashes

Shared-schema workload, 16-byte-ish structured fields/values:

| Fields/hash | Snug B/hash | Redis B/hash | Snug memory saving |
|---:|---:|---:|---:|
| 4 | 254.65 | 258.33 | 1.43% |
| 8 | 390.96 | 450.33 | 13.18% |
| 16 | 644.26 | 834.33 | 22.78% |
| 32 | 1252.66 | 1602.33 | 21.82% |
| 64 | 2445.88 | 3138.33 | 22.06% |

The mixed 80/20 shared/unique workload wins from 8 fields upward; fully unique
small hashes remain close to Redis and can lose at low field counts.

### SET — 100k sets, 16-byte members

Sequential/structured members:

| Members/set | Snug B/set | Redis B/set | Snug memory saving |
|---:|---:|---:|---:|
| 1 | 127.73 | 91.78 | -39.16% |
| 4 | 148.87 | 146.33 | -1.73% |
| 8 | 190.64 | 226.33 | 15.77% |
| 16 | 254.39 | 386.33 | 34.15% |
| 32 | 389.71 | 706.33 | 44.83% |
| 64 | 644.32 | 1346.33 | 52.14% |

Dispersed/random-like members cross over later; 32 and 64 members were still
lower than Redis in the recorded run.

### LIST — 100k lists, 16-byte elements

| Elements/list | Snug B/list | Redis B/list | Snug memory saving |
|---:|---:|---:|---:|
| 1 | 128.56 | 92.03 | -39.70% |
| 4 | 170.84 | 146.33 | -16.75% |
| 8 | 255.05 | 226.33 | -12.69% |
| 16 | 389.56 | 386.33 | -0.84% |
| 32 | 683.82 | 706.33 | 3.19% |
| 64 | 1251.95 | 1346.33 | 7.01% |

The SL1 payload itself is compact; the tiny-list deficit is dominated by shared
per-key/index overhead rather than list encoding.

### ZSET — 100k sorted sets, 16-byte structured members

After adaptive integer-score delta encoding and member front coding:

| Members/zset | Snug B/zset | Redis B/zset | Snug memory saving |
|---:|---:|---:|---:|
| 1 | 128.00 | 98.34 | -30.17% |
| 4 | 166.34 | 162.33 | -2.47% |
| 8 | 212.38 | 258.33 | 17.79% |
| 16 | 296.60 | 450.33 | 34.14% |
| 32 | 454.86 | 834.33 | 45.48% |
| 64 | 782.73 | 1602.33 | 51.15% |

The adaptive codec falls back to raw members and/or float64 scores when prefix or
integer-delta encoding would not reduce the physical representation.

## Scalar benchmark results

Recorded 10,000-key scalar runs showed engine-accounted memory reductions of
43.1% for session JSON and 35.1% for API JSON with JSON-shape encoding. A
synthetic highly repetitive 4 KiB workload reduced accounted memory by 91.0%
with LZ4. Random and already-compressed inputs stayed raw. See
`benchmarks/README.md` for the exact workload caveats.

## Remaining engineering work

- Strict WRONGTYPE audit for legacy scalar/numeric/bit commands against native containers.
- Infinite blocking client-disconnect detection hardening.
- Scan/glob compatibility audit.
- Shared per-key overhead reduction: 24-byte index slots, 40-byte common entries,
  reservation growth, sparse-shard entry floors, and first arena-segment cost.
- Fresh dedicated Redis benchmark baselines, multi-run variance, million-record
  datasets, and retained 24-hour soak evidence.
- Broader client compatibility testing.

See `PLAN.md` for the prioritized backlog and `KNOWN-LIMITATIONS.md` for public
product boundaries.
