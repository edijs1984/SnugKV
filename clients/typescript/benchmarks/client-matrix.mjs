import { performance } from "node:perf_hooks";
import Redis from "ioredis";
import { createClient } from "redis";
import { SnugKV } from "../dist/index.js";

const HOST = process.env.SNUG_HOST ?? "127.0.0.1";
const PORT = Number(process.env.SNUG_PORT ?? 6383);
const OPS = Number(process.env.OPS ?? 100000);
const REPEATS = Number(process.env.REPEATS ?? 3);
const KEYSPACE = Number(process.env.KEYSPACE ?? 50000);
const MIXED_WRITE_PERCENT = Number(process.env.MIXED_WRITE_PERCENT ?? 20);

const VALUE_SIZES = (process.env.VALUE_BYTES ?? "64,256,1024,4096")
  .split(",")
  .map(Number)
  .filter((n) => Number.isInteger(n) && n > 0);

const CONCURRENCIES = (process.env.CONCURRENCY ?? "32,128,512")
  .split(",")
  .map(Number)
  .filter((n) => Number.isInteger(n) && n > 0);

const WORKLOADS = (process.env.WORKLOADS ?? "set,get,mixed")
  .split(",")
  .map((s) => s.trim())
  .filter((s) => ["set", "get", "mixed"].includes(s));

function buildOperations(workload, ops, keyspace) {
  const out = new Array(ops);

  for (let i = 0; i < ops; i++) {
    const key = `client-matrix:${i % keyspace}`;

    if (workload === "set") {
      out[i] = { type: "set", key };
      continue;
    }
    if (workload === "get") {
      out[i] = { type: "get", key };
      continue;
    }

    const write =
      ((i * 1103515245 + 12345) >>> 0) % 100 < MIXED_WRITE_PERCENT;
    out[i] = { type: write ? "set" : "get", key };
  }

  return out;
}

async function runConcurrent(client, operations, concurrency, value, setFn, getFn) {
  const started = performance.now();

  for (let start = 0; start < operations.length; start += concurrency) {
    const end = Math.min(start + concurrency, operations.length);
    const promises = [];

    for (let i = start; i < end; i++) {
      const op = operations[i];
      promises.push(
        op.type === "set"
          ? setFn(client, op.key, value)
          : getFn(client, op.key),
      );
    }

    await Promise.all(promises);
  }

  const elapsedMs = performance.now() - started;
  return {
    elapsed_ms: Number(elapsedMs.toFixed(2)),
    ops_per_second: Math.round(operations.length / (elapsedMs / 1000)),
  };
}

async function preload(client, keyspace, concurrency, value, setFn) {
  for (let start = 0; start < keyspace; start += concurrency) {
    const end = Math.min(start + concurrency, keyspace);
    const promises = [];
    for (let i = start; i < end; i++) {
      promises.push(setFn(client, `client-matrix:${i}`, value));
    }
    await Promise.all(promises);
  }
}

async function runSnug(ctx) {
  const client = new SnugKV({
    host: HOST,
    port: PORT,
    autoPipeline: true,
    autoPipelineMaxCommands: 128,
  });
  await client.connect();
  await client.command(["FLUSHALL"]);

  if (ctx.workload !== "set") {
    await preload(
      client,
      KEYSPACE,
      ctx.concurrency,
      ctx.value,
      (c, key, value) => c.set(key, value),
    );
  }

  const before = client.stats();
  const result = await runConcurrent(
    client,
    ctx.operations,
    ctx.concurrency,
    ctx.value,
    (c, key, value) => c.set(key, value),
    (c, key) => c.get(key),
  );
  const after = client.stats();
  await client.close();

  return {
    label: "snug-auto-128",
    ...result,
    socket_writes: after.socketWrites - before.socketWrites,
  };
}

async function runIORedis(ctx, enableAutoPipelining) {
  const client = new Redis({
    host: HOST,
    port: PORT,
    enableAutoPipelining,
    enableReadyCheck: false,
    lazyConnect: true,
    maxRetriesPerRequest: null,
  });
  await client.connect();
  await client.flushall();

  if (ctx.workload !== "set") {
    await preload(
      client,
      KEYSPACE,
      ctx.concurrency,
      ctx.value,
      (c, key, value) => c.set(key, value),
    );
  }

  const result = await runConcurrent(
    client,
    ctx.operations,
    ctx.concurrency,
    ctx.value,
    (c, key, value) => c.set(key, value),
    (c, key) => c.get(key),
  );
  client.disconnect();

  return {
    label: enableAutoPipelining ? "ioredis-auto" : "ioredis-no-auto",
    ...result,
  };
}

