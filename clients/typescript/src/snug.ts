import { randomUUID } from "node:crypto";

import { type RespCommandArg, type RespValue } from "./resp.js";
import {
  asArray,
  asBuffer,
  asBufferArray,
  asInteger,
  asText,
  type Bytes,
} from "./parse.js";

/** What the wrappers need from the client. */
export interface FunctionCaller {
  fcall(name: string, keys: readonly Bytes[], args?: readonly RespCommandArg[]): Promise<RespValue>;
  fcallRo(name: string, keys: readonly Bytes[], args?: readonly RespCommandArg[]): Promise<RespValue>;
}

const sleep = (ms: number) => new Promise<void>((resolve) => setTimeout(resolve, ms));

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

export class LockNotAcquiredError extends Error {
  constructor(readonly lockName: string) {
    super(`lock ${lockName} is held by someone else`);
    this.name = "LockNotAcquiredError";
  }
}

/** A held lock. `fence` grows with every new holder: reject writes carrying a smaller one. */
export class LockHandle {
  constructor(
    private readonly snug: Snug,
    readonly lock: string,
    readonly owner: string,
    readonly fence: number,
  ) {}

  /** Extends the lock. False if it expired and someone else may hold it. */
  renew(ttlMs: number): Promise<boolean> {
    return this.snug.lockRenew(this.lock, this.owner, ttlMs);
  }

  /** Releases the lock. False if it was no longer ours. */
  release(): Promise<boolean> {
    return this.snug.lockRelease(this.lock, this.owner);
  }
}

export type IdempotencyState =
  | { status: "started" }
  | { status: "pending" }
  | { status: "done"; result: Buffer };

export class IdempotencyPendingError extends Error {
  constructor(readonly key: string) {
    super(`request ${key} is already being processed`);
    this.name = "IdempotencyPendingError";
  }
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
export class Snug {
  constructor(private readonly client: FunctionCaller) {}

  async version(): Promise<string> {
    return asText(await this.client.fcallRo("snug_functions_version", []), "snug_functions_version");
  }

  async rateLimit(bucket: Bytes, options: RateLimitOptions): Promise<RateLimitResult> {
    const args: RespCommandArg[] = [options.capacity, options.refillPerSecond];
    if (options.cost !== undefined) args.push(options.cost);
    const reply = asArray(await this.client.fcall("snug_rate_limit", [bucket], args), "snug_rate_limit");
    return {
      allowed: asInteger(reply[0], "snug_rate_limit") === 1,
      remaining: asInteger(reply[1], "snug_rate_limit"),
      retryAfterMs: asInteger(reply[2], "snug_rate_limit"),
    };
  }

  /** Takes the lock, or returns null if another owner holds it. */
  async lock(name: string, options: LockOptions): Promise<LockHandle | null> {
    const owner = options.owner ?? randomUUID();
    const fenceKey = options.fenceKey ?? `${name}:fence`;
    const reply = await this.client.fcall("snug_lock_acquire", [name, fenceKey], [owner, options.ttlMs]);
    const fence = asInteger(reply, "snug_lock_acquire");
    return fence === 0 ? null : new LockHandle(this, name, owner, fence);
  }

  async lockRelease(name: string, owner: string): Promise<boolean> {
    return asInteger(await this.client.fcall("snug_lock_release", [name], [owner]), "snug_lock_release") === 1;
  }

  async lockRenew(name: string, owner: string, ttlMs: number): Promise<boolean> {
    return asInteger(await this.client.fcall("snug_lock_renew", [name], [owner, ttlMs]), "snug_lock_renew") === 1;
  }

  /** Runs `fn` while holding the lock, and always releases it. */
  async withLock<T>(
    name: string,
    options: WithLockOptions,
    fn: (lock: LockHandle) => Promise<T>,
  ): Promise<T> {
    const deadline = Date.now() + (options.waitMs ?? 0);
    const delay = options.retryDelayMs ?? 50;
    let handle = await this.lock(name, options);
    while (!handle && Date.now() < deadline) {
      await sleep(delay);
      handle = await this.lock(name, options);
    }
    if (!handle) throw new LockNotAcquiredError(name);
    try {
      return await fn(handle);
    } finally {
      await handle.release().catch(() => false);
    }
  }

  async idempotencyBegin(key: string, ttlMs: number): Promise<IdempotencyState> {
    const reply = asArray(await this.client.fcall("snug_idem_begin", [key], [ttlMs]), "snug_idem_begin");
    if (asInteger(reply[0], "snug_idem_begin") === 1) return { status: "started" };
    if (asText(reply[1], "snug_idem_begin") === "pending") return { status: "pending" };
    return { status: "done", result: asBuffer(reply[2], "snug_idem_begin") };
  }

