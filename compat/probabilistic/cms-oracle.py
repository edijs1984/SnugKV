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

    print("\n=== init/query/incr ===")
    run(s,"CMS.INITBYDIM","cms","20","5")
    run(s,"CMS.QUERY","cms","apple","banana")
    run(s,"CMS.INCRBY","cms","apple","3","banana","2","apple","4")
    run(s,"CMS.QUERY","cms","apple","banana","missing")
    run(s,"CMS.INFO","cms")

    print("\n=== init by probability ===")
    run(s,"CMS.INITBYPROB","prob","0.01","0.01")
    run(s,"CMS.INFO","prob")
    run(s,"CMS.INCRBY","prob","x","5","y","7")
    run(s,"CMS.QUERY","prob","x","y","z")

    print("\n=== merge ===")
    run(s,"CMS.INITBYDIM","a","20","5")
    run(s,"CMS.INITBYDIM","b","20","5")
    run(s,"CMS.INCRBY","a","x","2","y","3")
    run(s,"CMS.INCRBY","b","x","5","z","7")
    run(s,"CMS.MERGE","merged","2","a","b")
    run(s,"CMS.QUERY","merged","x","y","z")
    run(s,"CMS.INFO","merged")

    print("\n=== weighted merge ===")
    run(s,"CMS.MERGE","weighted","2","a","b","WEIGHTS","2","3")
    run(s,"CMS.QUERY","weighted","x","y","z")
    run(s,"CMS.INFO","weighted")

    print("\n=== invalid/missing/wrong type ===")
    run(s,"CMS.QUERY","missing","x")
    run(s,"CMS.INFO","missing")
    run(s,"CMS.INITBYDIM","badw","0","5")
    run(s,"CMS.INITBYDIM","badd","20","0")
    run(s,"CMS.INITBYPROB","badprob1","0","0.01")
    run(s,"CMS.INITBYPROB","badprob2","0.01","0")
    run(s,"CMS.INCRBY","cms","x")
    run(s,"CMS.INCRBY","cms","x","nope")
    run(s,"CMS.MERGE","badmerge","2","a")
    run(s,"CMS.MERGE","badweights","2","a","b","WEIGHTS","1")
    run(s,"SET","plain","value")
    run(s,"CMS.QUERY","plain","x")
    run(s,"CMS.INCRBY","plain","x","1")
    run(s,"CMS.INFO","plain")

    print("\n=== cleanup ===")
    run(s,"FLUSHDB")
