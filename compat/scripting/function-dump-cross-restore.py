#!/usr/bin/env python3
import os
import socket

HOST = os.environ.get("REDIS_HOST", "127.0.0.1")
REDIS_PORT = int(os.environ.get("REDIS_PORT", "6390"))
SNUG_PORT = int(os.environ.get("SNUG_PORT", "6380"))

LIB = """#!lua name=crosslib
redis.register_function{
  function_name='cross_echo',
  callback=function(keys, args) return {keys[1], args[1]} end,
  description='cross restore',
  flags={'no-writes'}
}
"""

def encode(parts):
    out = [f"*{len(parts)}\r\n".encode()]
    for part in parts:
        b = part if isinstance(part, bytes) else str(part).encode()
        out.append(f"$"+"{len(b)}\r\n")
        out[-1] = out[-1].encode()
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
    raise RuntimeError(f"unknown RESP prefix: {first!r}")

def cmd(sock, *parts):
    sock.sendall(encode(parts))
    return recv_resp(sock)

def expect(label, got, want):
    print(f"{label}: {got}")
    if got != want:
        raise AssertionError(f"{label}: got {got!r}, want {want!r}")

def get_dump(sock):
    reply = cmd(sock, "FUNCTION", "DUMP")
    if reply[0] != "bulk" or reply[1] is None:
        raise AssertionError(f"FUNCTION DUMP: {reply!r}")
    return reply[1]

def reset_and_load(sock):
    expect("FUNCTION FLUSH", cmd(sock, "FUNCTION", "FLUSH"), ("simple", b"OK"))
    loaded = cmd(sock, "FUNCTION", "LOAD", LIB)
    expect("FUNCTION LOAD", loaded, ("bulk", b"crosslib"))

def assert_function(sock, label):
    got = cmd(sock, "FCALL_RO", "cross_echo", "1", "key-one", "arg-one")
    want = ("array", [("bulk", b"key-one"), ("bulk", b"arg-one")])
    expect(label, got, want)

print(f"FUNCTION DUMP cross-restore Redis={HOST}:{REDIS_PORT} SnugKV={HOST}:{SNUG_PORT}")

with socket.create_connection((HOST, REDIS_PORT), timeout=3) as redis_sock, \
     socket.create_connection((HOST, SNUG_PORT), timeout=3) as snug_sock:

    print("\n=== Redis -> SnugKV ===")
    reset_and_load(redis_sock)
    redis_dump = get_dump(redis_sock)
    print(f"Redis dump bytes: {len(redis_dump)}")

    expect("SnugKV flush", cmd(snug_sock, "FUNCTION", "FLUSH"), ("simple", b"OK"))
    expect("SnugKV restore Redis payload", cmd(snug_sock, "FUNCTION", "RESTORE", redis_dump, "FLUSH"), ("simple", b"OK"))
    assert_function(snug_sock, "SnugKV FCALL_RO after Redis restore")

    print("\n=== SnugKV -> Redis ===")
    reset_and_load(snug_sock)
    snug_dump = get_dump(snug_sock)
    print(f"SnugKV dump bytes: {len(snug_dump)}")

    expect("Redis flush", cmd(redis_sock, "FUNCTION", "FLUSH"), ("simple", b"OK"))
    expect("Redis restore SnugKV payload", cmd(redis_sock, "FUNCTION", "RESTORE", snug_dump, "FLUSH"), ("simple", b"OK"))
    assert_function(redis_sock, "Redis FCALL_RO after SnugKV restore")

    print("\n=== Payload comparison ===")
    print(f"Redis len={len(redis_dump)} SnugKV len={len(snug_dump)}")
    print(f"byte-identical={redis_dump == snug_dump}")

print("\nCROSS-RESTORE PASS")
