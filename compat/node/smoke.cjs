const Redis = require("ioredis");
const { createClient } = require("redis");

const HOST = process.env.SNUGKV_HOST || "127.0.0.1";
const PORT = Number(process.env.SNUGKV_PORT || 6380);
const URL = `redis://${HOST}:${PORT}`;
const prefix = `compat:${Date.now()}`;

function assert(condition, message) {
  if (!condition) {
    throw new Error(message);
  }
}

async function testIORedis() {
  console.log("\n[ioredis] connecting...");

  const redis = new Redis({
    host: HOST,
    port: PORT,
    connectTimeout: 3000,
    maxRetriesPerRequest: 1,
  });

  try {
    assert((await redis.ping()) === "PONG", "PING failed");

    await redis.set(`${prefix}:string`, "hello");
    assert(
      (await redis.get(`${prefix}:string`)) === "hello",
      "SET/GET failed"
    );

    await redis.mset(
      `${prefix}:a`, "one",
      `${prefix}:b`, "two"
    );

    const multi = await redis.mget(
      `${prefix}:a`,
      `${prefix}:b`
    );

    assert(
      JSON.stringify(multi) === JSON.stringify(["one", "two"]),
      `MSET/MGET mismatch: ${JSON.stringify(multi)}`
    );

    await redis.set(`${prefix}:counter`, "10");
    assert(
      (await redis.incr(`${prefix}:counter`)) === 11,
      "INCR failed"
    );

    await redis.pexpire(`${prefix}:string`, 60000);

    const ttl = await redis.pttl(`${prefix}:string`);
    assert(ttl > 0 && ttl <= 60000, `PTTL invalid: ${ttl}`);

    const binary = Buffer.from([0, 1, 2, 13, 10, 255, 128]);

    await redis.set(`${prefix}:binary`, binary);

    const binaryBack = await redis.getBuffer(`${prefix}:binary`);

    assert(
      Buffer.compare(binary, binaryBack) === 0,
      `binary mismatch: ${binaryBack.toString("hex")}`
    );

    const pipeline = redis.pipeline();

    pipeline.set(`${prefix}:pipe1`, "x");
    pipeline.set(`${prefix}:pipe2`, "y");
    pipeline.get(`${prefix}:pipe1`);
    pipeline.get(`${prefix}:pipe2`);

    const replies = await pipeline.exec();

    assert(replies.length === 4, "pipeline reply count mismatch");
    assert(replies[2][0] === null && replies[2][1] === "x", "pipeline GET 1 failed");
    assert(replies[3][0] === null && replies[3][1] === "y", "pipeline GET 2 failed");

    console.log("[ioredis] PASS");
  } finally {
    redis.disconnect();
  }
}

async function testNodeRedis() {
  console.log("\n[node-redis] connecting...");

  const client = createClient({
    url: URL,
    RESP: 2,
    socket: {
      connectTimeout: 3000,
      reconnectStrategy: false,
    },
  });

  client.on("error", (err) => {
    console.error("[node-redis] client error:", err.message);
  });

  try {
    await client.connect();

    assert((await client.ping()) === "PONG", "PING failed");

    await client.set(`${prefix}:nr:string`, "hello");

    assert(
      (await client.get(`${prefix}:nr:string`)) === "hello",
      "SET/GET failed"
    );

    await client.mSet({
      [`${prefix}:nr:a`]: "one",
      [`${prefix}:nr:b`]: "two",
    });

    const values = await client.mGet([
      `${prefix}:nr:a`,
      `${prefix}:nr:b`,
    ]);

    assert(
      JSON.stringify(values) === JSON.stringify(["one", "two"]),
      `MSET/MGET mismatch: ${JSON.stringify(values)}`
    );

    await client.set(`${prefix}:nr:counter`, "20");

    assert(
      (await client.incr(`${prefix}:nr:counter`)) === 21,
      "INCR failed"
    );

    await client.pExpire(`${prefix}:nr:string`, 60000);

    const ttl = await client.pTTL(`${prefix}:nr:string`);

    assert(
      ttl > 0 && ttl <= 60000,
      `PTTL invalid: ${ttl}`
    );

    const pipeline = client.multi();

    pipeline.set(`${prefix}:nr:pipe1`, "x");
    pipeline.set(`${prefix}:nr:pipe2`, "y");
    pipeline.get(`${prefix}:nr:pipe1`);

    const replies = await pipeline.execAsPipeline();

    assert(
      replies[2] === "x",
      `pipeline result mismatch: ${JSON.stringify(replies)}`
    );

    console.log("[node-redis] PASS");
  } finally {
    if (client.isOpen) {
      await client.quit();
    }
  }
}

async function testReconnect() {
  console.log("\n[reconnect] testing fresh connections...");

  const first = new Redis(PORT, HOST);

  try {
    await first.set(`${prefix}:reconnect`, "survives");
  } finally {
    first.disconnect();
  }

  const second = new Redis(PORT, HOST);

  try {
    assert(
      (await second.get(`${prefix}:reconnect`)) === "survives",
      "value unavailable after reconnect"
    );

    assert(
      (await second.ping()) === "PONG",
      "PING after reconnect failed"
    );
  } finally {
    second.disconnect();
  }

  console.log("[reconnect] PASS");
}

(async () => {
  try {
    await testIORedis();
    await testNodeRedis();
    await testReconnect();

    console.log("\nALL NODE CLIENT COMPATIBILITY TESTS PASSED");
    process.exit(0);
  } catch (err) {
    console.error("\nCOMPATIBILITY FAILURE");
    console.error(err);
    process.exit(1);
  }
})();
