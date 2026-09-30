const fs = require("node:fs");
const path = require("node:path");
const Redis = require("ioredis");
const { createCluster } = require("redis");

const p0 = Number(process.env.P0 || 7120);
const password = process.env.PASSWORD || "cluster-failover-secret";
const stateDir = process.env.CLIENT_STATE_DIR || "/tmp/snugkv-a3-failover-client";
fs.mkdirSync(stateDir, { recursive: true });
const ready = path.join(stateDir, "ready");
const proceed = path.join(stateDir, "post-failover");
const passed = path.join(stateDir, "passed");
const failed = path.join(stateDir, "failed");

function sleep(ms) { return new Promise(r => setTimeout(r, ms)); }
async function retry(label, fn, timeoutMs=15000) {
  const deadline = Date.now() + timeoutMs;
  let last;
  while (Date.now() < deadline) {
    try { return await fn(); } catch (e) { last = e; await sleep(100); }
  }
  throw new Error(`${label}: ${last ? last.message : "timeout"}`);
}

async function main() {
  const io = new Redis.Cluster([{ host: "127.0.0.1", port: p0 }], {
    redisOptions: { password, connectTimeout: 2000, maxRetriesPerRequest: 1 },
    slotsRefreshTimeout: 2000,
    clusterRetryStrategy: () => 100,
  });
  io.on("error", () => {});

  const nr = createCluster({
    rootNodes: [{ url: `redis://default:${encodeURIComponent(password)}@127.0.0.1:${p0}` }],
    defaults: { socket: { connectTimeout: 2000, reconnectStrategy: () => 100 } },
  });
  nr.on("error", () => {});

  try {
    await nr.connect();
    await retry("ioredis preflight", async () => {
      await io.set("a3:failover:{1}:io", "before");
      if (await io.get("a3:failover:{1}:io") !== "before") throw new Error("bad value");
    });
    await retry("node-redis preflight", async () => {
      await nr.set("a3:failover:{2}:nr", "before");
      if (await nr.get("a3:failover:{2}:nr") !== "before") throw new Error("bad value");
    });
    fs.writeFileSync(ready, "ready\n");

    while (!fs.existsSync(proceed)) await sleep(50);

    await retry("ioredis recovery", async () => {
      await io.set("a3:failover:{1}:io", "after");
      if (await io.get("a3:failover:{1}:io") !== "after") throw new Error("bad value");
    });
    await retry("node-redis recovery", async () => {
      await nr.set("a3:failover:{2}:nr", "after");
      if (await nr.get("a3:failover:{2}:nr") !== "after") throw new Error("bad value");
    });
    fs.writeFileSync(passed, "PASS\n");
    console.log("persistent ioredis + node-redis failover/reconnect: PASS");
  } finally {
    io.disconnect();
    if (nr.isOpen) nr.destroy();
  }
}

main().catch((err) => {
  fs.writeFileSync(failed, String(err && err.stack || err));
  console.error(err);
  process.exit(1);
});
