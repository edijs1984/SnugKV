# SnugKV Compatibility

SnugKV targets common Redis-compatible application workloads while staying
explicit about protocol, command, and topology differences. This document tracks
the public compatibility boundary; issue #55 tracks the implementation backlog.

## Current status

SnugKV is a single-node RESP2/RESP3 datastore with native STRING-style scalar storage,
HASH, SET, LIST, ZSET, and STREAM semantics. The broad Streams and consumer-group
surface is implemented, including blocking reads, pending-entry management,
claiming, XINFO introspection, lifetime stream metadata, MAXLEN/MINID trimming,
Redis 8.2 `KEEPREF` / `DELREF` / `ACKED` policies, `XDELEX`, and `XACKDEL`.
The documented Streams surface has completed a live Redis 8.2 differential
edge-case audit covering IDs, ranges, trimming, groups, pending entries, claims,
XINFO, reference policies, consumer lifecycle, and tested error behavior.

Classic and sharded Pub/Sub are implemented, along with connection-scoped Redis
transactions and optimistic locking (`MULTI`, `EXEC`, `DISCARD`, `WATCH`,
`UNWATCH`). HyperLogLog (`PFADD`, `PFCOUNT`, `PFMERGE`), the modern GEO surface
(`GEOADD`, `GEODIST`, `GEOHASH`, `GEOPOS`, `GEOSEARCH`, `GEOSEARCHSTORE`), Lua
scripting/read-only scripting (`EVAL`, `EVALSHA`, `EVAL_RO`, `EVALSHA_RO`,
`SCRIPT LOAD/EXISTS/FLUSH/KILL`), Redis Functions (`FUNCTION
LOAD/LIST/DELETE/FLUSH`, `DUMP`, `RESTORE`, `STATS`, `KILL`, `HELP`, `FCALL`,
`FCALL_RO`), `SORT` / `SORT_RO`, single-database `COPY`, and the current CLIENT
management/tooling slice are also implemented.

COMMAND metadata, common CONFIG tooling, the audited single-node AUTH/ACL
surface, and the core RESP3 protocol milestone are complete. ACL enforcement
covers commands/categories, keys, Pub/Sub channels, selectors, transactions,
DRYRUN/LOG, SETUSER modifier parity, and ACL-file persistence. RESP3 now includes
per-connection protocol state, `HELLO 3`, protocol switching, the RESP3 reply
shapes required by the audited surface, and RESP3 Pub/Sub pushes/subscribed-mode
semantics. The broad Redis 8.2 command-shape differential sweep is complete; remaining
RESP3 work is optional client-library smoke coverage and attribute/unused-type
support only where required. The largest remaining compatibility areas include
`SCRIPT DEBUG`, exact `allow-oom` behavior, migration/transfer scope, and advanced
CLIENT tracking/caching features. Replication, Sentinel-style failover, and Cluster
remain outside the current single-node scope.

## Client compatibility

| Client | Version tested | Protocol | Status |
|---|---:|---:|---|
| redis-cli | local Redis CLI | RESP2 | ✅ Pass |
| ioredis | compatibility harness | RESP2 | ✅ Pass |
| node-redis | compatibility harness | RESP2 | ✅ Pass |
| redis-py | 8.1.0 | RESP2 | ✅ Pass |
| go-redis | v9.22.0 | RESP2 | ✅ Pass |

Passing these smoke tests does not mean every command exposed by each client
library is implemented.

## RESP protocol support

RESP2 remains fully supported. Binary-safe bulk strings, empty values,
fragmented frames, pipelining, reconnect behavior, and RESP parser fuzzing are
covered by tests.

RESP3 is supported per connection through `HELLO 3`, with switching back through
`HELLO 2`. The audited surface covers RESP3 nulls, maps, sets, doubles, verbatim
strings, nested COMMAND/ACL structures, and classic/pattern/sharded Pub/Sub push
frames. RESP3 subscribers may continue executing ordinary commands, matching the
Redis 8.2 behavior exercised in the differential audit. The broad command-shape audit is complete. RESP3 attribute frames and exhaustive
client-library-specific parity are not yet claimed.

## Redis family status

