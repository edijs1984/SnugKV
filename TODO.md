# SnugKV TODO

## Goal

Expand SnugKV from the current Redis-compatible string/TTL subset into a broadly compatible Redis alternative while keeping SnugKV's advantages:

- low memory usage
- predictable performance
- native typed values
- adaptive encoding/compression
- Redis-compatible client support
- compatibility with common Redis UIs and tooling

---

# P0 — Redis Compatibility Basics

These should be implemented first because many Redis clients, admin tools, and UIs expect them.

## Key inspection

- [x] `TYPE key`
- [x] `SCAN cursor [MATCH pattern] [COUNT count] [TYPE type]`
- [x] `KEYS pattern`
- [x] `RANDOMKEY`
- [x] `RENAME key newkey`
- [x] `RENAMENX key newkey`
- [x] `UNLINK key`
- [x] `TOUCH key`

## Database commands

- [x] `FLUSHDB`
- [x] `FLUSHALL`
- [ ] `SWAPDB` — optional if multiple DBs are added later
- [ ] Improve `DBSIZE`
- [ ] Improve `INFO`
- [ ] Improve `COMMAND`
- [x] `COMMAND INFO`
- [x] `COMMAND COUNT`
- [ ] `COMMAND DOCS` — optional

## Expiration

Existing:

- [x] `EXPIRE`
- [x] `PEXPIRE`
- [x] `TTL`
- [x] `PTTL`
- [x] `PERSIST`

Add:

- [x] `EXPIREAT`
- [x] `PEXPIREAT`
- [x] `EXPIRETIME`
- [x] `PEXPIRETIME`
- [x] Redis-compatible `NX | XX | GT | LT` options for expiration commands

---

# P0 — Complete String Command Support

Existing:

- [x] `SET`
- [x] `GET`
- [x] `GETSET`
- [x] `SETNX`
- [x] `MSET`
- [x] `MGET`
- [x] `INCR`
- [x] `INCRBY`
- [x] `DECR`
- [x] `DECRBY`
- [x] `STRLEN`

Add:

- [x] `INCRBYFLOAT`
- [x] `APPEND`
- [x] `GETDEL`
- [x] `GETEX`
- [x] `GETRANGE`
- [x] `SETRANGE`
- [x] `MSETNX`
- [x] `SETEX`
- [x] `PSETEX`
- [x] `GETBIT`
- [x] `SETBIT`
- [x] `BITCOUNT`
- [x] `BITOP`
- [x] `BITPOS`

## SET compatibility

Add remaining Redis `SET` options where useful:

- [x] `GET`
- [x] `KEEPTTL`
- [x] `EXAT`
- [x] `PXAT`

---

# P0 — Native Typed Values

SnugKV should internally support typed values while preserving Redis-compatible behavior.

## Core types

- [ ] `STRING`
- [x] canonical `INT64` encoding
- [ ] `UINT64`
- [ ] `FLOAT64`
- [ ] `BOOL`
- [ ] `BYTES`
- [ ] `JSON`

## Requirements

- [ ] Add a compact type tag to stored entries
- [ ] Preserve exact Redis string round-trip behavior
- [ ] Automatically detect canonical integers
- [ ] Automatically detect canonical floats where safe
- [ ] Avoid converting strings such as `"00123"` into integers
- [ ] Avoid converting values when exact byte reconstruction would change
- [ ] Support direct typed API operations internally
- [ ] Add unit tests for every numeric edge case
- [ ] Add overflow tests
- [ ] Add NaN/Infinity policy
- [ ] Add persistence support for all new types
- [ ] Add snapshot/AOF compatibility tests
- [ ] Add memory benchmarks comparing typed vs raw ASCII values

---

# P0 — Decimal / Floating Point Support

- [ ] Implement strict Redis-compatible float parser
- [x] Implement `INCRBYFLOAT`
- [ ] Support atomic float increments
- [ ] Define canonical float serialization
- [ ] Preserve precision expectations
- [ ] Test negative values
- [ ] Test very small values
- [ ] Test very large values
- [ ] Test malformed floats
- [ ] Test concurrent increments

Possible future Snug-specific type:

- [ ] `DECIMAL128` or arbitrary precision decimal
- [ ] Evaluate whether decimal support is worth the memory/performance cost

