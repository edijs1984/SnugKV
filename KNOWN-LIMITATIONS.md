# Known Limitations

SnugKV is currently an alpha-stage, single-node RESP2/RESP3 datastore. It has broad
coverage across the common Redis datatype families, including Streams, Pub/Sub,
transactions, HyperLogLog, modern GEO, Lua scripting/read-only scripting, Redis
Functions core/management, `SORT` / `SORT_RO`, single-database `COPY`, and the
current CLIENT tooling slice, but it is not a complete Redis replacement.

## Protocol

- RESP2 is supported and remains regression-covered.
- RESP3 can be negotiated with `HELLO 3`; `HELLO 2` switches the connection back.
- The audited RESP3 surface includes null/map/set/double/verbatim reply forms used by current commands, nested COMMAND/ACL shapes, and Pub/Sub push frames.
- The broad Redis 8.2 command-shape differential sweep is complete; RESP3 attributes and exhaustive client-library-specific parity are not yet claimed.

See [COMPATIBILITY.md](COMPATIBILITY.md).

## Redis command coverage

Strings, counters, expiration, bit operations, key inspection, COPY, HASH, SET,
LIST, ZSET, SORT/SORT_RO, HyperLogLog, modern GEO, Streams/consumer groups,
classic/sharded Pub/Sub, transactions/WATCH, Lua scripting, Redis Functions core,
CLIENT connection/tooling commands, basic JSON, memory inspection, and
administration are implemented to the documented scope.

Major Redis-compatible features still not implemented or incomplete:

- full Lua/Functions parity (`SCRIPT DEBUG`, `allow-oom`, exact command flags/ACL behavior);
- Redis-RDB byte compatibility for `FUNCTION DUMP` / `RESTORE` payloads;
- cross-database COPY and broader migration/transfer command scope;
- optional RESP3 client-library smoke coverage and unused RESP3 types/attributes if required;
- deeper dynamic SORT/script/Function ACL-policy edge auditing;
- advanced CLIENT tracking/caching/redirection features;
- replication;
- Sentinel-style failover;
- cluster mode;
- modules.

The current JSON commands are not a complete RedisJSON implementation.

## Lua scripting and Functions boundaries

The implemented scripting surface is `EVAL`, `EVALSHA`, `EVAL_RO`, `EVALSHA_RO`,
`SCRIPT LOAD`, `SCRIPT EXISTS`, `SCRIPT FLUSH [SYNC|ASYNC]`, and `SCRIPT KILL`.
Scripts receive `KEYS` and `ARGV` and can use `redis.call`, `redis.pcall`,
`redis.error_reply`, `redis.status_reply`, and `redis.sha1hex`.

Redis Functions support includes `FUNCTION LOAD [REPLACE]`, `FUNCTION LIST
[LIBRARYNAME pattern] [WITHCODE]`, `FUNCTION DELETE`, `FUNCTION FLUSH
[SYNC|ASYNC]`, `FUNCTION DUMP`, `FUNCTION RESTORE [APPEND|REPLACE|FLUSH]`,
`FUNCTION STATS`, `FUNCTION KILL`, `FUNCTION HELP`, `FCALL`, and `FCALL_RO`.
`redis.register_function()` supports the common positional form and table-form
registration with `description` plus the currently safe standalone flags
`no-writes`, `allow-stale`, `no-cluster`, and `allow-cross-slot-keys`. Loaded
library Lua state is retained while the process is running, so library-local state
can persist across calls.

`FUNCTION STATS` can be queried from another client while an FCALL is executing.
It reports the active function name, original command vector, elapsed duration,
and Lua engine library/function counts without waiting on the normal global
command-serialization mutex. `FUNCTION HELP` returns the Redis-style Functions
subcommand help surface.

`FUNCTION KILL` can cancel a running FCALL/FCALL_RO only before the invocation has
crossed a dataset-write boundary. After the first writable nested command is
dispatched, KILL returns Redis-style `UNKILLABLE`; when no function is running it
returns `NOTBUSY`. This prevents cancellation after partial mutation.

`SCRIPT KILL` uses the same safety model for EVAL/EVALSHA/EVAL_RO/EVALSHA_RO. A
script is killable until its first writable nested dataset command is dispatched;
after that KILL returns Redis-style `UNKILLABLE`. Idle KILL returns `NOTBUSY`, and
read-only EVAL variants remain killable for their entire execution. See
[docs/SCRIPT-KILL.md](docs/SCRIPT-KILL.md).