| Family | Status | Notes |
|---|---|---|
| Strings / numeric / bits | Broad support | SET/GET variants, counters, ranges, bit operations, multi-key string operations |
| Expiration / keyspace | Broad support | TTL/expire variants, rename, copy, scan, type, delete, key inspection |
| HASH | Broad support | Native packed datatype |
| SET | Broad support | Native packed datatype and algebra/store operations |
| LIST | Broad support | Native packed datatype, moves, blocking reads/pops |
| ZSET | Broad support | Native packed datatype, ranges, algebra, blocking pops/multipops |
| SORT | Supported | `SORT`, `SORT_RO`, BY/LIMIT/GET/ASC/DESC/ALPHA, external string/hash patterns, STORE-to-LIST semantics |
| STREAM | Broad support | Core stream reads/writes, consumer groups, PEL, claims, XINFO, trimming/reference policies |
| JSON | Partial | `JSON.SET`, `JSON.GET`, `JSON.TYPE`, `JSON.DEL` only |
| Pub/Sub | Broad support | Classic and sharded Pub/Sub, pattern subscriptions, introspection, RESP2 subscribed-mode behavior |
| Transactions | Broad support | `MULTI`, `EXEC`, `DISCARD`, `WATCH`, `UNWATCH`, queue/runtime error semantics, AOF transaction frames |
| HyperLogLog | Supported | `PFADD`, `PFCOUNT`, `PFMERGE`; Redis-compatible serialized HLL strings |
| GEO | Modern surface supported | `GEOADD`, `GEODIST`, `GEOHASH`, `GEOPOS`, `GEOSEARCH`, `GEOSEARCHSTORE`; deprecated `GEORADIUS*` commands are not implemented |
| Lua scripting / Functions | Partial | `EVAL*`, read-only EVAL, SCRIPT load/exists/flush/kill, Functions core/management, restart persistence and standalone-safe flags; SCRIPT DEBUG, allow-oom and deeper command-flag/OOM parity remain |
| AUTH / ACL | Audited single-node support | Named authentication, command/category/key/channel rules, selectors, transaction enforcement, CAT/DRYRUN/GENPASS/LOG, SAVE/LOAD, startup ACL-file restore, and Redis 8.2 differential coverage |
| CONFIG | Broad tooling support | GET/SET/RESETSTAT/REWRITE/HELP for supported SnugKV settings with runtime mutation and restart persistence |
| CLIENT | Partial | ID/name/setinfo/info/list/list filters/kill/unblock/help implemented and differentially tested; tracking/caching/redirection not implemented |
| COMMAND metadata | Supported for implemented surface | Redis-shaped INFO/DOCS/GETKEYS/GETKEYSANDFLAGS, parent/subcommand metadata, dynamic key extraction, differential audit complete |
| RESP3 | Broad audited support | `HELLO 3`, protocol switching, audited null/map/set/double/verbatim reply shapes, Streams/tooling maps, GEO/ZSET numeric forms, and Pub/Sub push semantics; optional client/attribute hardening remains |
| Replication / Sentinel / Cluster | Not implemented | Outside current single-node scope |

## HASH

Supported commands:

```text
HSET HGET HDEL HLEN HEXISTS HMGET HGETALL HKEYS HVALS
HSETNX HSTRLEN HINCRBY HINCRBYFLOAT HSCAN HMSET HRANDFIELD
```

HASH is a native datatype with packed storage and typed WRONGTYPE behavior.

## SET

Supported commands:

```text
SADD SREM SISMEMBER SMISMEMBER SCARD SMEMBERS SSCAN
SUNION SINTER SDIFF SUNIONSTORE SINTERSTORE SDIFFSTORE
SMOVE SPOP SRANDMEMBER
```

STORE variants replace the destination and clear any previous destination TTL.

## LIST

Supported commands:

```text
LPUSH RPUSH LPUSHX RPUSHX LPOP RPOP LLEN LINDEX LRANGE
LSET LTRIM LREM LINSERT LPOS LMOVE RPOPLPUSH
BLPOP BRPOP BLMOVE BRPOPLPUSH
```

Blocking LIST commands use waiter/wakeup signaling rather than polling and do not
hold the AOF durability mutex while sleeping.

## ZSET

Supported commands:

```text
ZADD ZREM ZINCRBY ZSCORE ZMSCORE ZCARD ZCOUNT ZLEXCOUNT
ZRANK ZREVRANK ZRANGE ZREVRANGE
ZRANGEBYSCORE ZREVRANGEBYSCORE ZRANGEBYLEX ZREVRANGEBYLEX
ZREMRANGEBYRANK ZREMRANGEBYSCORE ZREMRANGEBYLEX
ZUNION ZINTER ZDIFF ZUNIONSTORE ZINTERSTORE ZDIFFSTORE ZINTERCARD
ZPOPMIN ZPOPMAX ZMPOP BZPOPMIN BZPOPMAX BZMPOP
ZRANDMEMBER ZSCAN ZRANGESTORE
```