---

# P1 — Hashes

Implement Redis hash semantics.

- [ ] `HSET`
- [ ] `HSETNX`
- [ ] `HGET`
- [ ] `HMGET`
- [ ] `HGETALL`
- [ ] `HDEL`
- [ ] `HEXISTS`
- [ ] `HLEN`
- [ ] `HKEYS`
- [ ] `HVALS`
- [ ] `HINCRBY`
- [ ] `HINCRBYFLOAT`
- [ ] `HSTRLEN`
- [ ] `HRANDFIELD`
- [ ] `HSCAN`

## Internal design

- [ ] Small-hash compact representation
- [ ] Upgrade representation when hash grows
- [ ] Per-hash memory accounting
- [ ] Expiration remains key-level initially
- [ ] Persistence support
- [ ] Benchmarks vs Redis memory usage

---

# P1 — Sets

- [ ] `SADD`
- [ ] `SREM`
- [ ] `SISMEMBER`
- [ ] `SMISMEMBER`
- [ ] `SMEMBERS`
- [ ] `SCARD`
- [ ] `SPOP`
- [ ] `SRANDMEMBER`
- [ ] `SMOVE`
- [ ] `SDIFF`
- [ ] `SDIFFSTORE`
- [ ] `SINTER`
- [ ] `SINTERSTORE`
- [ ] `SINTERCARD`
- [ ] `SUNION`
- [ ] `SUNIONSTORE`
- [ ] `SSCAN`

## Optimization

- [ ] Integer-only compact set representation
- [ ] Hash-table representation fallback
- [ ] Benchmark compact sets vs Redis intset/hashtable behavior

---

# P1 — Lists

- [ ] `LPUSH`
- [ ] `RPUSH`
- [ ] `LPOP`
- [ ] `RPOP`
- [ ] `LLEN`
- [ ] `LINDEX`
- [ ] `LRANGE`
- [ ] `LSET`
- [ ] `LTRIM`
- [ ] `LINSERT`
- [ ] `LPOS`
- [ ] `LMOVE`
- [ ] `LMPOP`
- [ ] `BLPOP`
- [ ] `BRPOP`
- [ ] `BLMOVE`
- [ ] `BLMPOP`

## Internal design

- [ ] Segmented/deque representation
- [ ] Avoid per-element Go object allocations
- [ ] Efficient head/tail operations
- [ ] Blocking command waiter management
- [ ] Cancellation/disconnect handling

---

# P1 — Sorted Sets

- [ ] `ZADD`
- [ ] `ZREM`
- [ ] `ZSCORE`
- [ ] `ZMSCORE`
- [ ] `ZCARD`
- [ ] `ZCOUNT`
- [ ] `ZLEXCOUNT`
- [ ] `ZRANK`
- [ ] `ZREVRANK`
- [ ] `ZRANGE`
- [ ] `ZRANGESTORE`
- [ ] `ZINCRBY`
- [ ] `ZPOPMIN`
- [ ] `ZPOPMAX`
- [ ] `ZMPOP`
- [ ] `ZDIFF`
- [ ] `ZINTER`
- [ ] `ZUNION`
- [ ] `ZSCAN`

## Internal design

Evaluate:

- [ ] skip list
- [ ] B-tree
- [ ] ordered tree + hash index
- [ ] compact array for small sorted sets

---

# P1 — Native JSON

SnugKV already has JSON-shape compression. Add first-class JSON operations.

## SnugKV JSON API

- [ ] Native JSON value type
- [ ] Parse JSON once on write
- [ ] Avoid repeated full JSON parsing where possible
- [ ] Store efficiently using shared shapes/dictionaries
- [ ] Preserve original JSON when exact byte round-trip is required
- [ ] Optional canonical JSON storage mode

## RedisJSON-compatible commands

Start with:

- [x] `JSON.SET`
- [x] `JSON.GET`
- [x] `JSON.DEL`
- [x] `JSON.TYPE`
- [ ] `JSON.CLEAR`
- [ ] `JSON.NUMINCRBY`
- [ ] `JSON.NUMMULTBY`
- [ ] `JSON.STRLEN`
- [ ] `JSON.STRAPPEND`
- [ ] `JSON.OBJKEYS`
- [ ] `JSON.OBJLEN`
- [ ] `JSON.ARRAPPEND`
- [ ] `JSON.ARRINSERT`
- [ ] `JSON.ARRLEN`
- [ ] `JSON.ARRPOP`
- [ ] `JSON.ARRTRIM`

