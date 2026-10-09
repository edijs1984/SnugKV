// Runs against a real SnugKV process. Set SNUGKV_BIN to a built binary, e.g.
//   (cd ../.. && go build -o snugkv ./cmd/snugkv) && SNUGKV_BIN=../../snugkv npm test
// Without it these tests are skipped.
import assert from "node:assert/strict";
import { spawn, type ChildProcess } from "node:child_process";
import net from "node:net";
import test, { after, before } from "node:test";

import {
  IdempotencyPendingError,
  LockNotAcquiredError,
  SnugKV,
  TransactionAbortedError,
  TransactionError,
} from "../src/index.js";

const bin = process.env.SNUGKV_BIN;
const skip = bin ? false : "set SNUGKV_BIN to run against a real server";

let child: ChildProcess | undefined;
let port = 0;
let client: SnugKV;

async function freePort(): Promise<number> {
  return new Promise((resolve, reject) => {
    const probe = net.createServer();
    probe.listen(0, "127.0.0.1", () => {
      const { port: p } = probe.address() as net.AddressInfo;
      probe.close(() => resolve(p));
    });
    probe.on("error", reject);
  });
}

before(async () => {
  if (!bin) return;
  port = await freePort();
  child = spawn(bin, ["-listen", `127.0.0.1:${port}`], { stdio: "ignore" });
  for (let attempt = 0; attempt < 100; attempt++) {
    const probe = new SnugKV({ port });
    try {
      await probe.connect();
      await probe.close();
      break;
    } catch {
      await new Promise((r) => setTimeout(r, 50));
    }
  }
  client = new SnugKV({ port });
  await client.connect();
});

after(async () => {
  if (client) await client.close();
  child?.kill();
});

test("typed transaction returns a tuple of parsed replies", { skip }, async () => {
  const [ok, n, m, value] = await client.multi().set("t:a", "1").incr("t:n").incrBy("t:n", 4).get("t:a").exec();
  assert.equal(ok, "OK");
  assert.equal(n, 1);
  assert.equal(m, 5);
  assert.equal(value?.toString(), "1");
});

test("atomic transaction rolls everything back and says which command failed", { skip }, async () => {
  await client.set("t:keep", "before");
  await assert.rejects(
    client
      .multi({ atomic: true })
      .set("t:keep", "after")
      .incr("t:keep") // "after" is not an integer
      .exec(),
    (error: unknown) => {
      assert.ok(error instanceof TransactionAbortedError);
      assert.equal(error.rolledBack, true);
      assert.equal(error.commandIndex, 1);
      assert.equal(error.command, "INCR");
      return true;
    },
  );
  assert.equal((await client.get("t:keep"))?.toString(), "before");
});

test("plain transaction keeps earlier writes and reports the failure", { skip }, async () => {
  await client.set("t:plain", "x");
  await assert.rejects(client.multi().set("t:plain2", "1").incr("t:plain").exec(), (error: unknown) => {
    assert.ok(error instanceof TransactionError && !(error instanceof TransactionAbortedError));
    assert.equal(error.rolledBack, false);
    assert.equal(error.commandIndex, 1);
    return true;
  });
  assert.equal((await client.get("t:plain2"))?.toString(), "1");
});

test("a command refused at queue time discards the transaction", { skip }, async () => {
  await assert.rejects(client.multi({ atomic: true }).set("t:q", "1").command(["PUBLISH", "c", "m"]).exec(), (error: unknown) => {
    assert.ok(error instanceof TransactionError);
    assert.equal(error.command, "PUBLISH");
    return true;
  });
  assert.equal(await client.get("t:q"), null);
});

test("concurrent callers cannot land inside a transaction", { skip }, async () => {
  const jobs: Promise<unknown>[] = [];
  for (let i = 0; i < 50; i++) {
    jobs.push(client.multi({ atomic: true }).set(`t:c${i}`, "1").incr("t:count").exec());
    jobs.push(client.get(`t:c${i}`));
  }
  await Promise.all(jobs);
  assert.equal((await client.get("t:count"))?.toString(), "50");
});

test("rate limiter", { skip }, async () => {
  const first = await client.snug.rateLimit("t:rl", { capacity: 2, refillPerSecond: 1 });
  assert.deepEqual(first, { allowed: true, remaining: 1, retryAfterMs: 0 });
  await client.snug.rateLimit("t:rl", { capacity: 2, refillPerSecond: 1 });
  const third = await client.snug.rateLimit("t:rl", { capacity: 2, refillPerSecond: 1 });
  assert.equal(third.allowed, false);
  assert.ok(third.retryAfterMs > 0);
});