`ZRANGE` supports rank mode plus `BYSCORE`, `BYLEX`, `REV`, `LIMIT`, and
`WITHSCORES` where applicable. ZSET algebra accepts SET and ZSET inputs.

## SORT / SORT_RO

Supported syntax:

```text
SORT key [BY pattern] [LIMIT offset count] [GET pattern ...]
         [ASC|DESC] [ALPHA] [STORE destination]
SORT_RO key [BY pattern] [LIMIT offset count] [GET pattern ...]
            [ASC|DESC] [ALPHA]
```

LIST, SET, and ZSET are valid sources. Default ordering is numeric and reports
`ERR One or more scores can't be converted into double` for invalid numeric
weights. `BY` and repeated `GET` support first-wildcard substitution, string-key
lookups, hash dereferences such as `user:*->score`, and `GET #`. A `BY` pattern
without `*` uses native/no-sort ordering; `DESC` reverses LIST/ZSET native order.
Missing external numeric weights behave as zero; missing GET values are returned
as null.

`SORT ... STORE destination` replaces any destination type with a native LIST,
clears any previous destination TTL, stores missing GET results as empty strings,
deletes the destination for an empty result, is journaled/replayed through the
logical AOF path, and wakes LIST waiters when a non-empty result is stored.
`SORT_RO` rejects `STORE`.

Current boundary: SnugKV uses bytewise comparison for `ALPHA`. Redis can use
locale-aware collation for non-STORE ALPHA replies, so locale-sensitive/non-ASCII
ordering is not claimed as exact parity. SET native iteration order under `BY`
without a wildcard is implementation-defined; STORE uses deterministic ordering.
Core command/key ACL enforcement is implemented. Deeper Redis parity for
dynamically resolved external SORT keys is still being audited, while Cluster
slot restrictions remain outside SnugKV's single-node scope.

## COPY

Supported syntax:

```text
COPY source destination [DB 0] [REPLACE]
```

COPY leaves the source unchanged and deep-copies the logical value into a new
independent destination allocation. STRING-style values and native HASH, SET,
LIST, ZSET, and STREAM values retain their datatype and logical contents. Stream
consumer-group/PEL metadata is part of the logical stream payload and is copied
with the stream. An existing source expiry is preserved as the same absolute
expiry on the destination.

If the source does not exist, COPY returns 0. If the destination exists and
`REPLACE` is absent, COPY returns 0 without changing either key. `REPLACE`
overwrites any destination datatype. Source and destination being the same key is
rejected with `ERR source and destination objects are the same`.

SnugKV exposes only logical database 0, so `DB 0` is accepted while any other DB
index returns `ERR DB index is out of range`. Cross-database copying is therefore
not available. COPY participates in max-memory admission/OOM rollback, logical
AOF persistence and restart recovery, MULTI/EXEC, WATCH invalidation, and
LIST/ZSET/STREAM waiter wakeups when a successful copy creates a ready destination.

## HyperLogLog

Supported commands:

```text
PFADD PFCOUNT PFMERGE
```

SnugKV stores HyperLogLog values as Redis-compatible STRING values rather than a
new native datatype. Compatibility coverage includes duplicate additions, unions,
merge behavior, TTL preservation, invalid-HLL handling, and byte-for-byte Redis
serialization on the 100,000-member comparison workload used during development.

## GEO

Supported modern commands:

```text
GEOADD GEODIST GEOHASH GEOPOS GEOSEARCH GEOSEARCHSTORE
```

GEO uses the same Redis model of storing 52-bit interleaved geospatial hashes as
ZSET scores. `GEOADD` therefore creates/updates an ordinary ZSET and preserves an
existing TTL just like `ZADD`. `GEOSEARCHSTORE` replaces the destination and clears
its previous TTL.

Supported GEOSEARCH forms include `FROMMEMBER` / `FROMLONLAT`, `BYRADIUS` /
`BYBOX`, `ASC` / `DESC`, `COUNT [ANY]`, `WITHDIST`, `WITHHASH`, `WITHCOORD`, and
`GEOSEARCHSTORE ... STOREDIST`.

Current implementation note: Redis uses geohash score ranges to prune radius
searches. SnugKV currently scans and decodes the packed source ZSET, so GEOSEARCH
is O(source cardinality). This avoids another permanent index but may be slower on
very large geospatial sets. Deprecated `GEORADIUS`, `GEORADIUSBYMEMBER`, and their
read-only variants are not currently implemented.