Later:

- [ ] JSONPath support
- [ ] recursive paths
- [ ] wildcard selectors
- [ ] filters
- [ ] multi-path operations

## JSON testing

- [ ] Nested objects
- [ ] Nested arrays
- [ ] Unicode
- [ ] large JSON objects
- [ ] malformed JSON
- [ ] numeric edge cases
- [ ] JSONPath fuzzing

---

# P1 — Redis UI / Client Compatibility

Target compatibility with:

- [ ] `redis-cli`
- [ ] RedisInsight
- [ ] ioredis
- [ ] node-redis
- [ ] go-redis
- [ ] Jedis
- [ ] Lettuce
- [ ] redis-py
- [ ] StackExchange.Redis

## Commands commonly probed by clients

- [ ] `CLIENT ID`
- [ ] `CLIENT SETNAME`
- [ ] `CLIENT GETNAME`
- [ ] `CLIENT LIST`
- [ ] `CLIENT INFO`
- [ ] `CLIENT SETINFO`
- [ ] `ROLE`
- [ ] `CONFIG GET`
- [ ] `CONFIG SET` — expose only safe runtime options
- [ ] `CONFIG RESETSTAT`
- [ ] `TIME`
- [ ] `LASTSAVE`
- [ ] `BGSAVE`
- [ ] `SAVE`

## Protocol

Current RESP2 support is good.

Add:

- [ ] RESP3 parser
- [ ] RESP3 response writer
- [ ] `HELLO 3`
- [ ] maps
- [ ] sets
- [ ] booleans
- [ ] doubles
- [ ] null
- [ ] push messages

RESP3 will make native SnugKV types much easier to expose correctly.

---

# P2 — Transactions

- [ ] `MULTI`
- [ ] `EXEC`
- [ ] `DISCARD`
- [ ] `WATCH`
- [ ] `UNWATCH`

Requirements:

- [ ] per-connection transaction queue
- [ ] optimistic key versioning
- [ ] cross-shard atomic commit strategy
- [ ] transaction memory bounds
- [ ] disconnect cleanup

---

# P2 — Pub/Sub

- [ ] `SUBSCRIBE`
- [ ] `UNSUBSCRIBE`
- [ ] `PSUBSCRIBE`
- [ ] `PUNSUBSCRIBE`
- [ ] `PUBLISH`
- [ ] `PUBSUB CHANNELS`
- [ ] `PUBSUB NUMSUB`
- [ ] `PUBSUB NUMPAT`

Later:

- [ ] sharded Pub/Sub compatibility

---

# P2 — Streams

Streams are substantial and should be implemented only after core compatibility is stable.

- [ ] `XADD`
- [ ] `XLEN`
- [ ] `XRANGE`
- [ ] `XREVRANGE`
- [ ] `XREAD`
- [ ] `XDEL`
- [ ] `XTRIM`
- [ ] `XGROUP`
- [ ] `XREADGROUP`
- [ ] `XACK`
- [ ] `XPENDING`
- [ ] `XCLAIM`
- [ ] `XAUTOCLAIM`
- [ ] `XINFO`
- [ ] blocking reads

---

# P2 — Scripting / Functions

Evaluate carefully because this increases complexity and attack surface.

Possible options:

- [ ] Lua compatibility
- [ ] embedded Starlark
- [ ] WASM scripting
- [ ] SnugKV-native functions

Redis compatibility commands if Lua is chosen:

- [ ] `EVAL`
- [ ] `EVALSHA`
- [ ] `SCRIPT LOAD`
- [ ] `SCRIPT EXISTS`
- [ ] `SCRIPT FLUSH`

Do not implement this before resource limits and sandboxing are designed.

---

# P2 — ACL / Authentication

- [ ] `AUTH`
- [ ] username/password support
- [ ] ACL users
- [ ] command categories
- [ ] key-pattern restrictions
- [ ] `ACL LIST`
- [ ] `ACL USERS`
- [ ] `ACL GETUSER`
- [ ] `ACL SETUSER`
- [ ] `ACL DELUSER`
- [ ] `ACL WHOAMI`