test("lock hands out growing fencing tokens and withLock releases", { skip }, async () => {
  const a = await client.snug.lock("t:lock", { ttlMs: 5000, owner: "a" });
  assert.ok(a);
  assert.equal(await client.snug.lock("t:lock", { ttlMs: 5000, owner: "b" }), null);
  assert.equal(await a.renew(5000), true);
  assert.equal(await a.release(), true);
  const b = await client.snug.lock("t:lock", { ttlMs: 5000, owner: "b" });
  assert.ok(b && b.fence > a.fence);
  await b.release();

  const out = await client.snug.withLock("t:lock", { ttlMs: 5000 }, async (lock) => lock.fence);
  assert.ok(out > b.fence);
  const held = await client.snug.lock("t:lock", { ttlMs: 5000, owner: "z" });
  await assert.rejects(client.snug.withLock("t:lock", { ttlMs: 5000 }, async () => 1), LockNotAcquiredError);
  await held?.release();
});

test("idempotent runs the work once", { skip }, async () => {
  let runs = 0;
  const work = async () => {
    runs++;
    return `charge-${runs}`;
  };
  const first = await client.snug.idempotent("t:idem", { pendingTtlMs: 5000 }, work);
  const second = await client.snug.idempotent("t:idem", { pendingTtlMs: 5000 }, work);
  assert.equal(first.replayed, false);
  assert.equal(second.replayed, true);
  assert.equal(second.result.toString(), "charge-1");
  assert.equal(runs, 1);

  await assert.rejects(
    client.snug.idempotent("t:idem2", { pendingTtlMs: 5000 }, async () => {
      throw new Error("boom");
    }),
    /boom/,
  );
  const retry = await client.snug.idempotent("t:idem2", { pendingTtlMs: 5000 }, async () => "ok");
  assert.equal(retry.replayed, false);

  await client.snug.idempotencyBegin("t:idem3", 5000);
  await assert.rejects(client.snug.idempotent("t:idem3", { pendingTtlMs: 5000 }, async () => "x"), IdempotencyPendingError);
});

test("bounded counter", { skip }, async () => {
  assert.deepEqual(await client.snug.counterAdd("t:stock", 3, { min: 0, max: 3 }), { applied: true, value: 3 });
  assert.deepEqual(await client.snug.counterAdd("t:stock", -2, { min: 0, max: 3 }), { applied: true, value: 1 });
  assert.deepEqual(await client.snug.counterAdd("t:stock", -2, { min: 0, max: 3 }), { applied: false, value: 1 });
});

test("queue redelivers unacknowledged messages", { skip }, async () => {
  const q = client.snug.queue("t:jobs");
  const id = await q.push("job-1");
  const got = await q.pop(60);
  assert.deepEqual({ id: got?.id, payload: got?.payload.toString() }, { id, payload: "job-1" });
  assert.equal(await q.pop(60), null);
  await new Promise((r) => setTimeout(r, 120));
  const again = await q.pop(5000);
  assert.equal(again?.id, id);
  assert.equal(await q.ack(id), true);
  assert.equal(await q.ack(id), false);
  assert.equal(await q.pop(5000), null);
});

test("leaderboard", { skip }, async () => {
  const board = client.snug.leaderboard("t:board");
  for (const [m, s] of [["ann", 50], ["bob", 30], ["cy", 60], ["di", 80], ["ed", 40]] as const) {
    await board.submit(m, s);
  }
  assert.deepEqual(await board.submit("bob", 10), { rank: 4, score: 30 });
  assert.deepEqual(await board.submit("bob", 10, "replace"), { rank: 4, score: 10 });
  const window = await board.around("cy", 1);
  assert.ok(window);
  assert.equal(window.rank, 1);
  assert.deepEqual(window.entries.map((e) => [e.member.toString(), e.score]), [["di", 80], ["cy", 60], ["ann", 50]]);
  assert.equal(await board.around("nobody", 2), null);
});

test("snug functions version", { skip }, async () => {
  assert.equal(await client.snug.version(), "1");
});