Current boundaries are intentional and documented rather than silently emulated:

- scripts/functions run in an embedded Lua 5.1-compatible runtime, not Redis's exact Lua VM;
- filesystem/process libraries are removed;
- each invocation has a five-second execution limit;
- blocking commands, subscription/connection state, transaction commands, nested
  scripting/functions, and SnugKV admin commands are rejected through the Lua bridge;
- `EVAL_RO`, `EVALSHA_RO`, `FCALL_RO`, and `no-writes` functions reject write or
  replication-capable commands, including `PUBLISH`/`SPUBLISH`;
- the EVAL SHA cache is volatile and process-local;
- when AOF or snapshot persistence is configured, Function library definitions are
  restored across restart from a checksum-protected atomic sidecar; without either
  persistence mode configured, Function libraries remain process-local and volatile;
- `FUNCTION DUMP` uses SnugKV's versioned `SNUGF001` payload rather than Redis RDB
  Function bytes, so Redis and SnugKV dump payloads are not cross-restorable;
- Function-local Lua variables are not serialized and reset when a library is
  restored or reconstructed after process restart;
- `no-writes`, `allow-stale`, `no-cluster`, and `allow-cross-slot-keys` are supported
  for the current standalone semantics; `allow-oom` remains intentionally deferred
  until exact scoped memory-admission behavior is implemented;
- `SCRIPT DEBUG`, exact Redis command-flag/ACL/OOM behavior, and every Lua edge
  case still need differential hardening.

Scripts and function calls are atomic with respect to other SnugKV clients because
they execute under the same command-serialization boundary as transactions. Lua
runtime errors do not roll back successful writes already made before the error.
With AOF enabled, resulting logical changes from one direct writable EVAL or FCALL
are persisted as one frame.

## ACL boundaries

Core AUTH/ACL support is implemented, including named-user authentication,
command/category rules, key patterns, `ACL CAT`, `DRYRUN`, `GENPASS`, `LOG`,
transaction enforcement, `SAVE`/`LOAD`, and configured ACL-file startup restore.
Password persistence uses hashes rather than plaintext, failed ACL-file loads are
atomic, and malformed configured ACL files fail startup closed.

The audited ACL surface includes channel patterns, `allchannels` /
`resetchannels`, selector parsing/serialization and root-or-selector evaluation,
and the hardened SETUSER reset/password/hash/sanitize modifier set. Remaining ACL
work is deeper auditing for dynamically resolved SORT external keys and scripting/
Function ACL-policy edge cases rather than a known core ACL feature gap.

See [docs/ACL-COMPATIBILITY.md](docs/ACL-COMPATIBILITY.md).

## CLIENT boundaries

The implemented CLIENT surface includes `CLIENT ID`, `GETNAME`, `SETNAME`,
`SETINFO LIB-NAME|LIB-VER`, `INFO`, `LIST`, `LIST ID`, `LIST TYPE NORMAL`,
`KILL ID [SKIPME YES|NO]`, `UNBLOCK [TIMEOUT|ERROR]`, and `HELP`. The registry is
connection-scoped/concurrency-safe and targeted KILL/UNBLOCK behavior has been
compared directly with Redis, including self-kill and blocked-connection survival.
See [docs/CLIENT-COMPATIBILITY.md](docs/CLIENT-COMPATIBILITY.md).

Advanced Redis CLIENT tracking/caching/redirection features are not implemented,
and only the NORMAL client class is currently meaningful for LIST TYPE filtering.
Additional client classes should be added only when the corresponding topology or
connection modes exist.

## SORT boundaries

`SORT` and `SORT_RO` support LIST/SET/ZSET sources, numeric ordering, `ALPHA`,
`ASC`/`DESC`, `LIMIT`, external `BY`, repeated `GET`, `GET #`, hash-field
patterns, and `SORT ... STORE` replacement as a native LIST. STORE clears the
previous destination TTL, deletes an empty-result destination, persists the
destination through the logical AOF path, and stores missing GET results as empty
strings.

Current compatibility boundaries:

- SnugKV uses bytewise comparison for `ALPHA`; Redis can use locale-aware collation
  for non-STORE ALPHA replies, so locale-sensitive/non-ASCII ordering can differ;
- SET native iteration order under a constant/no-wildcard `BY` is implementation
  dependent, so exact order is not promised for that intentionally-unsorted case;
- core command/key ACL enforcement is implemented, but deeper Redis parity for
  dynamically resolved BY/GET external keys is still being audited;
