import { performance } from "node:perf_hooks";
import Redis from "ioredis";
import { createClient } from "redis";
import { SnugKV } from "../dist/index.js";

const HOST = process.env.SNUG_HOST ?? "127.0.0.1";
const PORT = Number(process.env.SNUG_PORT ?? 6383);
const OPS = Number(process.env.OPS ?? 200000);
const CONCURRENCY = Number(process.env.CONCURRENCY ?? 128);
const VALUE_BYTES = Number(process.env.VALUE_BYTES ?? 1024);
const REPEATS = Number(process.env.REPEATS ?? 3);
const KEYSPACE = Number(process.env.KEYSPACE ?? 50000);
const MIXED_WRITE_PERCENT = Number(process.env.MIXED_WRITE_PERCENT ?? 20);
const VALUE = "x".repeat(VALUE_BYTES);

function buildOperations(workload) {
  const ops = new Array(OPS);

  for (let i = 0; i < OPS; i++) {
    const keyIndex = i % KEYSPACE;
    const key = `client-bench:${keyIndex}`;

    if (workload === "set") {
      ops[i] = { type: "set", key };
      continue;
    }

    if (workload === "get") {
      ops[i] = { type: "get", key };
      continue;
    }

    // Deterministic 80/20-style mix by default. Avoid randomness so every
    // client receives exactly the same command sequence.
    const write =
      ((i * 1103515245 + 12345) >>> 0) % 100 < MIXED_WRITE_PERCENT;
    ops[i] = { type: write ? "set" : "get", key };
  }

  return ops;
}

async function runConcurrent(client, operations, setFn, getFn) {
  const started = performance.now();

  for (let start = 0; start < operations.length; start += CONCURRENCY) {
    const end = Math.min(start + CONCURRENCY, operations.length);
    const promises = [];

    for (let i = start; i < end; i++) {
      const op = operations[i];
      promises.push(
        op.type === "set"
          ? setFn(client, op.key, VALUE)
          : getFn(client, op.key),
      );
    }

    await Promise.all(promises);
  }

  const elapsedMs = performance.now() - started;
  return {
    ops: operations.length,
    concurrency: CONCURRENCY,
    value_bytes: VALUE_BYTES,
    keyspace: KEYSPACE,
    mixed_write_percent: MIXED_WRITE_PERCENT,
    elapsed_ms: Number(elapsedMs.toFixed(2)),
    ops_per_second: Math.round(operations.length / (elapsedMs / 1000)),
  };
}

async function preload(client, setFn) {
  for (let start = 0; start < KEYSPACE; start += CONCURRENCY) {
    const end = Math.min(start + CONCURRENCY, KEYSPACE);
    const promises = [];
    for (let i = start; i < end; i++) {
      promises.push(setFn(client, `client-bench:${i}`, VALUE));
    }
    await Promise.all(promises);
  }
}

async function runSnug(workload, operations) {
  const client = new SnugKV({
    host: HOST,
    port: PORT,
    autoPipeline: true,
    autoPipelineMaxCommands: 128,
  });
  await client.connect();
  await client.command(["FLUSHALL"]);

  if (workload !== "set") {
    await preload(client, (c, key, value) => c.set(key, value));
  }

  const before = client.stats();
  const result = await runConcurrent(
    client,
    operations,
    (c, key, value) => c.set(key, value),
    (c, key) => c.get(key),
  );
  const after = client.stats();
  await client.close();

  return {
    workload,
    label: "snug-auto-128",
    ...result,
    socket_writes: after.socketWrites - before.socketWrites,
    auto_pipeline_batches:
      after.autoPipelineBatches - before.autoPipelineBatches,
    batched_commands: after.batchedCommands - before.batchedCommands,
  };
}

async function runIORedis(workload, operations, enableAutoPipelining) {
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

  if (workload !== "set") {
    await preload(client, (c, key, value) => c.set(key, value));
  }

  const result = await runConcurrent(
    client,
    operations,
    (c, key, value) => c.set(key, value),
    (c, key) => c.get(key),
  );
  client.disconnect();

  return {
    workload,
    label: enableAutoPipelining ? "ioredis-auto" : "ioredis-no-auto",
    ...result,
  };
}

async function runNodeRedis(workload, operations) {
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

  if (workload !== "set") {
    await preload(client, (c, key, value) => c.set(key, value));
  }

  const result = await runConcurrent(
    client,
    operations,
    (c, key, value) => c.set(key, value),
    (c, key) => c.get(key),
  );
  await client.close();

  return {
    workload,
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

const workloads = ["set", "get", "mixed"];
const rows = [];

for (const workload of workloads) {
  const operations = buildOperations(workload);

  const runners = [
    () => runSnug(workload, operations),
    () => runIORedis(workload, operations, false),
    () => runIORedis(workload, operations, true),
    () => runNodeRedis(workload, operations),
  ];

  for (let repeat = 1; repeat <= REPEATS; repeat++) {
    for (const run of runners) {
      try {
        const result = await run();
        const row = { repeat, ...result };
        rows.push(row);
        console.log(JSON.stringify(row));
      } catch (error) {
        const row = {
          repeat,
          workload,
          label: run.name || "client",
          error: error instanceof Error ? error.message : String(error),
        };
        rows.push(row);
        console.log(JSON.stringify(row));
      }
    }
  }
}

console.log("\nsummary");
for (const workload of workloads) {
  for (const label of labels) {
    const selected = rows.filter(
      (row) =>
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
