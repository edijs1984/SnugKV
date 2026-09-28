const assert = require("node:assert/strict");
const Redis = require("ioredis");
const { createCluster } = require("redis");

const seedHost = "127.0.0.1";
const seedPort = 7000;

async function testIORedis() {
  const cluster = new Redis.Cluster(
    [{ host: seedHost, port: seedPort }],
    {
      redisOptions: { connectTimeout: 2000 },
      slotsRefreshTimeout: 2000,
      clusterRetryStrategy: () => null,
    },
  );

  try {
    await cluster.set("client:{1}:a", "ioredis-a");
    assert.equal(await cluster.get("client:{1}:a"), "ioredis-a");

    await cluster.set("client:{2}:b", "ioredis-b");
    assert.equal(await cluster.get("client:{2}:b"), "ioredis-b");

    await cluster.mset("client:{42}:a", "A", "client:{42}:b", "B");
    assert.deepEqual(
      await cluster.mget("client:{42}:a", "client:{42}:b"),
      ["A", "B"],
    );

    console.log("ioredis cluster: PASS");
  } finally {
    cluster.disconnect();
  }
}

async function testNodeRedis() {
  const cluster = createCluster({
    rootNodes: [{ url: `redis://${seedHost}:${seedPort}` }],
    defaults: {
      socket: {
        connectTimeout: 2000,
      },
    },
  });

  cluster.on("error", (err) => {
    console.error("node-redis cluster error:", err.message);
  });

  try {
    await cluster.connect();

    await cluster.set("node:{1}:a", "redis-a");
    assert.equal(await cluster.get("node:{1}:a"), "redis-a");

    await cluster.set("node:{2}:b", "redis-b");
    assert.equal(await cluster.get("node:{2}:b"), "redis-b");

    await cluster.mSet([
      ["node:{42}:a", "A"],
      ["node:{42}:b", "B"],
    ]);
    assert.deepEqual(
      await cluster.mGet(["node:{42}:a", "node:{42}:b"]),
      ["A", "B"],
    );

    console.log("node-redis cluster: PASS");
  } finally {
    if (cluster.isOpen) {
      await cluster.close();
    }
  }
}

(async () => {
  await testIORedis();
  await testNodeRedis();
  console.log("node cluster clients: PASS");
})().catch((err) => {
  console.error(err);
  process.exit(1);
});
