import { type RespCommandArg, type RespValue } from "./resp.js";
import { type Bytes } from "./parse.js";
/** What the wrappers need from the client. */
export interface FunctionCaller {
    fcall(name: string, keys: readonly Bytes[], args?: readonly RespCommandArg[]): Promise<RespValue>;
    fcallRo(name: string, keys: readonly Bytes[], args?: readonly RespCommandArg[]): Promise<RespValue>;
}
export interface RateLimitOptions {
    /** Bucket size: the largest burst allowed. */
    capacity: number;
    /** Tokens added per second. */
    refillPerSecond: number;
    /** Tokens this call takes. Default 1; must not exceed `capacity`. */
    cost?: number;
}
export interface RateLimitResult {
    allowed: boolean;
    /** Whole tokens left after this call. */
    remaining: number;
    /** Milliseconds until `cost` tokens are available; 0 when allowed. */
    retryAfterMs: number;
}
export interface LockOptions {
    /** How long the lock lives without a renewal. */
    ttlMs: number;
    /** Identity of the holder. Default: a random id per acquisition. */
    owner?: string;
    /** Key of the fencing counter. Default `<name>:fence`; keep it in the same hash tag as the lock in a cluster. */
    fenceKey?: string;
}
export interface WithLockOptions extends LockOptions {
    /** Give up after this many milliseconds. Default 0 (try once). */
    waitMs?: number;
    /** Pause between attempts. Default 50. */
    retryDelayMs?: number;
}
export declare class LockNotAcquiredError extends Error {
    readonly lockName: string;
    constructor(lockName: string);
}
/** A held lock. `fence` grows with every new holder: reject writes carrying a smaller one. */
export declare class LockHandle {
    private readonly snug;
    readonly lock: string;
    readonly owner: string;
    readonly fence: number;
    constructor(snug: Snug, lock: string, owner: string, fence: number);
    /** Extends the lock. False if it expired and someone else may hold it. */
    renew(ttlMs: number): Promise<boolean>;
    /** Releases the lock. False if it was no longer ours. */
    release(): Promise<boolean>;
}
export type IdempotencyState = {
    status: "started";
} | {
    status: "pending";
} | {
    status: "done";
    result: Buffer;
};
export declare class IdempotencyPendingError extends Error {
    readonly key: string;
    constructor(key: string);
}
export interface IdempotentOptions {
    /** How long the claim lasts while the work runs. */
    pendingTtlMs: number;
    /** How long the stored result is kept. Default: `pendingTtlMs`. */
    resultTtlMs?: number;
}
export interface BoundedCounterOptions {
    min: number;
    max: number;
}
export interface BoundedCounterResult {
    /** False when the change would leave `[min, max]`; the counter is untouched then. */
    applied: boolean;
    value: number;
}
export interface QueueMessage {
    id: string;
    payload: Buffer;
}
export type LeaderboardMode = "max" | "min" | "replace";
export interface LeaderboardEntry {
    member: Buffer;
    score: number;
}
export interface LeaderboardPosition {
    /** Zero is first place. */
    rank: number;
    score: number;
}
export interface LeaderboardWindow extends LeaderboardPosition {
    /** The member and its neighbours, best first. */
    entries: LeaderboardEntry[];
}
/** Typed access to the built-in `snug_*` functions (see docs/BUILTIN-FUNCTIONS.md). */
export declare class Snug {
    private readonly client;
    constructor(client: FunctionCaller);
    version(): Promise<string>;
    rateLimit(bucket: Bytes, options: RateLimitOptions): Promise<RateLimitResult>;
    /** Takes the lock, or returns null if another owner holds it. */
    lock(name: string, options: LockOptions): Promise<LockHandle | null>;
    lockRelease(name: string, owner: string): Promise<boolean>;
    lockRenew(name: string, owner: string, ttlMs: number): Promise<boolean>;
    /** Runs `fn` while holding the lock, and always releases it. */
    withLock<T>(name: string, options: WithLockOptions, fn: (lock: LockHandle) => Promise<T>): Promise<T>;
    idempotencyBegin(key: string, ttlMs: number): Promise<IdempotencyState>;
    idempotencyCommit(key: string, result: Bytes, ttlMs: number): Promise<boolean>;
    idempotencyAbort(key: string): Promise<boolean>;
    /**
     * Runs `fn` at most once per key. A repeat call returns the stored result with
     * `replayed: true`; a call that arrives while the first is still running throws
     * IdempotencyPendingError. If `fn` throws, the claim is released so it can be retried.
     */
    idempotent(key: string, options: IdempotentOptions, fn: () => Promise<Bytes>): Promise<{
        replayed: boolean;
        result: Buffer;
    }>;
    /** Adds `delta` only if the result stays within `[min, max]` (stock, quota, seats). Integers only. */
    counterAdd(key: Bytes, delta: number, bounds: BoundedCounterOptions): Promise<BoundedCounterResult>;
    /** A queue with acknowledgement and redelivery. All three keys share one hash tag. */
    queue(name: string): SnugQueue;
    /** A leaderboard backed by one sorted set. */
    leaderboard(name: string): SnugLeaderboard;
}
export declare class SnugQueue {
    private readonly client;
    readonly name: string;
    private readonly keys;
    constructor(client: FunctionCaller, name: string);
    push(payload: Bytes): Promise<string>;
    /** Takes the next message, or null when empty. Unacknowledged messages come back after `visibilityMs`. */
    pop(visibilityMs: number): Promise<QueueMessage | null>;
    ack(id: string | number): Promise<boolean>;
    /** Returns the message to the queue straight away. */
    nack(id: string | number): Promise<boolean>;
}
export declare class SnugLeaderboard {
    private readonly client;
    readonly name: string;
    constructor(client: FunctionCaller, name: string);
    /** Records a score. `max` (default) keeps the better of old and new, `min` the lower, `replace` always overwrites. */
    submit(member: Bytes, score: number, mode?: LeaderboardMode): Promise<LeaderboardPosition>;
    /** The member with `radius` places above and below, or null if it is not on the board. */
    around(member: Bytes, radius: number): Promise<LeaderboardWindow | null>;
}
