const Redis = require("ioredis");
const { createClient } = require("redis");

const HOST = process.env.REDIS_HOST || "127.0.0.1";
const PORT = Number(process.env.REDIS_PORT || 6380);
const TARGET = process.env.TARGET_NAME || "snugkv";
const URL = `redis://${HOST}:${PORT}`;
const prefix = `compat:resp3:node:${Date.now()}:${Math.random().toString(16).slice(2)}`;

function assert(condition, message) {
  if (!condition) throw new Error(message);
}

function asArray(value) {
  if (Array.isArray(value)) return value;
  if (value instanceof Set) return [...value];
  return value;
}

async function exerciseRaw(client, label, send) {
  assert((await send(["PING"])) === "PONG", `${label}: PING failed`);

  await send(["SET", `${prefix}:string`, "hello"]);
  assert((await send(["GET", `${prefix}:string`])) === "hello", `${label}: SET/GET failed`);
  assert((await send(["GET", `${prefix}:missing`])) == null, `${label}: missing GET must be null`);

  await send(["MSET", `${prefix}:a`, "one", `${prefix}:b`, "two"]);
  const mget = await send(["MGET", `${prefix}:a`, `${prefix}:b`]);
  assert(JSON.stringify(mget) === JSON.stringify(["one", "two"]), `${label}: MGET mismatch ${JSON.stringify(mget)}`);

  await send(["HSET", `${prefix}:hash`, "a", "1", "b", "2"]);
  const hash = await send(["HGETALL", `${prefix}:hash`]);
  assert(hash != null, `${label}: HGETALL null`);

  await send(["SADD", `${prefix}:set`, "a", "b"]);
  const members = asArray(await send(["SMEMBERS", `${prefix}:set`]));
  assert(Array.isArray(members) && members.length === 2, `${label}: SMEMBERS mismatch ${JSON.stringify(members)}`);

  await send(["ZADD", `${prefix}:z`, "1.5", "a", "2.25", "b"]);
  const score = await send(["ZSCORE", `${prefix}:z`, "a"]);
  assert(score !== null && Number(score) === 1.5, `${label}: ZSCORE mismatch ${String(score)}`);

  const stream = `${prefix}:stream`;
  const id = await send(["XADD", stream, "1-0", "f", "one"]);
  assert(id === "1-0", `${label}: XADD mismatch ${String(id)}`);
  const range = await send(["XRANGE", stream, "-", "+"]);
  assert(Array.isArray(range) && range.length === 1, `${label}: XRANGE mismatch ${JSON.stringify(range)}`);

  await send(["XGROUP", "CREATE", stream, "g", "0"]);
  const grouped = await send(["XREADGROUP", "GROUP", "g", "c", "COUNT", "1", "STREAMS", stream, ">"]);
  assert(grouped != null, `${label}: XREADGROUP returned null`);
  const pending = await send(["XPENDING", stream, "g"]);
  assert(pending != null, `${label}: XPENDING returned null`);
  const groups = await send(["XINFO", "GROUPS", stream]);
  assert(Array.isArray(groups) && groups.length === 1, `${label}: XINFO GROUPS mismatch`);

  await send(["SET", `${prefix}:wrong`, "x"]);
  let wrongtype = false;
  try {
    await send(["HGETALL", `${prefix}:wrong`]);
  } catch (err) {
    wrongtype = String(err.message || err).includes("WRONGTYPE");
  }
  assert(wrongtype, `${label}: WRONGTYPE error not preserved`);
}

async function testIORedis() {
  const client = new Redis({
    host: HOST,
    port: PORT,
    protocol: 3,
    replyMapping: "resp3",
    connectTimeout: 3000,
    maxRetriesPerRequest: 1,
    enableReadyCheck: false,
  });
  try {
    await exerciseRaw(client, "ioredis", (args) => client.call(...args));

    const pipeline = client.pipeline();
    pipeline.set(`${prefix}:io:p1`, "x");
    pipeline.get(`${prefix}:io:p1`);
    const replies = await pipeline.exec();
    assert(replies[0][0] === null && replies[1][1] === "x", "ioredis: pipeline failed");

    const tx = client.multi();
    tx.set(`${prefix}:io:tx`, "ok");
    tx.get(`${prefix}:io:tx`);
    const txReplies = await tx.exec();
    assert(txReplies[1][1] === "ok", "ioredis: MULTI/EXEC failed");
  } finally {
    client.disconnect();
  }
  console.log("[ioredis RESP3] PASS");
}

async function testNodeRedis() {
  const client = createClient({
    url: URL,
    RESP: 3,
    socket: { connectTimeout: 3000, reconnectStrategy: false },
  });
  client.on("error", (err) => console.error("[node-redis]", err.message));

  await client.connect();
  try {
    await exerciseRaw(client, "node-redis", (args) => client.sendCommand(args));

    const pipe = client.multi();
    pipe.set(`${prefix}:nr:p1`, "x");
    pipe.get(`${prefix}:nr:p1`);
    const pipeReplies = await pipe.execAsPipeline();
    assert(pipeReplies[1] === "x", "node-redis: pipeline failed");

    const tx = client.multi();
    tx.set(`${prefix}:nr:tx`, "ok");
    tx.get(`${prefix}:nr:tx`);
    const txReplies = await tx.exec();
    assert(txReplies[1] === "ok", "node-redis: MULTI/EXEC failed");
  } finally {
    client.destroy();
  }
  console.log("[node-redis RESP3] PASS");
}

async function reconnect() {
  const one = createClient({ url: URL, RESP: 3, socket: { reconnectStrategy: false } });
  await one.connect();
  await one.set(`${prefix}:reconnect`, "survives");
  one.destroy();

  const two = createClient({ url: URL, RESP: 3, socket: { reconnectStrategy: false } });
  await two.connect();
  try {
    assert((await two.get(`${prefix}:reconnect`)) === "survives", "node-redis: reconnect GET failed");
  } finally {
    two.destroy();
  }
  console.log("[node-redis RESP3 reconnect] PASS");
}

(async () => {
  try {
    console.log(`RESP3 node smoke target=${TARGET} ${HOST}:${PORT}`);
    await testIORedis();
    await testNodeRedis();
    await reconnect();
    console.log("ALL NODE RESP3 TESTS PASSED");
  } catch (err) {
    console.error("RESP3 NODE COMPATIBILITY FAILURE");
    console.error(err);
    process.exit(1);
  }
})();
