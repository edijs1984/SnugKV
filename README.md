# SnugKV

SnugKV is a single-node RESP2 in-memory datastore written in Go. It focuses on
Redis-compatible application workloads, memory-efficient native containers,
exact byte round trips, bounded protocol handling, sharded concurrency,
expiration, memory limits, eviction, and logical persistence.

Use Go 1.27 or newer:

```sh
go run -buildvcs=false ./cmd/snugkv \
  -listen 127.0.0.1:6380 \
  -admin-listen 127.0.0.1:6381 \
  -metrics-listen 127.0.0.1:9090

make check
go run -buildvcs=false ./cmd/snugbench -dataset sessions -keys 10000
make soak DURATION=10m KEYS=100000
```

Run it with Docker Compose:

```sh
docker compose up --build
```

The server then listens on `127.0.0.1:6380`. A RESP2 client such as
`redis-cli` can be used directly:

```sh
redis-cli -p 6380 ping
redis-cli -p 6380 set example hello
redis-cli -p 6380 get example
```

See [operations](docs/operations.md) for configuration, persistence, metrics,
and administration details. See [COMPATIBILITY.md](COMPATIBILITY.md) for the
current Redis command/type matrix, [KNOWN-LIMITATIONS.md](KNOWN-LIMITATIONS.md)
for current boundaries, [PLAN.md](PLAN.md) for the remaining roadmap, and
[PROGRESS.md](PROGRESS.md) for implementation evidence. Benchmark details are in
[benchmarks/README.md](benchmarks/README.md).

## Supported command surface

- Connection: `PING`, `ECHO`, `QUIT`, `SELECT 0`, `HELLO 2`, `INFO`, `DBSIZE`, `COMMAND`.
- Keys: `DEL`, `UNLINK`, `EXISTS`, `TYPE`, `TOUCH`, `KEYS`, `SCAN`, `RANDOMKEY`, `RENAME`, `RENAMENX`.
- Strings: `SET`, `GET`, `GETSET`, `GETDEL`, `GETEX`, `SETNX`, `SETEX`, `PSETEX`, `MSET`, `MSETNX`, `MGET`, `APPEND`, `STRLEN`, `GETRANGE`, `SETRANGE`.
- Numeric: `INCR`, `INCRBY`, `DECR`, `DECRBY`, `INCRBYFLOAT`.
- Bit operations: `GETBIT`, `SETBIT`, `BITCOUNT`, `BITPOS`, `BITOP`.
- Expiration: `EXPIRE`, `PEXPIRE`, `EXPIREAT`, `PEXPIREAT`, `EXPIRETIME`, `PEXPIRETIME`, `TTL`, `PTTL`, `PERSIST`.
- HASH: `HSET`, `HGET`, `HDEL`, `HLEN`, `HEXISTS`, `HMGET`, `HGETALL`, `HKEYS`, `HVALS`, `HSETNX`, `HSTRLEN`, `HINCRBY`, `HINCRBYFLOAT`, `HSCAN`, `HMSET`, `HRANDFIELD`.
- SET: `SADD`, `SREM`, `SISMEMBER`, `SMISMEMBER`, `SCARD`, `SMEMBERS`, `SSCAN`, `SUNION`, `SINTER`, `SDIFF`, `SUNIONSTORE`, `SINTERSTORE`, `SDIFFSTORE`, `SMOVE`, `SPOP`, `SRANDMEMBER`.
- LIST: `LPUSH`, `RPUSH`, `LPUSHX`, `RPUSHX`, `LPOP`, `RPOP`, `LLEN`, `LINDEX`, `LRANGE`, `LSET`, `LTRIM`, `LREM`, `LINSERT`, `LPOS`, `LMOVE`, `RPOPLPUSH`, `BLPOP`, `BRPOP`, `BLMOVE`, `BRPOPLPUSH`.
- ZSET: `ZADD`, `ZREM`, `ZINCRBY`, `ZSCORE`, `ZMSCORE`, `ZCARD`, `ZCOUNT`, `ZLEXCOUNT`, `ZRANK`, `ZREVRANK`, `ZRANGE`, `ZREVRANGE`, `ZRANGEBYSCORE`, `ZREVRANGEBYSCORE`, `ZRANGEBYLEX`, `ZREVRANGEBYLEX`, `ZREMRANGEBYRANK`, `ZREMRANGEBYSCORE`, `ZREMRANGEBYLEX`, `ZUNION`, `ZINTER`, `ZDIFF`, `ZUNIONSTORE`, `ZINTERSTORE`, `ZDIFFSTORE`, `ZINTERCARD`, `ZPOPMIN`, `ZPOPMAX`, `ZMPOP`, `BZPOPMIN`, `BZPOPMAX`, `BZMPOP`, `ZRANDMEMBER`, `ZSCAN`, `ZRANGESTORE`.
- HyperLogLog: `PFADD`, `PFCOUNT`, `PFMERGE` with Redis-compatible serialized HLL strings.
- GEO: `GEOADD`, `GEODIST`, `GEOHASH`, `GEOPOS`, `GEOSEARCH`, `GEOSEARCHSTORE` using Redis-compatible 52-bit geospatial ZSET scores.
- STREAM: `XADD`, `XLEN`, `XRANGE`, `XREVRANGE`, `XDEL`, `XDELEX`, `XTRIM`, `XREAD`, `XGROUP`, `XREADGROUP`, `XACK`, `XACKDEL`, `XPENDING`, `XCLAIM`, `XAUTOCLAIM`, `XINFO`; Redis 8.2 `KEEPREF` / `DELREF` / `ACKED` reference policies are supported.
- Pub/Sub: `SUBSCRIBE`, `UNSUBSCRIBE`, `PSUBSCRIBE`, `PUNSUBSCRIBE`, `PUBLISH`, `SSUBSCRIBE`, `SUNSUBSCRIBE`, `SPUBLISH`, and `PUBSUB` classic/sharded introspection.
- Transactions: `MULTI`, `EXEC`, `DISCARD`, `WATCH`, `UNWATCH` with cross-client optimistic locking and EXEC error semantics.
- JSON: `JSON.SET`, `JSON.GET`, `JSON.TYPE`, `JSON.DEL`.
- Administration: `FLUSHDB`, `FLUSHALL`, `MEMORY`, `SNUG.ENCODING`, `SNUG.MEMORY`, `SNUG.STATS`, `SNUG.COMPACT`, `SNUG.POLICY`, `SNUG.AOFREWRITE`, `SNUG.SHAPES`, `SNUG.CANDIDATES`, `SNUG.TYPE`.

