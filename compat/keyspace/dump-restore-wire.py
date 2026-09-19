#!/usr/bin/env python3
import base64
import hashlib
import os
import socket
import time

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
            chunk = sock.recv(n - len(data))
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

def bulk_bytes(reply):
    if reply[0] != "bulk" or reply[1] is None:
        raise AssertionError("expected bulk reply, got " + repr(reply))
    return reply[1]

def show_payload(label, payload):
    print(label + ": bulk len=" + str(len(payload)))
    print("  sha256=" + hashlib.sha256(payload).hexdigest())
    print("  hex=" + payload.hex())
    print("  base64=" + base64.b64encode(payload).decode())

def expect(label, got, want):
    print(label + ": " + repr(got))
    if got != want:
        raise AssertionError(label + ": got " + repr(got) + ", want " + repr(want))

print("KEY DUMP/RESTORE oracle " + HOST + ":" + str(PORT))

with socket.create_connection((HOST, PORT), timeout=3) as s:
    expect("flushdb", cmd(s, "FLUSHDB"), ("simple", b"OK"))

    print("\n=== Missing key ===")
    print("dump missing:", cmd(s, "DUMP", "missing"))

    print("\n=== Plain STRING ===")
    expect("set plain", cmd(s, "SET", "plain", "hello"), ("simple", b"OK"))
    plain = bulk_bytes(cmd(s, "DUMP", "plain"))
    show_payload("plain dump", plain)

    print("\n=== Integer-encodable STRING ===")
    expect("set integer", cmd(s, "SET", "integer", "123"), ("simple", b"OK"))
    integer = bulk_bytes(cmd(s, "DUMP", "integer"))
    show_payload("integer dump", integer)

    print("\n=== Compressible STRING ===")
    compressible_value = (b"abc123-" * 64) + b"tail"
    expect("set compressible", cmd(s, "SET", "compressible", compressible_value), ("simple", b"OK"))
    compressible = bulk_bytes(cmd(s, "DUMP", "compressible"))
    show_payload("compressible dump", compressible)

    print("\n=== Binary STRING ===")
    binary_value = bytes([0, 1, 2, 3, 10, 13, 255, 128]) + b"binary\x00value"
    expect("set binary", cmd(s, "SET", b"binary-key", binary_value), ("simple", b"OK"))
    binary_dump = bulk_bytes(cmd(s, "DUMP", b"binary-key"))
    show_payload("binary dump", binary_dump)

    print("\n=== DUMP excludes TTL ===")
    expect("set ttl-copy", cmd(s, "SET", "ttl-copy", "hello"), ("simple", b"OK"))
    before_ttl = bulk_bytes(cmd(s, "DUMP", "ttl-copy"))
    expect("pexpire ttl-copy", cmd(s, "PEXPIRE", "ttl-copy", "60000"), ("integer", 1))
    after_ttl = bulk_bytes(cmd(s, "DUMP", "ttl-copy"))
    print("ttl payload identical:", before_ttl == after_ttl)
    print("plain vs ttl-copy identical:", plain == after_ttl)

    print("\n=== RESTORE relative TTL ===")
    expect("del restored", cmd(s, "DEL", "restored"), ("integer", 0))
    expect("restore ttl=0", cmd(s, "RESTORE", "restored", "0", plain), ("simple", b"OK"))
    print("get restored:", cmd(s, "GET", "restored"))
    print("pttl restored:", cmd(s, "PTTL", "restored"))

    expect("del restored-ttl", cmd(s, "DEL", "restored-ttl"), ("integer", 0))
    expect("restore ttl=5000", cmd(s, "RESTORE", "restored-ttl", "5000", plain), ("simple", b"OK"))
    print("get restored-ttl:", cmd(s, "GET", "restored-ttl"))
    print("pttl restored-ttl:", cmd(s, "PTTL", "restored-ttl"))

    print("\n=== RESTORE collision / REPLACE ===")
    expect("set collision", cmd(s, "SET", "collision", "old"), ("simple", b"OK"))
    print("restore collision:", cmd(s, "RESTORE", "collision", "0", plain))
    print("restore replace:", cmd(s, "RESTORE", "collision", "0", plain, "REPLACE"))
    print("get collision after replace:", cmd(s, "GET", "collision"))

    print("\n=== RESTORE ABSTTL ===")
    absttl_ms = int(time.time() * 1000) + 5000
    expect("del abs", cmd(s, "DEL", "abs"), ("integer", 0))
    print("restore absttl:", cmd(s, "RESTORE", "abs", str(absttl_ms), plain, "ABSTTL"))
    print("get abs:", cmd(s, "GET", "abs"))
    print("pttl abs:", cmd(s, "PTTL", "abs"))

    print("\n=== RESTORE metadata options ===")
    expect("del meta", cmd(s, "DEL", "meta"), ("integer", 0))
    print("restore idletime:", cmd(s, "RESTORE", "meta", "0", plain, "IDLETIME", "7"))
    print("object idletime:", cmd(s, "OBJECT", "IDLETIME", "meta"))
    expect("del meta", cmd(s, "DEL", "meta"), ("integer", 1))
    print("restore freq:", cmd(s, "RESTORE", "meta", "0", plain, "FREQ", "42"))
    print("object freq:", cmd(s, "OBJECT", "FREQ", "meta"))

    print("\n=== Corruption matrix ===")
    variants = []
    middle = bytearray(plain)
    middle[len(middle)//2] ^= 1
    variants.append(("middle-byte corruption", bytes(middle)))

    tail = bytearray(plain)
    tail[-1] ^= 1
    variants.append(("checksum-tail corruption", bytes(tail)))

    version = bytearray(plain)
    version[-10] ^= 1
    variants.append(("version-byte corruption", bytes(version)))

    variants.append(("truncated by 1", plain[:-1]))
    variants.append(("truncated by 10", plain[:-10]))

    for label, payload in variants:
        cmd(s, "DEL", "bad")
        print(label + ":", cmd(s, "RESTORE", "bad", "0", payload))

    print("\n=== Syntax / validation ===")
    print("restore negative ttl:", cmd(s, "RESTORE", "neg", "-1", plain))
    print("restore invalid ttl:", cmd(s, "RESTORE", "badttl", "nope", plain))
    print("restore bad option:", cmd(s, "RESTORE", "badopt", "0", plain, "NOPE"))
    print("restore idletime 0:", cmd(s, "RESTORE", "badidle0", "0", plain, "IDLETIME", "0"))
    print("restore idletime negative:", cmd(s, "RESTORE", "badidle", "0", plain, "IDLETIME", "-1"))
    print("restore freq 256:", cmd(s, "RESTORE", "badfreq", "0", plain, "FREQ", "256"))
    print("restore freq negative:", cmd(s, "RESTORE", "badfreqneg", "0", plain, "FREQ", "-1"))
    print("dump extra arg:", cmd(s, "DUMP", "plain", "extra"))
    print("restore missing args:", cmd(s, "RESTORE", "x"))
    print("restore extra arg:", cmd(s, "RESTORE", "x", "0", plain, "REPLACE", "extra"))

print("done")
