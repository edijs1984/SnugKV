#!/usr/bin/env python3
import os
import socket

HOST=os.environ.get("REDIS_HOST","127.0.0.1")
PORT=int(os.environ.get("REDIS_PORT","6392"))
TARGET=os.environ.get("TARGET_NAME","redis")

def enc(args):
    out=[f"*{len(args)}\r\n".encode()]
    for a in args:
        if isinstance(a,str): a=a.encode()
        out += [f"${len(a)}\r\n".encode(),a,b"\r\n"]
    return b"".join(out)

def read(s):
    def line():
        b=b""
        while not b.endswith(b"\r\n"): b+=s.recv(1)
        return b[:-2]
    p=s.recv(1)
    if p in (b"+",b"-",b":"): return p.decode()+line().decode(errors="replace")
    if p==b"$":
        n=int(line())
        if n<0:return None
        d=b""
        while len(d)<n:d+=s.recv(n-len(d))
        s.recv(2);return d.decode(errors="replace")
    if p==b"*":
        n=int(line())
        if n<0:return None
        return [read(s) for _ in range(n)]
    raise RuntimeError(p)

def run(s,*args):
    print("> "+" ".join(str(x) for x in args))
    s.sendall(enc(args)); r=read(s); print(repr(r)); return r

print(f"target={TARGET} port={PORT}")
with socket.create_connection((HOST,PORT)) as s:
    run(s,"FLUSHDB")

    print("\n=== scalable default expansion ===")
    run(s,"BF.RESERVE","scale","0.01","2")
    for i in range(1,9):
        run(s,"BF.ADD","scale",f"v{i}")
        run(s,"BF.INFO","scale")

    print("\n=== explicit expansion 3 ===")
    run(s,"BF.RESERVE","scale3","0.01","2","EXPANSION","3")
    for i in range(1,10):
        run(s,"BF.ADD","scale3",f"x{i}")
    run(s,"BF.INFO","scale3")

    print("\n=== nonscaling ===")
    run(s,"BF.RESERVE","fixed","0.01","2","NONSCALING")
    run(s,"BF.ADD","fixed","a")
    run(s,"BF.ADD","fixed","b")
    run(s,"BF.ADD","fixed","c")
    run(s,"BF.CARD","fixed")
    run(s,"BF.INFO","fixed")

    print("\n=== insert modifiers ===")
    run(s,"BF.INSERT","ins","CAPACITY","2","EXPANSION","3","ITEMS","a","b","c","d","e")
    run(s,"BF.INFO","ins")
    run(s,"BF.INSERT","ins2","CAPACITY","2","NONSCALING","ITEMS","a","b","c")
    run(s,"BF.INFO","ins2")

    print("\n=== errors ===")
    run(s,"BF.RESERVE","badexp0","0.01","2","EXPANSION","0")
    run(s,"BF.RESERVE","badexpneg","0.01","2","EXPANSION","-1")
    run(s,"BF.RESERVE","badopt","0.01","2","BOGUS")
    run(s,"BF.RESERVE","dupopts","0.01","2","EXPANSION","2","EXPANSION","3")
    run(s,"BF.INSERT","badinsert","CAPACITY","2","EXPANSION","0","ITEMS","a")
    run(s,"BF.INSERT","badinsert2","CAPACITY","2","NONSCALING","EXPANSION","2","ITEMS","a")

    print("\n=== cleanup ===")
    run(s,"FLUSHDB")
