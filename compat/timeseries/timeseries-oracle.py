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
    if p==b"_":
        line()
        return None
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

    print("\n=== create/basic add/get ===")
    run(s,"TS.CREATE","ts")
    run(s,"TS.ADD","ts","1000","1")
    run(s,"TS.ADD","ts","2000","2.5")
    run(s,"TS.ADD","ts","3000","3")
    run(s,"TS.GET","ts")
    run(s,"TS.RANGE","ts","-","+")
    run(s,"TS.REVRANGE","ts","-","+")

    print("\n=== duplicate policy ===")
    run(s,"TS.CREATE","last","DUPLICATE_POLICY","LAST")
    run(s,"TS.ADD","last","1000","1")
    run(s,"TS.ADD","last","1000","7")
    run(s,"TS.GET","last")
    run(s,"TS.RANGE","last","-","+")
    run(s,"TS.CREATE","first","DUPLICATE_POLICY","FIRST")
    run(s,"TS.ADD","first","1000","1")
    run(s,"TS.ADD","first","1000","7")
    run(s,"TS.GET","first")
    run(s,"TS.CREATE","defaultdup")
    run(s,"TS.ADD","defaultdup","1000","1")
    run(s,"TS.ADD","defaultdup","1000","7")
    run(s,"TS.CREATE","sum","DUPLICATE_POLICY","SUM")
    run(s,"TS.ADD","sum","1000","2")
    run(s,"TS.ADD","sum","1000","3")
    run(s,"TS.GET","sum")
    run(s,"TS.CREATE","minp","DUPLICATE_POLICY","MIN")
    run(s,"TS.ADD","minp","1000","5")
    run(s,"TS.ADD","minp","1000","2")
    run(s,"TS.GET","minp")
    run(s,"TS.CREATE","maxp","DUPLICATE_POLICY","MAX")
    run(s,"TS.ADD","maxp","1000","5")
    run(s,"TS.ADD","maxp","1000","8")
    run(s,"TS.GET","maxp")

    print("\n=== labels / retention / info ===")
    run(s,"TS.CREATE","meta","RETENTION","5000","DUPLICATE_POLICY","LAST","LABELS","sensor","temp","site","riga")
    run(s,"TS.ADD","meta","10000","10")
    run(s,"TS.ADD","meta","12000","12")
    run(s,"TS.ADD","meta","16000","16")
    run(s,"TS.ADD","meta","11500","11.5")
    run(s,"TS.RANGE","meta","-","+")
    run(s,"TS.INFO","meta")

    print("\n=== incrby/decrby ===")
    run(s,"TS.CREATE","counter")
    run(s,"TS.INCRBY","counter","5","TIMESTAMP","1000")
    run(s,"TS.INCRBY","counter","2","TIMESTAMP","2000")
    run(s,"TS.DECRBY","counter","1.5","TIMESTAMP","3000")
    run(s,"TS.GET","counter")
    run(s,"TS.RANGE","counter","-","+")
    run(s,"TS.INCRBY","counter","1","TIMESTAMP","2500")

    print("\n=== delete/ranges ===")
    run(s,"TS.CREATE","del")
    run(s,"TS.ADD","del","1000","1")
    run(s,"TS.ADD","del","2000","2")
    run(s,"TS.ADD","del","3000","3")
    run(s,"TS.ADD","del","4000","4")
    run(s,"TS.ADD","del","5000","5")
    run(s,"TS.DEL","del","2000","4000")
    run(s,"TS.RANGE","del","-","+")
    run(s,"TS.GET","del")
    run(s,"TS.RANGE","del","1500","4500")
    run(s,"TS.REVRANGE","del","1500","4500")

    print("\n=== autocreate ===")
    run(s,"TS.ADD","auto","1000","42","RETENTION","10000","DUPLICATE_POLICY","LAST","LABELS","kind","auto")
    run(s,"TS.GET","auto")
    run(s,"TS.INFO","auto")

    print("\n=== invalid/missing/wrong type ===")
    run(s,"TS.GET","missing")
    run(s,"TS.RANGE","missing","-","+")
    run(s,"TS.INFO","missing")
    run(s,"TS.ADD","ts","nope","1")
    run(s,"TS.ADD","ts","4000","nan")
    run(s,"TS.GET","ts")
    run(s,"TS.RANGE","ts","-","+")
    run(s,"TS.ADD","ts","500","5")
    run(s,"TS.RANGE","ts","-","+")
    run(s,"TS.INFO","ts")
    run(s,"TS.CREATE","badret","RETENTION","-1")
    run(s,"TS.CREATE","baddup","DUPLICATE_POLICY","BOGUS")
    run(s,"TS.DEL","missing","0","1000")
    run(s,"SET","plain","value")
    run(s,"TS.GET","plain")
    run(s,"TS.ADD","plain","1000","1")
    run(s,"TS.INFO","plain")

    print("\n=== cleanup ===")
    run(s,"FLUSHDB")
