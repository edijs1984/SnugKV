import net from "node:net";
import {
  encodeCommand,
  encodeCommands,
  encodedCommandLength,
  RespDecoder,
  RespError,
  type RespCommandArg,
  type RespValue,
} from "./resp.js";

export interface SnugKVOptions {
  host?: string;
  port?: number;
  autoPipeline?: boolean;
  autoPipelineMaxCommands?: number;
  autoPipelineMaxBytes?: number;
  connectTimeoutMs?: number;
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

interface PendingCommand {
  args: readonly CommandArg[];
  encodedLength: number;
  resolve: (value: RespValue) => void;
  reject: (error: Error) => void;
}

export class SnugKV {
  private readonly host: string;
  private readonly port: number;
  private readonly autoPipeline: boolean;
  private readonly maxCommands: number;
  private readonly maxBytes: number;
  private readonly connectTimeoutMs: number;

  private socket: net.Socket | null = null;
  private decoder = new RespDecoder();
  private pendingReplies: PendingCommand[] = [];
  private queue: PendingCommand[] = [];
  private queueBytes = 0;
  private flushScheduled = false;
  private connected = false;
  private closing = false;
  private commandCount = 0;
  private socketWriteCount = 0;
  private autoPipelineBatchCount = 0;
  private batchedCommandCount = 0;

  constructor(options: SnugKVOptions = {}) {
    this.host = options.host ?? "127.0.0.1";
    this.port = options.port ?? 6383;
    this.autoPipeline = options.autoPipeline ?? true;
    this.maxCommands = options.autoPipelineMaxCommands ?? 128;
    this.maxBytes = options.autoPipelineMaxBytes ?? 1024 * 1024;
    this.connectTimeoutMs = options.connectTimeoutMs ?? 5000;
  }

  async connect(): Promise<void> {
    if (this.connected) return;
    if (this.socket) throw new Error("connection already in progress");

    const socket = net.createConnection({ host: this.host, port: this.port });
    this.socket = socket;

    await new Promise<void>((resolve, reject) => {
      const timer = setTimeout(() => {
        socket.destroy();
        reject(new Error("SnugKV connection timeout"));
      }, this.connectTimeoutMs);

      const onError = (error: Error) => {
        clearTimeout(timer);
        socket.off("connect", onConnect);
        reject(error);
      };
      const onConnect = () => {
        clearTimeout(timer);
        socket.off("error", onError);
        this.connected = true;
        resolve();
      };

      socket.once("error", onError);
      socket.once("connect", onConnect);
    });

    socket.on("data", (chunk: Buffer) => this.onData(chunk));
    socket.on("error", (error) => this.failAll(error));
    socket.on("close", () => {
      this.connected = false;
      if (!this.closing) this.failAll(new Error("SnugKV connection closed"));
      this.socket = null;
    });
  }

  async close(): Promise<void> {
    this.closing = true;
    try {
      this.flush(true);
      if (!this.socket) return;
      const socket = this.socket;
      await new Promise<void>((resolve) => {
        socket.once("close", resolve);
        socket.end();
      });
    } finally {
      this.connected = false;
      this.socket = null;
      this.closing = false;
    }
  }

  stats(): SnugKVClientStats {
    return {
      commands: this.commandCount,
      socketWrites: this.socketWriteCount,
      autoPipelineBatches: this.autoPipelineBatchCount,
      batchedCommands: this.batchedCommandCount,
    };
  }

  command(args: readonly CommandArg[]): Promise<RespValue> {
    if (!this.connected || !this.socket) {
      return Promise.reject(new Error("SnugKV client is not connected"));
    }

    const encodedLength = encodedCommandLength(args);
    this.commandCount++;
    return new Promise<RespValue>((resolve, reject) => {
      const pending: PendingCommand = { args, encodedLength, resolve, reject };
      if (!this.autoPipeline) {
        this.pendingReplies.push(pending);
        this.socketWriteCount++;
        this.socket!.write(encodeCommand(args));
        return;
      }

      this.queue.push(pending);
      this.queueBytes += encodedLength;

      if (this.queue.length >= this.maxCommands || this.queueBytes >= this.maxBytes) {
        this.flush(true);
        return;
      }

      if (!this.flushScheduled) {
        this.flushScheduled = true;
        queueMicrotask(() => {
          this.flushScheduled = false;
          this.flush(true);
        });
      }
    });
  }

  async ping(): Promise<string> {
    const result = await this.command(["PING"]);
    if (typeof result !== "string") throw new Error("unexpected PING reply");
    return result;
  }

