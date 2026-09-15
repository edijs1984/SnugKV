# SnugKV Compatibility

SnugKV targets common Redis-compatible application workloads while staying
explicit about protocol, command, and topology differences.

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
fragmented frames, pipelining, and reconnect behavior are covered by tests.

Clients that default to RESP3 should explicitly select RESP2.

### node-redis

```js
createClient({
  url: "redis://127.0.0.1:6380",
  RESP: 2,
});
```

### redis-py

```python
redis.Redis(
    host="127.0.0.1",
    port=6380,
    protocol=2,
)
```

### go-redis

```go
redis.NewClient(&redis.Options{
    Addr:     "127.0.0.1:6380",
    Protocol: 2,
})
```

RESP3 is not currently implemented. `HELLO 3` intentionally returns:

```text
NOPROTO unsupported protocol version
```

SnugKV does not advertise RESP3 while returning RESP2 response types.

## Implemented datatype families

### Strings / numeric / bits / expiry

Supported application primitives include `SET`/`GET`, conditional and expiring
SET variants, multi-key string operations, counters, range operations, bit
operations, expiration/TTL commands, key inspection, rename, scan, and delete.

### HASH

Supported commands:

```text
HSET HGET HDEL HLEN HEXISTS HMGET HGETALL HKEYS HVALS
HSETNX HSTRLEN HINCRBY HINCRBYFLOAT HSCAN HMSET HRANDFIELD
```

HASH is a native datatype with packed storage and typed WRONGTYPE behavior on the
hash command surface.

### SET

Supported commands:

```text
SADD SREM SISMEMBER SMISMEMBER SCARD SMEMBERS SSCAN
SUNION SINTER SDIFF SUNIONSTORE SINTERSTORE SDIFFSTORE
SMOVE SPOP SRANDMEMBER
```

STORE variants replace the destination and clear any previous destination TTL.

### LIST

Supported commands:

```text
LPUSH RPUSH LPUSHX RPUSHX LPOP RPOP LLEN LINDEX LRANGE
LSET LTRIM LREM LINSERT LPOS LMOVE RPOPLPUSH
BLPOP BRPOP BLMOVE BRPOPLPUSH
```

Blocking LIST commands use waiter/wakeup signaling rather than polling and do not
hold the AOF durability mutex while sleeping. Infinite blockers are released on
server shutdown. Proactive detection of a client disconnect while infinitely
blocked is still a hardening item.

### ZSET

Supported commands:

```text
ZADD ZREM ZINCRBY ZSCORE ZMSCORE ZCARD ZCOUNT ZLEXCOUNT
ZRANK ZREVRANK ZRANGE ZREVRANGE
ZRANGEBYSCORE ZREVRANGEBYSCORE ZRANGEBYLEX ZREVRANGEBYLEX
ZREMRANGEBYRANK ZREMRANGEBYSCORE ZREMRANGEBYLEX
ZUNION ZINTER ZDIFF ZUNIONSTORE ZINTERSTORE ZDIFFSTORE ZINTERCARD
ZPOPMIN ZPOPMAX ZMPOP ZRANDMEMBER ZSCAN ZRANGESTORE
```

`ZRANGE` supports rank mode plus `BYSCORE`, `BYLEX`, `REV`, `LIMIT`, and
`WITHSCORES` where applicable. ZSET algebra accepts both ZSET and SET inputs;
plain SET members contribute score 1 before weights. `ZUNION`/`ZINTER` support
`WEIGHTS` and `AGGREGATE SUM|MIN|MAX|COUNT`.

Blocking ZSET commands are not implemented yet:

```text
BZPOPMIN BZPOPMAX BZMPOP
```

Lex-range behavior follows Redis's same-score use case; applications should not
rely on lex semantics across members with different scores.

## JSON

The current JSON surface is intentionally small:

```text
JSON.SET JSON.GET JSON.TYPE JSON.DEL
```

It is not a complete RedisJSON implementation.

## Transactions

Redis transactions are not currently implemented:

```text
MULTI EXEC WATCH UNWATCH DISCARD
```

Normal client pipelining is supported and covered by compatibility tests.

## Features outside the current single-node v1 scope

The following are not currently implemented as general Redis-compatible features:

```text
RESP3
Streams
Pub/Sub
Lua scripting
Redis Functions
Replication
Sentinel-style failover
Cluster mode
Modules
```

## Compatibility caveats under active audit

Native HASH/SET/LIST/ZSET commands enforce typed behavior. A focused audit remains
for older scalar/numeric/bit commands to ensure every access to a native container
returns Redis-style WRONGTYPE rather than interpreting native bytes as a scalar.
Until that audit is closed, applications should not intentionally mix scalar
commands with native container keys.

`SCAN`, `HSCAN`, `SSCAN`, and `ZSCAN` implement the useful cursor/MATCH/COUNT
surface, but exact cursor progression and every Redis glob edge case should not be
assumed identical.

## Compatibility philosophy

SnugKV prefers explicit incompatibility over silently approximating unsupported
Redis behavior. A command is documented as supported only when its observable
behavior has implementation tests and, for important compatibility paths, TCP or
redis-cli smoke coverage.
