# Known Limitations

SnugKV is currently an alpha-stage, single-node RESP2 datastore. It has a much
broader command surface than the original alpha docs described, but it is not a
complete Redis replacement.

## Protocol

- RESP2 is supported.
- RESP3 is not implemented.
- `HELLO 3` is intentionally rejected.
- Clients that default to RESP3 should be configured to use RESP2.

See [COMPATIBILITY.md](COMPATIBILITY.md).

## Redis command coverage

HASH, SET, LIST, and ZSET are implemented as native datatypes with broad command
coverage. Strings, counters, expiration, bit operations, key inspection, basic
JSON, memory inspection, and administration are also implemented. LIST and ZSET
blocking pop/move operations use waiter/wakeup paths rather than polling.

Not yet implemented as general Redis-compatible features:

- streams;
- Pub/Sub;
- Lua scripting;
- Redis Functions;
- transactions (`MULTI`, `EXEC`, `WATCH`, `UNWATCH`, `DISCARD`);
- replication;
- Sentinel-style failover;
- cluster mode;
- modules.

The current JSON commands are not a complete RedisJSON implementation.

## Compatibility hardening still in progress

- `SCAN`, `HSCAN`, `SSCAN`, and `ZSCAN` support cursor/MATCH/COUNT behavior, but
  exact Redis cursor progression and every glob edge case are not guaranteed.
- Blocking LIST/ZSET waiters are released on server shutdown. An infinitely
  blocked client that disconnects is not yet proactively detected until another
  wakeup or shutdown occurs.

Legacy string/numeric/bitmap commands now guard native HASH/SET/LIST/ZSET values
instead of decoding packed container bytes. Redis-specific exceptions such as
`MGET` nil slots, non-destructive `GETDEL` on non-string keys, and destination
overwrite behavior for `SET`/`BITOP` are covered by regression tests.

## Deployment topology

- SnugKV is single-node.
- There is no automatic replication or failover.
- High availability is not provided by SnugKV itself.

## Durability

SnugKV includes logical AOF/snapshot persistence and recovery testing, including
truncated-final-frame recovery, checksum-corruption rejection, append rollback,
and online AOF rewrite.

For alpha use:

- maintain external backups for important data;
- test restore procedures;
- do not treat SnugKV as the sole copy of critical data.

## Memory accounting and small-key overhead

`SNUG.STATS` reports engine-accounted memory, not process RSS. RSS additionally
includes Go runtime state, goroutine stacks, network/persistence buffers,
optimizer scratch space, and allocator overhead.

Native container payloads are compact, but the engine currently pays meaningful
fixed per-key overhead. In 100k-key datatype benchmarks the index reservation was
about 31.5 B/key and entry/key accounting about 54–55 B/key before container
payload. This is why one-element HASH/SET/LIST/ZSET values can use more memory than
Redis even when SnugKV's packed payload is small.

Sparse datasets are a separate weakness: 256 shards, per-shard entry reservation,
and initial arena segments can dominate memory at around 1,000 keys. Reducing this
shared overhead is a planned engine-wide optimization.

## Datatype performance characteristics

Native containers currently favor memory efficiency over asymptotically optimal
large-collection operations:

- HASH/SET/LIST/ZSET operations may decode and re-encode packed values for mutation.
- ZSET member lookup/update is linear in member count; there is no permanent
  skiplist/tree/member index.
- lex ZSET operations construct a temporary lexicographic view rather than keeping
  a second in-memory index.
- LIST uses one packed logical blob, so very large head mutations can be O(total
  encoded bytes).

These tradeoffs are intentional for v1 and should be revisited only with workload
benchmarks that justify extra permanent memory.

## Performance claims

Benchmark and soak results describe specific workloads and hardware. They are not
universal performance claims. Current native-container comparisons use SnugKV
engine-accounted deltas versus Redis `used_memory` deltas, not process RSS.
Benchmark SnugKV with your own workload before capacity decisions.

See [benchmarks/README.md](benchmarks/README.md).

## Security

SnugKV has not received an independent security audit. Follow
[SECURITY.md](SECURITY.md).

## Production use

The current alpha is suitable for evaluation, local development, benchmarks,
experiments, and non-critical caches where data can be recreated. It should not
be presented as a complete replacement for Redis in mission-critical production
systems yet.
