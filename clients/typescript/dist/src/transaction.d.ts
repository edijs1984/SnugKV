import { type RespCommandArg, type RespValue } from "./resp.js";
import { type Bytes } from "./parse.js";
type Arg = RespCommandArg;
/** What a transaction needs from the connection. */
export interface RawExecutor {
    /** Sends the commands back to back and resolves every reply, errors included. */
    pipelineRaw(commands: readonly (readonly Arg[])[]): Promise<RespValue[]>;
}
export interface TransactionOptions {
    /**
     * Start with `MULTI ATOMIC`: if any command fails, every write in the
     * transaction is rolled back. Without it Redis rules apply and earlier
     * commands stay applied when a later one fails. Default false.
     */
    atomic?: boolean;
}
/** A transaction that did not complete cleanly. */
export declare class TransactionError extends Error {
    /** True when the server undid every write of the transaction. */
    readonly rolledBack: boolean;
    /** Zero-based position of the failing command, when known. */
    readonly commandIndex: number | undefined;
    /** Upper-case name of the failing command, when known. */
    readonly command: string | undefined;
    /** The server's own error for that command. */
    readonly cause: Error | undefined;
    /** Every reply, in command order, when the server ran the transaction. */
    readonly replies: readonly RespValue[];
    constructor(message: string, details: {
        rolledBack: boolean;
        commandIndex?: number;
        command?: string;
        cause?: Error;
        replies?: readonly RespValue[];
    });
}
/** `MULTI ATOMIC` rolled the whole transaction back. Nothing was applied. */
export declare class TransactionAbortedError extends TransactionError {
    constructor(details: {
        commandIndex: number;
        command: string;
        cause: Error;
    });
}
/**
 * A typed MULTI/EXEC builder. Every method appends a command and widens the
 * result tuple, so `exec()` resolves to exactly the types of the commands queued:
 *
 *     const [ok, count, balance] = await client
 *       .multi({ atomic: true })
 *       .set("a", "1")
 *       .incr("hits")
 *       .incrBy("balance", -5)
 *       .exec();
 *
 * The whole transaction is written to the socket in one piece, so commands from
 * other callers on the same client can never land between MULTI and EXEC.
 */
export declare class Transaction<R extends unknown[] = []> {
    private readonly executor;
    private readonly options;
    private readonly steps;
    private used;
    constructor(executor: RawExecutor, options?: TransactionOptions);
    private add;
    /** Any command, reply left as the raw RESP value. */
    command(args: readonly Arg[]): Transaction<[...R, RespValue]>;
    set(key: Bytes, value: Bytes, options?: {
        ex?: number;
        px?: number;
    }): Transaction<[...R, "OK"]>;
    get(key: Bytes): Transaction<[...R, Buffer | null]>;
    del(...keys: Bytes[]): Transaction<[...R, number]>;
    exists(...keys: Bytes[]): Transaction<[...R, number]>;
    expire(key: Bytes, seconds: number): Transaction<[...R, number]>;
    incr(key: Bytes): Transaction<[...R, number]>;
    incrBy(key: Bytes, delta: number): Transaction<[...R, number]>;
    hSet(key: Bytes, field: Bytes, value: Bytes): Transaction<[...R, number]>;
    hIncrBy(key: Bytes, field: Bytes, delta: number): Transaction<[...R, number]>;
    hGetAll(key: Bytes): Transaction<[...R, Record<string, Buffer>]>;
    lPush(key: Bytes, ...values: Bytes[]): Transaction<[...R, number]>;
    rPush(key: Bytes, ...values: Bytes[]): Transaction<[...R, number]>;
    lRange(key: Bytes, start: number, stop: number): Transaction<[...R, Buffer[]]>;
    sAdd(key: Bytes, ...members: Bytes[]): Transaction<[...R, number]>;
    sRem(key: Bytes, ...members: Bytes[]): Transaction<[...R, number]>;
    zAdd(key: Bytes, score: number, member: Bytes): Transaction<[...R, number]>;
    zIncrBy(key: Bytes, increment: number, member: Bytes): Transaction<[...R, number]>;
    /** Calls a function. Scripts and functions inside `atomic` transactions are rolled back too. */
    fcall(name: string, keys: readonly Bytes[], args?: readonly Bytes[]): Transaction<[...R, RespValue]>;
    /** Number of commands queued so far. */
    get length(): number;
    exec(): Promise<R>;
}
export {};
