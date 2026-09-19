#!/usr/bin/env python3
import os
import socket
import time

HOST = os.environ.get("REDIS_HOST", "127.0.0.1")
SRC_PORT = int(os.environ.get("REDIS_PORT", "6390"))
DST_HOST = os.environ.get("REDIS_DST_HOST", HOST)
DST_PORT = int(os.environ.get("REDIS_DST_PORT", "6391"))
MIGRATE_HOST = os.environ.get("MIGRATE_HOST", HOST)
MIGRATE_PORT = int(os.environ.get("MIGRATE_PORT", str(DST_PORT)))

def encode(parts):
    out=[("*"+str(len(parts))+"\r\n").encode()]
    for part in parts:
        b=part if isinstance(part,bytes) else str(part).encode()
        out.append(("$"+str(len(b))+"\r\n").encode()); out.append(b); out.append(b"\r\n")
    return b"".join(out)

def recv_resp(sock):
    def line():
        buf=bytearray()
        while True:
            b=sock.recv(1)
            if not b: raise EOFError("connection closed")
            buf+=b
            if buf.endswith(b"\r\n"): return bytes(buf[:-2])
    first=sock.recv(1)
    if not first: raise EOFError("connection closed")
    if first==b"+": return ("simple",line())
    if first==b"-": return ("error",line())
    if first==b":": return ("integer",int(line()))
    if first==b"$":
        n=int(line())
        if n<0: return ("bulk",None)
        data=b""
        while len(data)<n:
            chunk=sock.recv(n-len(data))
            if not chunk: raise EOFError("connection closed in bulk")
            data+=chunk
        if sock.recv(2)!=b"\r\n": raise RuntimeError("bad bulk terminator")
        return ("bulk",data)
    if first==b"*":
        n=int(line())
        if n<0: return ("array",None)
        return ("array",[recv_resp(sock) for _ in range(n)])
    raise RuntimeError("unknown RESP prefix "+repr(first))

def cmd(sock,*parts):
    sock.sendall(encode(parts)); return recv_resp(sock)

def expect(label, got, want):
    print(label+": "+repr(got))
    if got!=want: raise AssertionError(label+": got "+repr(got)+", want "+repr(want))

def flush(sock,label):
    expect(label+" FLUSHDB",cmd(sock,"FLUSHDB"),("simple",b"OK"))

print("MIGRATE oracle source="+HOST+":"+str(SRC_PORT)+" dest-client="+DST_HOST+":"+str(DST_PORT)+" migrate-target="+MIGRATE_HOST+":"+str(MIGRATE_PORT))
with socket.create_connection((HOST,SRC_PORT),timeout=3) as src, socket.create_connection((DST_HOST,DST_PORT),timeout=3) as dst:
    flush(src,"src"); flush(dst,"dst")

    print("\n=== Basic move ===")
    expect("SET k",cmd(src,"SET","k","hello"),("simple",b"OK"))
    print("MIGRATE basic:",cmd(src,"MIGRATE",MIGRATE_HOST,str(MIGRATE_PORT),"k","0","5000"))
    print("src GET:",cmd(src,"GET","k"))
    print("dst GET:",cmd(dst,"GET","k"))

    print("\n=== COPY ===")
    flush(src,"src"); flush(dst,"dst")
    expect("SET copy",cmd(src,"SET","copy","v"),("simple",b"OK"))
    print("MIGRATE COPY:",cmd(src,"MIGRATE",MIGRATE_HOST,str(MIGRATE_PORT),"copy","0","5000","COPY"))
    print("src GET copy:",cmd(src,"GET","copy"))
    print("dst GET copy:",cmd(dst,"GET","copy"))

    print("\n=== Existing destination / REPLACE ===")
    flush(src,"src"); flush(dst,"dst")
    expect("SET src",cmd(src,"SET","same","source"),("simple",b"OK"))
    expect("SET dst",cmd(dst,"SET","same","dest"),("simple",b"OK"))
    print("MIGRATE collision:",cmd(src,"MIGRATE",MIGRATE_HOST,str(MIGRATE_PORT),"same","0","5000"))
    print("src GET after collision:",cmd(src,"GET","same"))
    print("dst GET after collision:",cmd(dst,"GET","same"))
    print("MIGRATE REPLACE:",cmd(src,"MIGRATE",MIGRATE_HOST,str(MIGRATE_PORT),"same","0","5000","REPLACE"))
    print("src GET after replace:",cmd(src,"GET","same"))
    print("dst GET after replace:",cmd(dst,"GET","same"))

    print("\n=== TTL transfer ===")
    flush(src,"src"); flush(dst,"dst")
    expect("SET ttl",cmd(src,"SET","ttl","value"),("simple",b"OK"))
    expect("PEXPIRE ttl",cmd(src,"PEXPIRE","ttl","60000"),("integer",1))
    before=cmd(src,"PTTL","ttl")
    print("src PTTL before:",before)
    print("MIGRATE ttl:",cmd(src,"MIGRATE",MIGRATE_HOST,str(MIGRATE_PORT),"ttl","0","5000"))
    print("dst PTTL:",cmd(dst,"PTTL","ttl"))

    print("\n=== Missing key ===")
    flush(src,"src"); flush(dst,"dst")
    print("MIGRATE missing:",cmd(src,"MIGRATE",MIGRATE_HOST,str(MIGRATE_PORT),"missing","0","5000"))

    print("\n=== KEYS mode ===")
    flush(src,"src"); flush(dst,"dst")
    expect("SET a",cmd(src,"SET","a","1"),("simple",b"OK"))
    expect("SET b",cmd(src,"SET","b","2"),("simple",b"OK"))
    print("MIGRATE KEYS:",cmd(src,"MIGRATE",MIGRATE_HOST,str(MIGRATE_PORT),"","0","5000","KEYS","a","b"))
    print("src MGET:",cmd(src,"MGET","a","b"))
    print("dst MGET:",cmd(dst,"MGET","a","b"))

    print("\n=== KEYS partial collision ===")
    flush(src,"src"); flush(dst,"dst")
    expect("SET a",cmd(src,"SET","a","1"),("simple",b"OK"))
    expect("SET b",cmd(src,"SET","b","2"),("simple",b"OK"))
    expect("SET dst b",cmd(dst,"SET","b","old"),("simple",b"OK"))
    print("MIGRATE KEYS collision:",cmd(src,"MIGRATE",MIGRATE_HOST,str(MIGRATE_PORT),"","0","5000","KEYS","a","b"))
    print("src MGET after collision:",cmd(src,"MGET","a","b"))
    print("dst MGET after collision:",cmd(dst,"MGET","a","b"))

    print("\n=== Validation ===")
    print("negative timeout:",cmd(src,"MIGRATE",MIGRATE_HOST,str(MIGRATE_PORT),"x","0","-1"))
    print("bad db:",cmd(src,"MIGRATE",MIGRATE_HOST,str(MIGRATE_PORT),"x","bad","5000"))
    print("bad timeout:",cmd(src,"MIGRATE",MIGRATE_HOST,str(MIGRATE_PORT),"x","0","bad"))
    print("COPY twice:",cmd(src,"MIGRATE",MIGRATE_HOST,str(MIGRATE_PORT),"x","0","5000","COPY","COPY"))
    print("KEYS with key arg:",cmd(src,"MIGRATE",MIGRATE_HOST,str(MIGRATE_PORT),"x","0","5000","KEYS","a"))
    print("KEYS empty:",cmd(src,"MIGRATE",MIGRATE_HOST,str(MIGRATE_PORT),"","0","5000","KEYS"))

print("done")
