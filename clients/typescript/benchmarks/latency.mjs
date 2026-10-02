import net from "node:net";
import { performance } from "node:perf_hooks";
import { SnugKV } from "../dist/src/index.js";

const TARGET_HOST = process.env.SNUG_HOST ?? "127.0.0.1";
const TARGET_PORT = Number(process.env.SNUG_PORT ?? 6383);
const OPS = Number(process.env.OPS ?? 50000);
const CONCURRENCY = Number(process.env.CONCURRENCY ?? 128);
const VALUE_BYTES = Number(process.env.VALUE_BYTES ?? 1024);
const REPEATS = Number(process.env.REPEATS ?? 3);
const RTT_MS = (process.env.RTT_MS ?? "0,0.5,1,5")
  .split(",")
  .map(Number)
  .filter((n) => Number.isFinite(n) && n >= 0);

const VALUE = "x".repeat(VALUE_BYTES);

function sleep(ms) {
  return new Promise((resolve) => setTimeout(resolve, ms));
}

async function createLatencyProxy(rttMs) {
  const oneWayMs = rttMs / 2;

  const server = net.createServer((clientSocket) => {
    const upstream = net.createConnection({
      host: TARGET_HOST,
      port: TARGET_PORT,
    });

    clientSocket.on("data", (chunk) => {
      if (oneWayMs <= 0) {
        upstream.write(chunk);
      } else {
        const copy = Buffer.from(chunk);
        setTimeout(() => {
          if (!upstream.destroyed) upstream.write(copy);
        }, oneWayMs);
      }
    });

    upstream.on("data", (chunk) => {
      if (oneWayMs <= 0) {
        clientSocket.write(chunk);
      } else {
        const copy = Buffer.from(chunk);
        setTimeout(() => {
          if (!clientSocket.destroyed) clientSocket.write(copy);
        }, oneWayMs);
      }
    });

    const closeBoth = () => {
      if (!clientSocket.destroyed) clientSocket.destroy();
      if (!upstream.destroyed) upstream.destroy();
    };

    clientSocket.on("error", closeBoth);
    upstream.on("error", closeBoth);
    clientSocket.on("close", () => {
      if (!upstream.destroyed) upstream.destroy();
    });
    upstream.on("close", () => {
      if (!clientSocket.destroyed) clientSocket.destroy();
    });
  });

  await new Promise((resolve) => server.listen(0, "127.0.0.1", resolve));
  const address = server.address();
  if (!address || typeof address === "string") {
    throw new Error("failed to start latency proxy");
  }

  return {
    port: address.port,
    close: () =>
      new Promise((resolve, reject) =>
        server.close((error) => (error ? reject(error) : resolve())),
      ),
  };
}

async function runCase(label, port, options) {
  const client = new SnugKV({
    host: "127.0.0.1",
    port,
    ...options,
  });
  await client.connect();

  await client.command(["FLUSHALL"]);
  const before = client.stats();
  const started = performance.now();

  for (let start = 0; start < OPS; start += CONCURRENCY) {
    const end = Math.min(start + CONCURRENCY, OPS);
    const promises = [];
    for (let i = start; i < end; i++) {
      promises.push(client.set(`latency:${i}`, VALUE));
    }
    await Promise.all(promises);
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
    socket_writes: after.socketWrites - before.socketWrites,
    auto_pipeline_batches:
      after.autoPipelineBatches - before.autoPipelineBatches,
    batched_commands: after.batchedCommands - before.batchedCommands,
  };
}

const rows = [];

for (const rttMs of RTT_MS) {
  const proxy = await createLatencyProxy(rttMs);
  try {
    // Give the ephemeral listener/upstream path a moment to settle between RTT cases.
    await sleep(20);

    for (let repeat = 1; repeat <= REPEATS; repeat++) {
      for (const entry of [
        { label: "no-auto", options: { autoPipeline: false } },
        {
          label: "auto-128",
          options: {
            autoPipeline: true,
            autoPipelineMaxCommands: 128,
          },
        },
      ]) {
        const result = await runCase(entry.label, proxy.port, entry.options);
        const row = { repeat, rtt_ms: rttMs, ...result };
        rows.push(row);
        console.log(JSON.stringify(row));
      }
    }
  } finally {
    await proxy.close();
  }
}

console.log("\nsummary");
for (const rttMs of RTT_MS) {
  for (const label of ["no-auto", "auto-128"]) {
    const selected = rows.filter(
      (row) => row.rtt_ms === rttMs && row.label === label,
    );
    const avgOps =
      selected.reduce((sum, row) => sum + row.ops_per_second, 0) /
      selected.length;
    const avgWrites =
      selected.reduce((sum, row) => sum + row.socket_writes, 0) /
      selected.length;

    console.log(
      JSON.stringify({
        rtt_ms: rttMs,
        label,
        repeats: selected.length,
        avg_ops_per_second: Math.round(avgOps),
        avg_socket_writes: Math.round(avgWrites),
      }),
    );
  }
}
