export class RespError extends Error {
  constructor(message: string) {
    super(message);
    this.name = "RespError";
  }
}

export type RespValue =
  | string
  | number
  | Buffer
  | null
  | RespError
  | RespValue[];

export function encodeCommand(args: readonly (string | Buffer | number)[]): Buffer {
  const parts: Buffer[] = [Buffer.from(`*${args.length}\r\n`)];
  for (const arg of args) {
    const value = Buffer.isBuffer(arg) ? arg : Buffer.from(String(arg));
    parts.push(Buffer.from(`$${value.length}\r\n`), value, Buffer.from("\r\n"));
  }
  return Buffer.concat(parts);
}

export class RespDecoder {
  private buffer: Buffer<ArrayBufferLike> = Buffer.alloc(0);

  push(chunk: Buffer): RespValue[] {
    if (chunk.length === 0) return [];
    this.buffer =
      this.buffer.length === 0 ? chunk : Buffer.concat([this.buffer, chunk]);

    const values: RespValue[] = [];
    let offset = 0;
    for (;;) {
      const parsed = this.parseAt(offset);
      if (!parsed) break;
      values.push(parsed.value);
      offset = parsed.next;
    }
    if (offset > 0) this.buffer = this.buffer.subarray(offset);
    return values;
  }

  private parseAt(offset: number): { value: RespValue; next: number } | null {
    if (offset >= this.buffer.length) return null;
    const prefix = this.buffer[offset];
    const line = this.readLine(offset + 1);
    if ((prefix === 43 || prefix === 45 || prefix === 58) && !line) return null;

    switch (prefix) {
      case 43:
        return { value: line!.text, next: line!.next };
      case 45:
        return { value: new RespError(line!.text), next: line!.next };
      case 58:
        return { value: Number(line!.text), next: line!.next };
      case 36: {
        if (!line) return null;
        const len = Number(line.text);
        if (len === -1) return { value: null, next: line.next };
        const end = line.next + len;
        if (this.buffer.length < end + 2) return null;
        if (this.buffer[end] !== 13 || this.buffer[end + 1] !== 10) {
          throw new Error("invalid RESP bulk terminator");
        }
        return { value: this.buffer.subarray(line.next, end), next: end + 2 };
      }
      case 42: {
        if (!line) return null;
        const count = Number(line.text);
        if (count === -1) return { value: null, next: line.next };
        const out: RespValue[] = [];
        let next = line.next;
        for (let i = 0; i < count; i++) {
          const item = this.parseAt(next);
          if (!item) return null;
          out.push(item.value);
          next = item.next;
        }
        return { value: out, next };
      }
      default:
        throw new Error(`unsupported RESP prefix: ${String.fromCharCode(prefix)}`);
    }
  }

  private readLine(offset: number): { text: string; next: number } | null {
    for (let i = offset; i + 1 < this.buffer.length; i++) {
      if (this.buffer[i] === 13 && this.buffer[i + 1] === 10) {
        return {
          text: this.buffer.toString("utf8", offset, i),
          next: i + 2,
        };
      }
    }
    return null;
  }
}
