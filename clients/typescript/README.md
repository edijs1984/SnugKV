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
