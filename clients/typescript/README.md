# @snugkv/client

TypeScript/Node.js client for SnugKV.

The client automatically pipelines commands issued during the same JavaScript turn. Application code does not need to create a pipeline explicitly.

```ts
import { SnugKV } from "@snugkv/client";

const client = new SnugKV({ host: "127.0.0.1", port: 6383 });
await client.connect();

const a = client.set("a", "1");
const b = client.set("b", "2");
const c = client.get("c");

await Promise.all([a, b, c]);
```

Those three calls are encoded into one contiguous RESP payload and normally written to the socket with one `socket.write()`.

## Options

```ts
new SnugKV({
  host: "127.0.0.1",
  port: 6383,
  autoPipeline: true,
  autoPipelineMaxCommands: 128,
  autoPipelineMaxBytes: 1024 * 1024,
});
```

Automatic pipelining is enabled by default. A batch flushes on the next microtask, or immediately when a configured command/byte threshold is reached.



## Typed command API

`@snugkv/client` keeps `command([...])` available for the full Redis-compatible
SnugKV command surface, and provides typed helpers for the common application
paths used by SnugKV v1:

```ts
await client.ping();

await client.set("cache:key", JSON.stringify(payload), { ex: 300 });
await client.setEx("session:1", 1800, JSON.stringify(session));
await client.get("cache:key");
await client.del("cache:key");
await client.exists("cache:key");
await client.expire("cache:key", 600);

await client.incr("rate:1");
await client.incrBy("counter:requests", 10);

await client.hSet("cart:1", "sku-1", JSON.stringify(item));
const cart = await client.hGetAll("cart:1");

await client.lPush("activity:1", JSON.stringify(event));
await client.lTrim("activity:1", 0, 99);
const activity = await client.lRange("activity:1", 0, 19);

await client.sAdd("tags:1", "typescript");
const tags = await client.sMembers("tags:1");

await client.zIncrBy("leaderboard", 5, "user:1");
const rank = await client.zRevRank("leaderboard", "user:1");
const top = await client.zRevRange("leaderboard", 0, 9, { withScores: true });

await client.dbSize();
await client.info("memory");
await client.snugStats();
```

Adaptive encoding, compression, JSON-shape optimization, native container storage,
and indexed ZSET optimizations are server-side and require no client opt-in. The
same client commands continue to work as SnugKV changes physical representation.

## Transactions

`client.multi()` builds a typed `MULTI`/`EXEC`. Each call widens the result tuple, so `exec()` resolves to exactly the types you queued. The whole transaction is written to the socket in one piece, so other callers on the same client can never land between `MULTI` and `EXEC`.

```ts
const [ok, hits, balance] = await client
  .multi({ atomic: true })       // MULTI ATOMIC: all-or-nothing
  .set("order:1", "paid")
  .incr("stats:orders")
  .incrBy("balance:7", -25)
  .exec();                       // [ "OK", number, number ]
```

With `atomic: true` a failing command rolls back every write and `exec()` throws `TransactionAbortedError` (`rolledBack`, `commandIndex`, `command`, `cause`). Without it Redis rules apply: earlier commands stay applied, and a run-time failure throws `TransactionError` with `rolledBack: false` and every reply in `replies`. A command the server refuses to queue throws `TransactionError` with the offending `command`. Use `client.multi().command([...])` for anything without a typed method.

`WATCH` needs a connection of its own and is not part of this builder yet.

## Built-in functions

`client.snug` wraps the server's `snug_*` function library (see `docs/BUILTIN-FUNCTIONS.md`):

```ts
const { allowed, retryAfterMs } = await client.snug.rateLimit(`rl:${userId}`, { capacity: 10, refillPerSecond: 5 });

await client.snug.withLock("job:nightly", { ttlMs: 30_000, waitMs: 5_000 }, async (lock) => {
  await doWork(lock.fence);            // reject writes carrying a smaller fence
});

const { replayed, result } = await client.snug.idempotent(`pay:${requestId}`, { pendingTtlMs: 30_000, resultTtlMs: 86_400_000 }, async () => charge());

const { applied, value } = await client.snug.counterAdd("stock:sku1", -1, { min: 0, max: 100 });

const q = client.snug.queue("emails");
const id = await q.push(JSON.stringify(job));
const msg = await q.pop(30_000);       // redelivered after 30 s unless acked
if (msg) { await handle(msg.payload); await q.ack(msg.id); }

const board = client.snug.leaderboard("scores");
await board.submit("ann", 120);        // { rank, score }, rank 0 is first
const near = await board.around("ann", 2);
```

`client.fcall(name, keys, args)`, `client.fcallRo(...)` and `client.functionLoad(code, { replace })` are available for your own functions. Queue keys share one hash tag (`{name}:ready`, ...), so queues work in a cluster; for locks in a cluster put the lock and its fence counter in one tag, e.g. `{job}:lock` with the default `{job}:lock:fence`.

## JSON

`client.json` wraps the JSON commands with typed documents. Paths are JSONPath (`$`, `$.a.b`, `$.items[*]`); values are encoded and decoded for you.

```ts
interface Profile { name: string; tags: string[]; stats: { hits: number } }

await client.json.set("user:1", "$", { name: "ann", tags: [], stats: { hits: 0 } });
const profile = await client.json.get<Profile>("user:1");          // Profile | null
const hits = await client.json.getPath<number>("user:1", "$.stats.hits"); // number[] | null

await client.json.numIncrBy("user:1", "$.stats.hits", 1);          // [1]
await client.json.arrAppend("user:1", "$.tags", "vip");            // [1]
await client.json.merge("user:1", "$", { stats: { last: 1700000000 } });
await client.json.mSet([{ key: "a", path: "$", value: {} }, { key: "b", path: "$", value: {} }]);
```

