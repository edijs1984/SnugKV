#!/usr/bin/env python3
import os
import socket
import time

HOST = os.environ.get("REDIS_HOST", "127.0.0.1")
REDIS_PORT = int(os.environ.get("REDIS_PORT", "6390"))
SNUG_PORT = int(os.environ.get("SNUG_PORT", "6380"))

CASES = [
    ("plain", b"hello"),
    ("integer", b"123"),
    ("compressible", (b"abc123-" * 64) + b"tail"),
    ("binary", bytes([0,1,2,3,10,13,255,128]) + b"binary\x00value"),
]

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
        raise AssertionError("DUMP failed: " + repr(r))
    return r[1]

print("KEY DUMP/RESTORE cross-restore Redis=" + HOST + ":" + str(REDIS_PORT) +
      " SnugKV=" + HOST + ":" + str(SNUG_PORT))

with socket.create_connection((HOST, REDIS_PORT), timeout=3) as redis_sock, \
     socket.create_connection((HOST, SNUG_PORT), timeout=3) as snug_sock:

    expect("Redis FLUSHDB", cmd(redis_sock, "FLUSHDB"), ("simple", b"OK"))
    expect("SnugKV FLUSHDB", cmd(snug_sock, "FLUSHDB"), ("simple", b"OK"))

    for name, value in CASES:
        print("\n=== " + name + " ===")
        redis_key = ("redis:" + name).encode()
        snug_key = ("snug:" + name).encode()
        redis_restored = ("redis-restored:" + name).encode()
        snug_restored = ("snug-restored:" + name).encode()

        expect("Redis SET", cmd(redis_sock, "SET", redis_key, value), ("simple", b"OK"))
        redis_dump = dump(redis_sock, redis_key)

        expect("SnugKV RESTORE Redis payload",
               cmd(snug_sock, "RESTORE", snug_restored, "0", redis_dump),
               ("simple", b"OK"))
        expect("SnugKV GET after Redis restore",
               cmd(snug_sock, "GET", snug_restored),
               ("bulk", value))

        expect("SnugKV SET", cmd(snug_sock, "SET", snug_key, value), ("simple", b"OK"))
        snug_dump = dump(snug_sock, snug_key)

        expect("Redis RESTORE SnugKV payload",
               cmd(redis_sock, "RESTORE", redis_restored, "0", snug_dump),
               ("simple", b"OK"))
        expect("Redis GET after SnugKV restore",
               cmd(redis_sock, "GET", redis_restored),
               ("bulk", value))

        print("Redis dump len=" + str(len(redis_dump)) +
              " SnugKV dump len=" + str(len(snug_dump)) +
              " byte-identical=" + str(redis_dump == snug_dump))
        if redis_dump != snug_dump:
            raise AssertionError(name + " payload mismatch")

    print("\n=== TTL semantics ===")
    redis_ttl_dump = dump(redis_sock, b"redis:plain")
    expect("SnugKV RESTORE TTL",
           cmd(snug_sock, "RESTORE", "ttl-relative", "5000", redis_ttl_dump),
           ("simple", b"OK"))
    pttl = cmd(snug_sock, "PTTL", "ttl-relative")
    print("SnugKV relative PTTL:", pttl)
    if pttl[0] != "integer" or not 0 < pttl[1] <= 5000:
        raise AssertionError("bad relative TTL")

    absttl = int(time.time() * 1000) + 5000
    expect("SnugKV RESTORE ABSTTL",
           cmd(snug_sock, "RESTORE", "ttl-absolute", str(absttl), redis_ttl_dump, "ABSTTL"),
           ("simple", b"OK"))
    pttl = cmd(snug_sock, "PTTL", "ttl-absolute")
    print("SnugKV absolute PTTL:", pttl)
    if pttl[0] != "integer" or not 0 < pttl[1] <= 5000:
        raise AssertionError("bad absolute TTL")

    print("\n=== BUSYKEY / REPLACE ===")
    expect("SnugKV SET collision", cmd(snug_sock, "SET", "collision", "old"), ("simple", b"OK"))
    expect("SnugKV BUSYKEY",
           cmd(snug_sock, "RESTORE", "collision", "0", redis_ttl_dump),
           ("error", b"BUSYKEY Target key name already exists."))
    expect("SnugKV REPLACE",
           cmd(snug_sock, "RESTORE", "collision", "0", redis_ttl_dump, "REPLACE"),
           ("simple", b"OK"))
    expect("SnugKV collision value", cmd(snug_sock, "GET", "collision"), ("bulk", b"hello"))

print("\nCROSS-RESTORE PASS")
