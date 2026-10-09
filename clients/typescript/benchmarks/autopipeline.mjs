import { performance } from "node:perf_hooks";
import { SnugKV } from "../dist/index.js";

const HOST = process.env.SNUG_HOST ?? "127.0.0.1";
const PORT = Number(process.env.SNUG_PORT ?? 6383);
const OPS = Number(process.env.OPS ?? 200000);
const CONCURRENCY = Number(process.env.CONCURRENCY ?? 512);
const VALUE_BYTES = Number(process.env.VALUE_BYTES ?? 1024);
const REPEATS = Number(process.env.REPEATS ?? 3);
const VALUE = "x".repeat(VALUE_BYTES);
const BATCHES = (process.env.BATCHES ?? "32,64,128,256,512")
  .split(",")
  .map(Number)
  .filter((n) => Number.isInteger(n) && n > 0);

function delta(after, before) {
  return {
    commands: after.commands - before.commands,
    socket_writes: after.socketWrites - before.socketWrites,
    auto_pipeline_batches:
      after.autoPipelineBatches - before.autoPipelineBatches,
    batched_commands: after.batchedCommands - before.batchedCommands,
  };
}

async function runCase(label, options, mode, batchSize = CONCURRENCY) {
  const client = new SnugKV({ host: HOST, port: PORT, ...options });
  await client.connect();

  await client.command(["FLUSHALL"]);
  const before = client.stats();
  const started = performance.now();

  if (mode === "concurrent") {
    for (let start = 0; start < OPS; start += CONCURRENCY) {
      const end = Math.min(start + CONCURRENCY, OPS);
      const promises = [];
      for (let i = start; i < end; i++) {
        promises.push(client.set(`bench:${i}`, VALUE));
      }
      await Promise.all(promises);
    }
  } else if (mode === "explicit") {
    for (let start = 0; start < OPS; start += batchSize) {
      const end = Math.min(start + batchSize, OPS);
      const commands = [];
      for (let i = start; i < end; i++) {
        commands.push(["SET", `bench:${i}`, VALUE]);
      }
      await client.pipeline(commands);
    }
  } else {
    throw new Error(`unknown mode: ${mode}`);
  }

  const elapsedMs = performance.now() - started;
  const after = client.stats();
  await client.close();

  return {
    label,
    ops: OPS,
    concurrency: CONCURRENCY,
    value_bytes: VALUE_BYTES,
    elapsed_ms: Number(elapsedMs.toFixed(2)),
    ops_per_second: Math.round(OPS / (elapsedMs / 1000)),
    ...delta(after, before),
  };
}

const cases = [
  {
    label: "no-auto",
    options: { autoPipeline: false },
    mode: "concurrent",
  },
  ...BATCHES.map((batch) => ({
    label: `auto-${batch}`,
    options: {
      autoPipeline: true,
      autoPipelineMaxCommands: batch,
    },
    mode: "concurrent",
  })),
  {
    label: "explicit-256",
    options: { autoPipeline: true },
    mode: "explicit",
    batchSize: 256,
  },
];

const all = [];
for (let repeat = 1; repeat <= REPEATS; repeat++) {
  for (const entry of cases) {
    const result = await runCase(
      entry.label,
      entry.options,
      entry.mode,
      entry.batchSize,
    );
    const row = { repeat, ...result };
    all.push(row);
    console.log(JSON.stringify(row));
  }
}

console.log("\nsummary");
for (const entry of cases) {
  const rows = all.filter((row) => row.label === entry.label);
  const avg = rows.reduce((sum, row) => sum + row.ops_per_second, 0) / rows.length;
  const avgWrites = rows.reduce((sum, row) => sum + row.socket_writes, 0) / rows.length;
  console.log(
    JSON.stringify({
      label: entry.label,
      repeats: rows.length,
      avg_ops_per_second: Math.round(avg),
      avg_socket_writes: Math.round(avgWrites),
    }),
  );
}
