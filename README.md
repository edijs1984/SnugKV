# SnugKV

SnugKV is a single-node RESP2 in-memory datastore written in Go. It focuses on
Redis-compatible application workloads, memory-efficient native containers,
exact byte round trips, bounded protocol handling, sharded concurrency,
expiration, memory limits, eviction, logical persistence, and a bounded Lua
scripting core.

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
- Keys: `DEL`, `UNLINK`, `EXISTS`, `TYPE`, `TOUCH`, `KEYS`, `SCAN`, `RANDOMKEY`, `RENAME`, `RENAMENX`, `COPY`.
- Strings: `SET`, `GET`, `GETSET`, `GETDEL`, `GETEX`, `SETNX`, `SETEX`, `PSETEX`, `MSET`, `MSETNX`, `MGET`, `APPEND`, `STRLEN`, `GETRANGE`, `SETRANGE`.
- Numeric: `INCR`, `INCRBY`, `DECR`, `DECRBY`, `INCRBYFLOAT`.
- Bit operations: `GETBIT`, `SETBIT`, `BITCOUNT`, `BITPOS`, `BITOP`.
- Expiration: `EXPIRE`, `PEXPIRE`, `EXPIREAT`, `PEXPIREAT`, `EXPIRETIME`, `PEXPIRETIME`, `TTL`, `PTTL`, `PERSIST`.
- HASH: `HSET`, `HGET`, `HDEL`, `HLEN`, `HEXISTS`, `HMGET`, `HGETALL`, `HKEYS`, `HVALS`, `HSETNX`, `HSTRLEN`, `HINCRBY`, `HINCRBYFLOAT`, `HSCAN`, `HMSET`, `HRANDFIELD`.
- SET: `SADD`, `SREM`, `SISMEMBER`, `SMISMEMBER`, `SCARD`, `SMEMBERS`, `SSCAN`, `SUNION`, `SINTER`, `SDIFF`, `SUNIONSTORE`, `SINTERSTORE`, `SDIFFSTORE`, `SMOVE`, `SPOP`, `SRANDMEMBER`.
- LIST: `LPUSH`, `RPUSH`, `LPUSHX`, `RPUSHX`, `LPOP`, `RPOP`, `LLEN`, `LINDEX`, `LRANGE`, `LSET`, `LTRIM`, `LREM`, `LINSERT`, `LPOS`, `LMOVE`, `RPOPLPUSH`, `BLPOP`, `BRPOP`, `BLMOVE`, `BRPOPLPUSH`.
- ZSET: `ZADD`, `ZREM`, `ZINCRBY`, `ZSCORE`, `ZMSCORE`, `ZCARD`, `ZCOUNT`, `ZLEXCOUNT`, `ZRANK`, `ZREVRANK`, `ZRANGE`, `ZREVRANGE`, `ZRANGEBYSCORE`, `ZREVRANGEBYSCORE`, `ZRANGEBYLEX`, `ZREVRANGEBYLEX`, `ZREMRANGEBYRANK`, `ZREMRANGEBYSCORE`, `ZREMRANGEBYLEX`, `ZUNION`, `ZINTER`, `ZDIFF`, `ZUNIONSTORE`, `ZINTERSTORE`, `ZDIFFSTORE`, `ZINTERCARD`, `ZPOPMIN`, `ZPOPMAX`, `ZMPOP`, `BZPOPMIN`, `BZPOPMAX`, `BZMPOP`, `ZRANDMEMBER`, `ZSCAN`, `ZRANGESTORE`.
- Sorting: `SORT`, `SORT_RO` over LIST/SET/ZSET sources with numeric or `ALPHA` ordering, `ASC`/`DESC`, `LIMIT`, external string/hash `BY` and `GET` patterns, `GET #`, `BY`-constant/native-order mode, and `SORT ... STORE` LIST replacement.
- HyperLogLog: `PFADD`, `PFCOUNT`, `PFMERGE` with Redis-compatible serialized HLL strings.
- GEO: `GEOADD`, `GEODIST`, `GEOHASH`, `GEOPOS`, `GEOSEARCH`, `GEOSEARCHSTORE` using Redis-compatible 52-bit geospatial ZSET scores.
- STREAM: `XADD`, `XLEN`, `XRANGE`, `XREVRANGE`, `XDEL`, `XDELEX`, `XTRIM`, `XREAD`, `XGROUP`, `XREADGROUP`, `XACK`, `XACKDEL`, `XPENDING`, `XCLAIM`, `XAUTOCLAIM`, `XINFO`; Redis 8.2 `KEEPREF` / `DELREF` / `ACKED` reference policies are supported.
- Pub/Sub: `SUBSCRIBE`, `UNSUBSCRIBE`, `PSUBSCRIBE`, `PUNSUBSCRIBE`, `PUBLISH`, `SSUBSCRIBE`, `SUNSUBSCRIBE`, `SPUBLISH`, and `PUBSUB` classic/sharded introspection.
- Transactions: `MULTI`, `EXEC`, `DISCARD`, `WATCH`, `UNWATCH` with cross-client optimistic locking and EXEC error semantics.
- Scripting: `EVAL`, `EVALSHA`, `EVAL_RO`, `EVALSHA_RO`, `SCRIPT LOAD`, `SCRIPT EXISTS`, `SCRIPT FLUSH`; Lua `KEYS`/`ARGV`, `redis.call`, `redis.pcall`, `redis.error_reply`, `redis.status_reply`, and `redis.sha1hex` are available.
- Functions: `FUNCTION LOAD [REPLACE]`, `FUNCTION LIST [LIBRARYNAME pattern] [WITHCODE]`, `FUNCTION DELETE`, `FUNCTION FLUSH [SYNC|ASYNC]`, `FUNCTION DUMP`, `FUNCTION RESTORE <payload> [APPEND|REPLACE|FLUSH]`, `FCALL`, and `FCALL_RO`; `redis.register_function()` supports positional registration and table registration with the `no-writes` flag.
- JSON: `JSON.SET`, `JSON.GET`, `JSON.TYPE`, `JSON.DEL`.
- Administration: `FLUSHDB`, `FLUSHALL`, `MEMORY`, `SNUG.ENCODING`, `SNUG.MEMORY`, `SNUG.STATS`, `SNUG.COMPACT`, `SNUG.POLICY`, `SNUG.AOFREWRITE`, `SNUG.SHAPES`, `SNUG.CANDIDATES`, `SNUG.TYPE`.