test("json: set, get, paths and typed documents", { skip }, async () => {
  interface Doc { name: string; tags: string[]; stats: { hits: number }; on: boolean }
  const j = client.json;
  assert.equal(await j.set("j:1", "$", { name: "ann", tags: ["a", "b"], stats: { hits: 1 }, on: true }), "OK");
  assert.equal(await j.set("j:1", "$.name", "zed", { nx: true }), null);
  const doc = await j.get<Doc>("j:1");
  assert.deepEqual(doc, { name: "ann", tags: ["a", "b"], stats: { hits: 1 }, on: true });
  assert.deepEqual(await j.getPath<number>("j:1", "$.stats.hits"), [1]);
  assert.deepEqual(await j.getPath("j:1", "$.nope"), []);
  assert.equal(await j.get("j:missing"), null);
  assert.deepEqual(await j.mGet<string>(["j:1", "j:missing"], "$.name"), [["ann"], null]);
  assert.deepEqual(await j.type("j:1", "$.tags"), ["array"]);
  assert.deepEqual(await j.objKeys("j:1", "$.stats"), ["hits"]);
  assert.deepEqual(await j.objLen("j:1", "$.stats"), [1]);
});

test("json: numbers, strings, booleans, arrays", { skip }, async () => {
  const j = client.json;
  await j.set("j:2", "$", { n: 1, s: "hi", on: true, arr: [1, 2, 3] });
  assert.deepEqual(await j.numIncrBy("j:2", "$.n", 2), [3]);
  assert.deepEqual(await j.numMultBy("j:2", "$.n", 3), [9]);
  assert.deepEqual(await j.strAppend("j:2", "$.s", "!"), [3]);
  assert.deepEqual(await j.strLen("j:2", "$.s"), [3]);
  assert.deepEqual(await j.toggle("j:2", "$.on"), [false]);
  assert.deepEqual(await j.arrAppend("j:2", "$.arr", 4, { x: 1 }), [5]);
  assert.deepEqual(await j.arrPop("j:2", "$.arr"), [{ x: 1 }]);
  assert.deepEqual(await j.arrInsert("j:2", "$.arr", 0, 0), [5]);
  assert.deepEqual(await j.arrIndex("j:2", "$.arr", 3), [3]);
  assert.deepEqual(await j.arrLen("j:2", "$.arr"), [5]);
  assert.deepEqual(await j.arrTrim("j:2", "$.arr", 0, 1), [2]);
  assert.deepEqual(await j.arrPop("j:2", "$.arr", 0), [0]);
});

test("json: merge, mSet, clear, del", { skip }, async () => {
  const j = client.json;
  await j.set("j:3", "$", { a: { b: 1 } });
  await j.merge("j:3", "$", { a: { c: 2 }, d: 3 });
  assert.deepEqual(await j.get("j:3"), { a: { b: 1, c: 2 }, d: 3 });
  await j.mSet([{ key: "j:3", path: "$.e", value: [1] }, { key: "j:4", path: "$", value: { z: 1 } }]);
  assert.deepEqual(await j.get("j:4"), { z: 1 });
  assert.equal(await j.clear("j:3", "$.e"), 1);
  assert.equal(await j.del("j:3", "$.a"), 1);
  assert.equal(await j.del("j:3"), 1);
  assert.equal(await j.get("j:3"), null);
  await assert.rejects(j.set("j:new", "$.a", 1)); // new documents start at the root
});

test("json: queries that match several values", { skip }, async () => {
  const j = client.json;
  await j.set("j:5", "$", { a: { arr: [1, 2] }, b: { arr: [3] }, items: [{ p: 1 }, { p: 2 }, { p: "x" }] });
  assert.deepEqual(await j.arrLen("j:5", "$..arr"), [2, 1]);
  assert.deepEqual(await j.arrAppend("j:5", "$..arr", 9), [3, 2]);
  assert.deepEqual(await j.numIncrBy("j:5", "$.items[*].p", 10), [11, 12, null]);
  assert.deepEqual(await j.getPath("j:5", "$..arr"), [[1, 2, 9], [3, 9]]);
  assert.deepEqual(await j.arrPop("j:5", "$..arr"), [9, 9]);
  assert.deepEqual(await j.arrIndex("j:5", "$..arr", 2), [1, -1]);
  assert.deepEqual(await j.arrLen("j:5", "$.nope"), []);
  assert.deepEqual(await j.numIncrBy("j:5", "$.nope", 1), []);
});