  async set(
    key: string | Buffer,
    value: string | Buffer,
    options: SetOptions = {},
  ): Promise<"OK"> {
    const args: CommandArg[] = ["SET", key, value];
    if (options.ex !== undefined && options.px !== undefined) {
      throw new Error("SET accepts only one of ex or px");
    }
    if (options.ex !== undefined) args.push("EX", options.ex);
    if (options.px !== undefined) args.push("PX", options.px);

    const result = await this.command(args);
    if (result !== "OK") throw new Error(`unexpected SET reply: ${String(result)}`);
    return "OK";
  }

  async setEx(
    key: string | Buffer,
    seconds: number,
    value: string | Buffer,
  ): Promise<"OK"> {
    const result = await this.command(["SETEX", key, seconds, value]);
    if (result !== "OK") throw new Error(`unexpected SETEX reply: ${String(result)}`);
    return "OK";
  }

  async get(key: string | Buffer): Promise<Buffer | null> {
    const result = await this.command(["GET", key]);
    if (result === null || Buffer.isBuffer(result)) return result;
    throw new Error("unexpected GET reply");
  }

  async del(...keys: (string | Buffer)[]): Promise<number> {
    const result = await this.command(["DEL", ...keys]);
    return this.expectInteger(result, "DEL");
  }

  async exists(...keys: (string | Buffer)[]): Promise<number> {
    const result = await this.command(["EXISTS", ...keys]);
    return this.expectInteger(result, "EXISTS");
  }

  async expire(key: string | Buffer, seconds: number): Promise<number> {
    const result = await this.command(["EXPIRE", key, seconds]);
    return this.expectInteger(result, "EXPIRE");
  }

  async incr(key: string | Buffer): Promise<number> {
    const result = await this.command(["INCR", key]);
    return this.expectInteger(result, "INCR");
  }

  async incrBy(key: string | Buffer, delta: number): Promise<number> {
    const result = await this.command(["INCRBY", key, delta]);
    return this.expectInteger(result, "INCRBY");
  }

  async hSet(
    key: string | Buffer,
    field: string | Buffer,
    value: string | Buffer,
  ): Promise<number> {
    const result = await this.command(["HSET", key, field, value]);
    return this.expectInteger(result, "HSET");
  }

  async hGetAll(key: string | Buffer): Promise<Record<string, Buffer>> {
    const result = await this.command(["HGETALL", key]);
    if (!Array.isArray(result) || result.length % 2 !== 0) {
      throw new Error("unexpected HGETALL reply");
    }
    const out: Record<string, Buffer> = {};
    for (let i = 0; i < result.length; i += 2) {
      const field = result[i];
      const value = result[i + 1];
      if (!Buffer.isBuffer(field) || !Buffer.isBuffer(value)) {
        throw new Error("unexpected HGETALL reply");
      }
      out[field.toString()] = value;
    }
    return out;
  }

  async lPush(key: string | Buffer, ...values: (string | Buffer)[]): Promise<number> {
    const result = await this.command(["LPUSH", key, ...values]);
    return this.expectInteger(result, "LPUSH");
  }

  async lTrim(key: string | Buffer, start: number, stop: number): Promise<"OK"> {
    const result = await this.command(["LTRIM", key, start, stop]);
    if (result !== "OK") throw new Error(`unexpected LTRIM reply: ${String(result)}`);
    return "OK";
  }

  async lRange(key: string | Buffer, start: number, stop: number): Promise<Buffer[]> {
    const result = await this.command(["LRANGE", key, start, stop]);
    return this.expectBufferArray(result, "LRANGE");
  }

  async sAdd(key: string | Buffer, ...members: (string | Buffer)[]): Promise<number> {
    const result = await this.command(["SADD", key, ...members]);
    return this.expectInteger(result, "SADD");
  }

  async sMembers(key: string | Buffer): Promise<Buffer[]> {
    const result = await this.command(["SMEMBERS", key]);
    return this.expectBufferArray(result, "SMEMBERS");
  }

  async zIncrBy(
    key: string | Buffer,
    increment: number,
    member: string | Buffer,
  ): Promise<number> {
    const result = await this.command(["ZINCRBY", key, increment, member]);
    return this.expectNumericBulk(result, "ZINCRBY");
  }

  async zRevRank(
    key: string | Buffer,
    member: string | Buffer,
  ): Promise<number | null> {
    const result = await this.command(["ZREVRANK", key, member]);
    if (result === null) return null;
    return this.expectInteger(result, "ZREVRANK");
  }

