## 1M Redis scalar benchmark / GET fast-path tuning

- Added a black-box RESP2/TCP benchmark path for true pipelined GET; the previous
  GET workload was sequential request/response despite reporting a pipeline flag.
- Current 1,000,000-key / 256-byte repetitive development reference: Redis 8.2
  clean run 429,664 GET/s; SnugKV raw 526,951 GET/s; optimized SnugKV latest three-run average 599,559 GET/s (best 612,393) after
  byte-key lookup, buffered RESP length parsing, direct hot-codec DecodeInto
  dispatch, and reusable per-connection GET key decoding; best p50/p95/p99 was
  5.95/9.88/14.30 us.
- Optimized accounted memory settles around 162.64 MB versus Redis reported
  memory around 393.26 MB on this deliberately repetitive workload, about 58.6%
  lower. This is workload-specific and not representative of incompressible data.
- GET tuning completed so far: true pipelining, sparse optimizer metadata,
  optimizer admission prefilter, scalar codec bypass for long values, direct
  decoded bulk framing, reusable per-connection decode scratch, single GET clock
  read, known-GET TCP dispatch bypass, and removal of the redundant shard entry
  rewrite after pointer-based activity metadata updates.
- Three consecutive SnugKV runs now average 599,559 GET/s versus 435,561 GET/s across
  three Redis 8.2 runs in the same comparison window, about 25.7% higher on this
  exact workload. Machine load caused substantial variance during tuning,
  so repeated clean runs remain mandatory before product claims.
- The next profiling target is the remaining key lookup conversion/index path,
  followed by codec decode and RESP parsing only where measurements justify it.
- Detailed methodology and caveats are recorded in `benchmarks/README.md`.

## Redis MIGRATE

- Implemented Redis 8.2-compatible `MIGRATE` for the audited standalone surface.
- Supports `COPY`, `REPLACE`, `KEYS`, `AUTH`, and `AUTH2`.
- Live SnugKV -> Redis and Redis -> SnugKV migration passed for moves, copies, replacement, TTL transfer, missing keys, multi-key batches, and partial BUSYKEY failures.
- Matches Redis per-key acknowledgement semantics: earlier successful keys stay moved even if a later RESTORE fails.
- Partial source deletions are persisted and invalidate WATCH.
- Focused tests, full race suite, vet, and RESP fuzz gates are green.
- Details: `docs/MIGRATE-COMPATIBILITY.md`.

## Redis key-level DUMP / RESTORE

- Added Redis 8.2-compatible key payloads with RDB version 12 and CRC64 validation.
- Live two-way Redis 8.2 cross-restore is verified for STRING, HyperLogLog, HASH, SET, LIST, ZSET, and STREAM.
- Audited STRING/HASH/SET/LIST/ZSET/HLL fixtures produce byte-identical payloads in both implementations.
- STREAM live-entry payloads are byte-identical; Redis STREAM payloads containing deleted tombstones restore semantically into SnugKV with last-generated ID, max-deleted-entry-id, entries-added, consumer groups, consumers, and PEL state preserved.
- SnugKV does not retain deleted stream field/value tombstone payloads, so post-XDEL re-dumps are not claimed byte-identical.
- Audit harnesses live under `compat/keyspace/`; details: `docs/DUMP-RESTORE-COMPATIBILITY.md`.



## ACL compatibility completion

The audited single-node Redis 8.2 ACL milestone is complete. Coverage now includes
AUTH/user management, command/category/key rules, classic and sharded Pub/Sub
channel patterns, SETUSER reset/password/hash/alias/sanitize modifiers, selectors
with atomic root-or-selector rule-set evaluation, DRYRUN, LOG, MULTI/EXEC
re-authorization, ACL SAVE/LOAD, and restart persistence. Live differential
scripts for channel rules, SETUSER modifiers, and selectors produced matching
Redis/SnugKV output after final error-string hardening.

Core RESP3 support has landed alongside RESP2. Redis 8.2 differential audits now
cover HELLO negotiation/switching, protocol-dependent reply shapes, nested
COMMAND/ACL structures, and classic/pattern/sharded Pub/Sub push semantics.

# Progress

## Current milestone