## STREAM

Supported commands and subcommands include:

```text
XADD XLEN XRANGE XREVRANGE XDEL XDELEX XTRIM
XREAD
XGROUP CREATE DESTROY SETID CREATECONSUMER DELCONSUMER
XREADGROUP XACK XACKDEL XPENDING XCLAIM XAUTOCLAIM
XINFO STREAM GROUPS CONSUMERS HELP
```

Implemented stream behavior includes:

- explicit, automatic (`*`), and partial (`ms-*`) IDs;
- `NOMKSTREAM`;
- `MAXLEN` and `MINID` trimming on `XTRIM` and `XADD`;
- Redis 8.2 `KEEPREF`, `DELREF`, and `ACKED` reference policies for trimming/deletion;
- `XDELEX` and `XACKDEL` with per-ID status replies and multi-group PEL semantics;
- blocking `XREAD` and `XREADGROUP` with waiter/wakeup signaling;
- client-disconnect and server-shutdown cancellation for blocking reads;
- durable consumer groups and pending-entry lists;
- claim ownership, idle gating, delivery counters, `JUSTID`, `FORCE`,
  `RETRYCOUNT`, and `XAUTOCLAIM` deleted-ID cleanup;
- `XINFO` stream/group/consumer metadata;
- persisted lifetime `entries-added`, `max-deleted-entry-id`, and separate
  consumer attempted/successful interaction timestamps;
- TTL/RENAME/persistence support and optimizer exclusion as a native datatype.

SnugKV has no Redis macro-node representation, so Redis's exact approximate
`XTRIM ~` removal granularity is not claimed: the grammar is supported, but the
number removed by a particular approximate trim can differ because Redis trims
according to its radix-tree/listpack node layout. `radix-tree-keys` /
`radix-tree-nodes` are likewise Redis-internal diagnostics and are not fabricated
for byte-identical metadata. The Redis 8.2 edge-case audit for the documented
Streams surface is complete; see `docs/STREAMS-DIFFERENTIAL-AUDIT.md`.

## Pub/Sub

Supported commands and subcommands include:

```text
SUBSCRIBE UNSUBSCRIBE PSUBSCRIBE PUNSUBSCRIBE PUBLISH
SSUBSCRIBE SUNSUBSCRIBE SPUBLISH
PUBSUB CHANNELS NUMSUB NUMPAT SHARDCHANNELS SHARDNUMSUB HELP
```

Classic and sharded Pub/Sub use separate subscription namespaces. Pattern
subscriptions use the same Redis-style binary-safe glob matcher as SCAN. RESP2
subscribed-mode command restrictions remain intact. Under RESP3, subscription
confirmations and message deliveries use push frames and subscribed clients may
continue executing ordinary commands; `PING` follows normal RESP3 command behavior.
`RESET`, disconnect cleanup, and serialized complete-response writes are covered
by TCP/race tests and Redis 8.2 differential checks.

## Transactions

Supported commands:

```text
MULTI EXEC DISCARD WATCH UNWATCH
```

Transaction behavior includes:

- connection-scoped queues and WATCH state;
- queue-time validation errors causing `EXECABORT` without executing queued work;
- runtime command errors returned as individual EXEC array elements while later
  queued commands continue;
- atomic command execution relative to other clients through global command
  serialization;
- cross-client WATCH invalidation, including change-then-restore detection;
- expiration invalidation of watched keys;
- `UNWATCH`, successful/failed EXEC cleanup, and disconnect cleanup;
- blocking LIST/ZSET/STREAM operations becoming nonblocking when executed inside
  a transaction;
- AOF transaction results persisted as one logical checksummed frame with rollback
  on append failure.

Current RESP2 limitation: subscription-state commands (`SUBSCRIBE`, `PSUBSCRIBE`,
`SSUBSCRIBE`, unsubscribe variants, `RESET`) are not supported as queued MULTI
commands. `PUBLISH` and `SPUBLISH` remain ordinary queueable commands.

## Lua scripting and Redis Functions

Supported scripting commands:

```text
EVAL EVALSHA EVAL_RO EVALSHA_RO
SCRIPT LOAD
SCRIPT EXISTS
SCRIPT FLUSH [SYNC|ASYNC]
SCRIPT KILL
```

Supported Function commands:

