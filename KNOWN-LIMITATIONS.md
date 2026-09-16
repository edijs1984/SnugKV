# Known Limitations

SnugKV is currently an alpha-stage, single-node RESP2 datastore. It has broad
coverage across the common Redis datatype families, including Streams, Pub/Sub,
transactions, HyperLogLog, modern GEO, and the common Lua scripting path, but it
is not a complete Redis replacement.

## Protocol

- RESP2 is supported.
- RESP3 is not implemented.
- `HELLO 3` is intentionally rejected.
- Clients that default to RESP3 should be configured to use RESP2.

See [COMPATIBILITY.md](COMPATIBILITY.md).

## Redis command coverage

Strings, counters, expiration, bit operations, key inspection, HASH, SET, LIST,
ZSET, HyperLogLog, modern GEO, Streams/consumer groups, classic/sharded Pub/Sub,
transactions/WATCH, partial Lua scripting, basic JSON, memory inspection, and
administration are implemented to the documented scope.

Major Redis-compatible features still not implemented or incomplete:

- Redis Functions (`FUNCTION`, `FCALL`, `FCALL_RO`);
- full Lua scripting parity (`EVAL_RO`, `EVALSHA_RO`, `SCRIPT KILL/DEBUG`, exact command flags/ACL behavior);
- `SORT` / `SORT_RO`;
- full COPY/migration scope;
- RESP3;
- broad CLIENT / CONFIG / ACL compatibility;
- replication;
- Sentinel-style failover;
- cluster mode;
- modules.

The current JSON commands are not a complete RedisJSON implementation.

## Lua scripting boundaries

The implemented scripting surface is `EVAL`, `EVALSHA`, `SCRIPT LOAD`,
`SCRIPT EXISTS`, and `SCRIPT FLUSH [SYNC|ASYNC]`. Scripts receive `KEYS` and
`ARGV` and can use `redis.call`, `redis.pcall`, `redis.error_reply`,
`redis.status_reply`, and `redis.sha1hex`.

Current boundaries are intentional and documented rather than silently emulated:

- scripts run in an embedded Lua 5.1-compatible runtime, not Redis's exact Lua VM;
- filesystem/process libraries are removed;
- each invocation has a five-second execution limit;
- blocking commands, subscription/connection state, transaction commands, nested
  scripting, and SnugKV admin commands are rejected through the script bridge;
- the script cache is volatile and process-local;
- `SCRIPT KILL`, `SCRIPT DEBUG`, Redis Functions, and read-only EVAL variants are
  not implemented;
- exact Redis command-flag, ACL, OOM/eviction, and every Lua edge-case still need
  differential testing.

Scripts are atomic with respect to other SnugKV clients because they execute under
the same command-serialization boundary as transactions. Lua runtime errors do not
roll back successful writes already made by the script. With AOF enabled, those
resulting logical changes are persisted in one frame even when the script later
returns a runtime error.

Modern GEO commands are implemented, but deprecated `GEORADIUS`,
`GEORADIUSBYMEMBER`, `GEORADIUS_RO`, and `GEORADIUSBYMEMBER_RO` aliases are not.
`GEOSEARCH` currently scans/decodes the packed source ZSET rather than maintaining
a permanent secondary geospatial index, making searches O(source cardinality).
This is a deliberate memory/performance tradeoff pending large-GEO benchmarks.

Streams include Redis 8.2 `KEEPREF`, `DELREF`, and `ACKED` reference-policy
selection plus `XDELEX` and `XACKDEL`. SnugKV has no Redis macro-node
representation, so accepted `~` stream trimming is exact except for an explicit
`LIMIT` cap. A differential Redis edge-case audit can still uncover small semantic
differences even though no known core Streams command-family gap remains.

Transactions implement `MULTI`, `EXEC`, `DISCARD`, `WATCH`, and `UNWATCH`,
including cross-client WATCH invalidation and change-then-restore detection.
Current RESP2 limitation: Pub/Sub subscription-state commands are not supported as
queued MULTI commands; `PUBLISH` and `SPUBLISH` remain ordinary queueable commands.

