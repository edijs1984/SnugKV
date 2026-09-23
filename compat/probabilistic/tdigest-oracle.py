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
    if p in (b"+",b"-",b":",b","):
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

    print("\n=== create/info/empty ===")
    run(s,"TDIGEST.CREATE","td")
    run(s,"TDIGEST.INFO","td")
    run(s,"TDIGEST.MIN","td")
    run(s,"TDIGEST.MAX","td")
    run(s,"TDIGEST.QUANTILE","td","0","0.5","1")
    run(s,"TDIGEST.CDF","td","0","10")
    run(s,"TDIGEST.RANK","td","0","10")
    run(s,"TDIGEST.REVRANK","td","0","10")
    run(s,"TDIGEST.BYRANK","td","0","1")
    run(s,"TDIGEST.BYREVRANK","td","0","1")

    print("\n=== add/statistics ===")
    run(s,"TDIGEST.ADD","td","1","2","3","4","5","10","10")
    run(s,"TDIGEST.MIN","td")
    run(s,"TDIGEST.MAX","td")
    run(s,"TDIGEST.QUANTILE","td","0","0.25","0.5","0.75","1")
    run(s,"TDIGEST.CDF","td","0","1","3","5","10","11")
    run(s,"TDIGEST.RANK","td","0","1","3","5","10","11")
    run(s,"TDIGEST.REVRANK","td","0","1","3","5","10","11")
    run(s,"TDIGEST.BYRANK","td","0","1","3","6","7")
    run(s,"TDIGEST.BYREVRANK","td","0","1","3","6","7")
    run(s,"TDIGEST.TRIMMED_MEAN","td","0","1")
    run(s,"TDIGEST.TRIMMED_MEAN","td","0.2","0.8")
    run(s,"TDIGEST.INFO","td")

    print("\n=== custom compression / merge ===")
    run(s,"TDIGEST.CREATE","a","COMPRESSION","50")
    run(s,"TDIGEST.CREATE","b","COMPRESSION","200")
    run(s,"TDIGEST.ADD","a","1","2","3")
    run(s,"TDIGEST.ADD","b","100","200","300")
    run(s,"TDIGEST.MERGE","merged","2","a","b")
    run(s,"TDIGEST.INFO","merged")
    run(s,"TDIGEST.QUANTILE","merged","0","0.5","1")
    run(s,"TDIGEST.MERGE","merged","1","a")
    run(s,"TDIGEST.INFO","merged")
    run(s,"TDIGEST.MERGE","merged","1","b","OVERRIDE")
    run(s,"TDIGEST.INFO","merged")
    run(s,"TDIGEST.QUANTILE","merged","0","0.5","1")

    print("\n=== reset ===")
    run(s,"TDIGEST.RESET","td")
    run(s,"TDIGEST.INFO","td")
    run(s,"TDIGEST.MIN","td")
    run(s,"TDIGEST.MAX","td")

    print("\n=== invalid/missing/wrong type ===")
    run(s,"TDIGEST.CREATE","bad0","COMPRESSION","0")
    run(s,"TDIGEST.CREATE","badword","BOGUS","100")
    run(s,"TDIGEST.ADD","td","nan")
    run(s,"TDIGEST.ADD","td","inf")
    run(s,"TDIGEST.QUANTILE","td","-0.1")
    run(s,"TDIGEST.QUANTILE","td","1.1")
    run(s,"TDIGEST.CDF","td","nan")
    run(s,"TDIGEST.RANK","td","nan")
    run(s,"TDIGEST.BYRANK","td","-1")
    run(s,"TDIGEST.TRIMMED_MEAN","td","0.8","0.2")
    run(s,"TDIGEST.INFO","missing")
    run(s,"TDIGEST.ADD","missing","1")
    run(s,"SET","plain","value")
    run(s,"TDIGEST.INFO","plain")
    run(s,"TDIGEST.ADD","plain","1")

    print("\n=== cleanup ===")
    run(s,"FLUSHDB")
