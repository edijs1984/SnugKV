import { type RespCommandArg, type RespValue } from "./resp.js";
import { asArray, asBuffer, asBufferOrNull, asOk, asText, type Bytes } from "./parse.js";

/** What the JSON helpers need from the client. */
export interface CommandCaller {
  command(args: readonly RespCommandArg[]): Promise<RespValue>;
}

/** Any value `JSON.stringify` accepts. */
export type JsonValue = string | number | boolean | null | JsonValue[] | { [key: string]: JsonValue };

export interface JsonSetOptions {
  /** Only set if the path does not exist. */
  nx?: boolean;
  /** Only set if the path exists. */
  xx?: boolean;
}

export interface JsonMSetEntry {
  key: Bytes;
  path: string;
  value: unknown;
}

const encode = (value: unknown): string => {
  const text = JSON.stringify(value);
  if (text === undefined) throw new Error("value is not JSON serializable");
  return text;
};

const parseJson = <T>(buf: Buffer): T => JSON.parse(buf.toString()) as T;

/** Matches of a JSONPath query come back as one JSON array; a missing key as null. */
function matches<T>(reply: RespValue, what: string): T[] | null {
  const buf = asBufferOrNull(reply, what);
  if (buf === null) return null;
  const parsed = parseJson<unknown>(buf);
  return Array.isArray(parsed) ? (parsed as T[]) : [parsed as T];
}

/**
 * Numeric results, one per matched path. Accepts both a bare number (a single
 * match) and an array (RedisJSON shape); a wrong-type match is null.
 */
function numbers(reply: RespValue, what: string): (number | null)[] {
  if (reply === null) return [null];
  if (typeof reply === "number") return [reply];
  if (Buffer.isBuffer(reply)) {
    const parsed = parseJson<unknown>(reply);
    return (Array.isArray(parsed) ? parsed : [parsed]).map((n) => (typeof n === "number" ? n : null));
  }
  return asArray(reply, what).map((item) => {
    if (item === null) return null;
    if (typeof item === "number") return item;
    return Number(asText(item, what));
  });
}

/** Typed access to the JSON commands. Paths use JSONPath (`$`, `$.a.b`, `$.items[*]`). */
export class SnugJson {
  constructor(private readonly client: CommandCaller) {}

  /** Sets the value at `path`. Returns null when `nx`/`xx` prevented the write. */
  async set(key: Bytes, path: string, value: unknown, options: JsonSetOptions = {}): Promise<"OK" | null> {
    if (options.nx && options.xx) throw new Error("JSON.SET accepts only one of nx or xx");
    const args: RespCommandArg[] = ["JSON.SET", key, path, encode(value)];
    if (options.nx) args.push("NX");
    if (options.xx) args.push("XX");
    const reply = await this.client.command(args);
    return reply === null ? null : asOk(reply, "JSON.SET");
  }

  /** Sets several paths in one atomic command. */
  async mSet(entries: readonly JsonMSetEntry[]): Promise<"OK"> {
    const args: RespCommandArg[] = ["JSON.MSET"];
    for (const entry of entries) args.push(entry.key, entry.path, encode(entry.value));
    return asOk(await this.client.command(args), "JSON.MSET");
  }

  /** Merges `value` into the document at `path` (RFC 7396). */
  async merge(key: Bytes, path: string, value: unknown): Promise<"OK"> {
    return asOk(await this.client.command(["JSON.MERGE", key, path, encode(value)]), "JSON.MERGE");
  }

  /** The whole document, or null if the key does not exist. */
  async get<T = unknown>(key: Bytes): Promise<T | null> {
    const buf = asBufferOrNull(await this.client.command(["JSON.GET", key, "$"]), "JSON.GET");
    if (buf === null) return null;
    const found = parseJson<T[]>(buf);
    return found.length === 0 ? null : found[0];
  }

  /** Every value matching a JSONPath (empty array if none), or null if the key does not exist. */
  async getPath<T = unknown>(key: Bytes, path: string): Promise<T[] | null> {
    return matches<T>(await this.client.command(["JSON.GET", key, path]), "JSON.GET");
  }

  /** `path` of several documents; null for a key that does not exist. */
  async mGet<T = unknown>(keys: readonly Bytes[], path = "$"): Promise<(T[] | null)[]> {
    const reply = asArray(await this.client.command(["JSON.MGET", ...keys, path]), "JSON.MGET");
    return reply.map((item) => matches<T>(item, "JSON.MGET"));
  }

  /** Deletes the value at `path` (the whole key for `$`). Returns how many paths were removed. */
  async del(key: Bytes, path = "$"): Promise<number> {
    const reply = await this.client.command(["JSON.DEL", key, path]);
    if (typeof reply !== "number") throw new Error("unexpected JSON.DEL reply");
    return reply;
  }

