import { type RespValue } from "./resp.js";

/** A key, field, member or value: text, raw bytes. */
export type Bytes = string | Buffer;

export function asInteger(value: RespValue, what: string): number {
  if (typeof value !== "number") throw new Error(`unexpected ${what} reply`);
  return value;
}

export function asOk(value: RespValue, what: string): "OK" {
  if (value !== "OK") throw new Error(`unexpected ${what} reply: ${String(value)}`);
  return "OK";
}

export function asBufferOrNull(value: RespValue, what: string): Buffer | null {
  if (value === null || Buffer.isBuffer(value)) return value;
  throw new Error(`unexpected ${what} reply`);
}

export function asBuffer(value: RespValue, what: string): Buffer {
  if (Buffer.isBuffer(value)) return value;
  throw new Error(`unexpected ${what} reply`);
}

export function asBufferArray(value: RespValue, what: string): Buffer[] {
  if (!Array.isArray(value) || value.some((item) => !Buffer.isBuffer(item))) {
    throw new Error(`unexpected ${what} reply`);
  }
  return value as Buffer[];
}

export function asArray(value: RespValue, what: string): RespValue[] {
  if (!Array.isArray(value)) throw new Error(`unexpected ${what} reply`);
  return value;
}

export function asText(value: RespValue, what: string): string {
  if (typeof value === "string") return value;
  if (Buffer.isBuffer(value)) return value.toString();
  throw new Error(`unexpected ${what} reply`);
}

export function asNumber(value: RespValue, what: string): number {
  if (typeof value === "number") return value;
  const n = Number(asText(value, what));
  if (!Number.isFinite(n)) throw new Error(`unexpected ${what} numeric reply`);
  return n;
}

export function asHash(value: RespValue, what: string): Record<string, Buffer> {
  const items = asArray(value, what);
  if (items.length % 2 !== 0) throw new Error(`unexpected ${what} reply`);
  const out: Record<string, Buffer> = {};
  for (let i = 0; i < items.length; i += 2) {
    out[asBuffer(items[i], what).toString()] = asBuffer(items[i + 1], what);
  }
  return out;
}