Blocking LIST, ZSET, `XREAD`, and `XREADGROUP` commands use waiter/wakeup paths
rather than polling, and sleeping blockers do not hold the global durability
mutex. RESP3 is not implemented; see [COMPATIBILITY.md](COMPATIBILITY.md).

## Lua scripting and Functions

SnugKV embeds a Lua 5.1-compatible runtime for the common Redis scripting path.
`EVAL`, `EVALSHA`, `EVAL_RO`, and `EVALSHA_RO` execute under the same global
command-serialization boundary as ordinary writes and `MULTI`/`EXEC`, so another
client cannot interleave a command halfway through a script. `EVAL`/`EVAL_RO`
and `SCRIPT LOAD` populate a volatile SHA-1 script cache; `SCRIPT FLUSH
[SYNC|ASYNC]` clears it. Read-only script variants reject commands that mutate
or replicate state, including `PUBLISH`/`SPUBLISH`.

The script bridge maps RESP2 values to/from Lua and supports ordinary nonblocking
data/key commands through `redis.call` and `redis.pcall`. Blocking commands,
connection/subscription state, transactions, nested scripting, and SnugKV admin
commands are intentionally rejected from inside scripts. Filesystem/process Lua
libraries are not exposed, and each script has a five-second execution limit.

Redis Functions core support includes Lua libraries loaded with `FUNCTION LOAD`,
global function-name lookup through `FCALL`/`FCALL_RO`, library replacement,
listing/deletion/flushing, persistent library-local Lua state while the process
is running, and table-form `redis.register_function()` metadata with the
`no-writes` flag. `FCALL_RO` and `no-writes` functions use the same write/replication
barrier as `EVAL_RO`.