## Compatibility hardening still in progress

- `SCAN`, `HSCAN`, `SSCAN`, and `ZSCAN` support cursor/MATCH/COUNT behavior, but
  exact Redis cursor values/page boundaries are not guaranteed.
- Blocking LIST/ZSET/STREAM waiters are released on server shutdown on all
  platforms. Linux builds additionally detect TCP peer disconnects while blocked.
  Equivalent proactive socket-disconnect monitoring is not yet implemented on
  non-Linux builds.
- Some operational/client metadata commands used by Redis tooling are still
  incomplete even when ordinary application workloads work.
- The modern GEO command set has focused command-level compatibility tests; large
  dataset differential/performance testing is intentionally still pending.
- Lua scripting has focused unit/durability/transaction coverage; live redis-cli
  and direct Redis differential testing should be completed before claiming full
  scripting compatibility.

## Deployment topology

- SnugKV is single-node.
- There is no automatic replication or failover.
- High availability is not provided by SnugKV itself.

## Durability

SnugKV includes logical AOF/snapshot persistence and recovery testing, including
truncated-final-frame recovery, checksum-corruption rejection, append rollback,
online AOF rewrite, native datatype restore coverage, single logical AOF frames
for successful transaction results, and single logical frames for direct script
mutations including partial writes before runtime errors.

For alpha use:

- maintain external backups for important data;
- test restore procedures;
- do not treat SnugKV as the sole copy of critical data.

## Memory accounting and small-key overhead

`SNUG.STATS` reports engine-accounted memory, not process RSS. RSS additionally
includes Go runtime state, goroutine stacks, network/persistence buffers,
optimizer scratch space, allocator overhead, and temporary Lua VM state while a
script is executing.

The sparse 1,000-key / 256-shard / 16-byte-value benchmark improved from 527,768
to 179,224 accounted bytes, a 66.04% reduction. Current measured sparse layout:

- 16-byte index slots;
- 32-byte common entries;
- 192 bytes of static shard structure;
- 24-byte arena segment descriptors;
- 1,184 entry slots for 1,000 keys in the canonical benchmark.

This removes much of the earlier structural waste, but tiny keys/containers can
still lose to Redis because fixed per-key index, entry, and arena overhead remains
material. Further structural changes should be benchmark-driven rather than
assumed to be wins.

## Datatype performance characteristics

Native containers favor memory efficiency over asymptotically optimal very-large
collection mutation:

- HASH/SET/LIST/ZSET/STREAM mutations may decode and re-encode packed values.
- ZSET member lookup/update is linear in member count; there is no permanent
  skiplist/tree/member index.
- GEOSEARCH currently scans the ZSET and decodes candidate scores, so it is O(N)
  in the source set rather than using Redis-style geohash range pruning.
- lex ZSET operations construct a temporary lexicographic view rather than keeping
  a second permanent index.
- LIST uses one packed logical blob, so very large head mutations can be O(total
  encoded bytes).
- Streams use packed logical state, including group/PEL metadata, rather than a
  Redis radix-tree/listpack layout.
- the current Lua implementation creates an isolated VM per invocation rather than
  pooling VM state or compiled chunks, favoring isolation/simplicity over minimum
  script-call latency.

These tradeoffs are intentional for the current single-node design and should be
revisited only with workload benchmarks that justify extra permanent memory or
complexity.

## Performance claims

Benchmark and soak results describe specific workloads and hardware. They are not
universal performance claims. Current comparisons use SnugKV engine-accounted
deltas versus Redis `used_memory` deltas, not process RSS. Benchmark SnugKV with
your own workload before capacity decisions.

See [benchmarks/README.md](benchmarks/README.md).

## Security

SnugKV has not received an independent security audit. The scripting runtime is
sandboxed by omitting filesystem/process libraries and limiting execution time,
but this is not a substitute for a security review. Follow [SECURITY.md](SECURITY.md).

## Production use

The current alpha is suitable for evaluation, local development, benchmarks,
experiments, and non-critical caches/queues where data can be recreated. It should
not be presented as a complete replacement for Redis in mission-critical
production systems yet.