Blocking LIST, ZSET, `XREAD`, and `XREADGROUP` commands use waiter/wakeup paths
rather than polling, and sleeping blockers do not hold the global durability
mutex. RESP3 is not implemented; see [COMPATIBILITY.md](COMPATIBILITY.md).

## Native container storage

HASH, SET, LIST, ZSET, and STREAM are native semantic types rather than strings
carrying Redis-like payloads. Their packed formats are excluded from the generic
scalar optimizer and are persisted as logical container state.

- HASH uses canonical packed SH1 and an adaptive shared-field-shape SH2 physical representation.
- SET uses canonical SS1 plus adaptive singleton and prefix-coded physical forms.
- LIST uses canonical ordered SL1 storage.
- ZSET uses adaptive packed SZ formats with integer score delta-varints and member front coding when they reduce size, with float64/raw-member fallback otherwise.
- STREAM uses versioned packed storage with backward decode for earlier stream formats, durable consumer groups/PEL state, lifetime entry metadata, separate consumer activity timestamps, and Redis 8.2 reference-policy semantics.

GEO intentionally reuses the ZSET representation, matching Redis's data model.
`GEOSEARCH` currently scans the packed ZSET and filters decoded coordinates rather
than maintaining a second permanent geospatial index; this favors memory economy
and makes query cost linear in source cardinality for now.

## Memory and benchmarks

The memory limit covers engine-accounted index capacity, arena capacity, entry/key
charges, and bounded schema/dictionary state. It is not process RSS. Network
buffers, stacks, persistence buffers, Go runtime state, and optimizer scratch can
add additional RSS.

The sparse 1,000-key / 256-shard benchmark has been reduced from the original
527,768 accounted bytes to 179,224 bytes: a 348,544-byte reduction, or 66.04%.
The current layout in that benchmark uses 16-byte index slots, 32-byte common
entries, 192 bytes of static shard structure, and 24-byte arena segment
descriptors. This result is workload-specific and does not imply equivalent RSS
reduction.

On the recorded 100,000-key datatype benchmarks with 16-byte members/elements,
SnugKV's measured engine-accounted memory versus Redis `used_memory` delta was:

- SET sequential members: about 15.8% lower at 8 members, 34.2% at 16, 44.8% at 32, and 52.1% at 64.
- LIST: approximately tied at 16 elements, then about 3.2% lower at 32 and 7.0% at 64.
- ZSET structured members after score delta + prefix coding: about 17.8% lower at 8 members, 34.1% at 16, 45.5% at 32, and 51.2% at 64.
- HASH shared schemas: about 13.2% lower at 8 fields, 22.8% at 16, 21.8% at 32, and 22.1% at 64.

Tiny containers can still lose to Redis because fixed per-key/index/arena overhead
remains significant even after the sparse-memory work. The benchmark page records
the exact workloads and caveats; these figures are engineering measurements, not
universal claims.

Raw storage is the default for ordinary scalar values. `-encoding` enables
verified canonical integer, UUID, and timestamp storage. `-json-shape` and
`-compression` require `-encoding` and add background shape/dictionary, LZ4, and
Zstandard candidates. Native container types are excluded from the generic scalar
optimizer.

## Major remaining compatibility work

The next large Redis gaps are scripting/functions, `SORT`/`SORT_RO`, COPY/migration
scope, RESP3, and client/tooling compatibility (`CLIENT`, `CONFIG`, ACL/auth,
COMMAND metadata). Deprecated `GEORADIUS*` compatibility is not part of the modern
GEO surface yet. A final differential Redis edge-case audit remains useful for
Streams, but there is no known core Streams command-family gap. See
[COMPATIBILITY.md](COMPATIBILITY.md) and GitHub issue #55.

## License

SnugKV is available under the
[GNU Affero General Public License v3.0](LICENSE).

For organizations that require proprietary use, embedding, redistribution, or
hosted-service terms incompatible with AGPL-3.0, separate commercial licensing is
available. See [COMMERCIAL-LICENSE.md](COMMERCIAL-LICENSE.md).

SnugKV is an independent project and is not affiliated with or endorsed by Redis.
Redis and related marks are trademarks of their respective owners.