  async zRevRange(
    key: string | Buffer,
    start: number,
    stop: number,
    options: ZRangeOptions & { withScores: true },
  ): Promise<ZRangeItem[]>;
  async zRevRange(
    key: string | Buffer,
    start: number,
    stop: number,
    options?: ZRangeOptions,
  ): Promise<Buffer[]>;
  async zRevRange(
    key: string | Buffer,
    start: number,
    stop: number,
    options: ZRangeOptions = {},
  ): Promise<Buffer[] | ZRangeItem[]> {
    const args: CommandArg[] = ["ZREVRANGE", key, start, stop];
    if (options.withScores) args.push("WITHSCORES");
    const result = await this.command(args);
    const values = this.expectBufferArray(result, "ZREVRANGE");
    if (!options.withScores) return values;
    if (values.length % 2 !== 0) throw new Error("unexpected ZREVRANGE WITHSCORES reply");

    const out: ZRangeItem[] = [];
    for (let i = 0; i < values.length; i += 2) {
      const score = Number(values[i + 1].toString());
      if (!Number.isFinite(score)) throw new Error("unexpected ZREVRANGE score");
      out.push({ member: values[i], score });
    }
    return out;
  }

  async flushDb(): Promise<"OK"> {
    const result = await this.command(["FLUSHDB"]);
    if (result !== "OK") throw new Error(`unexpected FLUSHDB reply: ${String(result)}`);
    return "OK";
  }

  async dbSize(): Promise<number> {
    const result = await this.command(["DBSIZE"]);
    return this.expectInteger(result, "DBSIZE");
  }

  async info(section?: string): Promise<string> {
    const result = await this.command(section ? ["INFO", section] : ["INFO"]);
    if (!Buffer.isBuffer(result)) throw new Error("unexpected INFO reply");
    return result.toString();
  }

  async snugStats(): Promise<string> {
    const result = await this.command(["SNUG.STATS"]);
    if (!Buffer.isBuffer(result)) throw new Error("unexpected SNUG.STATS reply");
    return result.toString();
  }

  async pipeline(commands: readonly (readonly CommandArg[])[]): Promise<RespValue[]> {
    if (!this.connected || !this.socket) {
      throw new Error("SnugKV client is not connected");
    }
    if (commands.length === 0) return [];

    this.commandCount += commands.length;
    const promises = commands.map((args) => {
      const encodedLength = encodedCommandLength(args);
      return new Promise<RespValue>((resolve, reject) => {
        this.queue.push({ args, encodedLength, resolve, reject });
        this.queueBytes += encodedLength;
      });
    });
    this.flush(false);
    return Promise.all(promises);
  }

  flush(auto = true): void {
    if (this.queue.length === 0) return;
    if (!this.socket || !this.connected) {
      const error = new Error("SnugKV client is not connected");
      const queued = this.queue.splice(0);
      this.queueBytes = 0;
      for (const pending of queued) pending.reject(error);
      return;
    }

    const batch = this.queue.splice(0);
    this.queueBytes = 0;
    this.pendingReplies.push(...batch);
    this.socketWriteCount++;
    if (auto && batch.length > 1) {
      this.autoPipelineBatchCount++;
      this.batchedCommandCount += batch.length;
    }
    this.socket.write(encodeCommands(batch.map((pending) => pending.args)));
  }

  private expectInteger(result: RespValue, command: string): number {
    if (typeof result !== "number") {
      throw new Error(`unexpected ${command} reply`);
    }
    return result;
  }

  private expectBufferArray(result: RespValue, command: string): Buffer[] {
    if (!Array.isArray(result) || result.some((item) => !Buffer.isBuffer(item))) {
      throw new Error(`unexpected ${command} reply`);
    }
    return result as Buffer[];
  }

  private expectNumericBulk(result: RespValue, command: string): number {
    if (!Buffer.isBuffer(result) && typeof result !== "string") {
      throw new Error(`unexpected ${command} reply`);
    }
    const value = Number(Buffer.isBuffer(result) ? result.toString() : result);
    if (!Number.isFinite(value)) throw new Error(`unexpected ${command} numeric reply`);
    return value;
  }

  private onData(chunk: Buffer): void {
    let values: RespValue[];
    try {
      values = this.decoder.push(chunk);
    } catch (error) {
      this.failAll(error instanceof Error ? error : new Error(String(error)));
      this.socket?.destroy();
      return;
    }

    for (const value of values) {
      const pending = this.pendingReplies.shift();
      if (!pending) {
        this.failAll(new Error("received RESP reply with no pending command"));
        this.socket?.destroy();
        return;
      }
      if (value instanceof RespError) pending.reject(value);
      else pending.resolve(value);
    }
  }

  private failAll(error: Error): void {
    const all = [...this.pendingReplies, ...this.queue];
    this.pendingReplies = [];
    this.queue = [];
    this.queueBytes = 0;
    for (const pending of all) pending.reject(error);
  }
}
