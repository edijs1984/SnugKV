# SnugKV Compatibility

SnugKV targets common Redis-compatible application workloads while staying
explicit about protocol, command, and topology differences. This document tracks
the public compatibility boundary; issue #55 tracks the implementation backlog.

## Current status

SnugKV is a single-node RESP2 datastore with native STRING-style scalar storage,
HASH, SET, LIST, ZSET, and STREAM semantics. The core Streams and consumer-group
surface is now implemented, including blocking reads, pending-entry management,
claiming, XINFO introspection, lifetime stream metadata, and MAXLEN/MINID
trimming.

The largest remaining Redis compatibility families are Pub/Sub, transactions,
HyperLogLog, GEO, scripting/functions, RESP3, and broader CLIENT/CONFIG/ACL/tooling
compatibility. Replication, Sentinel-style failover, and Cluster remain outside
the current single-node scope.

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
| STREAM | Broad support | Core stream reads/writes, consumer groups, PEL, claims, XINFO, trimming |
| JSON | Partial | `JSON.SET`, `JSON.GET`, `JSON.TYPE`, `JSON.DEL` only |
| Pub/Sub | Not implemented | Planned |
| Transactions | Not implemented | `MULTI`, `EXEC`, `WATCH`, `UNWATCH`, `DISCARD` |
| HyperLogLog | Not implemented | `PFADD`, `PFCOUNT`, `PFMERGE` |
| GEO | Not implemented | GEO command family |
| Lua / Functions | Not implemented | EVAL/SCRIPT/FUNCTION/FCALL scope not yet implemented |
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

## STREAM

Supported commands and subcommands include:

```text
XADD XLEN XRANGE XREVRANGE XDEL XTRIM
XREAD
XGROUP CREATE DESTROY SETID CREATECONSUMER DELCONSUMER
XREADGROUP XACK XPENDING XCLAIM XAUTOCLAIM
XINFO STREAM GROUPS CONSUMERS HELP
```

Implemented stream behavior includes:

- explicit, automatic (`*`), and partial (`ms-*`) IDs;
- `NOMKSTREAM`;
- `MAXLEN` and `MINID` trimming on `XTRIM` and `XADD`;
- blocking `XREAD` and `XREADGROUP` with waiter/wakeup signaling;
- client-disconnect and server-shutdown cancellation for blocking reads;
- durable consumer groups and pending-entry lists;
- claim ownership, idle gating, delivery counters, `JUSTID`, `FORCE`,
  `RETRYCOUNT`, and `XAUTOCLAIM` deleted-ID cleanup;
- `XINFO` stream/group/consumer metadata;
- persisted lifetime `entries-added`, `max-deleted-entry-id`, and separate
  consumer attempted/successful interaction timestamps;
- TTL/RENAME/persistence support and optimizer exclusion as a native datatype.

Current trimming keeps existing PEL references when stream entries are removed.
Redis 8.2 `KEEPREF` / `DELREF` / `ACKED` policy selection is the remaining Streams
compatibility slice. SnugKV has no Redis macro-node representation, so `~` is
accepted but trimming remains exact except for an explicit `LIMIT` cap.

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

1. Finish Redis 8.2 Streams reference policies (`KEEPREF`, `DELREF`, `ACKED`).
2. Pub/Sub.
3. Transactions / optimistic locking.
4. HyperLogLog.
5. GEO.
6. Scripting / Redis Functions scope.
7. SORT/SORT_RO, COPY/MIGRATE scope, CLIENT/CONFIG/ACL compatibility, and COMMAND metadata completeness.
8. RESP3 where required by clients/tooling.
9. Replication/failover/cluster only after the single-node compatibility target is mature.

See GitHub issue #55 and `PLAN.md` for the working roadmap.

## Compatibility philosophy

SnugKV prefers explicit incompatibility over silently approximating unsupported
Redis behavior. A command is documented as supported only when its observable
behavior has implementation tests and, for important paths, redis-cli/TCP smoke
coverage. Production configuration interactions such as background optimization
are included in regression coverage for native datatypes.
