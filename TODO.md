# SnugKV TODO

> **Historical backlog:** this file preserves early planning material and many
> checkboxes below no longer reflect the implemented repository. Current status
> and next work are authoritative in `README.md`, `COMPATIBILITY.md`, `PLAN.md`,
> `PROGRESS.md`, and GitHub issue #55. In particular, RESP3, AUTH/ACL, and the
> documented Streams Redis 8.2 differential audit are complete for their current
> scoped milestones.

## Goal

Expand SnugKV from the current Redis-compatible string/TTL subset into a broadly compatible Redis alternative while keeping SnugKV's advantages:

- low memory usage
- predictable performance
- native typed values
- adaptive encoding/compression
- Redis-compatible client support
- compatibility with common Redis UIs and tooling

---


# EXECUTION ROADMAP — Quality → Public Alpha → Revenue

This section defines the current execution order.

The large Redis compatibility backlog below remains valid, but work should follow
this roadmap unless a newly discovered correctness or security issue takes priority.

## Product target

The first public target is:

> SnugKV v0.1 alpha — a Redis-compatible, memory-efficient datastore for
> common cache/string/TTL workloads, suitable for evaluation, development,
> benchmarks, and non-critical workloads.

Do not claim that SnugKV is a complete production Redis replacement until the
quality gates below are satisfied.

## Decision rule

Work in this order:

1. correctness
2. crash/security safety
3. durability and recovery
4. client compatibility
5. operational usability
6. reproducible performance evidence
7. monetization
8. broader Redis feature coverage
9. micro-optimizations

Do not trade significant latency, correctness, or maintainability for small RAM
savings.

---

## Phase A — Public-alpha quality gate

### Crash and protocol safety

- [x] Legal large RESP values no longer crash arena allocation
- [x] Arena tested through 32 MiB value size
- [x] 1 MiB real `redis-cli SET` round-trip tested
- [x] RESP fuzzing runs in CI
- [ ] Add explicit 32 MiB server-level boundary test
- [ ] Test oversized RESP rejection above configured limit
- [ ] Test malformed/truncated request handling under real TCP connections
- [ ] Test slow-client behavior and connection cleanup
- [ ] Audit all reachable `panic` paths for remotely controlled input
- [ ] Add connection/request resource limits where needed

### Persistence and recovery

- [x] Persistence preserves native semantic value types
- [ ] Restart test after normal writes
- [ ] Restart test with TTL values
- [ ] Restart test with all semantic types
- [ ] Restart test after JSON-shape encoding
- [ ] Test truncated persistence record
- [ ] Test corrupted persistence record
- [ ] Test interrupted write/recovery behavior
- [ ] Test recovery near configured memory limit
- [ ] Define and document durability guarantees
- [ ] Define persistence format/version compatibility policy

### Memory safety and limits

- [x] Memory accounting exists
- [x] Arena allocation accounting exists
- [ ] Verify MaxMemory under rewrite pressure
- [ ] Verify MaxMemory during compaction
- [ ] Verify atomic failure when an operation would exceed memory
- [ ] Stress repeated allocate/free cycles for large values
- [ ] Test fragmentation after mixed-size workloads
- [ ] Verify eviction cannot violate memory accounting
- [ ] Run leak/growth tests over long workloads

### Concurrency

- [x] Full repository race test runs in CI
- [ ] Concurrent GET/SET/DEL stress
- [ ] Concurrent TTL expiry stress
- [ ] Concurrent optimizer/rewrite stress
- [ ] Concurrent compaction stress
- [ ] Concurrent persistence stress
- [ ] Connection churn stress

### Soak testing

- [ ] 1 hour mixed-workload soak
- [ ] 24 hour mixed-workload soak
- [ ] 72 hour mixed-workload soak before beta
- [ ] Track RSS/heap over time
- [ ] Track goroutine count over time
- [ ] Track error count and latency percentiles
- [ ] Assert zero data mismatches

---

## Phase B — Real Redis client compatibility

Test normal application flows, not only individual commands.

- [x] `redis-cli` basic compatibility
- [ ] ioredis smoke suite
- [ ] node-redis smoke suite
- [ ] go-redis smoke suite
- [ ] redis-py smoke suite
- [ ] Jedis smoke suite
- [ ] Lettuce smoke suite
- [ ] RedisInsight basic connectivity

For each client record:

- connection/setup behavior
- commands automatically issued by the client
- pipelining behavior
- reconnect behavior
- error handling
- unsupported-command behavior

Create and maintain a public compatibility matrix.

---

## Phase C — v0.1-alpha release readiness

### Repository

