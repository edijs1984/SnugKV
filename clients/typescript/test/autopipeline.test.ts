import assert from "node:assert/strict";
import net from "node:net";
import test from "node:test";

import { SnugKV } from "../src/client.js";

function countCommands(buffer: Buffer): number {
  let count = 0;
  for (let i = 0; i + 2 < buffer.length; i++) {
    if (buffer[i] === 42 && (i === 0 || (buffer[i - 2] === 13 && buffer[i - 1] === 10))) {
      count++;
    }
  }
  return count;
}

test("same-tick commands are sent as one automatic pipeline", async () => {
  let dataEvents = 0;
  let received = Buffer.alloc(0);

  const server = net.createServer((socket) => {
    socket.on("data", (chunk) => {
      dataEvents++;
      received = Buffer.concat([received, chunk]);

      const commands = countCommands(received);
      if (commands >= 3) {
        socket.write("+OK\r\n+OK\r\n$5\r\nthree\r\n");
      }
    });
  });

  await new Promise<void>((resolve) => server.listen(0, "127.0.0.1", resolve));
  const address = server.address();
  assert.ok(address && typeof address !== "string");

  const client = new SnugKV({
    host: "127.0.0.1",
    port: address.port,
    autoPipeline: true,
  });
  await client.connect();

  try {
    const p1 = client.set("one", "1");
    const p2 = client.set("two", "2");
    const p3 = client.get("three");

    const [one, two, three] = await Promise.all([p1, p2, p3]);

    assert.equal(one, "OK");
    assert.equal(two, "OK");
    assert.equal(three?.toString(), "three");
    assert.equal(countCommands(received), 3);
    assert.equal(dataEvents, 1);
  } finally {
    await client.close();
    await new Promise<void>((resolve, reject) =>
      server.close((error) => (error ? reject(error) : resolve())),
    );
  }
});

test("autoPipeline false writes commands immediately", async () => {
  let dataEvents = 0;
  let received = Buffer.alloc(0);

  const server = net.createServer((socket) => {
    socket.on("data", (chunk) => {
      dataEvents++;
      received = Buffer.concat([received, chunk]);
      const commands = countCommands(received);
      while ((socket as net.Socket & { _replied?: number })._replied! < commands) {
        const state = socket as net.Socket & { _replied?: number };
        state._replied = (state._replied ?? 0) + 1;
        socket.write("+OK\r\n");
      }
    });
  });

  await new Promise<void>((resolve) => server.listen(0, "127.0.0.1", resolve));
  const address = server.address();
  assert.ok(address && typeof address !== "string");

  const client = new SnugKV({
    host: "127.0.0.1",
    port: address.port,
    autoPipeline: false,
  });
  await client.connect();

  try {
    await client.set("one", "1");
    await client.set("two", "2");
    assert.equal(countCommands(received), 2);
    assert.ok(dataEvents >= 2);
  } finally {
    await client.close();
    await new Promise<void>((resolve, reject) =>
      server.close((error) => (error ? reject(error) : resolve())),
    );
  }
});