- Redis Cluster slot restrictions remain outside SnugKV's single-node scope.

The implemented common SORT surface has been compared manually against Redis for
LIST/SET/ZSET sources, BY/GET/hash patterns, nosort, STORE/TTL, missing values,
and tested error replies.

## COPY boundaries

`COPY source destination [DB 0] [REPLACE]` is implemented. It deep-copies the
logical value, preserves datatype and absolute expiry, leaves the source intact,
and can replace any destination type with `REPLACE`.

SnugKV exposes only database 0. `COPY ... DB 0` is accepted, while every nonzero
DB index is rejected with `ERR DB index is out of range`; cross-database COPY is
not emulated. Migration/transfer commands and multi-database semantics remain out
of scope for the current single-node target. The documented DB0 COPY surface has
been manually differentially tested against Redis for return values, errors, TTL,
native type preservation, REPLACE, and deep-copy independence.

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
- COMMAND metadata and common CONFIG tooling are complete. Core AUTH/ACL command
  and key enforcement is also implemented; remaining ACL work is limited to the
  channel/selector/uncommon-modifier edge surface described above. Advanced CLIENT
  tracking/caching remains incomplete.
- The modern GEO command set has focused command-level compatibility tests; large
  dataset differential/performance testing is intentionally still pending.
- Lua scripting/read-only variants and Functions core have focused unit,
  durability, transaction, restart-persistence, live introspection, cancellation,
  standalone-safe flag, and live Redis differential coverage; deeper
  command-flag/ACL/OOM auditing remains.
- `FUNCTION DUMP`/`RESTORE` command policy/error semantics have automated coverage,
  but byte-level payload compatibility with Redis is intentionally not claimed.

## Deployment topology

- SnugKV is single-node.
- There is no automatic replication or failover.
- High availability is not provided by SnugKV itself.

## Durability

SnugKV includes logical AOF/snapshot persistence and recovery testing, including
truncated-final-frame recovery, checksum-corruption rejection, append rollback,
online AOF rewrite, native datatype restore coverage, single logical AOF frames
for successful transaction results, single logical frames for direct script and
writable FCALL mutations including partial writes before runtime errors,
destination-only persistence/replay for `SORT ... STORE`, and destination-only
COPY persistence.

When AOF or snapshot persistence is configured, Function library definitions are
stored separately from user keyspace records in an atomic checksummed sidecar
(`<aof>.functions`, or `<snapshot>.functions` when AOF is disabled). Successful
`FUNCTION LOAD`, `DELETE`, `FLUSH`, and `RESTORE` rewrite the complete durable
registry snapshot. Startup validates and compiles the sidecar before exposing the
restored Functions; corrupt durable Function state causes recovery to fail rather
than silently dropping libraries. This sidecar write path is synchronous and does
not inherit the main AOF `everysec`/`no` fsync relaxation.

For alpha use:

- maintain external backups for important data;
- test restore procedures;
- do not treat SnugKV as the sole copy of critical data.

## Memory accounting and small-key overhead

`SNUG.STATS` reports engine-accounted memory, not process RSS. RSS additionally
includes Go runtime state, goroutine stacks, network/persistence buffers,
optimizer scratch space, allocator overhead, temporary EVAL Lua VM state, and
persistent loaded Function Lua states.

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
- SORT materializes the selected source collection and performs in-memory ordering;
  ordinary sorting is O(N log N), and external BY/GET patterns add key/hash lookups.
- COPY materializes the logical source representation and allocates an independent
  destination value, so copying large values temporarily requires memory for both.
- lex ZSET operations construct a temporary lexicographic view rather than keeping
  a second permanent index.
- LIST uses one packed logical blob, so very large head mutations can be O(total
  encoded bytes).
- Streams use packed logical state, including group/PEL metadata, rather than a
  Redis radix-tree/listpack layout.
- EVAL currently creates an isolated VM per invocation rather than pooling VM state
  or compiled chunks; loaded Function libraries keep their own Lua VM/state until
  delete/flush/replacement or process exit.

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

SnugKV has not received an independent security audit. The scripting/Functions
runtime is sandboxed by omitting filesystem/process libraries and limiting
execution time, but this is not a substitute for a security review. Follow
[SECURITY.md](SECURITY.md).

## Production use

The current alpha is suitable for evaluation, local development, benchmarks,
experiments, and non-critical caches/queues where data can be recreated. It should
not be presented as a complete replacement for Redis in mission-critical
production systems yet.