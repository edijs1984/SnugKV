#!/usr/bin/env python3
import os
import socket

HOST = os.environ.get("REDIS_HOST", "127.0.0.1")
REDIS_PORT = int(os.environ.get("REDIS_PORT", "6390"))
SNUG_PORT = int(os.environ.get("SNUG_PORT", "6380"))

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

def setup_case(sock, name):
    if name == "hash":
        expect("setup hash", cmd(sock, "HSET", "src", "a", "1", "b", "two", "c", "three"), ("integer", 3))
        return ("HGETALL", "dst")
    if name == "set-strings":
        expect("setup set strings", cmd(sock, "SADD", "src", "alpha", "beta", "gamma"), ("integer", 3))
        return ("SMEMBERS", "dst")
    if name == "set-ints":
        expect("setup set ints", cmd(sock, "SADD", "src", "1", "2", "3", "1000"), ("integer", 4))
        return ("SMEMBERS", "dst")
    if name == "list":
        expect("setup list", cmd(sock, "RPUSH", "src", "one", "two", "three", "four"), ("integer", 4))
        return ("LRANGE", "dst", "0", "-1")
    if name == "zset":
        expect("setup zset", cmd(sock, "ZADD", "src", "1.5", "one", "2", "two", "-3.25", "three"), ("integer", 3))
        return ("ZRANGE", "dst", "0", "-1", "WITHSCORES")
    if name == "hll":
        expect("setup hll", cmd(sock, "PFADD", "src", "alice", "bob", "carol"), ("integer", 1))
        return ("PFCOUNT", "dst")
    raise AssertionError(name)

CASES = ["hash", "set-strings", "set-ints", "list", "zset", "hll"]

print("KEY DUMP native cross-restore Redis=" + HOST + ":" + str(REDIS_PORT) +
      " SnugKV=" + HOST + ":" + str(SNUG_PORT))

with socket.create_connection((HOST, REDIS_PORT), timeout=3) as redis_sock, \
     socket.create_connection((HOST, SNUG_PORT), timeout=3) as snug_sock:

    for name in CASES:
        print("\n=== " + name + " Redis -> SnugKV ===")
        expect("Redis FLUSHDB", cmd(redis_sock, "FLUSHDB"), ("simple", b"OK"))
        expect("SnugKV FLUSHDB", cmd(snug_sock, "FLUSHDB"), ("simple", b"OK"))
        check = setup_case(redis_sock, name)
        redis_dump = dump(redis_sock, "src")
        expect("SnugKV RESTORE", cmd(snug_sock, "RESTORE", "dst", "0", redis_dump), ("simple", b"OK"))
        redis_check = cmd(redis_sock, *tuple(["RENAME", "src", "dst"])) if False else None
        snug_check = cmd(snug_sock, *check)
        print("SnugKV check:", snug_check)

        print("\n=== " + name + " SnugKV -> Redis ===")
        expect("Redis FLUSHDB", cmd(redis_sock, "FLUSHDB"), ("simple", b"OK"))
        expect("SnugKV FLUSHDB", cmd(snug_sock, "FLUSHDB"), ("simple", b"OK"))
        check2 = setup_case(snug_sock, name)
        snug_dump = dump(snug_sock, "src")
        expect("Redis RESTORE", cmd(redis_sock, "RESTORE", "dst", "0", snug_dump), ("simple", b"OK"))
        redis_check = cmd(redis_sock, *check2)
        print("Redis check:", redis_check)

        print("Redis dump len=" + str(len(redis_dump)) +
              " SnugKV dump len=" + str(len(snug_dump)) +
              " byte-identical=" + str(redis_dump == snug_dump))
        if redis_dump != snug_dump:
            print("Redis hex=" + redis_dump.hex())
            print("SnugKV hex=" + snug_dump.hex())
            raise AssertionError(name + " payload mismatch")

print("\nNATIVE CROSS-RESTORE PASS")
