# Progress

## Current milestone

SnugKV now has a broad single-node RESP2 command surface with native HASH, SET,
LIST, ZSET, and STREAM types, logical durability, memory accounting, adaptive
scalar encoding, observability, operational tooling, classic/sharded Pub/Sub,
Redis-style transactions with optimistic locking, HyperLogLog, modern GEO, a
bounded Lua scripting core, and `SORT` / `SORT_RO`.

The current engineering focus has moved from building the core native datatype
set to finishing remaining Redis compatibility/tooling families and validating
release behavior. The main remaining application-level gaps are Redis Functions
and full scripting parity, COPY/migration scope, RESP3, and client/tooling
compatibility. Streams are broadly implemented through Redis 8.2 reference-policy
behavior; final differential edge-case audits remain useful for Streams and SORT.

## Recently completed

### SORT / SORT_RO

- `SORT` and `SORT_RO` accept LIST, SET, and ZSET sources.
- Numeric sorting is the default; `ALPHA`, `ASC`, `DESC`, and `LIMIT` are supported.
- `BY` and repeated `GET` support first-`*` substitution against external string
  keys or hash fields (`key-pattern->field`), plus `GET #`.
- A constant `BY` pattern preserves native/no-sort order; LIST/ZSET DESC reverses
  that native order and LIMIT is applied against the resulting sequence.
- Missing numeric BY lookups use score zero. Missing GET results are null in wire
  replies and become empty list elements when stored.
- `SORT ... STORE` atomically replaces any destination type with a native LIST,
  clears destination TTL, deletes the destination for an empty result, and wakes
  blocked LIST consumers for non-empty results.
- STORE durability journals/replays the destination only; transaction execution
  continues to use the existing whole-transaction logical diff.
- OOM retries protect the source, STORE destination, and resolved BY/GET external
  keys so a retry cannot silently observe an eviction-altered input set.
- Focused tests cover LIST/SET/ZSET sources, numeric/ALPHA ordering, LIMIT,
  external strings/hashes, GET #, BY nosort, missing lookups, WRONGTYPE,
  SORT_RO STORE rejection, TTL clearing, empty-result deletion, and AOF restart.
- Current documented boundary: ALPHA comparison is bytewise in SnugKV, while Redis
  can use locale-aware collation for non-STORE replies. Direct Redis differential
  smoke testing remains the next validation step before stronger parity claims.

### Lua scripting core

- `EVAL` and `EVALSHA` with Redis-style `numkeys`, `KEYS`, and `ARGV` handling.
- `SCRIPT LOAD`, `SCRIPT EXISTS`, and `SCRIPT FLUSH [SYNC|ASYNC]` with a volatile
  per-server SHA-1 cache.
- Embedded Lua 5.1-compatible runtime with filesystem/process libraries removed.
- `redis.call`, `redis.pcall`, `redis.error_reply`, `redis.status_reply`, and
  `redis.sha1hex`.
- RESP2/Lua conversions for integers, bulk strings, arrays, null/false, status
  replies, and error replies.
- Scripts execute under the same global command serialization used by ordinary
  commands and MULTI/EXEC, preventing cross-client interleaving mid-script.
- Scripts can execute inside MULTI/EXEC; runtime errors remain EXEC elements while
  earlier script writes remain applied.
- WATCH is invalidated by transient script changes even if a later command in the
  same script restores the original value.
- Direct EVAL/EVALSHA logical mutations are persisted as one AOF frame. Successful
  writes before a later Lua runtime error remain durable rather than being rolled
  back.
- Malformed commands passed to `redis.pcall` are validated before command routing,
  preventing internal type-guard panics and returning Lua error tables instead.
- Manual Redis comparison confirmed basic EVAL replies, KEYS/ARGV handling,
  SET/GET through `redis.call`, SCRIPT LOAD/EXISTS/FLUSH, identical script SHA-1,
  exact `NOSCRIPT` behavior, and writes surviving later Lua runtime errors.
- Redis reports wrong-arity nested commands as `ERR Wrong number of args calling
  Redis command from script`; SnugKV now matches that wording for the Lua bridge.
  Runtime-error stack text remains VM-specific, while command/write semantics match.
- Each script has a five-second execution limit. Blocking, connection/subscription,
  transaction, nested scripting, and SnugKV administrative commands are rejected
  from the Lua command bridge.

### HyperLogLog

- `PFADD`, `PFCOUNT`, and `PFMERGE`.
- Redis-compatible HLL STRING serialization with sparse/dense handling.
- Duplicate additions, union/merge, TTL preservation, invalid-value handling, and
  large-cardinality automated coverage.
