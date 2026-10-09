import assert from "node:assert/strict";
import net from "node:net";
import test from "node:test";
import { SnugKV } from "../src/client.js";
import { RespError } from "../src/resp.js";
async function listen(onConnection) {
    const server = net.createServer(onConnection);
    await new Promise((resolve) => server.listen(0, "127.0.0.1", resolve));
    const address = server.address();
    assert.ok(address && typeof address !== "string");
    return { server, port: address.port };
}
async function closeServer(server) {
    await new Promise((resolve, reject) => server.close((error) => (error ? reject(error) : resolve())));
}
function countCommands(buffer) {
    let count = 0;
    for (let i = 0; i + 1 < buffer.length; i++) {
        if (buffer[i] === 42 &&
            (i === 0 || (buffer[i - 2] === 13 && buffer[i - 1] === 10))) {
            count++;
        }
    }
    return count;
}
test("mixed commands share one automatic pipeline and preserve reply order", async () => {
    let received = Buffer.alloc(0);
    let replied = false;
    const { server, port } = await listen((socket) => {
        socket.on("data", (chunk) => {
            received = Buffer.concat([received, chunk]);
            if (!replied && countCommands(received) >= 4) {
                replied = true;
                socket.write("+OK\r\n$5\r\nvalue\r\n:1\r\n:0\r\n");
            }
        });
    });
    const client = new SnugKV({ port });
    await client.connect();
    try {
        const set = client.set("mixed:key", "value");
        const get = client.get("mixed:key");
        const exists = client.exists("mixed:key");
        const del = client.del("missing:key");
        const [setReply, getReply, existsReply, delReply] = await Promise.all([
            set,
            get,
            exists,
            del,
        ]);
        assert.equal(setReply, "OK");
        assert.equal(getReply?.toString(), "value");
        assert.equal(existsReply, 1);
        assert.equal(delReply, 0);
        const stats = client.stats();
        assert.equal(stats.commands, 4);
        assert.equal(stats.socketWrites, 1);
        assert.equal(stats.autoPipelineBatches, 1);
        assert.equal(stats.batchedCommands, 4);
    }
    finally {
        await client.close();
        await closeServer(server);
    }
});
test("one RESP error rejects only its matching command", async () => {
    let received = Buffer.alloc(0);
    let replied = false;
    const { server, port } = await listen((socket) => {
        socket.on("data", (chunk) => {
            received = Buffer.concat([received, chunk]);
            if (!replied && countCommands(received) >= 3) {
                replied = true;
                socket.write("+OK\r\n-ERR middle failed\r\n+OK\r\n");
            }
        });
    });
    const client = new SnugKV({ port });
    await client.connect();
    try {
        const first = client.command(["PING"]);
        const middle = client.command(["PING"]);
        const last = client.command(["PING"]);
        const results = await Promise.allSettled([first, middle, last]);
        assert.deepEqual(results[0], { status: "fulfilled", value: "OK" });
        assert.equal(results[1].status, "rejected");
        if (results[1].status === "rejected") {
            assert.ok(results[1].reason instanceof RespError);
            assert.equal(results[1].reason.message, "ERR middle failed");
        }
        assert.deepEqual(results[2], { status: "fulfilled", value: "OK" });
    }
    finally {
        await client.close();
        await closeServer(server);
    }
});
test("fragmented RESP replies resolve a pipelined batch correctly", async () => {
    let received = Buffer.alloc(0);
    let replied = false;
    const { server, port } = await listen((socket) => {
        socket.on("data", (chunk) => {
            received = Buffer.concat([received, chunk]);
            if (!replied && countCommands(received) >= 3) {
                replied = true;
                socket.write("+O");
                setImmediate(() => socket.write("K\r\n$5\r\nhe"));
                setImmediate(() => socket.write("llo\r\n:7\r"));
                setImmediate(() => socket.write("\n"));
            }
        });
    });
    const client = new SnugKV({ port });
    await client.connect();
    try {
        const a = client.command(["PING"]);
        const b = client.get("fragmented");
        const c = client.command(["EXISTS", "fragmented"]);
        const [one, two, three] = await Promise.all([a, b, c]);
        assert.equal(one, "OK");
        assert.equal(two.toString(), "hello");
        assert.equal(three, 7);
    }
    finally {
        await client.close();
        await closeServer(server);
    }
});
test("disconnect rejects all outstanding pipelined promises", async () => {
    let destroyed = false;
    const { server, port } = await listen((socket) => {
        socket.on("data", () => {
            if (!destroyed) {
                destroyed = true;
                socket.destroy();
            }
        });
    });
    const client = new SnugKV({ port });
    await client.connect();
    const promises = Array.from({ length: 64 }, (_, i) => client.set(`disconnect:${i}`, "value"));
    const results = await Promise.allSettled(promises);
    assert.equal(results.length, 64);
    assert.ok(results.every((result) => result.status === "rejected"));
    await client.close();
    await closeServer(server);
});
test("autoPipelineMaxCommands flushes before the microtask boundary", async () => {
    let received = Buffer.alloc(0);
    let repliedCommands = 0;
    const { server, port } = await listen((socket) => {
        socket.on("data", (chunk) => {
            received = Buffer.concat([received, chunk]);
            const commands = countCommands(received);
            if (commands > repliedCommands) {
                socket.write("+OK\r\n".repeat(commands - repliedCommands));
                repliedCommands = commands;
            }
        });
    });
    const client = new SnugKV({
        port,
        autoPipelineMaxCommands: 2,
    });
    await client.connect();
    try {
        const replies = await Promise.all([
            client.set("cap:1", "1"),
            client.set("cap:2", "2"),
            client.set("cap:3", "3"),
            client.set("cap:4", "4"),
            client.set("cap:5", "5"),
        ]);
        assert.deepEqual(replies, ["OK", "OK", "OK", "OK", "OK"]);
        const stats = client.stats();
        assert.equal(stats.commands, 5);
        assert.equal(stats.socketWrites, 3);
        assert.equal(stats.autoPipelineBatches, 2);
        assert.equal(stats.batchedCommands, 4);
    }
    finally {
        await client.close();
        await closeServer(server);
    }
});
test("autoPipelineMaxBytes flushes when queued RESP bytes reach the cap", async () => {
    let received = Buffer.alloc(0);
    let repliedCommands = 0;
    const { server, port } = await listen((socket) => {
        socket.on("data", (chunk) => {
            received = Buffer.concat([received, chunk]);
            const commands = countCommands(received);
            if (commands > repliedCommands) {
                socket.write("+OK\r\n".repeat(commands - repliedCommands));
                repliedCommands = commands;
            }
        });
    });
    const client = new SnugKV({
        port,
        autoPipelineMaxCommands: 256,
        autoPipelineMaxBytes: 1,
    });
    await client.connect();
    try {
        await Promise.all([
            client.set("bytes:1", "a"),
            client.set("bytes:2", "b"),
            client.set("bytes:3", "c"),
        ]);
        const stats = client.stats();
        assert.equal(stats.commands, 3);
        assert.equal(stats.socketWrites, 3);
        assert.equal(stats.autoPipelineBatches, 0);
        assert.equal(stats.batchedCommands, 0);
    }
    finally {
        await client.close();
        await closeServer(server);
    }
});
