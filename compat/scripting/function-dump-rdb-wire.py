#!/usr/bin/env python3
import base64
import hashlib
import os
import socket
import struct

HOST = os.environ.get("REDIS_HOST", "127.0.0.1")
PORT = int(os.environ.get("REDIS_PORT", "6390"))

LIB_A = """#!lua name=lib_a
redis.register_function{
  function_name='echo_a',
  callback=function(keys, args) return args[1] end,
  description='alpha function',
  flags={'no-writes'}
}
"""

LIB_B = """#!lua name=lib_b
redis.register_function('echo_b', function(keys, args)
  return {keys[1], args[1]}
end)
"""

def encode(parts):
    out = [f"*{len(parts)}\r\n".encode()]
    for part in parts:
        if isinstance(part, bytes):
            b = part
        else:
            b = str(part).encode()
        out.append(f"${len(b)}\r\n".encode())
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

def show(label, reply):
    kind, value = reply
    if kind == "bulk" and value is not None:
        print(f"{label}: bulk len={len(value)}")
        print(f"  sha256={hashlib.sha256(value).hexdigest()}")
        print(f"  hex={value.hex()}")
        print(f"  base64={base64.b64encode(value).decode()}")
    else:
        print(f"{label}: {reply}")

def require_ok(reply):
    assert reply == ("simple", b"OK"), reply

print(f"FUNCTION DUMP/RESTORE oracle {HOST}:{PORT}")

with socket.create_connection((HOST, PORT), timeout=3) as s:
    # Empty registry baseline.
    require_ok(cmd(s, "FUNCTION", "FLUSH"))
    empty_dump = cmd(s, "FUNCTION", "DUMP")
    show("empty dump", empty_dump)

    # One library with metadata/flags.
    show("load lib_a", cmd(s, "FUNCTION", "LOAD", LIB_A))
    one_dump = cmd(s, "FUNCTION", "DUMP")
    show("one-library dump", one_dump)

    # Two libraries to expose ordering/container structure.
    show("load lib_b", cmd(s, "FUNCTION", "LOAD", LIB_B))
    two_dump = cmd(s, "FUNCTION", "DUMP")
    show("two-library dump", two_dump)

    payload = two_dump[1]
    assert isinstance(payload, (bytes, bytearray))

    # Default APPEND restore into empty registry.
    require_ok(cmd(s, "FUNCTION", "FLUSH"))
    show("restore default append into empty", cmd(s, "FUNCTION", "RESTORE", payload))
    show("list after append restore", cmd(s, "FUNCTION", "LIST", "WITHCODE"))

    # APPEND collision.
    show("restore append collision", cmd(s, "FUNCTION", "RESTORE", payload, "APPEND"))

    # REPLACE collision behavior.
    show("restore replace collision", cmd(s, "FUNCTION", "RESTORE", payload, "REPLACE"))
    show("list after replace", cmd(s, "FUNCTION", "LIST", "WITHCODE"))

    # FLUSH behavior with an unrelated existing library.
    require_ok(cmd(s, "FUNCTION", "FLUSH"))
    other = """#!lua name=other
redis.register_function('other_fn', function(keys,args) return 1 end)
"""
    show("load unrelated", cmd(s, "FUNCTION", "LOAD", other))
    show("restore flush", cmd(s, "FUNCTION", "RESTORE", payload, "FLUSH"))
    show("list after flush restore", cmd(s, "FUNCTION", "LIST", "WITHCODE"))

    # Corruption matrix.
    variants = {}

    if len(payload) > 0:
        x = bytearray(payload)
        x[len(x)//2] ^= 0x01
        variants["middle-byte corruption"] = bytes(x)

    if len(payload) >= 8:
        x = bytearray(payload)
        x[-1] ^= 0x01
        variants["checksum-tail corruption"] = bytes(x)

    if len(payload) >= 10:
        x = bytearray(payload)
        # Redis DUMP-style payloads conventionally end with 2-byte RDB version + 8-byte checksum.
        x[-10] ^= 0x01
        variants["version-byte corruption"] = bytes(x)

    variants["truncated by 1"] = payload[:-1]
    variants["truncated by 10"] = payload[:-10] if len(payload) >= 10 else b""

    for label, bad in variants.items():
        show(label, cmd(s, "FUNCTION", "RESTORE", bad, "FLUSH"))

    # Syntax / arity.
    show("restore bad policy", cmd(s, "FUNCTION", "RESTORE", payload, "NOPE"))
    show("restore missing payload", cmd(s, "FUNCTION", "RESTORE"))
    show("dump extra arg", cmd(s, "FUNCTION", "DUMP", "extra"))

print("done")
