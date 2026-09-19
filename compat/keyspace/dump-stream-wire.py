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
    raise RuntimeError("unknown RESP prefix " + repr(first))

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
        raise AssertionError("DUMP failed: " + repr(r))
    return r[1]

def show(label, payload):
    print(label + ": len=" + str(len(payload)))
    print("  type-byte=" + str(payload[0]))
    print("  sha256=" + hashlib.sha256(payload).hexdigest())
    print("  hex=" + payload.hex())
    print("  base64=" + base64.b64encode(payload).decode())

print("STREAM DUMP oracle " + HOST + ":" + str(PORT))

with socket.create_connection((HOST, PORT), timeout=3) as s:
    expect("flushdb", cmd(s, "FLUSHDB"), ("simple", b"OK"))

    print("\n=== Entries only ===")
    expect("xadd 1000-0", cmd(s, "XADD", "st", "1000-0", "f1", "v1", "f2", "v2"), ("bulk", b"1000-0"))
    expect("xadd 1001-0", cmd(s, "XADD", "st", "1001-0", "f1", "v3"), ("bulk", b"1001-0"))
    p1 = dump(s, "st")
    show("entries-only dump", p1)
    print("xinfo stream:", cmd(s, "XINFO", "STREAM", "st"))
    print("xrange:", cmd(s, "XRANGE", "st", "-", "+"))

    print("\n=== Deleted-entry metadata ===")
    expect("xdel 1001-0", cmd(s, "XDEL", "st", "1001-0"), ("integer", 1))
    p2 = dump(s, "st")
    show("after-delete dump", p2)
    print("xinfo stream after delete:", cmd(s, "XINFO", "STREAM", "st"))
    print("xrange after delete:", cmd(s, "XRANGE", "st", "-", "+"))

    print("\n=== Consumer group, no pending ===")
    expect("xadd 1002-0", cmd(s, "XADD", "st", "1002-0", "f1", "v4"), ("bulk", b"1002-0"))
    expect("xgroup create", cmd(s, "XGROUP", "CREATE", "st", "g1", "1000-0"), ("simple", b"OK"))
    expect("xgroup createconsumer", cmd(s, "XGROUP", "CREATECONSUMER", "st", "g1", "c1"), ("integer", 1))
    p3 = dump(s, "st")
    show("group-no-pending dump", p3)
    print("xinfo groups:", cmd(s, "XINFO", "GROUPS", "st"))
    print("xinfo consumers:", cmd(s, "XINFO", "CONSUMERS", "st", "g1"))

    print("\n=== Consumer group with pending ===")
    r = cmd(s, "XREADGROUP", "GROUP", "g1", "c1", "COUNT", "1", "STREAMS", "st", ">")
    print("xreadgroup:", r)
    p4 = dump(s, "st")
    show("group-with-pending dump", p4)
    print("xpending summary:", cmd(s, "XPENDING", "st", "g1"))
    print("xinfo groups pending:", cmd(s, "XINFO", "GROUPS", "st"))
    print("xinfo consumers pending:", cmd(s, "XINFO", "CONSUMERS", "st", "g1"))

    print("\n=== Redis self-restore final payload ===")
    expect("restore copy", cmd(s, "RESTORE", "st2", "0", p4), ("simple", b"OK"))
    print("st2 xrange:", cmd(s, "XRANGE", "st2", "-", "+"))
    print("st2 xinfo stream:", cmd(s, "XINFO", "STREAM", "st2"))
    print("st2 xinfo groups:", cmd(s, "XINFO", "GROUPS", "st2"))
    print("st2 xpending:", cmd(s, "XPENDING", "st2", "g1"))
    print("st2 consumers:", cmd(s, "XINFO", "CONSUMERS", "st2", "g1"))

print("done")
