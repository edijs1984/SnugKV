# Known Limitations

SnugKV is currently an alpha-stage, single-node RESP2 datastore.

## Protocol

- RESP2 is supported.
- RESP3 is not implemented.
- `HELLO 3` is intentionally rejected.
- Clients that default to RESP3 should be configured to use RESP2.

See [COMPATIBILITY.md](COMPATIBILITY.md).

## Redis command coverage

SnugKV implements a useful Redis-compatible subset focused on strings, numeric
values, expiration, bit operations, key inspection, basic JSON, memory
inspection, and administration.

Not yet implemented as general Redis-compatible features:

- hashes;
- sets;
- lists;
- sorted sets;
- streams;
- Pub/Sub;
- Lua scripting;
- Redis Functions;
- transactions (`MULTI`, `EXEC`, `WATCH`, `UNWATCH`, `DISCARD`);
- replication;
- Sentinel;
- cluster mode;
- modules.

## Deployment topology

- SnugKV is single-node.
- There is no automatic replication or failover.
- High availability is not provided by SnugKV itself.

## Durability

SnugKV includes logical persistence and recovery testing, including truncated
final-frame recovery and checksum-corruption rejection.

For alpha use:
- maintain external backups for important data;
- test restore procedures;
- do not treat SnugKV as the sole copy of critical data.

## Memory accounting

`SNUG.STATS` reports SnugKV engine-accounted memory. Accounted memory is not the
same as process RSS. RSS can additionally include Go runtime overhead, stacks,
network buffers, persistence buffers, optimizer scratch space, and allocator
overhead.

## Performance

Benchmark and soak results describe specific workloads and hardware. They are
not universal performance claims. Benchmark SnugKV with your own workload before
making capacity decisions.

## Compatibility

Tested RESP2 clients currently include `redis-cli`, ioredis, node-redis,
redis-py, and go-redis. Passing smoke tests does not imply every feature exposed
by those libraries is supported.

## Security

SnugKV has not received an independent security audit. Follow
[SECURITY.md](SECURITY.md).

## Production use

The `v0.1` alpha target is suitable for evaluation, local development,
benchmarks, experiments, and non-critical caches where data can be recreated.

It should not yet be presented as a complete replacement for Redis in
mission-critical production systems.
