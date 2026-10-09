import assert from "node:assert/strict";
import net from "node:net";
import test from "node:test";

import { SnugKV } from "../src/index.js";

test("connect sends AUTH then SELECT before any other command", async () => {
  const seen: string[] = [];
  const server = net.createServer((socket) => {
    socket.on("data", (chunk) => {
      const text = chunk.toString();
      seen.push(text);
      socket.write("+OK\r\n");
    });
  });
  await new Promise<void>((resolve) => server.listen(0, "127.0.0.1", resolve));
  const { port } = server.address() as net.AddressInfo;
  const client = new SnugKV({ port, username: "app", password: "s3cret", database: 2 });
  await client.connect();
  await client.close();
  server.close();
  assert.equal(seen[0], "*3\r\n$4\r\nAUTH\r\n$3\r\napp\r\n$6\r\ns3cret\r\n");
  assert.equal(seen[1], "*2\r\n$6\r\nSELECT\r\n$1\r\n2\r\n");
});

test("a rejected password fails connect", async () => {
  const server = net.createServer((socket) => {
    socket.on("data", () => socket.write("-WRONGPASS invalid username-password pair\r\n"));
  });
  await new Promise<void>((resolve) => server.listen(0, "127.0.0.1", resolve));
  const { port } = server.address() as net.AddressInfo;
  const client = new SnugKV({ port, password: "bad" });
  await assert.rejects(client.connect(), /WRONGPASS/);
  server.close();
});
