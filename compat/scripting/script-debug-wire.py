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

script = """redis.call('SET','debug:wire','x')
redis.call('INCR','debug:wire:counter')
return redis.call('GET','debug:wire')
"""

with socket.create_connection((HOST, PORT), timeout=3) as s:
    print("connected %s:%d" % (HOST, PORT))

    s.sendall(resp("DEL", "debug:wire", "debug:wire:counter"))
    recv_window(s, "DEL")

    s.sendall(resp("SCRIPT", "DEBUG", "YES"))
    recv_window(s, "SCRIPT DEBUG YES")

    s.sendall(resp("EVAL", script, "0"))
    recv_window(s, "EVAL initial debugger reply")

    print(">>> debugger command 'C'")
    s.sendall(resp("C"))
    recv_window(s, "debugger C", idle=0.25, total=3.0)

with socket.create_connection((HOST, PORT), timeout=3) as check:
    check.sendall(resp("EXISTS", "debug:wire"))
    recv_window(check, "EXISTS after async session")

print("done")


print("\n=== sync session ===")
with socket.create_connection((HOST, PORT), timeout=3) as s:
    s.sendall(resp("DEL", "debug:wire", "debug:wire:counter"))
    recv_window(s, "SYNC DEL")

    s.sendall(resp("SCRIPT", "DEBUG", "SYNC"))
    recv_window(s, "SCRIPT DEBUG SYNC")

    s.sendall(resp("EVAL", script, "0"))
    recv_window(s, "SYNC EVAL initial debugger reply")

    print(">>> debugger command 'C'")
    s.sendall(resp("C"))
    recv_window(s, "SYNC debugger C", idle=0.25, total=3.0)

with socket.create_connection((HOST, PORT), timeout=3) as check:
    check.sendall(resp("GET", "debug:wire"))
    recv_window(check, "GET after sync session")
    check.sendall(resp("GET", "debug:wire:counter"))
    recv_window(check, "counter after sync session")
