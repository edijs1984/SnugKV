import { RespError } from "./resp.js";
import { asBufferArray, asBufferOrNull, asHash, asInteger, asNumber, asOk, } from "./parse.js";
/** A transaction that did not complete cleanly. */
export class TransactionError extends Error {
    /** True when the server undid every write of the transaction. */
    rolledBack;
    /** Zero-based position of the failing command, when known. */
    commandIndex;
    /** Upper-case name of the failing command, when known. */
    command;
    /** The server's own error for that command. */
    cause;
    /** Every reply, in command order, when the server ran the transaction. */
    replies;
    constructor(message, details) {
        super(message);
        this.name = "TransactionError";
        this.rolledBack = details.rolledBack;
        this.commandIndex = details.commandIndex;
        this.command = details.command;
        this.cause = details.cause;
        this.replies = details.replies ?? [];
    }
}
/** `MULTI ATOMIC` rolled the whole transaction back. Nothing was applied. */
export class TransactionAbortedError extends TransactionError {
    constructor(details) {
        super(`atomic transaction rolled back: command ${details.commandIndex + 1} (${details.command}) failed: ${details.cause.message}`, { ...details, rolledBack: true });
        this.name = "TransactionAbortedError";
    }
}
const ABORT_PATTERN = /^EXECABORT Atomic transaction rolled back: command (\d+) \(([^)]*)\) failed: ([\s\S]*)$/;
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
export class Transaction {
    executor;
    options;
    steps = [];
    used = false;
    constructor(executor, options = {}) {
        this.executor = executor;
        this.options = options;
    }
    add(args, parse) {
        if (this.used)
            throw new Error("transaction already executed");
        this.steps.push({ args, parse });
        return this;
    }
    /** Any command, reply left as the raw RESP value. */
    command(args) {
        return this.add(args, (reply) => reply);
    }
    set(key, value, options = {}) {
        if (options.ex !== undefined && options.px !== undefined) {
            throw new Error("SET accepts only one of ex or px");
        }
        const args = ["SET", key, value];
        if (options.ex !== undefined)
            args.push("EX", options.ex);
        if (options.px !== undefined)
            args.push("PX", options.px);
        return this.add(args, (reply) => asOk(reply, "SET"));
    }
    get(key) {
        return this.add(["GET", key], (reply) => asBufferOrNull(reply, "GET"));
    }
    del(...keys) {
        return this.add(["DEL", ...keys], (reply) => asInteger(reply, "DEL"));
    }
    exists(...keys) {
        return this.add(["EXISTS", ...keys], (reply) => asInteger(reply, "EXISTS"));
    }
    expire(key, seconds) {
        return this.add(["EXPIRE", key, seconds], (reply) => asInteger(reply, "EXPIRE"));
    }
    incr(key) {
        return this.add(["INCR", key], (reply) => asInteger(reply, "INCR"));
    }
    incrBy(key, delta) {
        return this.add(["INCRBY", key, delta], (reply) => asInteger(reply, "INCRBY"));
    }
    hSet(key, field, value) {
        return this.add(["HSET", key, field, value], (reply) => asInteger(reply, "HSET"));
    }
    hIncrBy(key, field, delta) {
        return this.add(["HINCRBY", key, field, delta], (reply) => asInteger(reply, "HINCRBY"));
    }
    hGetAll(key) {
        return this.add(["HGETALL", key], (reply) => asHash(reply, "HGETALL"));
    }
    lPush(key, ...values) {
        return this.add(["LPUSH", key, ...values], (reply) => asInteger(reply, "LPUSH"));
    }
    rPush(key, ...values) {
        return this.add(["RPUSH", key, ...values], (reply) => asInteger(reply, "RPUSH"));
    }
    lRange(key, start, stop) {
        return this.add(["LRANGE", key, start, stop], (reply) => asBufferArray(reply, "LRANGE"));
    }
    sAdd(key, ...members) {
        return this.add(["SADD", key, ...members], (reply) => asInteger(reply, "SADD"));
    }
    sRem(key, ...members) {
        return this.add(["SREM", key, ...members], (reply) => asInteger(reply, "SREM"));
    }
    zAdd(key, score, member) {
        return this.add(["ZADD", key, score, member], (reply) => asInteger(reply, "ZADD"));
    }
    zIncrBy(key, increment, member) {
        return this.add(["ZINCRBY", key, increment, member], (reply) => asNumber(reply, "ZINCRBY"));
    }
    /** Calls a function. Scripts and functions inside `atomic` transactions are rolled back too. */
    fcall(name, keys, args = []) {
        return this.add(["FCALL", name, keys.length, ...keys, ...args], (reply) => reply);
    }
    /** Number of commands queued so far. */
    get length() {
        return this.steps.length;
    }
    async exec() {
        if (this.used)
            throw new Error("transaction already executed");
        this.used = true;
        if (this.steps.length === 0)
            return [];
        const multi = this.options.atomic ? ["MULTI", "ATOMIC"] : ["MULTI"];
        const replies = await this.executor.pipelineRaw([
            multi,
            ...this.steps.map((step) => step.args),
            ["EXEC"],
        ]);
        const opening = replies[0];
        if (opening instanceof RespError) {
            throw new TransactionError(`MULTI failed: ${opening.message}`, {
                rolledBack: true,
                cause: opening,
            });
        }
        // A command the server refused to queue (unknown command, a command that is
        // not allowed in ATOMIC mode, wrong argument count) discards the transaction.
        for (let i = 0; i < this.steps.length; i++) {
            const queued = replies[i + 1];
            if (queued instanceof RespError) {
                throw new TransactionError(`command ${i + 1} (${commandName(this.steps[i].args)}) was rejected: ${queued.message}`, {
                    rolledBack: true,
                    commandIndex: i,
                    command: commandName(this.steps[i].args),
                    cause: queued,
                });
            }
        }
        const result = replies[this.steps.length + 1];
        if (result instanceof RespError) {
            const match = ABORT_PATTERN.exec(result.message);
            if (match) {
                throw new TransactionAbortedError({
                    commandIndex: Number(match[1]) - 1,
                    command: match[2],
                    cause: new RespError(match[3]),
                });
            }
            throw new TransactionError(`EXEC failed: ${result.message}`, {
                rolledBack: result.code === "EXECABORT",
                cause: result,
            });
        }
        if (result === null) {
            throw new TransactionError("transaction aborted: a watched key changed", { rolledBack: true });
        }
        if (!Array.isArray(result) || result.length !== this.steps.length) {
            throw new Error("unexpected EXEC reply");
        }
        const failed = result.findIndex((item) => item instanceof RespError);
        if (failed !== -1) {
            const cause = result[failed];
            throw new TransactionError(`command ${failed + 1} (${commandName(this.steps[failed].args)}) failed, earlier commands were applied: ${cause.message}`, {
                rolledBack: false,
                commandIndex: failed,
                command: commandName(this.steps[failed].args),
                cause,
                replies: result,
            });
        }
        return result.map((item, i) => this.steps[i].parse(item));
    }
}
function commandName(args) {
    return String(args[0]).toUpperCase();
}
