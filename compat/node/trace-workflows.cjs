const { createClient } = require("redis");

const host = process.env.REDIS_HOST || "127.0.0.1";
const port = Number(process.env.REDIS_PORT || 6380);
const url = `redis://${host}:${port}`;

async function pubsub() {
  const publisher = createClient({ url, RESP: 3, socket: { reconnectStrategy: false } });
  const subscriber = publisher.duplicate();
  publisher.on("error", () => {}); subscriber.on("error", () => {});
  await publisher.connect(); await subscriber.connect();
  let seen = false;
  await subscriber.subscribe("trace:channel", (message) => { if (message === "hello") seen = true; });
  await publisher.publish("trace:channel", "hello");
  for (let i = 0; i < 20 && !seen; i++) await new Promise(r => setTimeout(r, 10));
  if (!seen) throw new Error("Pub/Sub message not observed");
  await subscriber.unsubscribe("trace:channel");
  subscriber.destroy(); publisher.destroy();
}

async function tracking() {
  const client = createClient({ url, RESP: 3, socket: { reconnectStrategy: false } });
  client.on("error", () => {});
  await client.connect();
  await client.sendCommand(["CLIENT","TRACKING","ON","OPTIN"]);
  await client.sendCommand(["CLIENT","CACHING","YES"]);
  await client.set("trace:tracking", "value");
  await client.get("trace:tracking");
  await client.sendCommand(["CLIENT","TRACKING","OFF"]);
  client.destroy();
}

(async () => {
  await pubsub();
  await tracking();
  console.log("node Pub/Sub + tracking trace workflows: PASS");
})().catch((err) => { console.error(err); process.exit(1); });