- [ ] `SECURITY.md`
- [ ] `CLA.md`
- [ ] `CHANGELOG.md`
- [ ] `KNOWN-LIMITATIONS.md`
- [ ] `COMPATIBILITY.md`
- [ ] release/version policy
- [ ] supported Go version documented
- [ ] reproducible release build
- [ ] tagged `v0.1.0-alpha`

### Installation

- [x] Dockerfile exists
- [ ] Publish Docker image
- [ ] Single-command Docker quick start
- [ ] Persistent-volume example
- [ ] Production-ish config example
- [ ] graceful shutdown documentation
- [ ] backup/restore documentation

### README

- [ ] 30-second quick start
- [ ] Node.js example
- [ ] Go example
- [ ] Python example
- [ ] migration-from-Redis example
- [ ] supported-command matrix link
- [ ] known-limitations warning
- [ ] benchmark methodology link
- [ ] commercial support/licensing section

---

## Phase D — Prove SnugKV's value

Build one reproducible benchmark harness comparing:

- SnugKV
- Redis
- Valkey
- Dragonfly

Datasets:

- [ ] sessions
- [ ] booleans
- [ ] signed integers
- [ ] unsigned integers
- [ ] UUIDs
- [ ] timestamps
- [ ] small JSON
- [ ] medium JSON
- [ ] compressible blobs
- [ ] random/incompressible blobs

Measure:

- [ ] logical bytes
- [ ] resident/process memory
- [ ] accounted SnugKV bytes
- [ ] bytes per key
- [ ] GET p50/p95/p99
- [ ] SET p50/p95/p99
- [ ] throughput
- [ ] CPU
- [ ] load time
- [ ] recovery time

Rules:

- never publish conclusions from one run
- run repeated samples
- preserve raw benchmark output
- publish hardware/software versions
- do not claim SnugKV is faster or smaller unless the data demonstrates it

Primary positioning to test:

> Store more useful application data per GB while retaining Redis-compatible clients.

---

## Phase E — First revenue

Do not wait for complete Redis compatibility before testing monetization.

### Commercial model

- [x] AGPL open-source license
- [x] commercial-license document
- [ ] commercial licensing FAQ
- [ ] support/contact page
- [ ] simple pricing page
- [ ] define commercial/OEM licensing process

Initial services to offer:

- [ ] Redis memory-efficiency assessment
- [ ] Redis → SnugKV migration assistance
- [ ] deployment/integration support
- [ ] performance profiling
- [ ] custom codec/command development
- [ ] commercial embedding license
- [ ] paid support plan

Initial pricing experiments:

- Developer support: €99–199/month
- Startup support: around €499/month
- Business support: from €1,500/month
- Consulting/performance work: €75–150/hour
- Fixed performance/memory audit: €500–2,000
- OEM/commercial licensing: custom

First commercial milestone:

- [ ] first external benchmark user
- [ ] first external production-like test
- [ ] first paying customer
- [ ] €500/month recurring revenue
- [ ] 3 paying customers

---

## Phase F — Compatibility expansion

Only accelerate these after the alpha quality gate is credible.

Priority order:

1. Hashes
2. Sets
3. Lists
4. Sorted sets
5. Transactions
6. Pub/Sub
7. RESP3
8. Streams
9. replication
10. clustering

Do not implement every Redis feature merely for command-count parity. Prioritize
features used by real users and paying prospects.

---

## Near-term engineering queue

Work through these approximately in order:

- [x] prevent large legal values from crashing arena allocator
- [ ] lazy JSON-shape store allocation
- [ ] audit remotely reachable panic paths
- [ ] persistence/restart fault tests
- [ ] server-level RESP boundary tests
- [ ] client smoke-test harness
- [ ] 1-hour soak harness/run
- [ ] 24-hour soak
- [ ] compatibility matrix
- [ ] public-alpha documentation
- [ ] reproducible Redis/Valkey/Dragonfly comparison
- [ ] v0.1.0-alpha release
- [ ] outreach for first benchmark users
- [ ] paid support/migration offer

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

- [x] `STRING`
- [x] canonical `INT64` encoding
- [x] `UINT64`
- [x] `FLOAT64`
- [x] `BOOL`
- [x] `BYTES`
- [x] `JSON`

## Requirements

- [x] Add a compact type tag to stored entries
- [x] Preserve exact Redis string round-trip behavior
- [x] Automatically detect canonical integers
- [x] Automatically detect canonical floats where safe
- [x] Avoid converting strings such as `"00123"` into integers
- [x] Avoid converting values when exact byte reconstruction would change
- [ ] Support direct typed API operations internally
- [ ] Add unit tests for every numeric edge case
- [ ] Add overflow tests
- [x] Add NaN/Infinity policy
- [x] Add persistence support for all new types
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

- [x] `SNUG.TYPE key`
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