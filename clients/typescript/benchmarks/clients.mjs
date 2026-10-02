import { performance } from "node:perf_hooks";
import Redis from "ioredis";
import { createClient } from "redis";
import { SnugKV } from "../dist/src/index.js";

const HOST = process.env.SNUG_HOST ?? "127.0.0.1";
const PORT = Number(process.env.SNUG_PORT ?? 6383);
const OPS = Number(process.env.OPS ?? 200000);
const CONCURRENCY = Number(process.env.CONCURRENCY ?? 128);
const VALUE_BYTES = Number(process.env.VALUE_BYTES ?? 1024);
const REPEATS = Number(process.env.REPEATS ?? 3);
const VALUE = "x".repeat(VALUE_BYTES);

async function runConcurrentSet(client, setFn) {
  const started = performance.now();

  for (let start = 0; start < OPS; start += CONCURRENCY) {
    const end = Math.min(start + CONCURRENCY, OPS);
    const promises = [];
    for (let i = start; i < end; i++) {
      promises.push(setFn(client, `client-bench:${i}`, VALUE));
    }
    await Promise.all(promises);
  }

  const elapsedMs = performance.now() - started;
  return {
    ops: OPS,
    concurrency: CONCURRENCY,
    value_bytes: VALUE_BYTES,
    elapsed_ms: Number(elapsedMs.toFixed(2)),
    ops_per_second: Math.round(OPS / (elapsedMs / 1000)),
  };
}

async function runSnug() {
  const client = new SnugKV({
    host: HOST,
    port: PORT,
    autoPipeline: true,
    autoPipelineMaxCommands: 128,
  });
  await client.connect();
  await client.command(["FLUSHALL"]);

  const before = client.stats();
  const result = await runConcurrentSet(
    client,
    (c, key, value) => c.set(key, value),
  );
  const after = client.stats();
  await client.close();

  return {
    label: "snug-auto-128",
    ...result,
    socket_writes: after.socketWrites - before.socketWrites,
    auto_pipeline_batches:
      after.autoPipelineBatches - before.autoPipelineBatches,
    batched_commands: after.batchedCommands - before.batchedCommands,
  };
}

async function runIORedis(enableAutoPipelining) {
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

  const result = await runConcurrentSet(
    client,
    (c, key, value) => c.set(key, value),
  );
  client.disconnect();

  return {
    label: enableAutoPipelining ? "ioredis-auto" : "ioredis-no-auto",
    ...result,
  };
}

async function runNodeRedis() {
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

  const result = await runConcurrentSet(
    client,
    (c, key, value) => c.set(key, value),
  );
  await client.close();

  return {
    label: "node-redis-auto",
    ...result,
  };
}

const runners = [
  runSnug,
  () => runIORedis(false),
  () => runIORedis(true),
  runNodeRedis,
];

const rows = [];

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
        label: run.name || "client",
        error: error instanceof Error ? error.message : String(error),
      };
      rows.push(row);
      console.log(JSON.stringify(row));
    }
  }
}

console.log("\nsummary");
for (const label of [
  "snug-auto-128",
  "ioredis-no-auto",
  "ioredis-auto",
  "node-redis-auto",
]) {
  const selected = rows.filter(
    (row) => row.label === label && typeof row.ops_per_second === "number",
  );
  if (selected.length === 0) continue;

  const avg =
    selected.reduce((sum, row) => sum + row.ops_per_second, 0) /
    selected.length;
  const min = Math.min(...selected.map((row) => row.ops_per_second));
  const max = Math.max(...selected.map((row) => row.ops_per_second));

  const summary = {
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