Covered: `set` (`nx`/`xx`), `mSet`, `merge`, `get`, `getPath`, `mGet`, `del`, `type`, `clear`, `numIncrBy`, `numMultBy`, `toggle`, `strAppend`, `strLen`, `arrAppend`, `arrInsert`, `arrIndex`, `arrLen`, `arrPop`, `arrTrim`, `objKeys`, `objLen`. Per-path results are arrays (one entry per match), and `null` marks a match of the wrong type. Replies from the server are accepted either as a single value or as RedisJSON-style arrays. `JSON.RESP` and `JSON.DEBUG` are available through `client.command`.

## Connecting with a password

```ts
new SnugKV({ host, port, username: "app", password: process.env.SNUGKV_PASSWORD, database: 0 });
```

`AUTH` and `SELECT` are sent as part of `connect()`; a rejected password makes `connect()` throw.

## Tests against a real server

```bash
(cd ../.. && go build -o snugkv ./cmd/snugkv)
SNUGKV_BIN=../../snugkv npm test
```

Without `SNUGKV_BIN` the integration tests are skipped.

## Auto-pipeline observability

`client.stats()` exposes client-side transport counters:

```ts
{
  commands,
  socketWrites,
  autoPipelineBatches,
  batchedCommands,
}
```

These counters make it possible to verify how effectively application traffic is being coalesced without changing command semantics.

## Benchmark

With SnugKV listening on `127.0.0.1:6383`:

```bash
npm run bench:autopipeline
```

The benchmark clears the database before every case, runs three repetitions by default, and compares immediate writes, automatic pipeline caps of 32/64/128/256/512, and an explicit 256-command pipeline.

Environment overrides:

```bash
OPS=200000 \
CONCURRENCY=512 \
VALUE_BYTES=1024 \
REPEATS=3 \
BATCHES=32,64,128,256,512 \
npm run bench:autopipeline
```


## Default automatic pipeline size

The default automatic pipeline cap is 128 commands. Local 1 KiB SET benchmarks at concurrency 512 showed the best combined throughput around 64-128 commands, with 128 reducing 200,000 logical commands to roughly 1,563 socket writes while avoiding the throughput drop observed at 256 and 512.


## Local client benchmark snapshot

Environment: SnugKV server on localhost, Node.js 20, 200,000 operations per workload, concurrency 128, 1 KiB values, 50,000-key read keyspace, three repetitions.

| Workload | @snugkv/client auto-128 | ioredis auto | ioredis no-auto | node-redis |
| --- | ---: | ---: | ---: | ---: |
| SET | 73,825 ops/s | 55,688 ops/s | 33,379 ops/s | 30,293 ops/s |
| GET | 93,701 ops/s | 66,178 ops/s | 39,845 ops/s | 33,900 ops/s |
| 80% GET / 20% SET | 78,262 ops/s | 50,345 ops/s | 30,831 ops/s | 31,729 ops/s |

In this specific local benchmark, `@snugkv/client` was approximately 33% faster on SET, 42% faster on GET, and 55% faster on the mixed workload than ioredis with automatic pipelining enabled. These are benchmark-specific measurements rather than general claims across all workloads or environments.

The Snug client also reduced 200,000 logical commands to roughly 1,563 socket writes in each workload through same-tick batching.


## Client matrix benchmark

Run the full value-size/concurrency/workload matrix:

```bash
npm run bench:client-matrix
```

Defaults:

```text
OPS=100000
REPEATS=3
VALUE_BYTES=64,256,1024,4096
CONCURRENCY=32,128,512
WORKLOADS=set,get,mixed
KEYSPACE=50000
MIXED_WRITE_PERCENT=20
```

For a faster smoke pass before the full matrix:

```bash
OPS=20000 \
REPEATS=1 \
VALUE_BYTES=64,1024 \
CONCURRENCY=32,128 \
WORKLOADS=set,get,mixed \
npm run bench:client-matrix
```


## Full client matrix snapshot

A 36-cell local matrix covered four payload sizes (64 B, 256 B, 1 KiB, 4 KiB), three concurrency levels (32, 128, 512), and SET / GET / 80% GET + 20% SET workloads, with three repetitions per cell.

In that matrix, `@snugkv/client` with the 128-command automatic pipeline cap had the highest average throughput in all 36 cells. Relative to ioredis with automatic pipelining enabled, the lead ranged from roughly 19% to 127%, with a simple average of about 58% across the 36 cells.

Average relative lead versus ioredis-auto by dimension:

```text
Payload size:
  64 B    ~52.6%
  256 B   ~62.3%
  1 KiB   ~60.4%
  4 KiB   ~56.7%

Concurrency:
  32      ~44.8%
  128     ~42.7%
  512     ~86.5%

Workload:
  SET     ~58.0%
  GET     ~51.8%
  mixed   ~64.2%
```

These figures describe this local SnugKV-server benchmark only; they are not universal claims about every Redis-compatible server, machine, network, workload, or client configuration. The matrix benchmark rotates client execution order across repetitions to reduce systematic order bias.


## Current scope

Version 0.2.x is a fast standalone RESP client with automatic pipelining and
typed helpers for the common SnugKV application surface. Arbitrary supported
commands remain available through `client.command([...])`.

Cluster topology discovery, automatic `MOVED`/`ASK` redirection, and automatic
reconnect/failover are not yet implemented in this client. Until those are added,
use the client against a single SnugKV endpoint or a stable proxy/load-balancer
endpoint.