SnugKV now has a broad single-node RESP2/RESP3 command surface with native HASH, SET,
LIST, ZSET, and STREAM types, logical durability, memory accounting, adaptive
scalar encoding, observability, operational tooling, classic/sharded Pub/Sub,
Redis-style transactions with optimistic locking, HyperLogLog, modern GEO,
read/write Lua scripting including `SCRIPT KILL`, Redis Functions through
`FUNCTION KILL` plus the standalone-safe Function flag subset, `SORT` /
`SORT_RO`, single-database `COPY`, and the current CLIENT management/tooling
slice.

COMMAND metadata/tooling and the common CONFIG compatibility milestone are
complete. Core AUTH/ACL support is also implemented through command/category/key
authorization, transaction enforcement, ACL LOG, SAVE/LOAD, and startup ACL-file
persistence. The audited ACL core and broad RESP3 command-shape milestone are
complete. The Redis 8.2 Streams differential edge-case audit is also complete.
The current compatibility focus is optional RESP3 client-library/attribute
hardening, exact `allow-oom` semantics, `SCRIPT DEBUG`, migration/transfer scope
beyond COPY, deeper dynamic SORT/script/Function ACL audits, and advanced CLIENT
tracking/caching only where real clients require it.

## Recently completed

- Streams Redis 8.2 differential edge-case audit: deterministic passes covered
  IDs/ranges, exact and approximate trim grammar, group creation/SETID/
  ENTRIESREAD/lag, XREADGROUP/XPENDING, XCLAIM/XAUTOCLAIM, consumer deletion,
  XINFO, and Redis 8.2 KEEPREF/DELREF/ACKED policies. The audit fixed XACKDEL
  dangling-reference status, trim `max-deleted-entry-id`, XRANGE COUNT 0,
  XTRIM LIMIT grammar/error parity, group entries-read reporting, DELCONSUMER PEL
  cleanup, never-active consumer metadata, empty XPENDING shape, and BUSYGROUP/
  NOGROUP framing. Remaining observed differences are intentional Redis-internal
  radix-tree diagnostics and exact approximate-`~` trimming granularity. See
  `docs/STREAMS-DIFFERENTIAL-AUDIT.md`.

- Broad RESP3 Redis 8.2 structural sweep: ZSET score doubles/pair replies, GEO
  coordinate doubles, XREAD/XREADGROUP maps, XINFO maps, FUNCTION STATS maps,
  CLIENT INFO verbatim strings, CONFIG GET maps, and null-bearing replies all
  matched the Redis oracle. The final structural diff contained only expected
  HELLO module metadata and SCAN dataset/order differences. The sweep also exposed
  and fixed independent XINFO GROUPS entries-read/lag inference semantics.

- Core RESP3 protocol support: per-connection `HELLO 3` / `HELLO 2` switching,
  RESP3 null/map/set/double/verbatim shapes for the audited surface, nested
  COMMAND INFO and ACL GETUSER conversions, and RESP3 Pub/Sub push frames with
  Redis 8.2 subscribed-mode behavior. Direct Redis-vs-SnugKV oracle runs matched
  the targeted wire types; full race tests and RESP2 Pub/Sub regressions remained green.

### COMMAND, CONFIG, and ACL compatibility

- COMMAND tooling now covers Redis-shaped ten-field INFO, documented-surface DOCS,
  GETKEYS, GETKEYSANDFLAGS, parent/subcommand metadata, and audited dynamic-key
  extraction for scripting, COPY, BITOP, ZSET algebra/pops, and stream reads.
- CONFIG supports GET/SET/RESETSTAT/REWRITE/HELP for SnugKV-backed settings,
  runtime maxmemory/policy/maxclients/appendfsync mutation, atomic JSON rewrite,
  and restart persistence.
- AUTH supports default-user and named-user authentication with immediate
  revocation when a user is disabled or deleted.
- ACL management covers WHOAMI, USERS, GETUSER, LIST, SETUSER, DELUSER, CAT,
  DRYRUN, GENPASS, LOG, SAVE, LOAD, and HELP.
- Command rules support explicit commands and Redis 8.2 categories with ordered
  overrides; key rules use shared fixed/dynamic command-key discovery.
- MULTI queue-time ACL denials poison the transaction, and EXEC re-authorizes
  queued commands so permission changes take effect before execution.