  async idempotencyCommit(key: string, result: Bytes, ttlMs: number): Promise<boolean> {
    return asInteger(await this.client.fcall("snug_idem_commit", [key], [result, ttlMs]), "snug_idem_commit") === 1;
  }

  async idempotencyAbort(key: string): Promise<boolean> {
    return asInteger(await this.client.fcall("snug_idem_abort", [key]), "snug_idem_abort") === 1;
  }

  /**
   * Runs `fn` at most once per key. A repeat call returns the stored result with
   * `replayed: true`; a call that arrives while the first is still running throws
   * IdempotencyPendingError. If `fn` throws, the claim is released so it can be retried.
   */
  async idempotent(
    key: string,
    options: IdempotentOptions,
    fn: () => Promise<Bytes>,
  ): Promise<{ replayed: boolean; result: Buffer }> {
    const state = await this.idempotencyBegin(key, options.pendingTtlMs);
    if (state.status === "done") return { replayed: true, result: state.result };
    if (state.status === "pending") throw new IdempotencyPendingError(key);
    let result: Bytes;
    try {
      result = await fn();
    } catch (error) {
      await this.idempotencyAbort(key).catch(() => false);
      throw error;
    }
    await this.idempotencyCommit(key, result, options.resultTtlMs ?? options.pendingTtlMs);
    return { replayed: false, result: Buffer.isBuffer(result) ? result : Buffer.from(result) };
  }

  /** Adds `delta` only if the result stays within `[min, max]` (stock, quota, seats). Integers only. */
  async counterAdd(key: Bytes, delta: number, bounds: BoundedCounterOptions): Promise<BoundedCounterResult> {
    const reply = asArray(
      await this.client.fcall("snug_counter_add", [key], [delta, bounds.min, bounds.max]),
      "snug_counter_add",
    );
    return {
      applied: asInteger(reply[0], "snug_counter_add") === 1,
      value: asInteger(reply[1], "snug_counter_add"),
    };
  }

  /** A queue with acknowledgement and redelivery. All three keys share one hash tag. */
  queue(name: string): SnugQueue {
    return new SnugQueue(this.client, name);
  }

  /** A leaderboard backed by one sorted set. */
  leaderboard(name: string): SnugLeaderboard {
    return new SnugLeaderboard(this.client, name);
  }
}

export class SnugQueue {
  private readonly keys: [string, string, string];

  constructor(
    private readonly client: FunctionCaller,
    readonly name: string,
  ) {
    this.keys = [`{${name}}:ready`, `{${name}}:inflight`, `{${name}}:payloads`];
  }

  async push(payload: Bytes): Promise<string> {
    return String(asInteger(await this.client.fcall("snug_queue_push", this.keys, [payload]), "snug_queue_push"));
  }

  /** Takes the next message, or null when empty. Unacknowledged messages come back after `visibilityMs`. */
  async pop(visibilityMs: number): Promise<QueueMessage | null> {
    const reply = await this.client.fcall("snug_queue_pop", this.keys, [visibilityMs]);
    if (reply === null) return null;
    const [id, payload] = asBufferArray(asArray(reply, "snug_queue_pop"), "snug_queue_pop");
    return { id: id.toString(), payload };
  }

  async ack(id: string | number): Promise<boolean> {
    return asInteger(await this.client.fcall("snug_queue_ack", this.keys, [id]), "snug_queue_ack") === 1;
  }

  /** Returns the message to the queue straight away. */
  async nack(id: string | number): Promise<boolean> {
    return asInteger(await this.client.fcall("snug_queue_nack", this.keys, [id]), "snug_queue_nack") === 1;
  }
}

export class SnugLeaderboard {
  constructor(
    private readonly client: FunctionCaller,
    readonly name: string,
  ) {}

  /** Records a score. `max` (default) keeps the better of old and new, `min` the lower, `replace` always overwrites. */
  async submit(member: Bytes, score: number, mode: LeaderboardMode = "max"): Promise<LeaderboardPosition> {
    const reply = asArray(
      await this.client.fcall("snug_leaderboard_submit", [this.name], [member, score, mode]),
      "snug_leaderboard_submit",
    );
    return { rank: asInteger(reply[0], "snug_leaderboard_submit"), score: Number(asText(reply[1], "snug_leaderboard_submit")) };
  }

  /** The member with `radius` places above and below, or null if it is not on the board. */
  async around(member: Bytes, radius: number): Promise<LeaderboardWindow | null> {
    const reply = asArray(
      await this.client.fcallRo("snug_leaderboard_around", [this.name], [member, radius]),
      "snug_leaderboard_around",
    );
    if (reply[0] === null) return null;
    const flat = asBufferArray(asArray(reply[2], "snug_leaderboard_around"), "snug_leaderboard_around");
    const entries: LeaderboardEntry[] = [];
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
