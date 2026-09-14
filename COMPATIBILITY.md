# SnugKV Compatibility

SnugKV aims to support common Redis-compatible application workloads while
remaining explicit about unsupported protocol and command behavior.

## Client compatibility

| Client | Version tested | Protocol | Status |
|---|---:|---:|---|
| redis-cli | local Redis CLI | RESP2 | ✅ Pass |
| ioredis | compatibility harness | RESP2 | ✅ Pass |
| node-redis | compatibility harness | RESP2 | ✅ Pass |
| redis-py | 8.1.0 | RESP2 | ✅ Pass |
| go-redis | v9.22.0 | RESP2 | ✅ Pass |

## Tested behavior

The compatibility smoke tests exercise:

- connection establishment
- PING
- SET / GET
- MSET / MGET
- INCR
- expiration with PEXPIRE / PTTL
- binary-safe values
- pipelining
- disconnect/reconnect behavior

## RESP protocol support

### RESP2

RESP2 is supported and currently recommended.

Clients that default to RESP3 should explicitly select RESP2.

#### node-redis

```js
createClient({
  url: "redis://127.0.0.1:6380",
  RESP: 2,
});
```

#### redis-py

```python
redis.Redis(
    host="127.0.0.1",
    port=6380,
    protocol=2,
)
```

#### go-redis

```go
redis.NewClient(&redis.Options{
    Addr:     "127.0.0.1:6380",
    Protocol: 2,
})
```

## RESP3

RESP3 is not currently implemented.

HELLO 3 intentionally returns:

```text
NOPROTO unsupported protocol version
```

SnugKV does not advertise RESP3 support while returning RESP2 response types.

## Transactions

Redis transactions are not currently implemented.

Unsupported commands currently include:

```text
MULTI
EXEC
WATCH
UNWATCH
DISCARD
```

Normal client pipelining is supported and covered by the compatibility smoke
tests.

## Compatibility philosophy

SnugKV prefers explicit incompatibility over partially emulating Redis behavior
incorrectly.

A client or command should only be marked supported when its observable
behavior has been tested against the SnugKV TCP server.