- ACL LOG records auth/command/key denials, aggregates repeated equivalent
  violations, and returns Redis-shaped newest-first records.
- ACL SAVE/LOAD uses Redis-style ACL-file lines with hashed passwords; LOAD is
  atomic on malformed input, and configured ACL files restore on startup.
- Live restart validation confirmed persisted users survive process restart and
  malformed configured ACL files fail startup closed.
- See `docs/COMMAND-COMPATIBILITY.md`, `docs/CONFIG-COMPATIBILITY.md`, and
  `docs/ACL-COMPATIBILITY.md`.

### CLIENT management/tooling

- Connection-scoped stable `CLIENT ID` with concurrency-safe per-listener client
  registry state.
- `CLIENT GETNAME`, `SETNAME`, and `SETINFO LIB-NAME|LIB-VER`.
- `CLIENT INFO` and `CLIENT LIST` with Redis-shaped core metadata fields.
- `CLIENT LIST ID <id> [<id> ...]` and `CLIENT LIST TYPE NORMAL` filtering.
- `CLIENT KILL ID <id> [SKIPME YES|NO]`, including Redis-compatible self-kill
  behavior.
- `CLIENT UNBLOCK <id> [TIMEOUT|ERROR]` wakes blocked clients without tearing down
  the connection and returns Redis-shaped timeout/error results.
- `CLIENT HELP` covers the implemented surface.
- Admin-listener registry initialization was hardened after a race-suite regression
  exposed a nil-map panic; defensive registry initialization now prevents that
  failure class.
- Focused CLIENT tests, blocking/disconnect regression tests, the full race suite,
  `go vet`, RESP fuzz, and build passed after the fix.
- Live Redis 6379 vs SnugKV 6380 differential testing matched the implemented
  INFO/LIST, LIST filters, targeted kill, self-kill/SKIPME, UNBLOCK TIMEOUT/ERROR,
  invalid ID/reason, connection-survival, and tested arity/error behavior.
- See `docs/CLIENT-COMPATIBILITY.md`.

### Redis Functions and read-only scripting

- `EVAL_RO` and `EVALSHA_RO` share the scripting cache/key/argv path with EVAL but
  reject nested commands that write or may replicate, including PUBLISH/SPUBLISH.
- `FUNCTION LOAD [REPLACE]`, `LIST`, `DELETE`, `FLUSH`, `FCALL`, and `FCALL_RO`
  are implemented on persistent library-local Lua VMs.
- Table-form `redis.register_function()` supports descriptions and the current
  standalone-safe flags `no-writes`, `allow-stale`, `no-cluster`, and
  `allow-cross-slot-keys`, plus the audited `allow-oom` behavior.
- `FUNCTION DUMP` / `RESTORE` implement default APPEND plus REPLACE/FLUSH restore
  policy using Redis 8.2-compatible RDB Function payloads, including CRC64/version
  validation and Redis LZF string encoding/decoding.
- Function library definitions survive restart when AOF or snapshot persistence is
  configured, using an atomic checksummed sidecar separate from user keyspace data.
- `FUNCTION STATS` exposes the active Function name, command vector, duration, and
  engine counts without blocking behind the global durability mutex; `FUNCTION
  HELP` exposes the Functions help surface.
- `FUNCTION KILL` cancels a running FCALL/FCALL_RO before its first dataset-write
  boundary, returns `NOTBUSY` when idle, and returns `UNKILLABLE` after a writable
  nested command has been dispatched.
- `SCRIPT KILL` uses the corresponding first-write safety boundary for
  EVAL/EVALSHA/EVAL_RO/EVALSHA_RO and is implemented with Redis-style
  NOTBUSY/UNKILLABLE behavior.
- KILL and the first write synchronize on the same running-invocation state,
  closing the race where cancellation could otherwise report success immediately
  before a write slipped through.
- Writable FCALL changes are persisted in one logical AOF frame; writes completed
  before a later runtime error remain applied and durable.
- Live Redis differential testing covered EVAL_RO/EVALSHA_RO, FCALL/FCALL_RO,
  `no-writes`, persistent library-local state, LOAD REPLACE, DELETE/FLUSH, and
  FUNCTION LIST metadata. The RESP type for Function flags was corrected to match
  Redis simple-string encoding.