- Manual 100,000-member Redis comparison produced identical `PFCOUNT` (99,471),
  identical `STRLEN` (12,304), and an identical SHA-256 of the serialized value.

### GEO

- `GEOADD`, `GEODIST`, `GEOHASH`, `GEOPOS`, `GEOSEARCH`, and `GEOSEARCHSTORE`.
- Redis-compatible 52-bit coordinate encoding stored directly as native ZSET scores.
- `GEOADD` supports `NX`, `XX`, and `CH` while preserving existing TTL.
- `GEOSEARCH` supports member/coordinate origins, radius/box shapes, sorting,
  `COUNT [ANY]`, `WITHDIST`, `WITHHASH`, and `WITHCOORD`.
- `GEOSEARCHSTORE` replaces the destination, clears previous TTL, supports
  `STOREDIST`, and protects both source/destination during OOM retry eviction.
- Focused tests use the canonical Redis Sicily coordinates and expected hashes,
  scores, positions, and distance values.
- Manual Redis parity confirmed coordinates, hashes, distances, search results,
  store behavior, errors, TTL behavior, and full-decimal GEO ZSET score formatting.
- Current search implementation is intentionally O(source cardinality), scanning
  the packed ZSET instead of maintaining a second permanent spatial index.

### Transactions / WATCH

- `MULTI`, `EXEC`, `DISCARD`, `WATCH`, and `UNWATCH`.
- Queue-time validation failures poison the transaction and make EXEC return
  `EXECABORT` without applying queued writes.
- Runtime errors are returned as individual EXEC array elements while later
  queued commands continue.
- EXEC is serialized against other client commands.
- WATCH invalidates across clients, including change-then-restore sequences and
  key expiration.
- `UNWATCH`, successful/failed EXEC, and disconnects clear transaction state.
- Blocking LIST/ZSET/STREAM commands execute nonblockingly when reached inside
  MULTI/EXEC.
- AOF transaction results are appended as one logical checksummed frame with
  rollback on append failure.
- Validation includes focused two-client TCP tests, full race/vet/fuzz gates, and
  an isolated Python smoke harness that exercises live MULTI/EXEC/WATCH behavior.

### Pub/Sub

- Classic `SUBSCRIBE`, `UNSUBSCRIBE`, `PSUBSCRIBE`, `PUNSUBSCRIBE`, and `PUBLISH`.
- Sharded `SSUBSCRIBE`, `SUNSUBSCRIBE`, and `SPUBLISH`.
- `PUBSUB CHANNELS`, `NUMSUB`, `NUMPAT`, `SHARDCHANNELS`, `SHARDNUMSUB`, and HELP.
- Classic and sharded namespaces are isolated even for identical channel names.
- RESP2 subscribed-mode command restrictions, subscribed `PING`, `RESET`, async
  pushes, disconnect cleanup, and serialized complete socket writes.

### Streams

- Native versioned STREAM storage with backward decode.
- `XADD`, `XLEN`, `XRANGE`, `XREVRANGE`, `XDEL`, `XTRIM`.
- `MAXLEN` and `MINID` trimming, including XADD trimming and LIMIT-bounded trims.
- Redis 8.2 `KEEPREF`, `DELREF`, and `ACKED` reference policies.
- `XDELEX` and `XACKDEL` per-ID status behavior across multiple consumer groups.
- `XREAD` with blocking wakeup, disconnect cancellation, and shutdown cancellation.
- Durable consumer groups: `XGROUP CREATE/DESTROY/SETID/CREATECONSUMER/DELCONSUMER`.
- `XREADGROUP`, `XACK`, `XPENDING`.
- `XCLAIM` and `XAUTOCLAIM`, including ownership transfer, retry counters,
  `JUSTID`, `FORCE`, idle gating, and deleted-PEL cleanup.
- `XINFO STREAM`, `GROUPS`, `CONSUMERS`, and `HELP`.
- Persisted `entries-added`, `max-deleted-entry-id`, and distinct consumer
  attempted/successful interaction timestamps for accurate `idle`/`inactive`.
- STREAM excluded from the generic scalar optimizer after production-config
  testing exposed and fixed a corruption path.
- Export/restore, TTL, rename, WRONGTYPE, race, RESP, redis-cli smoke, and
  reference-policy coverage.

### Sparse-memory optimization

Canonical workload:

```text
keys=1000
value_bytes=16
shards=256
```

Original accounted memory:

```text
527,768 bytes
527.77 B/key total
```

Current verified result:

```text
179,224 bytes
179.22 B/key total
130,072-byte dynamic delta
```

Improvement:

```text
348,544 bytes saved
66.04% lower accounted memory
```

Current measured layout in that benchmark:

```text
index_slot_bytes                16
entry_struct_bytes              32
shard_struct_bytes             192
arena segment descriptor        24 bytes
entry_capacity_after          1184
entry_slots_used              1000
entry_free_slots               184
index_reserved_bytes         71040
entry_bytes                  52888
arena_bytes                  55296
```

The reduction came from structural accounting fixes, embedded/compact index
metadata, smaller sparse index stages, full 4-slot tiny tables plus a zero-size
negative lookup filter, staged entry growth, smaller/lazy arena allocation, entry
TTL sidecars, and compact arena segment descriptors.

## Broader completed surface

- Bounded streaming RESP2 parsing with fragmentation, pipelining, binary payloads,
  request limits, deadlines, connection limits, and graceful shutdown.
- Sharded collision-safe indexing, segmented arenas, expiration, compaction,
  explicit max-memory accounting, OOM rollback, and sampled LRU eviction.
- String, numeric, bit, expiration, key, JSON, HyperLogLog, GEO, scripting, SORT, and administration commands.
- Native HASH, SET, LIST, and ZSET with broad Redis-style command coverage.
- Blocking LIST/ZSET/STREAM waits register before readiness checks and use
  waiter/wakeup signaling instead of polling.
- Linux TCP peer-disconnect monitoring cancels blocked commands without consuming
  queued RESP bytes.
- Logical AOF/snapshot persistence with checksums, restart recovery,
  truncated-final-frame handling, corruption rejection, online AOF rewrite,
  atomic transaction-frame persistence, atomic logical script frames, and
  destination-only SORT STORE persistence.
- Prometheus metrics, separate loopback-only administration listener, Docker,
  Make targets, CI, benchmark harnesses, and soak tooling.

## Current verification discipline

Every feature branch is expected to pass:

```text
go test -race -count=1 ./...
go vet ./...
go test ./internal/resp -run '^$' -fuzz FuzzReadCommand -fuzztime=10s
```

Important durable/multi-key/native-type work also receives focused restart,
redis-cli/TCP, TTL, WRONGTYPE, OOM/rollback, and production-configuration tests as
appropriate.

Recent real-server verification includes:

- HyperLogLog parity at 100,000 unique inputs with identical Redis estimate,
  serialized size, and serialized bytes;
- GEO parity for the Sicily examples and score formatting;
- Lua parity for basic EVAL/KEYS/ARGV, SET/GET via `redis.call`, script cache
  load/exists/flush/EVALSHA, exact SHA/NOSCRIPT behavior, partial writes before
  runtime errors, and Redis-specific nested-command wrong-arity wording;
- transaction queue-time EXECABORT behavior;
- runtime WRONGTYPE inside EXEC while later queued work still commits;
- two-client WATCH invalidation and change-then-restore invalidation;
- `UNWATCH` restoring successful EXEC;
- blocking `BLPOP` returning immediately when executed inside a transaction;
- `RETRYCOUNT 0` surviving encode/decode and subsequent stream operations;
- optimizer-enabled operation without stream corruption;
- `XAUTOCLAIM` returning deleted PEL IDs and cleaning them up;
- exact lifetime `entries-added` and `max-deleted-entry-id` after delete/trim histories;
- `XTRIM MINID ... LIMIT` behavior;
- consumer `idle` dropping after an empty read attempt while `inactive` continues
  from the last successful delivery;
- multi-group `KEEPREF` / `DELREF` / `ACKED` behavior for trimming/deletion,
  including dangling PEL cleanup through `XDELEX` / `XACKDEL`.

## Native datatype benchmark snapshot

The existing 100k-key benchmark tables remain in `benchmarks/README.md`. Recorded
results show the packed container payloads become increasingly competitive as
collection cardinality grows, while tiny collections can still pay more fixed
per-key overhead than Redis.

Do not turn single runs into universal latency claims; use paired/multi-run tests
when evaluating CPU tradeoffs.

## Remaining engineering work

1. Redis Functions and remaining scripting parity (`EVAL_RO`, `SCRIPT KILL/DEBUG`, command-flag/ACL semantics, differential testing).
2. COPY/migration scope.
3. RESP3 and CLIENT/CONFIG/ACL/COMMAND tooling compatibility.
4. Differential Redis edge-case audits for SORT and the completed Streams surface.
5. Optional legacy `GEORADIUS*` aliases if real client usage requires them.
6. Fresh release-scale benchmarks, multi-run variance, million-record datasets,
   broader client compatibility, retained long-duration soak evidence, dedicated
   large-GEO benchmarking, script runtime/cache benchmarks, and SORT external-key
   performance testing.
7. Distributed features only after the single-node target is mature.

See `PLAN.md`, `COMPATIBILITY.md`, `KNOWN-LIMITATIONS.md`, and GitHub issue #55.
