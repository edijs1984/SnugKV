# SnugKV Compatibility

SnugKV targets common Redis-compatible application workloads while staying
explicit about protocol, command, and topology differences. This document tracks
the public compatibility boundary; issue #55 tracks the implementation backlog.

## Current status

SnugKV is a single-node RESP2 datastore with native STRING-style scalar storage,
HASH, SET, LIST, ZSET, and STREAM semantics. The broad Streams and consumer-group
surface is implemented, including blocking reads, pending-entry management,
claiming, XINFO introspection, lifetime stream metadata, MAXLEN/MINID trimming,
Redis 8.2 `KEEPREF` / `DELREF` / `ACKED` policies, `XDELEX`, and `XACKDEL`.

Classic and sharded Pub/Sub are implemented, along with connection-scoped Redis
transactions and optimistic locking (`MULTI`, `EXEC`, `DISCARD`, `WATCH`,
`UNWATCH`). HyperLogLog (`PFADD`, `PFCOUNT`, `PFMERGE`), the modern GEO surface
(`GEOADD`, `GEODIST`, `GEOHASH`, `GEOPOS`, `GEOSEARCH`, `GEOSEARCHSTORE`), and the
common Lua scripting path (`EVAL`, `EVALSHA`, `SCRIPT LOAD/EXISTS/FLUSH`) are also
implemented.

The largest remaining Redis compatibility families are Redis Functions/full
scripting parity, RESP3, `SORT`/`SORT_RO`, COPY/migration scope, and broader
CLIENT/CONFIG/ACL/tooling compatibility. Replication, Sentinel-style failover, and
Cluster remain outside the current single-node scope.

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

RESP2 is supported and recommended. Binary-safe bulk strings, empty values,
fragmented frames, pipelining, reconnect behavior, and RESP parser fuzzing are
covered by tests.

Clients that default to RESP3 should explicitly select RESP2. RESP3 is not
currently implemented; `HELLO 3` intentionally returns `NOPROTO unsupported
protocol version`.

## Redis family status

| Family | Status | Notes |
|---|---|---|
| Strings / numeric / bits | Broad support | SET/GET variants, counters, ranges, bit operations, multi-key string operations |
| Expiration / keyspace | Broad support | TTL/expire variants, rename, scan, type, delete, key inspection |
| HASH | Broad support | Native packed datatype |
| SET | Broad support | Native packed datatype and algebra/store operations |
| LIST | Broad support | Native packed datatype, moves, blocking reads/pops |
| ZSET | Broad support | Native packed datatype, ranges, algebra, blocking pops/multipops |
| STREAM | Broad support | Core stream reads/writes, consumer groups, PEL, claims, XINFO, trimming/reference policies |
| JSON | Partial | `JSON.SET`, `JSON.GET`, `JSON.TYPE`, `JSON.DEL` only |
| Pub/Sub | Broad support | Classic and sharded Pub/Sub, pattern subscriptions, introspection, RESP2 subscribed-mode behavior |
| Transactions | Broad support | `MULTI`, `EXEC`, `DISCARD`, `WATCH`, `UNWATCH`, queue/runtime error semantics, AOF transaction frames |
| HyperLogLog | Supported | `PFADD`, `PFCOUNT`, `PFMERGE`; Redis-compatible serialized HLL strings |
| GEO | Modern surface supported | `GEOADD`, `GEODIST`, `GEOHASH`, `GEOPOS`, `GEOSEARCH`, `GEOSEARCHSTORE`; deprecated `GEORADIUS*` commands are not implemented |
| Lua scripting | Partial | `EVAL`, `EVALSHA`, `SCRIPT LOAD/EXISTS/FLUSH`, KEYS/ARGV and common `redis.*` helpers; Functions/read-only/kill/debug surfaces remain |
| RESP3 | Not implemented | RESP2 only |
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

SnugKV has no Redis macro-node representation, so `~` is accepted but trimming is
exact except for an explicit `LIMIT` cap. A final differential Redis edge-case
audit remains useful, but no known core Streams command-family gap is currently
tracked.

## Pub/Sub

Supported commands and subcommands include:

```text
SUBSCRIBE UNSUBSCRIBE PSUBSCRIBE PUNSUBSCRIBE PUBLISH
SSUBSCRIBE SUNSUBSCRIBE SPUBLISH
PUBSUB CHANNELS NUMSUB NUMPAT SHARDCHANNELS SHARDNUMSUB HELP
```

Classic and sharded Pub/Sub use separate subscription namespaces. Pattern
subscriptions use the same Redis-style binary-safe glob matcher as SCAN. RESP2
subscribed-mode command restrictions, subscribed `PING`, `RESET`, asynchronous
socket pushes, disconnect cleanup, and serialized complete-response writes are
covered by TCP/race tests.

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

## Lua scripting

Supported scripting commands:

```text
EVAL EVALSHA
SCRIPT LOAD
SCRIPT EXISTS
SCRIPT FLUSH [SYNC|ASYNC]
```

The embedded runtime is Lua 5.1-compatible. `KEYS` and `ARGV` are populated with
binary-safe strings. The Redis bridge supports `redis.call`, `redis.pcall`,
`redis.error_reply`, `redis.status_reply`, and `redis.sha1hex`, including the
usual RESP2/Lua reply conversions for strings, integers, arrays, null/false,
status replies, and error replies.

Scripts execute under SnugKV's global command-serialization boundary, so ordinary
clients and transactions cannot interleave writes halfway through an EVAL. A
script can be queued inside MULTI/EXEC. A WATCH is invalidated by transient script
mutations even if the script later restores the original value.

With logical AOF enabled, all resulting logical changes from one direct EVAL are
stored in one persistence frame. A Lua runtime error does not roll back successful
`redis.call` writes performed earlier in the script; those changes are still
persisted. The SHA-1 script cache is process-local/volatile and is cleared by
`SCRIPT FLUSH` or restart.

Current scripting boundaries:

- each invocation has a five-second execution limit;
- filesystem/process Lua libraries are not exposed;
- blocking commands, connection/subscription state, transaction commands, nested
  EVAL/SCRIPT, and SnugKV admin commands are rejected from `redis.call`/`redis.pcall`;
- Redis Functions (`FUNCTION`, `FCALL`, `FCALL_RO`) are not implemented;
- `EVAL_RO`, `EVALSHA_RO`, `SCRIPT KILL`, `SCRIPT DEBUG`, and full Redis scripting
  command-flag/ACL parity are not implemented;
- SnugKV does not claim Redis's exact Lua VM implementation details or every
  scripting edge-case yet.

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

1. Redis Functions and remaining scripting parity/hardening.
2. `SORT` / `SORT_RO`.
3. `COPY` and migration scope decision.
4. CLIENT/CONFIG/ACL compatibility and COMMAND metadata completeness.
5. RESP3 where required by clients/tooling.
6. Deprecated `GEORADIUS*` aliases if legacy client compatibility justifies them.
7. Replication/failover/cluster only after the single-node compatibility target is mature.

See GitHub issue #55 and `PLAN.md` for the working roadmap.

## Compatibility philosophy

SnugKV prefers explicit incompatibility over silently approximating unsupported
Redis behavior. A command is documented as supported only when its observable
behavior has implementation tests and, for important paths, redis-cli/TCP smoke
coverage. Production configuration interactions such as background optimization
are included in regression coverage for native datatypes.