- Live Function dump audit verified Redis 8.2 -> SnugKV restore and SnugKV -> Redis
  restore, successful FCALL_RO after both directions, and byte-identical payload
  output for the shared audited fixture.
- `SCRIPT DEBUG`, `allow-oom`, and Redis-RDB Function payload compatibility are
  complete for the audited Redis 8.2 surface; remaining work is deeper optional
  scripting edge/parity hardening.

### COPY

- `COPY source destination [DB 0] [REPLACE]` is implemented for SnugKV's single
  logical database.
- Missing sources return 0; existing destinations return 0 unless `REPLACE` is
  supplied; REPLACE can overwrite any Redis-visible datatype.
- The source stays unchanged and the destination receives an independent logical
  copy rather than sharing mutable storage.
- STRING-style values and native HASH, SET, LIST, ZSET, and STREAM values retain
  their logical datatype and contents through the persistence representation.
- Existing source TTL is copied as the same absolute expiry instead of being
  cleared or restarted from a relative duration.
- `DB 0` is accepted for Redis grammar compatibility. Nonzero destination DBs are
  rejected with `ERR DB index is out of range` because SnugKV exposes only DB 0.
- Destination-only AOF journaling/replay is covered, with rollback preserving the
  previous destination if persistence append fails.
- Max-memory admission uses atomic logical Restore, so OOM leaves both source and
  previous destination unchanged. Source and destination are protected from
  eviction during retry.
- COPY can run inside MULTI/EXEC and successful destination changes invalidate
  WATCH state on other clients.
- Successful copies of LIST, ZSET, and STREAM values wake blockers waiting on the
  destination key.
- Focused automated coverage includes basic/no-op/REPLACE cases, option/error
  handling, TTL preservation, deep-copy independence, native container types,
  restart persistence, OOM rollback, transactions, and WATCH invalidation.
- Direct Redis 6379 vs SnugKV 6380 differential testing matched return values,
  tested error wording, TTL behavior, LIST/SET/ZSET type preservation, REPLACE,
  and deep-copy independence. `DB 1` remains the intentional single-database
  boundary.

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
- Manual Redis 6379 vs SnugKV 6380 differential testing matched the implemented
  common surface command-for-command, including errors and STORE/TTL behavior.
- Current documented boundary: ALPHA comparison is bytewise in SnugKV, while Redis
  can use locale-aware collation for non-STORE replies.

### Lua scripting core

- `EVAL`, `EVALSHA`, `EVAL_RO`, and `EVALSHA_RO` with Redis-style `numkeys`,
  `KEYS`, and `ARGV` handling.
- `SCRIPT LOAD`, `SCRIPT EXISTS`, `SCRIPT FLUSH [SYNC|ASYNC]`, and `SCRIPT KILL`
  with a volatile per-server SHA-1 cache and safe cancellation semantics.
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
  exact `NOSCRIPT` behavior, writes surviving later Lua runtime errors, and
  read-only nested-write rejection.
- Redis reports wrong-arity nested commands as `ERR Wrong number of args calling
  Redis command from script`; SnugKV matches that wording for the Lua bridge.
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
- Completed Redis 8.2 differential edge-case audit across IDs/ranges, trimming,
  groups/ENTRIESREAD/lag, pending entries, claims/autoclaims, consumer lifecycle,
  XINFO, reference policies, and tested error behavior. See
  `docs/STREAMS-DIFFERENTIAL-AUDIT.md`.

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
- String, numeric, bit, expiration, key, JSON, HyperLogLog, GEO, scripting,
  Functions, SORT, COPY, COMMAND/CONFIG tooling, AUTH/ACL enforcement, CLIENT
  management, and administration commands.
- Native HASH, SET, LIST, and ZSET with broad Redis-style command coverage.
- Blocking LIST/ZSET/STREAM waits register before readiness checks and use
  waiter/wakeup signaling instead of polling.
- Linux TCP peer-disconnect monitoring cancels blocked commands without consuming
  queued RESP bytes.
- Logical AOF/snapshot persistence with checksums, restart recovery,
  truncated-final-frame handling, corruption rejection, online AOF rewrite,
  atomic transaction-frame persistence, atomic logical script/FCALL frames,
  Function registry restart sidecars, destination-only SORT STORE persistence,
  and destination-only COPY persistence.
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
- SORT/SORT_RO parity for LIST/SET/ZSET sources, BY/GET/hash patterns, nosort,
  STORE/TTL behavior, missing values, and exact tested error replies;