async function runNodeRedis(ctx) {
  const client = createClient({
    socket: {
      host: HOST,
      port: PORT,
      reconnectStrategy: false,
    },
  });
  client.on("error", () => {});
  await client.connect();
  await client.flushAll();

  if (ctx.workload !== "set") {
    await preload(
      client,
      KEYSPACE,
      ctx.concurrency,
      ctx.value,
      (c, key, value) => c.set(key, value),
    );
  }

  const result = await runConcurrent(
    client,
    ctx.operations,
    ctx.concurrency,
    ctx.value,
    (c, key, value) => c.set(key, value),
    (c, key) => c.get(key),
  );
  await client.close();

  return {
    label: "node-redis-auto",
    ...result,
  };
}

const labels = [
  "snug-auto-128",
  "ioredis-no-auto",
  "ioredis-auto",
  "node-redis-auto",
];

const rows = [];

for (const valueBytes of VALUE_SIZES) {
  const value = "x".repeat(valueBytes);

  for (const concurrency of CONCURRENCIES) {
    for (const workload of WORKLOADS) {
      const operations = buildOperations(workload, OPS, KEYSPACE);
      const ctx = { workload, concurrency, value, operations };

      const runners = [
        { label: "snug-auto-128", run: () => runSnug(ctx) },
        { label: "ioredis-no-auto", run: () => runIORedis(ctx, false) },
        { label: "ioredis-auto", run: () => runIORedis(ctx, true) },
        { label: "node-redis-auto", run: () => runNodeRedis(ctx) },
      ];

      for (let repeat = 1; repeat <= REPEATS; repeat++) {
        // Rotate client order on every repetition so a fixed execution order
        // cannot systematically benefit one client through cache warmth,
        // optimizer state, GC timing, or thermal effects.
        const offset = (repeat - 1) % runners.length;
        const ordered = [
          ...runners.slice(offset),
          ...runners.slice(0, offset),
        ];

        for (const entry of ordered) {
          const run = entry.run;
          try {
            const result = await run();
            const row = {
              repeat,
              workload,
              concurrency,
              value_bytes: valueBytes,
              ops: OPS,
              keyspace: KEYSPACE,
              ...result,
            };
            rows.push(row);
            console.log(JSON.stringify(row));
          } catch (error) {
            const row = {
              repeat,
              workload,
              concurrency,
              value_bytes: valueBytes,
              label: entry.label,
              error: error instanceof Error ? error.message : String(error),
            };
            rows.push(row);
            console.log(JSON.stringify(row));
          }
        }
      }
    }
  }
}

console.log("\nsummary");
for (const valueBytes of VALUE_SIZES) {
  for (const concurrency of CONCURRENCIES) {
    for (const workload of WORKLOADS) {
      for (const label of labels) {
        const selected = rows.filter(
          (row) =>
            row.value_bytes === valueBytes &&
            row.concurrency === concurrency &&
            row.workload === workload &&
            row.label === label &&
            typeof row.ops_per_second === "number",
        );
        if (selected.length === 0) continue;

        const avg =
          selected.reduce((sum, row) => sum + row.ops_per_second, 0) /
          selected.length;
        const min = Math.min(...selected.map((row) => row.ops_per_second));
        const max = Math.max(...selected.map((row) => row.ops_per_second));

        const summary = {
          value_bytes: valueBytes,
          concurrency,
          workload,
          label,
          repeats: selected.length,
          avg_ops_per_second: Math.round(avg),
          min_ops_per_second: min,
          max_ops_per_second: max,
        };

        if (label === "snug-auto-128") {
          summary.avg_socket_writes = Math.round(
            selected.reduce((sum, row) => sum + row.socket_writes, 0) /
              selected.length,
          );
        }

        console.log(JSON.stringify(summary));
      }
    }
  }
}