`FUNCTION DUMP` and `FUNCTION RESTORE` support checksum-protected library export
and import with Redis-style default `APPEND` plus `REPLACE` and `FLUSH` policies.
SnugKV currently uses its own versioned `SNUGF001` payload rather than Redis's RDB
Function payload bytes, so payloads are not cross-restorable between Redis and
SnugKV yet. See [docs/FUNCTION-DUMP-RESTORE.md](docs/FUNCTION-DUMP-RESTORE.md).

With logical AOF enabled, resulting database changes from one writable EVAL or
FCALL are persisted as one frame. Writes completed before a later Lua runtime
error remain applied and durable, matching Redis's non-rollback execution model.
When AOF or snapshot persistence is configured, Function library definitions are
also restored across restart from an atomic checksummed sidecar next to the
configured persistence file. Function-local Lua VM variables are reconstructed
from source and therefore reset after restore/restart; arbitrary live VM state is
not serialized.

Remaining scripting/function management gaps include `FUNCTION STATS/KILL/HELP`,
`SCRIPT KILL`/`DEBUG`, the broader Redis function-flag surface, exact Redis RDB
byte compatibility for Function DUMP/RESTORE payloads, and full Redis
Lua/ACL/command-flag parity.

## SORT compatibility

`SORT` and `SORT_RO` accept LIST, SET, and ZSET sources. The implemented option
surface includes `BY`, `LIMIT`, repeated `GET`, `GET #`, `ASC`, `DESC`, and
`ALPHA`; `SORT` additionally supports `STORE`, which replaces the destination as
a native LIST and clears any previous destination TTL. Missing `GET` lookups are
null in command replies and become empty list elements under `STORE`, matching
Redis behavior. External patterns can dereference strings (`weight_*`) or hash
fields (`user:*->score`).

SnugKV uses bytewise comparison for `ALPHA`. Redis can use locale-aware collation
for non-STORE ALPHA replies, so locale-sensitive/non-ASCII order is not claimed
to be byte-for-byte identical yet. ACL/Cluster restrictions around dynamic
external patterns are also outside the current single-node/no-ACL scope.

## COPY compatibility

`COPY source destination [DB 0] [REPLACE]` deep-copies the logical value while
leaving the source unchanged. Native HASH, SET, LIST, ZSET, STREAM state and
STRING-style values retain their Redis-visible datatype, and an existing source
TTL is copied as the same absolute expiry. Without `REPLACE`, an existing live
destination makes the command return 0; with `REPLACE`, any destination datatype
can be overwritten. Successful COPY writes participate in AOF rollback,
MULTI/EXEC, WATCH invalidation, max-memory admission, and blocking container
wakeups.

SnugKV intentionally exposes only database 0, so `COPY ... DB 0` is accepted and
other destination DB indexes return `ERR DB index is out of range`. Cross-database
copy and Redis migration/transfer commands are not implemented.

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

The next large Redis gaps are remaining scripting/function management and exact
command-flag/ACL parity, migration scope, RESP3, and client/tooling compatibility
(`CLIENT`, `CONFIG`, ACL/auth, COMMAND metadata). Deprecated `GEORADIUS*`
compatibility is not part of the modern GEO surface yet. A final differential
Redis edge-case audit remains useful for Streams, but there is no known core
Streams command-family gap. See [COMPATIBILITY.md](COMPATIBILITY.md) and GitHub
issue #55.

## License

SnugKV is available under the
[GNU Affero General Public License v3.0](LICENSE).

For organizations that require proprietary use, embedding, redistribution, or
hosted-service terms incompatible with AGPL-3.0, separate commercial licensing is
available. See [COMMERCIAL-LICENSE.md](COMMERCIAL-LICENSE.md).

SnugKV is an independent project and is not affiliated with or endorsed by Redis.
Redis and related marks are trademarks of their respective owners.