- COPY parity for DB0 return values/errors, TTL, native type preservation,
  REPLACE, and independent-copy behavior;
- Lua parity for basic EVAL/KEYS/ARGV, SET/GET via `redis.call`, script cache
  load/exists/flush/EVALSHA, exact SHA/NOSCRIPT behavior, partial writes before
  runtime errors, Redis-specific nested-command wrong-arity wording, read-only
  nested-write rejection, and safe SCRIPT KILL behavior;
- Functions parity for FCALL/FCALL_RO, `no-writes`, persistent local state,
  LOAD REPLACE, DELETE/FLUSH, LIST metadata formatting, and standalone-safe
  Function flag handling;
- CLIENT parity for IDs/names/setinfo, INFO/LIST/list filters, targeted kill,
  self-kill/SKIPME, UNBLOCK TIMEOUT/ERROR, invalid IDs/reasons, and connection
  survival after unblock;
- COMMAND parity for audited INFO/DOCS/key-discovery metadata and dynamic-key
  commands;
- CONFIG parity for GET/SET/RESETSTAT/REWRITE/HELP on the supported settings;
- AUTH/ACL parity for user management, command/category/key rules, DRYRUN/GENPASS,
  transaction enforcement, ACL LOG aggregation, SAVE/LOAD, and startup restore;
- end-to-end Function-library restart persistence on a live SnugKV process;
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

## Redis-wire SET/load optimization milestone — 2026-09-20

The million-key RESP2/TCP load path was profiled and optimized without removing
encoding/compression features. The completed work includes concurrent load-worker
support in `cmd/rediswirebench`, transient raw-clone removal on SET, a reusable
buffered plain-SET decoder, reusable optimizer candidate scratch, borrowed raw
optimizer fallbacks, known-hash indexed publication, and backlog-aware optimizer
CPU yielding.

On the 4-logical-CPU development machine with 1,000,000 random 256-byte values,
8 workers, and pipeline depth 256, the final five-run SnugKV median was 315,256
SET/s (best 318,331) versus the recorded Redis 8.2 reference of about 318,145
SET/s. SnugKV's engine-accounted load delta was 362.27 B/key versus Redis's
~392.39 B/key on that exact workload. Three sustained 5,000,000-key SnugKV runs
had a 299,270 SET/s median and 352.68 B/key load delta.

Allocation profiling during the work reduced 1M-load allocation traffic from
roughly 1.38 GB to about 467 MB. The remaining dominant allocation sites are
primarily persistent arena/index/entry growth rather than request/optimizer
garbage. CPU profiling still shows background compression as a meaningful cost
on intentionally incompressible values, so future tuning should preserve the
memory feature set and focus on scheduling/admission rather than benchmark-only
feature disabling.

See `benchmarks/README.md` for exact runs, caveats, and reproduction details.

## Native datatype benchmark snapshot

The existing 100k-key benchmark tables remain in `benchmarks/README.md`. Recorded
results show the packed container payloads become increasingly competitive as
collection cardinality grows, while tiny collections can still pay more fixed
per-key overhead than Redis.

Do not turn single runs into universal latency claims; use paired/multi-run tests
when evaluating CPU tradeoffs.

## Remaining engineering work

1. Optional RESP3 client-library smoke coverage and attribute-frame support if required.
2. Deeper dynamic SORT/script/Function ACL edge audits.
3. `SCRIPT DEBUG`, exact `allow-oom`, deeper command-flag/OOM semantics, and
   optional Redis-RDB Function payload parity.
4. Migration/transfer scope beyond single-database COPY and advanced CLIENT tracking/caching/redirection where required.
5. Optional legacy `GEORADIUS*` aliases if real client usage requires them.
6. Fresh release-scale benchmarks, multi-run variance, million-record datasets,
   broader client compatibility, retained long-duration soak evidence, dedicated
   large-GEO benchmarking, script runtime/cache benchmarks, and SORT external-key
   performance testing.
7. Distributed features only after the single-node target is mature.

See `PLAN.md`, `COMPATIBILITY.md`, `KNOWN-LIMITATIONS.md`, and GitHub issue #55.