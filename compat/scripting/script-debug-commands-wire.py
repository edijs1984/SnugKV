#!/usr/bin/env python3
import os
import socket
import time

HOST = os.environ.get("REDIS_HOST", "127.0.0.1")
PORT = int(os.environ.get("REDIS_PORT", "6390"))

def resp(*parts: str) -> bytes:
    out = [("*%d\r\n" % len(parts)).encode()]
    for part in parts:
        b = part.encode()
        out.append(("$%d\r\n" % len(b)).encode())
        out.append(b + b"\r\n")
    return b"".join(out)

def recv_window(sock, label, idle=0.15, total=2.0):
    sock.setblocking(False)
    deadline = time.time() + total
    last = time.time()
    chunks = []
    while time.time() < deadline:
        try:
            data = sock.recv(65536)
            if not data:
                break
            chunks.append(data)
            last = time.time()
        except BlockingIOError:
            if chunks and time.time() - last >= idle:
                break
            time.sleep(0.01)
    raw = b"".join(chunks)
    print("--- %s (%d bytes) ---" % (label, len(raw)))
    print(repr(raw))
    print(raw.decode("utf-8", "replace"))
    return raw

def session(label, script, commands):
    print("\n=== %s ===" % label)
    with socket.create_connection((HOST, PORT), timeout=3) as s:
        s.sendall(resp("SCRIPT", "DEBUG", "YES"))
        recv_window(s, "SCRIPT DEBUG YES")
        s.sendall(resp("EVAL", script, "0"))
        recv_window(s, "initial stop")
        for cmd in commands:
            print(">>> %r" % (cmd,))
            s.sendall(resp(*cmd))
            recv_window(s, " ".join(cmd), idle=0.25, total=3.0)

step_script = """local a = 1
local b = 2
local c = a + b
return c
"""

break_script = """local a = 1
local b = 2
local c = a + b
return c
"""

redis_debug_script = """redis.debug('hello', 123)
return 'done'
"""

redis_break_script = """local a = 1
redis.breakpoint()
local b = 2
return a + b
"""

session("step commands", step_script, [
    ("S",),
    ("S",),
    ("N",),
    ("C",),
])

session("breakpoint commands", break_script, [
    ("B", "3"),
    ("C",),
    ("C",),
])

session("source/list command", step_script, [
    ("L",),
    ("C",),
])

session("stack/backtrace command", step_script, [
    ("T",),
    ("C",),
])

session("redis.debug output", redis_debug_script, [
    ("C",),
])

session("redis.breakpoint", redis_break_script, [
    ("C",),
    ("C",),
])

print("done")