```text
FUNCTION LOAD [REPLACE] <library-code>
FUNCTION LIST [LIBRARYNAME pattern] [WITHCODE]
FUNCTION DELETE <library-name>
FUNCTION FLUSH [SYNC|ASYNC]
FUNCTION DUMP
FUNCTION RESTORE <payload> [APPEND|REPLACE|FLUSH]
FUNCTION STATS
FUNCTION KILL
FUNCTION HELP
FCALL function numkeys [key ...] [arg ...]
FCALL_RO function numkeys [key ...] [arg ...]
```

The embedded runtime is Lua 5.1-compatible. `KEYS` and `ARGV` are populated with
binary-safe strings. The Redis bridge supports `redis.call`, `redis.pcall`,
`redis.error_reply`, `redis.status_reply`, and `redis.sha1hex`, including the
usual RESP2/Lua reply conversions for strings, integers, arrays, null/false,
status replies, and error replies. Read-only EVAL variants, `FCALL_RO`, and
Functions registered with `no-writes` reject commands that mutate or replicate
state.

Scripts and Functions execute under SnugKV's global command-serialization
boundary, so ordinary clients and transactions cannot interleave writes halfway
through an invocation. A script can be queued inside MULTI/EXEC. A WATCH is
invalidated by transient script/function mutations even if the invocation later
restores the original key value.

With logical AOF enabled, all resulting logical keyspace changes from one direct
writable EVAL or FCALL are stored in one persistence frame. A Lua runtime error
does not roll back successful `redis.call` writes performed earlier in the
invocation; those changes are still persisted. The SHA-1 script cache is
process-local/volatile and is cleared by `SCRIPT FLUSH` or restart.

Function libraries use persistent library-local Lua state while the process is
running. `FUNCTION DUMP`/`RESTORE` serialize library definitions with checksum
validation and Redis-style APPEND/REPLACE/FLUSH policy semantics. When AOF or
snapshot persistence is configured, the current Function registry is also stored
in an atomic checksummed sidecar and restored on restart. Arbitrary live Lua VM
state is not serialized; library-local variables are reconstructed from source
and therefore reset after restore/restart.

`FUNCTION STATS` reports the running Function name, original FCALL/FCALL_RO
command vector, elapsed `duration_ms`, and per-engine library/function counts. It
uses a read-only synchronized fast path so another client can query it while an
FCALL holds SnugKV's normal command-serialization mutex. `FUNCTION HELP` returns
the Redis-style Function subcommand help array. See `docs/FUNCTION-STATS-HELP.md`.

`FUNCTION KILL` cancels an active FCALL/FCALL_RO before it executes a writable
nested dataset command. The first dispatched write marks the invocation dirty;
subsequent KILL attempts return `UNKILLABLE` instead of interrupting partially
mutated state. KILL with no active Function returns `NOTBUSY`. See
`docs/FUNCTION-KILL.md`.

`SCRIPT KILL` uses the corresponding running-script state and first-write safety
boundary: it can cancel EVAL/EVALSHA/EVAL_RO/EVALSHA_RO before a writable nested
command is dispatched, returns `UNKILLABLE` after the write boundary, and returns
`NOTBUSY` when idle. See `docs/SCRIPT-KILL.md`.

Payload compatibility boundary: SnugKV currently uses its own versioned
`SNUGF001` Function dump payload rather than Redis RDB Function bytes. Redis and
SnugKV Function DUMP payloads are therefore not cross-restorable yet. See
`docs/FUNCTION-DUMP-RESTORE.md`.

Current scripting/Functions boundaries:

- each invocation has a five-second execution limit;
- filesystem/process Lua libraries are not exposed;
- blocking commands, connection/subscription state, transaction commands, nested
  EVAL/SCRIPT, and SnugKV admin commands are rejected from `redis.call`/`redis.pcall`;
- `SCRIPT DEBUG` is intentionally not implemented until real Redis LDB-style
  semantics are supported;
- Function flags supported for current standalone semantics are `no-writes`,
  `allow-stale`, `no-cluster`, and `allow-cross-slot-keys`;
- `allow-oom` remains deferred pending exact scoped memory-admission semantics;
- full Redis scripting command-flag/ACL/OOM parity is not implemented;
- SnugKV does not claim Redis's exact Lua VM implementation details or every
  scripting edge-case yet.

## Authentication and ACL

Implemented authentication and ACL commands:

```text
AUTH
ACL WHOAMI USERS GETUSER LIST SETUSER DELUSER
ACL CAT DRYRUN GENPASS LOG SAVE LOAD HELP
```

