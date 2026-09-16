# Known Limitations

SnugKV is currently an alpha-stage, single-node RESP2 datastore. It has broad
coverage across the common Redis datatype families, including Streams, but it is
not a complete Redis replacement.

## Protocol

- RESP2 is supported.
- RESP3 is not implemented.
- `HELLO 3` is intentionally rejected.
- Clients that default to RESP3 should be configured to use RESP2.

See [COMPATIBILITY.md](COMPATIBILITY.md).

## Redis command coverage

Strings, counters, expiration, bit operations, key inspection, HASH, SET, LIST,
ZSET, Streams/consumer groups, basic JSON, memory inspection, and administration
are implemented to the documented scope.

Major Redis-compatible features still not implemented:

- Pub/Sub;
- transactions (`MULTI`, `EXEC`, `WATCH`, `UNWATCH`, `DISCARD`);
- HyperLogLog;
- GEO;
- Lua scripting;
- Redis Functions;
- RESP3;
- broad CLIENT / CONFIG / ACL compatibility;
- replication;
- Sentinel-style failover;
- cluster mode;
- modules.

The current JSON commands are not a complete RedisJSON implementation.

Streams are broadly implemented, but Redis 8.2 trimming reference-policy selection
(`KEEPREF`, `DELREF`, `ACKED`) remains outstanding. Current trimming preserves PEL
references. SnugKV also has no Redis macro-node representation, so accepted `~`
stream trimming is exact except for an explicit `LIMIT` cap.

## Compatibility hardening still in progress

- `SCAN`, `HSCAN`, `SSCAN`, and `ZSCAN` support cursor/MATCH/COUNT behavior, but
  exact Redis cursor values/page boundaries are not guaranteed.
- Blocking LIST/ZSET/STREAM waiters are released on server shutdown on all
  platforms. Linux builds additionally detect TCP peer disconnects while blocked.
  Equivalent proactive socket-disconnect monitoring is not yet implemented on
  non-Linux builds.
- Some operational/client metadata commands used by Redis tooling are still
  incomplete even when ordinary application workloads work.

## Deployment topology

- SnugKV is single-node.
- There is no automatic replication or failover.
- High availability is not provided by SnugKV itself.

## Durability

SnugKV includes logical AOF/snapshot persistence and recovery testing, including
truncated-final-frame recovery, checksum-corruption rejection, append rollback,
online AOF rewrite, and native datatype restore coverage.

For alpha use:

- maintain external backups for important data;
- test restore procedures;
- do not treat SnugKV as the sole copy of critical data.

## Memory accounting and small-key overhead

`SNUG.STATS` reports engine-accounted memory, not process RSS. RSS additionally
includes Go runtime state, goroutine stacks, network/persistence buffers,
optimizer scratch space, and allocator overhead.

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
- lex ZSET operations construct a temporary lexicographic view rather than keeping
  a second permanent index.
- LIST uses one packed logical blob, so very large head mutations can be O(total
  encoded bytes).
- Streams use packed logical state, including group/PEL metadata, rather than a
  Redis radix-tree/listpack layout.

These tradeoffs are intentional for the current single-node design and should be
revisited only with workload benchmarks that justify extra permanent memory.

## Performance claims

Benchmark and soak results describe specific workloads and hardware. They are not
universal performance claims. Current comparisons use SnugKV engine-accounted
deltas versus Redis `used_memory` deltas, not process RSS. Benchmark SnugKV with
your own workload before capacity decisions.

See [benchmarks/README.md](benchmarks/README.md).

## Security

SnugKV has not received an independent security audit. Follow
[SECURITY.md](SECURITY.md).

## Production use

The current alpha is suitable for evaluation, local development, benchmarks,
experiments, and non-critical caches/queues where data can be recreated. It should
not be presented as a complete replacement for Redis in mission-critical
production systems yet.
