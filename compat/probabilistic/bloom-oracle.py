#!/usr/bin/env python3
import os
import socket

HOST = os.environ.get("REDIS_HOST", "127.0.0.1")
PORT = int(os.environ.get("REDIS_PORT", "6392"))
TARGET = os.environ.get("TARGET_NAME", "redis")

def encode(args):
    out = [f"*{len(args)}\r\n".encode()]
    for arg in args:
        if isinstance(arg, str):
            arg = arg.encode()
        out.append(f"${len(arg)}\r\n".encode())
        out.append(arg)
        out.append(b"\r\n")
    return b"".join(out)

def read(sock):
    def line():
        buf = b""
        while not buf.endswith(b"\r\n"):
            chunk = sock.recv(1)
            if not chunk:
                raise EOFError
            buf += chunk
        return buf[:-2]

    prefix = sock.recv(1)
    if prefix in (b"+", b"-", b":"):
        return prefix.decode() + line().decode(errors="replace")
    if prefix == b"$":
        n = int(line())
        if n < 0:
            return None
        data = b""
        while len(data) < n:
            data += sock.recv(n - len(data))
        sock.recv(2)
        return data.decode(errors="replace")
    if prefix == b"*":
        n = int(line())
        if n < 0:
            return None
        return [read(sock) for _ in range(n)]
    if prefix == b"_":
        line()
        return None
    if prefix == b"%":
        n = int(line())
        return [read(sock) for _ in range(n * 2)]
    raise RuntimeError(f"unsupported RESP prefix: {prefix!r}")

def run(sock, *args):
    pretty = " ".join(str(a) for a in args)
    print(f"> {pretty}")
    sock.sendall(encode(args))
    reply = read(sock)
    print(repr(reply))
    return reply

print(f"target={TARGET} port={PORT}")
with socket.create_connection((HOST, PORT)) as s:
    run(s, "FLUSHDB")

    print("\n=== reserve/add/exists ===")
    run(s, "BF.RESERVE", "bf", "0.01", "100")
    run(s, "BF.ADD", "bf", "alice")
    run(s, "BF.ADD", "bf", "alice")
    run(s, "BF.EXISTS", "bf", "alice")
    run(s, "BF.EXISTS", "bf", "missing")

    print("\n=== multi ===")
    run(s, "BF.MADD", "bf", "bob", "carol", "dave")
    run(s, "BF.MEXISTS", "bf", "alice", "bob", "missing")
    run(s, "BF.CARD", "bf")

    print("\n=== info ===")
    run(s, "BF.INFO", "bf")
    run(s, "BF.INFO", "bf", "CAPACITY")
    run(s, "BF.INFO", "bf", "SIZE")
    run(s, "BF.INFO", "bf", "FILTERS")
    run(s, "BF.INFO", "bf", "ITEMS")
    run(s, "BF.INFO", "bf", "EXPANSION")

    print("\n=== autocreate ===")
    run(s, "BF.ADD", "auto", "x")
    run(s, "BF.EXISTS", "auto", "x")
    run(s, "BF.CARD", "auto")

    print("\n=== insert ===")
    run(s, "BF.INSERT", "inserted", "CAPACITY", "10", "ERROR", "0.01", "ITEMS", "a", "b", "c")
    run(s, "BF.MEXISTS", "inserted", "a", "b", "z")
    run(s, "BF.CARD", "inserted")

    print("\n=== invalid ===")
    run(s, "BF.RESERVE", "baderr", "0", "100")
    run(s, "BF.RESERVE", "badcap", "0.01", "0")
    run(s, "BF.ADD", "bf")
    run(s, "BF.MADD", "bf")
    run(s, "BF.MEXISTS", "bf")
    run(s, "BF.INFO", "missing")
    run(s, "SET", "plain", "value")
    run(s, "BF.ADD", "plain", "x")
    run(s, "BF.EXISTS", "plain", "x")

    print("\n=== cleanup ===")
    run(s, "FLUSHDB")
