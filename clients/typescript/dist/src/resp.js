export class RespError extends Error {
    /** First word of the error reply, e.g. "ERR", "WRONGTYPE", "EXECABORT". */
    code;
    constructor(message) {
        super(message);
        this.name = "RespError";
        const space = message.indexOf(" ");
        this.code = space === -1 ? message : message.slice(0, space);
    }
}
function argByteLength(arg) {
    return Buffer.isBuffer(arg) ? arg.length : Buffer.byteLength(String(arg));
}
export function encodedCommandLength(args) {
    let total = Buffer.byteLength(`*${args.length}\r\n`);
    for (const arg of args) {
        const len = argByteLength(arg);
        total += Buffer.byteLength("$" + len + "\r\n") + len + 2;
    }
    return total;
}
function writeCommand(target, offset, args) {
    offset += target.write(`*${args.length}\r\n`, offset, "ascii");
    for (const arg of args) {
        const isBuffer = Buffer.isBuffer(arg);
        const text = isBuffer ? "" : String(arg);
        const len = isBuffer ? arg.length : Buffer.byteLength(text);
        offset += target.write("$" + len + "\r\n", offset, "ascii");
        if (isBuffer) {
            arg.copy(target, offset);
            offset += arg.length;
        }
        else {
            offset += target.write(text, offset, len, "utf8");
        }
        target[offset++] = 13;
        target[offset++] = 10;
    }
    return offset;
}
export function encodeCommand(args) {
    const out = Buffer.allocUnsafe(encodedCommandLength(args));
    writeCommand(out, 0, args);
    return out;
}
export function encodeCommands(commands) {
    let total = 0;
    for (const args of commands)
        total += encodedCommandLength(args);
    const out = Buffer.allocUnsafe(total);
    let offset = 0;
    for (const args of commands)
        offset = writeCommand(out, offset, args);
    return out;
}
export class RespDecoder {
    buffer = Buffer.alloc(0);
    push(chunk) {
        if (chunk.length === 0)
            return [];
        this.buffer =
            this.buffer.length === 0 ? chunk : Buffer.concat([this.buffer, chunk]);
        const values = [];
        let offset = 0;
        for (;;) {
            const parsed = this.parseAt(offset);
            if (!parsed)
                break;
            values.push(parsed.value);
            offset = parsed.next;
        }
        if (offset > 0)
            this.buffer = this.buffer.subarray(offset);
        return values;
    }
    parseAt(offset) {
        if (offset >= this.buffer.length)
            return null;
        const prefix = this.buffer[offset];
        const line = this.readLine(offset + 1);
        if ((prefix === 43 || prefix === 45 || prefix === 58) && !line)
            return null;
        switch (prefix) {
            case 43:
                return { value: line.text, next: line.next };
            case 45:
                return { value: new RespError(line.text), next: line.next };
            case 58:
                return { value: Number(line.text), next: line.next };
            case 36: {
                if (!line)
                    return null;
                const len = Number(line.text);
                if (len === -1)
                    return { value: null, next: line.next };
                const end = line.next + len;
                if (this.buffer.length < end + 2)
                    return null;
                if (this.buffer[end] !== 13 || this.buffer[end + 1] !== 10) {
                    throw new Error("invalid RESP bulk terminator");
                }
                return { value: this.buffer.subarray(line.next, end), next: end + 2 };
            }
            case 42: {
                if (!line)
                    return null;
                const count = Number(line.text);
                if (count === -1)
                    return { value: null, next: line.next };
                const out = [];
                let next = line.next;
                for (let i = 0; i < count; i++) {
                    const item = this.parseAt(next);
                    if (!item)
                        return null;
                    out.push(item.value);
                    next = item.next;
                }
                return { value: out, next };
            }
            default:
                throw new Error(`unsupported RESP prefix: ${String.fromCharCode(prefix)}`);
        }
    }
    readLine(offset) {
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
