#!/usr/bin/env python3
import os
import socket

HOST=os.environ.get("HOST","127.0.0.1")
SRC_PORT=int(os.environ.get("SRC_PORT","6380"))
DST_PORT=int(os.environ.get("DST_PORT","6392"))
MIGRATE_HOST=os.environ.get("MIGRATE_HOST","127.0.0.1")
MIGRATE_PORT=int(os.environ.get("MIGRATE_PORT",str(DST_PORT)))
USER=os.environ.get("MIGRATE_USER","migrator")
PASS=os.environ.get("MIGRATE_PASS","secret")

def enc(parts):
    out=[("*"+str(len(parts))+"\r\n").encode()]
    for p in parts:
        b=p if isinstance(p,bytes) else str(p).encode()
        out.append(("$"+str(len(b))+"\r\n").encode()); out.append(b); out.append(b"\r\n")
    return b"".join(out)

def recv(s):
    def line():
        b=bytearray()
        while True:
            c=s.recv(1)
            if not c: raise EOFError
            b+=c
            if b.endswith(b"\r\n"): return bytes(b[:-2])
    p=s.recv(1)
    if p==b"+": return ("simple",line())
    if p==b"-": return ("error",line())
    if p==b":": return ("integer",int(line()))
    if p==b"$":
        n=int(line())
        if n<0:return ("bulk",None)
        d=b""
        while len(d)<n:d+=s.recv(n-len(d))
        s.recv(2);return ("bulk",d)
    if p==b"*":
        n=int(line()); return ("array",[recv(s) for _ in range(n)])
    raise RuntimeError(p)

def cmd(s,*parts):
    s.sendall(enc(parts)); return recv(s)

def expect(label,got,want):
    print(label+": "+repr(got))
    if got!=want: raise AssertionError((label,got,want))

print("MIGRATE AUTH cross src="+HOST+":"+str(SRC_PORT)+" dst="+HOST+":"+str(DST_PORT)+" target="+MIGRATE_HOST+":"+str(MIGRATE_PORT))
with socket.create_connection((HOST,SRC_PORT),timeout=3) as src, socket.create_connection((HOST,DST_PORT),timeout=3) as dst:
    # Destination is expected to require authentication.
    print("unauth PING:",cmd(dst,"PING"))
    expect("src FLUSHDB",cmd(src,"FLUSHDB"),("simple",b"OK"))
    # Authenticate the direct validation connection.
    auth=cmd(dst,"AUTH",USER,PASS)
    if auth[0]=="error":
        auth=cmd(dst,"AUTH",PASS)
    expect("dst AUTH",auth,("simple",b"OK"))
    expect("dst FLUSHDB",cmd(dst,"FLUSHDB"),("simple",b"OK"))

    print("\n=== AUTH2 success ===")
    expect("SET auth2",cmd(src,"SET","auth2","v"),("simple",b"OK"))
    expect("MIGRATE AUTH2",cmd(src,"MIGRATE",MIGRATE_HOST,str(MIGRATE_PORT),"auth2","0","5000","AUTH2",USER,PASS),("simple",b"OK"))
    expect("dst GET auth2",cmd(dst,"GET","auth2"),("bulk",b"v"))

    print("\n=== AUTH2 failure ===")
    expect("SET bad",cmd(src,"SET","bad","v"),("simple",b"OK"))
    got=cmd(src,"MIGRATE",MIGRATE_HOST,str(MIGRATE_PORT),"bad","0","5000","AUTH2",USER,"wrong")
    print("MIGRATE bad AUTH2:",got)
    if got[0]!="error" or b"Target instance replied with error:" not in got[1]:
        raise AssertionError(got)
    expect("source retained",cmd(src,"GET","bad"),("bulk",b"v"))

print("\nMIGRATE AUTH PASS")
