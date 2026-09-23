#!/usr/bin/env python3
import os
import socket

HOST=os.environ.get("REDIS_HOST","127.0.0.1")
PORT=int(os.environ.get("REDIS_PORT","6392"))
TARGET=os.environ.get("TARGET_NAME","redis")

def enc(args):
    out=[f"*{len(args)}\r\n".encode()]
    for a in args:
        if isinstance(a,str):
            a=a.encode()
        out += [f"${len(a)}\r\n".encode(),a,b"\r\n"]
    return b"".join(out)

def read(s):
    def line():
        b=b""
        while not b.endswith(b"\r\n"):
            b+=s.recv(1)
        return b[:-2]
    p=s.recv(1)
    if p in (b"+",b"-",b":"):
        return p.decode()+line().decode(errors="replace")
    if p==b"$":
        n=int(line())
        if n<0:return None
        d=b""
        while len(d)<n:
            d+=s.recv(n-len(d))
        s.recv(2)
        return d.decode(errors="replace")
    if p==b"*":
        n=int(line())
        if n<0:return None
        return [read(s) for _ in range(n)]
    raise RuntimeError(p)

def run(s,*args):
    print("> "+" ".join(str(x) for x in args))
    s.sendall(enc(args))
    r=read(s)
    print(repr(r))
    return r

print(f"target={TARGET} port={PORT}")
with socket.create_connection((HOST,PORT)) as s:
    run(s,"FLUSHDB")

    print("\n=== reserve/info ===")
    run(s,"TOPK.RESERVE","tk","3")
    run(s,"TOPK.INFO","tk")
    run(s,"TOPK.RESERVE","custom","3","50","5","0.9")
    run(s,"TOPK.INFO","custom")

    print("\n=== add/query/count/list ===")
    run(s,"TOPK.ADD","custom","a","b","c")
    run(s,"TOPK.QUERY","custom","a","b","c","z")
    run(s,"TOPK.COUNT","custom","a","b","c","z")
    run(s,"TOPK.LIST","custom")
    run(s,"TOPK.LIST","custom","WITHCOUNT")

    print("\n=== incrby/ejection ===")
    run(s,"TOPK.RESERVE","rank","2","100","5","0.9")
    run(s,"TOPK.INCRBY","rank","a","10","b","20")
    run(s,"TOPK.LIST","rank","WITHCOUNT")
    run(s,"TOPK.INCRBY","rank","c","30")
    run(s,"TOPK.QUERY","rank","a","b","c")
    run(s,"TOPK.COUNT","rank","a","b","c")
    run(s,"TOPK.LIST","rank","WITHCOUNT")

    print("\n=== zero increment ===")
    run(s,"TOPK.RESERVE","zero","2","100","5","0.9")
    run(s,"TOPK.INCRBY","zero","x","0")
    run(s,"TOPK.QUERY","zero","x")
    run(s,"TOPK.COUNT","zero","x")
    run(s,"TOPK.LIST","zero","WITHCOUNT")

    print("\n=== invalid/missing/wrong type ===")
    run(s,"TOPK.RESERVE","badk","0")
    run(s,"TOPK.RESERVE","badw","2","0","5","0.9")
    run(s,"TOPK.RESERVE","badd","2","10","0","0.9")
    run(s,"TOPK.RESERVE","baddecay0","2","10","5","0")
    run(s,"TOPK.RESERVE","baddecay2","2","10","5","1.1")
    run(s,"TOPK.ADD","missing","x")
    run(s,"TOPK.QUERY","missing","x")
    run(s,"TOPK.COUNT","missing","x")
    run(s,"TOPK.LIST","missing")
    run(s,"TOPK.INFO","missing")
    run(s,"TOPK.INCRBY","custom","x","-1")
    run(s,"TOPK.INCRBY","custom","x","100001")
    run(s,"TOPK.INCRBY","custom","x","nope")
    run(s,"TOPK.LIST","custom","BOGUS")
    run(s,"SET","plain","value")
    run(s,"TOPK.ADD","plain","x")
    run(s,"TOPK.QUERY","plain","x")
    run(s,"TOPK.COUNT","plain","x")
    run(s,"TOPK.LIST","plain")
    run(s,"TOPK.INFO","plain")

    print("\n=== cleanup ===")
    run(s,"FLUSHDB")