Named users support enable/disable state, password hashes, `nopass`, explicit
command allow/deny rules, Redis 8.2 command categories, `allkeys` /
`resetkeys`, and `~pattern` key rules. Category rules are applied left-to-right,
and key authorization uses the same fixed/dynamic command-key discovery layer as
COMMAND introspection.

ACL failures while queueing MULTI poison the transaction; EXEC re-authorizes the
queued commands against the current rules. `ACL LOG` records auth/command/key
denials and aggregates equivalent repeated violations.

With `acl_file` configured, `ACL SAVE` writes Redis-style user lines with
SHA-256 password hashes, `ACL LOAD` is atomic on parse failure, and startup
restores the file before clients are accepted. Missing or malformed configured ACL
files fail startup closed.

Current boundaries are channel-pattern enforcement, selectors, some uncommon
SETUSER reset/removal modifiers, exact non-default channel presentation, and
deeper dynamic SORT/script/Function ACL edge auditing. See
`docs/ACL-COMPATIBILITY.md`.

## CLIENT

Supported management/introspection commands and forms:

```text
CLIENT ID
CLIENT GETNAME
CLIENT SETNAME <name>
CLIENT SETINFO LIB-NAME <value>
CLIENT SETINFO LIB-VER <value>
CLIENT INFO
CLIENT LIST
CLIENT LIST ID <id> [<id> ...]
CLIENT LIST TYPE NORMAL
CLIENT KILL ID <id> [SKIPME YES|NO]
CLIENT UNBLOCK <id> [TIMEOUT|ERROR]
CLIENT HELP
```

The implementation uses a concurrency-safe per-listener registry. Targeted
`CLIENT KILL` closes the selected connection while preserving Redis-style default
SKIPME behavior for self-kill. `CLIENT UNBLOCK` wakes supported blocking commands
without tearing down the connection and supports both Redis-style TIMEOUT and
ERROR responses. Main/admin listener registry initialization and shutdown behavior
are covered by race regression tests.

Live Redis differential testing covered multiple persistent client IDs, names and
SETINFO metadata, INFO/LIST fields, LIST ID/TYPE NORMAL filters, targeted and
self-kill behavior, targeted TIMEOUT/ERROR unblock behavior, connection survival,
nonexistent/invalid IDs, invalid unblock reasons, and tested arity/error replies.
See `docs/CLIENT-COMPATIBILITY.md`.

Advanced tracking/caching/redirection features are not implemented, and additional
LIST TYPE classes should only be added when their corresponding connection modes
or topology exist.

## SCAN family

`SCAN`, `HSCAN`, `SSCAN`, and `ZSCAN` support cursor iteration, `MATCH`, and
`COUNT`. Global `SCAN` also supports `TYPE` filtering. MATCH is binary-safe and
supports Redis-style `*`, `?`, bracket classes/ranges, negated classes, and
backslash escaping.

SnugKV cursor tokens are opaque snapshot/index positions and are not expected to
match Redis cursor values or page boundaries byte-for-byte.

## Blocking client disconnects

Blocking LIST, ZSET, XREAD, and XREADGROUP commands are canceled during server
shutdown. Linux server builds also detect TCP peer half-close/hangup while blocked
using a non-consuming socket poll.

On non-Linux builds finite command timeouts and server shutdown still release
waiters, but equivalent proactive peer-disconnect detection is not yet implemented.

## JSON

The current JSON surface is intentionally small:

```text
JSON.SET JSON.GET JSON.TYPE JSON.DEL
```

It is not a complete RedisJSON implementation.

## Major remaining Redis compatibility work

Prioritized backlog:

1. Deeper dynamic SORT/script/Function ACL edge audits.
2. `SCRIPT DEBUG`, exact `allow-oom`, and deeper scripting command-flag/OOM parity.
3. Migration/transfer scope beyond single-database `COPY`.
4. Optional RESP3 client-library smoke coverage and attribute-frame support if required.
5. Advanced CLIENT tracking/caching/redirection features where real clients require them.
6. Optional Redis-RDB byte compatibility for Function DUMP/RESTORE payloads.
7. Deprecated `GEORADIUS*` aliases if legacy client compatibility justifies them.
8. Replication/failover/cluster only after the single-node compatibility target is mature.

See GitHub issue #55 and `PLAN.md` for the working roadmap.

## Compatibility philosophy

SnugKV prefers explicit incompatibility over silently approximating unsupported
Redis behavior. A command is documented as supported only when its observable
behavior has implementation tests and, for important paths, redis-cli/TCP smoke
coverage. Production configuration interactions such as background optimization
are included in regression coverage for native datatypes.