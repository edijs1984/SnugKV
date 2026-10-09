import { type RespCommandArg, type RespValue } from "./resp.js";
import { type Bytes } from "./parse.js";
/** What the JSON helpers need from the client. */
export interface CommandCaller {
    command(args: readonly RespCommandArg[]): Promise<RespValue>;
}
/** Any value `JSON.stringify` accepts. */
export type JsonValue = string | number | boolean | null | JsonValue[] | {
    [key: string]: JsonValue;
};
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
/** Typed access to the JSON commands. Paths use JSONPath (`$`, `$.a.b`, `$.items[*]`). */
export declare class SnugJson {
    private readonly client;
    constructor(client: CommandCaller);
    /** Sets the value at `path`. Returns null when `nx`/`xx` prevented the write. */
    set(key: Bytes, path: string, value: unknown, options?: JsonSetOptions): Promise<"OK" | null>;
    /** Sets several paths in one atomic command. */
    mSet(entries: readonly JsonMSetEntry[]): Promise<"OK">;
    /** Merges `value` into the document at `path` (RFC 7396). */
    merge(key: Bytes, path: string, value: unknown): Promise<"OK">;
    /** The whole document, or null if the key does not exist. */
    get<T = unknown>(key: Bytes): Promise<T | null>;
    /** Every value matching a JSONPath (empty array if none), or null if the key does not exist. */
    getPath<T = unknown>(key: Bytes, path: string): Promise<T[] | null>;
    /** `path` of several documents; null for a key that does not exist. */
    mGet<T = unknown>(keys: readonly Bytes[], path?: string): Promise<(T[] | null)[]>;
    /** Deletes the value at `path` (the whole key for `$`). Returns how many paths were removed. */
    del(key: Bytes, path?: string): Promise<number>;
    /** JSON type name for each match (`object`, `array`, `string`, `integer`, `number`, `boolean`, `null`). */
    type(key: Bytes, path?: string): Promise<string[] | null>;
    /** Empties arrays and objects and zeroes numbers at `path`. Returns how many values changed. */
    clear(key: Bytes, path?: string): Promise<number>;
    numIncrBy(key: Bytes, path: string, by: number): Promise<(number | null)[]>;
    numMultBy(key: Bytes, path: string, by: number): Promise<(number | null)[]>;
    /** Flips booleans at `path`; resolves to the new values (null where the value is not a boolean). */
    toggle(key: Bytes, path: string): Promise<(boolean | null)[]>;
    /** Appends text to the string at `path`; resolves to the new length(s). */
    strAppend(key: Bytes, path: string, text: string): Promise<(number | null)[]>;
    strLen(key: Bytes, path?: string): Promise<(number | null)[]>;
    arrAppend(key: Bytes, path: string, ...values: unknown[]): Promise<(number | null)[]>;
    arrInsert(key: Bytes, path: string, index: number, ...values: unknown[]): Promise<(number | null)[]>;
    /** Position of `value` in the array, or -1. `start`/`stop` narrow the search (stop 0 means to the end). */
    arrIndex(key: Bytes, path: string, value: unknown, start?: number, stop?: number): Promise<(number | null)[]>;
    arrLen(key: Bytes, path?: string): Promise<(number | null)[]>;
    /** Removes and returns the element at `index` (default: the last). */
    arrPop<T = unknown>(key: Bytes, path?: string, index?: number): Promise<(T | null)[]>;
    /** Keeps only the elements from `start` to `stop`; resolves to the new length(s). */
    arrTrim(key: Bytes, path: string, start: number, stop: number): Promise<(number | null)[]>;
    /** Keys of the object at `path`; null when the key or path is missing. */
    objKeys(key: Bytes, path?: string): Promise<string[] | null>;
    objLen(key: Bytes, path?: string): Promise<(number | null)[]>;
}