Security:

- [ ] constant-time credential comparison
- [ ] password hashing policy
- [ ] connection rate limiting
- [ ] failed-auth rate limiting
- [ ] audit events

---

# P3 — Replication

Only begin after the single-node command/storage model is stable.

- [ ] replication protocol design
- [ ] primary/replica roles
- [ ] replication offset
- [ ] replication backlog
- [ ] partial resynchronization
- [ ] full snapshot synchronization
- [ ] replica reconnect
- [ ] read-only replicas
- [ ] `REPLICAOF`
- [ ] `ROLE`
- [ ] `WAIT`
- [ ] `WAITAOF`

Consider whether SnugKV should copy Redis replication semantics exactly or use a simpler native protocol.

---

# P3 — Cluster / Distributed Mode

Do not rush this.

- [ ] partitioning strategy
- [ ] consistent hashing vs Redis hash slots
- [ ] node membership
- [ ] failure detection
- [ ] leader election strategy
- [ ] replica placement
- [ ] rebalance
- [ ] migration
- [ ] redirects
- [ ] topology API
- [ ] rolling upgrades
- [ ] multi-region design

Redis Cluster compatibility:

- [ ] `CLUSTER SLOTS`
- [ ] `CLUSTER SHARDS`
- [ ] `CLUSTER NODES`
- [ ] `ASK`
- [ ] `MOVED`

---

# SnugKV-Specific Commands

Expose SnugKV advantages without compromising Redis compatibility.

Possible namespace:

```text
SNUG.*
```

Existing:

- [x] `SNUG.ENCODING`
- [x] `SNUG.MEMORY`
- [x] `SNUG.STATS`
- [x] `SNUG.COMPACT`
- [x] `SNUG.POLICY`
- [x] `SNUG.AOFREWRITE`

Potential additions:

- [ ] `SNUG.TYPE key`
- [ ] `SNUG.ENCODING key`
- [ ] `SNUG.SIZE key`
- [ ] `SNUG.JSONSTATS key`
- [ ] `SNUG.COMPRESS key`
- [ ] `SNUG.REENCODE key`
- [ ] `SNUG.MEMORY key`
- [ ] `SNUG.DEBUG key`

Example:

```text
SNUG.TYPE user:1
JSON

SNUG.ENCODING user:1
json-shape+dictionary

SNUG.SIZE user:1
stored_bytes=142
logical_bytes=581
saving_percent=75.56
```

---

# Architecture Work

## Command registry

Replace large command switches with a command registry.

Each command should describe:

- [ ] name
- [ ] arity
- [ ] flags
- [ ] read/write
- [ ] admin/data
- [ ] key positions
- [ ] ACL category
- [ ] handler
- [ ] RESP2 compatibility
- [ ] RESP3 compatibility

This metadata can power:

- `COMMAND`
- ACL
- documentation
- validation
- metrics
- testing

---

# Storage Model

Introduce a common value abstraction.

Example:

```go
type ValueType uint8

const (
    TypeString ValueType = iota
    TypeInt64
    TypeFloat64
    TypeBool
    TypeBytes
    TypeJSON
    TypeHash
    TypeList
    TypeSet
    TypeZSet
    TypeStream
)
```

Requirements:

- [ ] no unnecessary interface allocations
- [ ] compact metadata
- [ ] arena-backed storage where practical
- [ ] precise memory accounting
- [ ] type-safe command validation
- [ ] backwards-compatible persistence migration

---

# Persistence

Every new data type must support:

- [ ] AOF
- [ ] snapshot
- [ ] crash recovery
- [ ] checksum validation
- [ ] version migration
- [ ] corruption tests
- [ ] deterministic replay

Add:

- [ ] persistence format version
- [ ] migration tests
- [ ] compatibility fixtures for old SnugKV versions

---

# Performance Requirements

Every major data structure should include benchmarks before merge.

Measure:

- [ ] operations/sec
- [ ] p50 latency
- [ ] p95 latency
- [ ] p99 latency
- [ ] p99.9 latency
- [ ] bytes/key
- [ ] RSS
- [ ] engine-accounted memory
- [ ] allocations/op
- [ ] CPU usage
- [ ] performance per core

Compare against:

