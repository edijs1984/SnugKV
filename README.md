# SnugKV

SnugKV is a single-node RESP2 in-memory store with exact byte round trips,
bounded protocol handling, sharded concurrency, expiration, memory limits,
optional adaptive encoding, eviction, and logical persistence.

Use Go 1.27 or newer:

```sh
go run -buildvcs=false ./cmd/snugkv \
  -listen 127.0.0.1:6380 \
  -admin-listen 127.0.0.1:6381 \
  -metrics-listen 127.0.0.1:9090

make check
go run -buildvcs=false ./cmd/snugbench -dataset sessions -keys 10000
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
and administration details. See [COMPATIBILITY.md](COMPATIBILITY.md) for tested
clients, [KNOWN-LIMITATIONS.md](KNOWN-LIMITATIONS.md) for alpha limitations,
and [SECURITY.md](SECURITY.md) for security guidance. Contributions should
follow [CONTRIBUTING.md](CONTRIBUTING.md). Release changes are tracked in
[CHANGELOG.md](CHANGELOG.md).

Supported commands:

- Connection: `PING`, `ECHO`, `QUIT`, `SELECT 0`, `HELLO 2`, `INFO`, `DBSIZE`, `COMMAND`.
- Keys: `DEL`, `UNLINK`, `EXISTS`, `TYPE`, `TOUCH`, `KEYS`, `SCAN`, `RANDOMKEY`, `RENAME`, `RENAMENX`.
- Strings: `SET`, `GET`, `GETSET`, `GETDEL`, `GETEX`, `SETNX`, `SETEX`, `PSETEX`, `MSET`, `MSETNX`, `MGET`, `APPEND`, `STRLEN`, `GETRANGE`, `SETRANGE`.
- Numeric: `INCR`, `INCRBY`, `DECR`, `DECRBY`, `INCRBYFLOAT`.
- Bit operations: `GETBIT`, `SETBIT`, `BITCOUNT`, `BITPOS`, `BITOP`.
- Expiration: `EXPIRE`, `PEXPIRE`, `EXPIREAT`, `PEXPIREAT`, `EXPIRETIME`, `PEXPIRETIME`, `TTL`, `PTTL`, `PERSIST`.
- JSON: `JSON.SET`, `JSON.GET`, `JSON.TYPE`, `JSON.DEL`.
- Administration: `FLUSHDB`, `FLUSHALL`, `MEMORY`, `SNUG.ENCODING`, `SNUG.MEMORY`, `SNUG.STATS`, `SNUG.COMPACT`, `SNUG.POLICY`, `SNUG.AOFREWRITE`.

The admin listener defaults to loopback and accepts diagnostics only. When it is
active, `SNUG.*` commands are rejected on the public listener. Metrics are also
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

`cmd/snugsoak` supplies the release soak workload. It sends 90% of reads to 10%
of keys while performing periodic writes, counter increments, TTL churn, cleanup,
and background optimization. It verifies every read and reports start, peak, and
end accounted memory as JSON. `make soak` defaults to 24 hours; override `DURATION`
for a smoke run.

## License

SnugKV is available under the
[GNU Affero General Public License v3.0](LICENSE).

For organizations that require proprietary use, embedding, redistribution,
or hosted-service terms that are not compatible with the AGPL-3.0,
separate commercial licensing is available.

See [COMMERCIAL-LICENSE.md](COMMERCIAL-LICENSE.md) for details.

SnugKV is an independent project and is not affiliated with or endorsed by
Redis. Redis and related marks are trademarks of their respective owners.

