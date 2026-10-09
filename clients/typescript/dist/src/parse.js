export function asInteger(value, what) {
    if (typeof value !== "number")
        throw new Error(`unexpected ${what} reply`);
    return value;
}
export function asOk(value, what) {
    if (value !== "OK")
        throw new Error(`unexpected ${what} reply: ${String(value)}`);
    return "OK";
}
export function asBufferOrNull(value, what) {
    if (value === null || Buffer.isBuffer(value))
        return value;
    throw new Error(`unexpected ${what} reply`);
}
export function asBuffer(value, what) {
    if (Buffer.isBuffer(value))
        return value;
    throw new Error(`unexpected ${what} reply`);
}
export function asBufferArray(value, what) {
    if (!Array.isArray(value) || value.some((item) => !Buffer.isBuffer(item))) {
        throw new Error(`unexpected ${what} reply`);
    }
    return value;
}
export function asArray(value, what) {
    if (!Array.isArray(value))
        throw new Error(`unexpected ${what} reply`);
    return value;
}
export function asText(value, what) {
    if (typeof value === "string")
        return value;
    if (Buffer.isBuffer(value))
        return value.toString();
    throw new Error(`unexpected ${what} reply`);
}
export function asNumber(value, what) {
    if (typeof value === "number")
        return value;
    const n = Number(asText(value, what));
    if (!Number.isFinite(n))
        throw new Error(`unexpected ${what} numeric reply`);
    return n;
}
export function asHash(value, what) {
    const items = asArray(value, what);
    if (items.length % 2 !== 0)
        throw new Error(`unexpected ${what} reply`);
    const out = {};
    for (let i = 0; i < items.length; i += 2) {
        out[asBuffer(items[i], what).toString()] = asBuffer(items[i + 1], what);
    }
    return out;
}