- [ ] Redis
- [ ] Valkey
- [ ] Dragonfly where useful
- [ ] KeyDB where useful

Test datasets:

- [ ] tiny values
- [ ] 100 B
- [ ] 1 KB
- [ ] 10 KB
- [ ] 100 KB
- [ ] JSON
- [ ] integers
- [ ] floats
- [ ] hashes
- [ ] sets
- [ ] mixed workload

---

# Testing

Every command requires:

- [ ] happy-path tests
- [ ] invalid arity tests
- [ ] wrong-type tests
- [ ] malformed number tests
- [ ] TTL interaction tests
- [ ] persistence tests
- [ ] concurrency tests
- [ ] race-detector tests

Add compatibility suite:

- [ ] run Redis command fixtures against Redis
- [ ] run the same fixtures against SnugKV
- [ ] compare RESP responses byte-for-byte where applicable

Add fuzzing for:

- [ ] RESP3
- [ ] JSONPath
- [ ] numeric parsing
- [ ] hashes
- [ ] lists
- [ ] sets
- [ ] sorted sets
- [ ] persistence decoder

---

# Recommended Implementation Order

## Milestone 1 — Tooling compatibility

- [ ] `TYPE`
- [ ] `SCAN`
- [ ] `KEYS`
- [ ] `FLUSHDB`
- [ ] `RANDOMKEY`
- [ ] `RENAME`
- [ ] missing TTL commands
- [ ] missing basic string commands
- [ ] client metadata commands

Target:

> RedisInsight and common Redis clients can connect, browse keys, inspect values, and perform normal string operations.

---

## Milestone 2 — Numbers + RESP3

- [ ] `FLOAT64`
- [ ] `BOOL`
- [ ] `INCRBYFLOAT`
- [ ] RESP3
- [ ] `HELLO 3`

Target:

> SnugKV supports efficient native scalar types while remaining Redis-compatible.

---

## Milestone 3 — Hashes

Implement the full commonly used hash command set.

Target:

> SnugKV can replace Redis for most application object/session/cache workloads.

---

## Milestone 4 — Sets + Lists

Target:

> Common queues, membership sets, tags, and collections work without Redis-specific workarounds.

---

## Milestone 5 — Sorted Sets

Target:

> Leaderboards, ranking systems, scheduling indexes, and scoring workloads work natively.

---

## Milestone 6 — JSON

Implement native JSON plus the most useful RedisJSON-compatible commands.

Target:

> SnugKV becomes a high-density JSON cache/document store as well as a Redis-compatible KV store.

---

## Milestone 7 — Transactions + Pub/Sub

Target:

> Broader framework and application compatibility.

---

## Milestone 8 — Streams

Target:

> Redis-based event stream and queue workloads become portable to SnugKV.

---

# Definition of Broad Redis Compatibility

Do not define success as "every Redis command implemented."

A better goal:

- [ ] major Redis clients work
- [ ] RedisInsight works
- [ ] common frameworks work
- [ ] Strings complete
- [ ] Hashes complete
- [ ] Lists complete
- [ ] Sets complete
- [ ] Sorted Sets complete
- [ ] TTL semantics compatible
- [ ] transactions work
- [ ] Pub/Sub works
- [ ] RESP2 works
- [ ] RESP3 works
- [ ] JSON extension available
- [ ] compatibility test suite passes

Rare administrative, cluster, scripting, module, geospatial, probabilistic, and specialized commands can be implemented later.

---

# Immediate Next Tasks

Start here:

1. [ ] Implement `TYPE`
2. [ ] Implement cursor-based `SCAN`
3. [ ] Implement `KEYS`
4. [ ] Implement `FLUSHDB`
5. [ ] Implement `RANDOMKEY`
6. [ ] Implement `RENAME` / `RENAMENX`
7. [ ] Implement `INCRBYFLOAT`
8. [ ] Implement remaining expiration commands
9. [ ] Implement remaining high-value string commands
10. [ ] Add client compatibility tests using `ioredis`, `node-redis`, and `go-redis`
11. [ ] Test RedisInsight against SnugKV
12. [ ] Design the typed-value storage metadata
13. [ ] Design RESP3 support
14. [ ] Begin Hash implementation
15. [ ] Add compatibility benchmark suite against Redis and Valkey
