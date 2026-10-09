export declare class RespError extends Error {
    /** First word of the error reply, e.g. "ERR", "WRONGTYPE", "EXECABORT". */
    readonly code: string;
    constructor(message: string);
}
export type RespValue = string | number | Buffer | null | RespError | RespValue[];
export type RespCommandArg = string | Buffer | number;
export declare function encodedCommandLength(args: readonly RespCommandArg[]): number;
export declare function encodeCommand(args: readonly RespCommandArg[]): Buffer;
export declare function encodeCommands(commands: readonly (readonly RespCommandArg[])[]): Buffer;
export declare class RespDecoder {
    private buffer;
    push(chunk: Buffer): RespValue[];
    private parseAt;
    private readLine;
}
