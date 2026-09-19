#!/usr/bin/env python3
import base64
import hashlib
import os
import socket

HOST = os.environ.get("REDIS_HOST", "127.0.0.1")
PORT = int(os.environ.get("REDIS_PORT", "6390"))

def encode(parts):
    out = [("*" + str(len(parts)) + "\r\n").encode()]
    for part in parts:
        b = part if isinstance(part, bytes) else str(part).encode()
        out.append(("$" + str(len(b)) + "\r\n").encode())
        out.append(b)
        out.append(b"\r\n")
    return b"".join(out)

def recv_resp(sock):
    def line():
        buf = bytearray()
        while True:
            b = sock.recv(1)
            if not b:
                raise EOFError("connection closed")
            buf += b
            if buf.endswith(b"\r\n"):
                return bytes(buf[:-2])

    first = sock.recv(1)
    if not first:
        raise EOFError("connection closed")
    if first == b"+":
        return ("simple", line())
    if first == b"-":
        return ("error", line())
    if first == b":":
        return ("integer", int(line()))
    if first == b"$":
        n = int(line())
        if n < 0:
            return ("bulk", None)
        data = b""
        while len(data) < n:
            chunk = sock.recv(n-len(data))
            if not chunk:
                raise EOFError("connection closed in bulk")
            data += chunk
        if sock.recv(2) != b"\r\n":
            raise RuntimeError("bad bulk terminator")
        return ("bulk", data)
    if first == b"*":
        n = int(line())
        if n < 0:
            return ("array", None)
        return ("array", [recv_resp(sock) for _ in range(n)])
    raise RuntimeError("unknown RESP prefix: " + repr(first))

def cmd(sock, *parts):
    sock.sendall(encode(parts))
    return recv_resp(sock)

def expect(label, got, want):
    print(label + ": " + repr(got))
    if got != want:
        raise AssertionError(label + ": got " + repr(got) + ", want " + repr(want))

def dump(sock, key):
    r = cmd(sock, "DUMP", key)
    if r[0] != "bulk" or r[1] is None:
        raise AssertionError("DUMP failed for " + str(key) + ": " + repr(r))
    return r[1]

def show(label, payload):
    print(label + ": len=" + str(len(payload)))
    print("  type-byte=" + str(payload[0]))
    print("  sha256=" + hashlib.sha256(payload).hexdigest())
    print("  hex=" + payload.hex())
    print("  base64=" + base64.b64encode(payload).decode())

print("KEY DUMP native-type oracle " + HOST + ":" + str(PORT))

with socket.create_connection((HOST, PORT), timeout=3) as s:
    expect("flushdb", cmd(s, "FLUSHDB"), ("simple", b"OK"))

    print("\n=== HASH ===")
    expect("hset", cmd(s, "HSET", "h", "a", "1", "b", "two", "c", "three"), ("integer", 3))
    show("hash dump", dump(s, "h"))

    print("\n=== SET strings ===")
    expect("sadd strings", cmd(s, "SADD", "ss", "alpha", "beta", "gamma"), ("integer", 3))
    show("set strings dump", dump(s, "ss"))

    print("\n=== SET integers ===")
    expect("sadd ints", cmd(s, "SADD", "si", "1", "2", "3", "1000"), ("integer", 4))
    show("set ints dump", dump(s, "si"))

    print("\n=== LIST ===")
    expect("rpush", cmd(s, "RPUSH", "l", "one", "two", "three", "four"), ("integer", 4))
    show("list dump", dump(s, "l"))

    print("\n=== ZSET ===")
    expect("zadd", cmd(s, "ZADD", "z", "1.5", "one", "2", "two", "-3.25", "three"), ("integer", 3))
    show("zset dump", dump(s, "z"))

    print("\n=== HLL ===")
    expect("pfadd", cmd(s, "PFADD", "hll", "alice", "bob", "carol"), ("integer", 1))
    hll_dump = dump(s, "hll")
    show("hll dump", hll_dump)
    print("hll type:", cmd(s, "TYPE", "hll"))
    print("hll strlen:", cmd(s, "STRLEN", "hll"))

    print("\n=== STREAM ===")
    x1 = cmd(s, "XADD", "st", "*", "f1", "v1", "f2", "v2")
    x2 = cmd(s, "XADD", "st", "*", "f1", "v3")
    print("xadd1:", x1)
    print("xadd2:", x2)
    show("stream dump", dump(s, "st"))

    print("\n=== Restore type round-trip inside Redis ===")
    for src, dst, check in [
        ("h", "h2", ("HGETALL", "h2")),
        ("ss", "ss2", ("SMEMBERS", "ss2")),
        ("si", "si2", ("SMEMBERS", "si2")),
        ("l", "l2", ("LRANGE", "l2", "0", "-1")),
        ("z", "z2", ("ZRANGE", "z2", "0", "-1", "WITHSCORES")),
        ("hll", "hll2", ("PFCOUNT", "hll2")),
        ("st", "st2", ("XRANGE", "st2", "-", "+")),
    ]:
        payload = dump(s, src)
        expect("restore " + dst, cmd(s, "RESTORE", dst, "0", payload), ("simple", b"OK"))
        print(dst + " check:", cmd(s, *check))

print("done")
