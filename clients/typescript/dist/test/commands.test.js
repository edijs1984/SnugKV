import assert from "node:assert/strict";
import net from "node:net";
import test from "node:test";
import { SnugKV } from "../src/client.js";
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
test("typed v1 command surface preserves Redis-compatible replies", async () => {
    const replies = [
        "+PONG\r\n",
        "+OK\r\n",
        "+OK\r\n",
        ":1\r\n",
        ":2\r\n",
        ":7\r\n",
        ":1\r\n",
        "*4\r\n$1\r\nf\r\n$1\r\nv\r\n$1\r\ng\r\n$1\r\nw\r\n",
        ":2\r\n",
        "+OK\r\n",
        "*2\r\n$1\r\na\r\n$1\r\nb\r\n",
        ":2\r\n",
        "*2\r\n$1\r\nx\r\n$1\r\ny\r\n",
        "$3\r\n1.5\r\n",
        ":0\r\n",
        "*4\r\n$1\r\nu\r\n$1\r\n9\r\n$1\r\nv\r\n$1\r\n8\r\n",
        "+OK\r\n",
        ":4\r\n",
        "$17\r\nused_memory:123\r\n\r\n",
        "$21\r\naccounted_bytes:456\r\n\r\n",
    ];
    let received = Buffer.alloc(0);
    let replied = 0;
    const server = net.createServer((socket) => {
        socket.on("data", (chunk) => {
            received = Buffer.concat([received, chunk]);
            const commands = countCommands(received);
            while (replied < commands) {
                socket.write(replies[replied]);
                replied++;
            }
        });
    });
    await new Promise((resolve) => server.listen(0, "127.0.0.1", resolve));
    const address = server.address();
    assert.ok(address && typeof address !== "string");
    const client = new SnugKV({ port: address.port });
    await client.connect();
    try {
        assert.equal(await client.ping(), "PONG");
        assert.equal(await client.set("session", "json", { ex: 60 }), "OK");
        assert.equal(await client.setEx("cache", 60, "value"), "OK");
        assert.equal(await client.expire("cache", 120), 1);
        assert.equal(await client.incr("counter"), 2);
        assert.equal(await client.incrBy("counter", 5), 7);
        assert.equal(await client.hSet("cart", "sku", "value"), 1);
        assert.deepEqual(Object.fromEntries(Object.entries(await client.hGetAll("cart")).map(([k, v]) => [k, v.toString()])), { f: "v", g: "w" });
        assert.equal(await client.lPush("activity", "a", "b"), 2);
        assert.equal(await client.lTrim("activity", 0, 99), "OK");
        assert.deepEqual((await client.lRange("activity", 0, 9)).map(String), ["a", "b"]);
        assert.equal(await client.sAdd("tags", "x", "y"), 2);
        assert.deepEqual((await client.sMembers("tags")).map(String), ["x", "y"]);
        assert.equal(await client.zIncrBy("leaderboard", 1.5, "u"), 1.5);
        assert.equal(await client.zRevRank("leaderboard", "u"), 0);
        assert.deepEqual((await client.zRevRange("leaderboard", 0, 1, { withScores: true })).map((item) => ({
            member: item.member.toString(),
            score: item.score,
        })), [
            { member: "u", score: 9 },
            { member: "v", score: 8 },
        ]);
        assert.equal(await client.flushDb(), "OK");
        assert.equal(await client.dbSize(), 4);
        assert.equal(await client.info("memory"), "used_memory:123\r\n");
        assert.equal(await client.snugStats(), "accounted_bytes:456\r\n");
    }
    finally {
        await client.close();
        await new Promise((resolve, reject) => server.close((error) => (error ? reject(error) : resolve())));
    }
});