  /** JSON type name for each match (`object`, `array`, `string`, `integer`, `number`, `boolean`, `null`). */
  async type(key: Bytes, path = "$"): Promise<string[] | null> {
    const reply = await this.client.command(["JSON.TYPE", key, path]);
    if (reply === null) return null;
    return asArray(reply, "JSON.TYPE").map((item) => asText(item, "JSON.TYPE"));
  }

  /** Empties arrays and objects and zeroes numbers at `path`. Returns how many values changed. */
  async clear(key: Bytes, path = "$"): Promise<number> {
    const reply = await this.client.command(["JSON.CLEAR", key, path]);
    if (typeof reply !== "number") throw new Error("unexpected JSON.CLEAR reply");
    return reply;
  }

  async numIncrBy(key: Bytes, path: string, by: number): Promise<(number | null)[]> {
    return numbers(await this.client.command(["JSON.NUMINCRBY", key, path, by]), "JSON.NUMINCRBY");
  }

  async numMultBy(key: Bytes, path: string, by: number): Promise<(number | null)[]> {
    return numbers(await this.client.command(["JSON.NUMMULTBY", key, path, by]), "JSON.NUMMULTBY");
  }

  /** Flips booleans at `path`; resolves to the new values (null where the value is not a boolean). */
  async toggle(key: Bytes, path: string): Promise<(boolean | null)[]> {
    return numbers(await this.client.command(["JSON.TOGGLE", key, path]), "JSON.TOGGLE").map((n) =>
      n === null ? null : n === 1,
    );
  }

  /** Appends text to the string at `path`; resolves to the new length(s). */
  async strAppend(key: Bytes, path: string, text: string): Promise<(number | null)[]> {
    return numbers(await this.client.command(["JSON.STRAPPEND", key, path, encode(text)]), "JSON.STRAPPEND");
  }

  async strLen(key: Bytes, path = "$"): Promise<(number | null)[]> {
    return numbers(await this.client.command(["JSON.STRLEN", key, path]), "JSON.STRLEN");
  }

  async arrAppend(key: Bytes, path: string, ...values: unknown[]): Promise<(number | null)[]> {
    if (values.length === 0) throw new Error("JSON.ARRAPPEND needs at least one value");
    return numbers(await this.client.command(["JSON.ARRAPPEND", key, path, ...values.map(encode)]), "JSON.ARRAPPEND");
  }

  async arrInsert(key: Bytes, path: string, index: number, ...values: unknown[]): Promise<(number | null)[]> {
    if (values.length === 0) throw new Error("JSON.ARRINSERT needs at least one value");
    return numbers(
      await this.client.command(["JSON.ARRINSERT", key, path, index, ...values.map(encode)]),
      "JSON.ARRINSERT",
    );
  }

  /** Position of `value` in the array, or -1. `start`/`stop` narrow the search (stop 0 means to the end). */
  async arrIndex(key: Bytes, path: string, value: unknown, start?: number, stop?: number): Promise<(number | null)[]> {
    const args: RespCommandArg[] = ["JSON.ARRINDEX", key, path, encode(value)];
    if (start !== undefined) args.push(start);
    if (stop !== undefined) args.push(stop);
    return numbers(await this.client.command(args), "JSON.ARRINDEX");
  }

  async arrLen(key: Bytes, path = "$"): Promise<(number | null)[]> {
    return numbers(await this.client.command(["JSON.ARRLEN", key, path]), "JSON.ARRLEN");
  }

  /** Removes and returns the element at `index` (default: the last). */
  async arrPop<T = unknown>(key: Bytes, path = "$", index = -1): Promise<(T | null)[]> {
    const reply = await this.client.command(["JSON.ARRPOP", key, path, index]);
    if (reply === null) return [null];
    if (Buffer.isBuffer(reply)) return [parseJson<T>(reply)];
    return asArray(reply, "JSON.ARRPOP").map((item) => (item === null ? null : parseJson<T>(asBuffer(item, "JSON.ARRPOP"))));
  }

  /** Keeps only the elements from `start` to `stop`; resolves to the new length(s). */
  async arrTrim(key: Bytes, path: string, start: number, stop: number): Promise<(number | null)[]> {
    return numbers(await this.client.command(["JSON.ARRTRIM", key, path, start, stop]), "JSON.ARRTRIM");
  }

  /** Keys of the object at `path`; null when the key or path is missing. */
  async objKeys(key: Bytes, path = "$"): Promise<string[] | null> {
    const reply = await this.client.command(["JSON.OBJKEYS", key, path]);
    if (reply === null) return null;
    const items = asArray(reply, "JSON.OBJKEYS");
    // RedisJSON nests one list per match; a single match may arrive flat.
    const flat = items.length > 0 && Array.isArray(items[0]) ? (items[0] as RespValue[]) : items;
    return flat.map((item) => asText(item, "JSON.OBJKEYS"));
  }

  async objLen(key: Bytes, path = "$"): Promise<(number | null)[]> {
    return numbers(await this.client.command(["JSON.OBJLEN", key, path]), "JSON.OBJLEN");
  }
}
