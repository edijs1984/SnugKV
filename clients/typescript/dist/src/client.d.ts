import { type RespCommandArg, type RespValue } from "./resp.js";
import { SnugJson } from "./json.js";
import { Snug } from "./snug.js";
import { Transaction, type RawExecutor, type TransactionOptions } from "./transaction.js";
import { type Bytes } from "./parse.js";
export interface SnugKVOptions {
    host?: string;
    port?: number;
    autoPipeline?: boolean;
    autoPipelineMaxCommands?: number;
    autoPipelineMaxBytes?: number;
    connectTimeoutMs?: number;
    /** Sent as AUTH right after connecting. */
    password?: string;
    /** With `password`: ACL user name. */
    username?: string;
    /** Sent as SELECT right after connecting. */
    database?: number;
}
export interface SnugKVClientStats {
    commands: number;
    socketWrites: number;
    autoPipelineBatches: number;
    batchedCommands: number;
}
export interface SetOptions {
    ex?: number;
    px?: number;
}
export interface ZRangeOptions {
    withScores?: boolean;
}
export interface ZRangeItem {
    member: Buffer;
    score: number;
}
type CommandArg = RespCommandArg;
export declare class SnugKV implements RawExecutor {
    private readonly host;
    private readonly port;
    private readonly autoPipeline;
    private readonly maxCommands;
    private readonly maxBytes;
    private readonly connectTimeoutMs;
    private readonly username;
    private readonly password;
    private readonly database;
    private snugFunctions;
    private jsonCommands;
    private socket;
    private decoder;
    private pendingReplies;
    private queue;
    private queueBytes;
    private flushScheduled;
    private connected;
    private closing;
    private commandCount;
    private socketWriteCount;
    private autoPipelineBatchCount;
    private batchedCommandCount;
    constructor(options?: SnugKVOptions);
    /** Typed JSON commands: `client.json.set("doc", "$", {a: 1})`, `client.json.get<Doc>("doc")`. */
    get json(): SnugJson;
    /** Typed access to the built-in `snug_*` functions: rate limit, locks, idempotency, counters, queues, leaderboards. */
    get snug(): Snug;
    connect(): Promise<void>;
    close(): Promise<void>;
    stats(): SnugKVClientStats;
    command(args: readonly CommandArg[]): Promise<RespValue>;
    /**
     * Starts a typed transaction. With `{ atomic: true }` the server uses
     * `MULTI ATOMIC` and undoes every write if one command fails.
     */
    multi(options?: TransactionOptions): Transaction;
    /** Calls a function: `FCALL name numkeys key... arg...`. */
    fcall(name: string, keys: readonly Bytes[], args?: readonly CommandArg[]): Promise<RespValue>;
    /** Like `fcall`, for functions that do not write; works on replicas. */
    fcallRo(name: string, keys: readonly Bytes[], args?: readonly CommandArg[]): Promise<RespValue>;
    /** Loads a function library and returns its name. */
    functionLoad(code: string, options?: {
        replace?: boolean;
    }): Promise<string>;
    /**
     * Sends the commands back to back, in one socket write, and resolves every
     * reply. Error replies are returned as RespError values instead of rejecting.
     */
    pipelineRaw(commands: readonly (readonly CommandArg[])[]): Promise<RespValue[]>;
    ping(): Promise<string>;
    set(key: string | Buffer, value: string | Buffer, options?: SetOptions): Promise<"OK">;
    setEx(key: string | Buffer, seconds: number, value: string | Buffer): Promise<"OK">;
    get(key: string | Buffer): Promise<Buffer | null>;
    del(...keys: (string | Buffer)[]): Promise<number>;
    exists(...keys: (string | Buffer)[]): Promise<number>;
    expire(key: string | Buffer, seconds: number): Promise<number>;
    incr(key: string | Buffer): Promise<number>;
    incrBy(key: string | Buffer, delta: number): Promise<number>;
    hSet(key: string | Buffer, field: string | Buffer, value: string | Buffer): Promise<number>;
    hGetAll(key: string | Buffer): Promise<Record<string, Buffer>>;
    lPush(key: string | Buffer, ...values: (string | Buffer)[]): Promise<number>;
    lTrim(key: string | Buffer, start: number, stop: number): Promise<"OK">;
    lRange(key: string | Buffer, start: number, stop: number): Promise<Buffer[]>;
    sAdd(key: string | Buffer, ...members: (string | Buffer)[]): Promise<number>;
    sMembers(key: string | Buffer): Promise<Buffer[]>;
    zIncrBy(key: string | Buffer, increment: number, member: string | Buffer): Promise<number>;
    zRevRank(key: string | Buffer, member: string | Buffer): Promise<number | null>;
    zRevRange(key: string | Buffer, start: number, stop: number, options: ZRangeOptions & {
        withScores: true;
    }): Promise<ZRangeItem[]>;
    zRevRange(key: string | Buffer, start: number, stop: number, options?: ZRangeOptions): Promise<Buffer[]>;
    flushDb(): Promise<"OK">;
    dbSize(): Promise<number>;
    info(section?: string): Promise<string>;
    snugStats(): Promise<string>;
    pipeline(commands: readonly (readonly CommandArg[])[]): Promise<RespValue[]>;
    flush(auto?: boolean): void;
    private expectInteger;
    private expectBufferArray;
    private expectNumericBulk;
    private onData;
    private failAll;
}
export {};
