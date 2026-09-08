# MorphCache

MorphCache is a single-node RESP2 in-memory store with exact byte round trips,
bounded protocol handling, sharded concurrency, expiration, memory limits,
optional adaptive encoding, eviction, and logical persistence.

Use Go 1.27 or newer:

```sh
go run -buildvcs=false ./cmd/morphcache \
  -listen 127.0.0.1:6380 \
  -admin-listen 127.0.0.1:6381 \
  -metrics-listen 127.0.0.1:9090

make check
go run -buildvcs=false ./cmd/morphbench -dataset sessions -keys 10000
make soak DURATION=10m KEYS=100000
```

Run it with Docker Compose:

```sh
docker compose up --build
```

The server then listens on `127.0.0.1:6380`. For a quick check, use a RESP2
client such as `redis-cli`:

```sh
redis-cli -p 6380 ping
redis-cli -p 6380 set example hello
redis-cli -p 6380 get example
```

See [operations](docs/operations.md) for configuration, persistence, metrics,
and administration details. Contributions should follow
[CONTRIBUTING.md](CONTRIBUTING.md).

Supported commands:

- Connection: `PING`, `ECHO`, `QUIT`, `SELECT 0`, `HELLO 2`, `INFO`, `DBSIZE`, `COMMAND`.
- Strings: `SET` with `NX`/`XX` and `EX`/`PX`, `GET`, `MGET`, `DEL`, `EXISTS`, `GETSET`, `SETNX`, `MSET`, `INCR`, `INCRBY`, `DECR`, `DECRBY`, `STRLEN`.
- Expiration: `EXPIRE`, `PEXPIRE`, `TTL`, `PTTL`, `PERSIST`.
- Administration: `MORPH.ENCODING`, `MORPH.MEMORY`, `MORPH.STATS`, `MORPH.COMPACT`, `MORPH.POLICY`, `MORPH.AOFREWRITE`.

The admin listener defaults to loopback and accepts diagnostics only. When it is
active, `MORPH.*` commands are rejected on the public listener. Metrics are also
loopback-only. See [protocol](docs/protocol.md), [operations](docs/operations.md),
and [memory format](docs/memory-format.md).

Raw storage is the default. `-encoding` enables verified canonical integer, UUID,
and timestamp storage. `-json-shape` and `-compression` require `-encoding` and add
background shape/dictionary, LZ4, and Zstandard candidates. Every selected codec
is decoded and compared before publication; clients receive the exact input bytes.

The memory limit covers engine-accounted index capacity, arena capacity, entry/key
charges, and reserved bounded schema/dictionary state. It is not process RSS.
Network buffers, stacks, persistence buffers, and optimizer scratch have separate
bounds. Benchmark results are measurements on one machine, not general claims.

`cmd/morphsoak` supplies the release soak workload. It sends 90% of reads to 10%
of keys while performing periodic writes, counter increments, TTL churn, cleanup,
and background optimization. It verifies every read and reports start, peak, and
end accounted memory as JSON. `make soak` defaults to 24 hours; override `DURATION`
for a smoke run.
