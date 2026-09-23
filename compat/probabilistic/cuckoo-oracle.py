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

    print("\n=== reserve/add/count/exists ===")
    run(s,"CF.RESERVE","cf","10")
    run(s,"CF.ADD","cf","alice")
    run(s,"CF.ADD","cf","alice")
    run(s,"CF.ADDNX","cf","alice")
    run(s,"CF.ADDNX","cf","bob")
    run(s,"CF.EXISTS","cf","alice")
    run(s,"CF.EXISTS","cf","missing")
    run(s,"CF.COUNT","cf","alice")
    run(s,"CF.COUNT","cf","bob")
    run(s,"CF.MEXISTS","cf","alice","bob","missing")

    print("\n=== delete ===")
    run(s,"CF.DEL","cf","alice")
    run(s,"CF.COUNT","cf","alice")
    run(s,"CF.EXISTS","cf","alice")
    run(s,"CF.DEL","cf","alice")
    run(s,"CF.COUNT","cf","alice")

    print("\n=== autocreate ===")
    run(s,"CF.ADD","auto","x")
    run(s,"CF.COUNT","auto","x")
    run(s,"CF.INFO","auto")

    print("\n=== insert ===")
    run(s,"CF.INSERT","ins","CAPACITY","5","ITEMS","a","a","b","c")
    run(s,"CF.COUNT","ins","a")
    run(s,"CF.MEXISTS","ins","a","b","z")
    run(s,"CF.INSERTNX","insnx","CAPACITY","5","ITEMS","a","a","b","c")
    run(s,"CF.COUNT","insnx","a")
    run(s,"CF.MEXISTS","insnx","a","b","z")

    print("\n=== info ===")
    run(s,"CF.INFO","cf")
    run(s,"CF.INFO","cf","SIZE")
    run(s,"CF.INFO","cf","NUMBUCKETS")
    run(s,"CF.INFO","cf","NUMFILTERS")
    run(s,"CF.INFO","cf","NUMITEMS")
    run(s,"CF.INFO","cf","NUMDELETES")
    run(s,"CF.INFO","cf","BUCKETSIZE")
    run(s,"CF.INFO","cf","EXPANSION")
    run(s,"CF.INFO","cf","MAXITERATIONS")

    print("\n=== invalid ===")
    run(s,"CF.RESERVE","bad0","0")
    run(s,"CF.RESERVE","badneg","-1")
    run(s,"CF.ADD","cf")
    run(s,"CF.MEXISTS","cf")
    run(s,"CF.COUNT","missing","x")
    run(s,"CF.INFO","missing")
    run(s,"SET","plain","value")
    run(s,"CF.ADD","plain","x")
    run(s,"CF.EXISTS","plain","x")
    run(s,"CF.COUNT","plain","x")
    run(s,"CF.DEL","plain","x")

    print("\n=== cleanup ===")
    run(s,"FLUSHDB")
