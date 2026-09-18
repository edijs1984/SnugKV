# RESP3 client smoke suite

This suite validates SnugKV through real Redis client libraries using RESP3,
separately from the existing RESP2 compatibility smoke tests.

## Targets

Run the same client programs against:

- SnugKV on `127.0.0.1:6380`
- Redis 8.2 on `127.0.0.1:6390`

Environment variables:

- `REDIS_HOST` (default `127.0.0.1`)
- `REDIS_PORT` (default `6380`)
- `TARGET_NAME` (default `snugkv`)

## Coverage

Each client smoke test exercises RESP3 negotiation plus representative reply
shapes and application flows:

- PING and string/null replies
- MSET/MGET and pipelining
- HGETALL map replies
- SMEMBERS set replies
- ZSET doubles / WITHSCORES
- Streams: XADD, XRANGE, XGROUP CREATE, XREADGROUP, XPENDING, XINFO GROUPS
- MULTI/EXEC
- WRONGTYPE error handling
- reconnect/fresh connection behavior

The goal is not to force each client library to expose identical language-level
objects. The goal is to prove that each library can negotiate and decode SnugKV's
RESP3 replies using its normal APIs.

## Run

Node dependencies are already declared in `compat/node/package.json`.

```sh
# SnugKV
REDIS_PORT=6380 TARGET_NAME=snugkv bash compat/resp3/run.sh

# Redis 8.2 oracle
REDIS_PORT=6390 TARGET_NAME=redis82 bash compat/resp3/run.sh
```

You can also run one client directly:

```sh
cd compat/node
REDIS_PORT=6380 node resp3-smoke.cjs

cd ../python
REDIS_PORT=6380 python3 resp3_smoke.py

cd ../go
REDIS_PORT=6380 go run ./resp3
```