import { randomUUID } from "node:crypto";
import { asArray, asBuffer, asBufferArray, asInteger, asText, } from "./parse.js";
const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms));
export class LockNotAcquiredError extends Error {
    lockName;
    constructor(lockName) {
        super(`lock ${lockName} is held by someone else`);
        this.lockName = lockName;
        this.name = "LockNotAcquiredError";
    }
}
/** A held lock. `fence` grows with every new holder: reject writes carrying a smaller one. */
export class LockHandle {
    snug;
    lock;
    owner;
    fence;
    constructor(snug, lock, owner, fence) {
        this.snug = snug;
        this.lock = lock;
        this.owner = owner;
        this.fence = fence;
    }
    /** Extends the lock. False if it expired and someone else may hold it. */
    renew(ttlMs) {
        return this.snug.lockRenew(this.lock, this.owner, ttlMs);
    }
    /** Releases the lock. False if it was no longer ours. */
    release() {
        return this.snug.lockRelease(this.lock, this.owner);
    }
}
export class IdempotencyPendingError extends Error {
    key;
    constructor(key) {
        super(`request ${key} is already being processed`);
        this.key = key;
        this.name = "IdempotencyPendingError";
    }
}
/** Typed access to the built-in `snug_*` functions (see docs/BUILTIN-FUNCTIONS.md). */
export class Snug {
    client;
    constructor(client) {
        this.client = client;
    }
    async version() {
        return asText(await this.client.fcallRo("snug_functions_version", []), "snug_functions_version");
    }
    async rateLimit(bucket, options) {
        const args = [options.capacity, options.refillPerSecond];
        if (options.cost !== undefined)
            args.push(options.cost);
        const reply = asArray(await this.client.fcall("snug_rate_limit", [bucket], args), "snug_rate_limit");
        return {
            allowed: asInteger(reply[0], "snug_rate_limit") === 1,
            remaining: asInteger(reply[1], "snug_rate_limit"),
            retryAfterMs: asInteger(reply[2], "snug_rate_limit"),
        };
    }
    /** Takes the lock, or returns null if another owner holds it. */
    async lock(name, options) {
        const owner = options.owner ?? randomUUID();
        const fenceKey = options.fenceKey ?? `${name}:fence`;
        const reply = await this.client.fcall("snug_lock_acquire", [name, fenceKey], [owner, options.ttlMs]);
        const fence = asInteger(reply, "snug_lock_acquire");
        return fence === 0 ? null : new LockHandle(this, name, owner, fence);
    }
    async lockRelease(name, owner) {
        return asInteger(await this.client.fcall("snug_lock_release", [name], [owner]), "snug_lock_release") === 1;
    }
    async lockRenew(name, owner, ttlMs) {
        return asInteger(await this.client.fcall("snug_lock_renew", [name], [owner, ttlMs]), "snug_lock_renew") === 1;
    }
    /** Runs `fn` while holding the lock, and always releases it. */
    async withLock(name, options, fn) {
        const deadline = Date.now() + (options.waitMs ?? 0);
        const delay = options.retryDelayMs ?? 50;
        let handle = await this.lock(name, options);
        while (!handle && Date.now() < deadline) {
            await sleep(delay);
            handle = await this.lock(name, options);
        }
        if (!handle)
            throw new LockNotAcquiredError(name);
        try {
            return await fn(handle);
        }
        finally {
            await handle.release().catch(() => false);
        }
    }
    async idempotencyBegin(key, ttlMs) {
        const reply = asArray(await this.client.fcall("snug_idem_begin", [key], [ttlMs]), "snug_idem_begin");
        if (asInteger(reply[0], "snug_idem_begin") === 1)
            return { status: "started" };
        if (asText(reply[1], "snug_idem_begin") === "pending")
            return { status: "pending" };
        return { status: "done", result: asBuffer(reply[2], "snug_idem_begin") };
    }
    async idempotencyCommit(key, result, ttlMs) {
        return asInteger(await this.client.fcall("snug_idem_commit", [key], [result, ttlMs]), "snug_idem_commit") === 1;
    }
    async idempotencyAbort(key) {
        return asInteger(await this.client.fcall("snug_idem_abort", [key]), "snug_idem_abort") === 1;
    }
    /**
     * Runs `fn` at most once per key. A repeat call returns the stored result with
     * `replayed: true`; a call that arrives while the first is still running throws
     * IdempotencyPendingError. If `fn` throws, the claim is released so it can be retried.
     */
    async idempotent(key, options, fn) {
        const state = await this.idempotencyBegin(key, options.pendingTtlMs);
        if (state.status === "done")
            return { replayed: true, result: state.result };
        if (state.status === "pending")
            throw new IdempotencyPendingError(key);
        let result;
        try {
            result = await fn();
        }
        catch (error) {
            await this.idempotencyAbort(key).catch(() => false);
            throw error;
        }
        await this.idempotencyCommit(key, result, options.resultTtlMs ?? options.pendingTtlMs);
        return { replayed: false, result: Buffer.isBuffer(result) ? result : Buffer.from(result) };
    }
    /** Adds `delta` only if the result stays within `[min, max]` (stock, quota, seats). Integers only. */
    async counterAdd(key, delta, bounds) {
        const reply = asArray(await this.client.fcall("snug_counter_add", [key], [delta, bounds.min, bounds.max]), "snug_counter_add");
        return {
            applied: asInteger(reply[0], "snug_counter_add") === 1,
            value: asInteger(reply[1], "snug_counter_add"),
        };
    }
    /** A queue with acknowledgement and redelivery. All three keys share one hash tag. */
    queue(name) {
        return new SnugQueue(this.client, name);
    }
    /** A leaderboard backed by one sorted set. */
    leaderboard(name) {
        return new SnugLeaderboard(this.client, name);
    }
}
export class SnugQueue {
    client;
    name;
    keys;
    constructor(client, name) {
        this.client = client;
        this.name = name;
        this.keys = [`{${name}}:ready`, `{${name}}:inflight`, `{${name}}:payloads`];
    }
    async push(payload) {
        return String(asInteger(await this.client.fcall("snug_queue_push", this.keys, [payload]), "snug_queue_push"));
    }
    /** Takes the next message, or null when empty. Unacknowledged messages come back after `visibilityMs`. */
    async pop(visibilityMs) {
        const reply = await this.client.fcall("snug_queue_pop", this.keys, [visibilityMs]);
        if (reply === null)
            return null;
        const [id, payload] = asBufferArray(asArray(reply, "snug_queue_pop"), "snug_queue_pop");
        return { id: id.toString(), payload };
    }
    async ack(id) {
        return asInteger(await this.client.fcall("snug_queue_ack", this.keys, [id]), "snug_queue_ack") === 1;
    }
    /** Returns the message to the queue straight away. */
    async nack(id) {
        return asInteger(await this.client.fcall("snug_queue_nack", this.keys, [id]), "snug_queue_nack") === 1;
    }
}
export class SnugLeaderboard {
    client;
    name;
    constructor(client, name) {
        this.client = client;
        this.name = name;
    }
    /** Records a score. `max` (default) keeps the better of old and new, `min` the lower, `replace` always overwrites. */
    async submit(member, score, mode = "max") {
        const reply = asArray(await this.client.fcall("snug_leaderboard_submit", [this.name], [member, score, mode]), "snug_leaderboard_submit");
        return { rank: asInteger(reply[0], "snug_leaderboard_submit"), score: Number(asText(reply[1], "snug_leaderboard_submit")) };
    }
    /** The member with `radius` places above and below, or null if it is not on the board. */
    async around(member, radius) {
        const reply = asArray(await this.client.fcallRo("snug_leaderboard_around", [this.name], [member, radius]), "snug_leaderboard_around");
        if (reply[0] === null)
            return null;
        const flat = asBufferArray(asArray(reply[2], "snug_leaderboard_around"), "snug_leaderboard_around");
        const entries = [];
        for (let i = 0; i + 1 < flat.length; i += 2) {
            entries.push({ member: flat[i], score: Number(flat[i + 1].toString()) });
        }
        return {
            rank: asInteger(reply[0], "snug_leaderboard_around"),
            score: Number(asText(reply[1], "snug_leaderboard_around")),
            entries,
        };
    }
}
