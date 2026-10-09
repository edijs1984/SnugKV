import { type RespValue } from "./resp.js";
/** A key, field, member or value: text, raw bytes. */
export type Bytes = string | Buffer;
export declare function asInteger(value: RespValue, what: string): number;
export declare function asOk(value: RespValue, what: string): "OK";
export declare function asBufferOrNull(value: RespValue, what: string): Buffer | null;
export declare function asBuffer(value: RespValue, what: string): Buffer;
export declare function asBufferArray(value: RespValue, what: string): Buffer[];
export declare function asArray(value: RespValue, what: string): RespValue[];
export declare function asText(value: RespValue, what: string): string;
export declare function asNumber(value: RespValue, what: string): number;
export declare function asHash(value: RespValue, what: string): Record<string, Buffer>;
