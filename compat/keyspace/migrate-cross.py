#!/usr/bin/env python3
import os
import socket

HOST = os.environ.get("HOST", "127.0.0.1")
SNUG_PORT = int(os.environ.get("SNUG_PORT", "6380"))
REDIS_DST_PORT = int(os.environ.get("REDIS_DST_PORT", "6391"))
MIGRATE_HOST = os.environ.get("MIGRATE_HOST", HOST)
MIGRATE_PORT = int(os.environ.get("MIGRATE_PORT", str(REDIS_DST_PORT)))

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

print("MIGRATE cross SnugKV="+HOST+":"+str(SNUG_PORT)+" Redis-dst="+HOST+":"+str(REDIS_DST_PORT)+" target="+MIGRATE_HOST+":"+str(MIGRATE_PORT))

with socket.create_connection((HOST,SNUG_PORT),timeout=3) as snug, socket.create_connection((HOST,REDIS_DST_PORT),timeout=3) as dst:
    print("\n=== Basic move ===")
    flush(snug,"SnugKV"); flush(dst,"Redis")
    expect("SET k",cmd(snug,"SET","k","hello"),("simple",b"OK"))
    expect("MIGRATE basic",cmd(snug,"MIGRATE",MIGRATE_HOST,str(MIGRATE_PORT),"k","0","5000"),("simple",b"OK"))
    expect("src GET",cmd(snug,"GET","k"),("bulk",None))
    expect("dst GET",cmd(dst,"GET","k"),("bulk",b"hello"))

    print("\n=== COPY ===")
    flush(snug,"SnugKV"); flush(dst,"Redis")
    expect("SET copy",cmd(snug,"SET","copy","v"),("simple",b"OK"))
    expect("MIGRATE COPY",cmd(snug,"MIGRATE",MIGRATE_HOST,str(MIGRATE_PORT),"copy","0","5000","COPY"),("simple",b"OK"))
    expect("src GET copy",cmd(snug,"GET","copy"),("bulk",b"v"))
    expect("dst GET copy",cmd(dst,"GET","copy"),("bulk",b"v"))

    print("\n=== Existing destination / REPLACE ===")
    flush(snug,"SnugKV"); flush(dst,"Redis")
    expect("SET src",cmd(snug,"SET","same","source"),("simple",b"OK"))
    expect("SET dst",cmd(dst,"SET","same","dest"),("simple",b"OK"))
    expect("MIGRATE collision",cmd(snug,"MIGRATE",MIGRATE_HOST,str(MIGRATE_PORT),"same","0","5000"),("error",b"ERR Target instance replied with error: BUSYKEY Target key name already exists."))
    expect("src remains",cmd(snug,"GET","same"),("bulk",b"source"))
    expect("dst remains",cmd(dst,"GET","same"),("bulk",b"dest"))
    expect("MIGRATE REPLACE",cmd(snug,"MIGRATE",MIGRATE_HOST,str(MIGRATE_PORT),"same","0","5000","REPLACE"),("simple",b"OK"))
    expect("src gone",cmd(snug,"GET","same"),("bulk",None))
    expect("dst replaced",cmd(dst,"GET","same"),("bulk",b"source"))

    print("\n=== TTL transfer ===")
    flush(snug,"SnugKV"); flush(dst,"Redis")
    expect("SET ttl",cmd(snug,"SET","ttl","value"),("simple",b"OK"))
    expect("PEXPIRE ttl",cmd(snug,"PEXPIRE","ttl","60000"),("integer",1))
    before=cmd(snug,"PTTL","ttl")
    print("src PTTL before:",before)
    expect("MIGRATE ttl",cmd(snug,"MIGRATE",MIGRATE_HOST,str(MIGRATE_PORT),"ttl","0","5000"),("simple",b"OK"))
    after=cmd(dst,"PTTL","ttl")
    print("dst PTTL:",after)
    if after[0]!="integer" or after[1] <= 0 or after[1] > 60000:
        raise AssertionError("unexpected destination TTL "+repr(after))

    print("\n=== Missing key ===")
    flush(snug,"SnugKV"); flush(dst,"Redis")
    expect("MIGRATE missing",cmd(snug,"MIGRATE",MIGRATE_HOST,str(MIGRATE_PORT),"missing","0","5000"),("simple",b"NOKEY"))

    print("\n=== KEYS mode ===")
    flush(snug,"SnugKV"); flush(dst,"Redis")
    expect("SET a",cmd(snug,"SET","a","1"),("simple",b"OK"))
    expect("SET b",cmd(snug,"SET","b","2"),("simple",b"OK"))
    expect("MIGRATE KEYS",cmd(snug,"MIGRATE",MIGRATE_HOST,str(MIGRATE_PORT),"","0","5000","KEYS","a","b"),("simple",b"OK"))
    expect("src MGET",cmd(snug,"MGET","a","b"),("array",[("bulk",None),("bulk",None)]))
    expect("dst MGET",cmd(dst,"MGET","a","b"),("array",[("bulk",b"1"),("bulk",b"2")]))

    print("\n=== KEYS partial collision ===")
    flush(snug,"SnugKV"); flush(dst,"Redis")
    expect("SET a",cmd(snug,"SET","a","1"),("simple",b"OK"))
    expect("SET b",cmd(snug,"SET","b","2"),("simple",b"OK"))
    expect("SET dst b",cmd(dst,"SET","b","old"),("simple",b"OK"))
    expect("MIGRATE KEYS collision",cmd(snug,"MIGRATE",MIGRATE_HOST,str(MIGRATE_PORT),"","0","5000","KEYS","a","b"),("error",b"ERR Target instance replied with error: BUSYKEY Target key name already exists."))
    expect("src partial",cmd(snug,"MGET","a","b"),("array",[("bulk",None),("bulk",b"2")]))
    expect("dst partial",cmd(dst,"MGET","a","b"),("array",[("bulk",b"1"),("bulk",b"old")]))

    print("\n=== Validation ===")
    expect("negative timeout missing",cmd(snug,"MIGRATE",MIGRATE_HOST,str(MIGRATE_PORT),"x","0","-1"),("simple",b"NOKEY"))
    expect("bad db",cmd(snug,"MIGRATE",MIGRATE_HOST,str(MIGRATE_PORT),"x","bad","5000"),("error",b"ERR value is not an integer or out of range"))
    expect("bad timeout",cmd(snug,"MIGRATE",MIGRATE_HOST,str(MIGRATE_PORT),"x","0","bad"),("error",b"ERR value is not an integer or out of range"))
    expect("KEYS with key arg",cmd(snug,"MIGRATE",MIGRATE_HOST,str(MIGRATE_PORT),"x","0","5000","KEYS","a"),("error",b"ERR When using MIGRATE KEYS option, the key argument must be set to the empty string"))
    expect("KEYS empty",cmd(snug,"MIGRATE",MIGRATE_HOST,str(MIGRATE_PORT),"","0","5000","KEYS"),("simple",b"NOKEY"))

print("\nMIGRATE CROSS PASS")
