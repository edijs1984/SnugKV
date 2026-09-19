#!/usr/bin/env python3
import os
import socket

HOST = os.environ.get("REDIS_HOST", "127.0.0.1")
REDIS_PORT = int(os.environ.get("REDIS_PORT", "6390"))
SNUG_PORT = int(os.environ.get("SNUG_PORT", "6380"))

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

def expect(label,got,want):
    print(label+": "+repr(got))
    if got!=want: raise AssertionError(label+": got "+repr(got)+", want "+repr(want))

def dump(sock,key):
    r=cmd(sock,"DUMP",key)
    if r[0]!="bulk" or r[1] is None: raise AssertionError("DUMP failed: "+repr(r))
    return r[1]

print("STREAM DUMP cross-restore Redis="+HOST+":"+str(REDIS_PORT)+" SnugKV="+HOST+":"+str(SNUG_PORT))

with socket.create_connection((HOST,REDIS_PORT),timeout=3) as redis_sock, socket.create_connection((HOST,SNUG_PORT),timeout=3) as snug_sock:
    print("\n=== Redis -> SnugKV full metadata ===")
    expect("Redis FLUSHDB",cmd(redis_sock,"FLUSHDB"),("simple",b"OK"))
    expect("SnugKV FLUSHDB",cmd(snug_sock,"FLUSHDB"),("simple",b"OK"))

    expect("xadd 1000-0",cmd(redis_sock,"XADD","st","1000-0","f1","v1","f2","v2"),("bulk",b"1000-0"))
    expect("xadd 1001-0",cmd(redis_sock,"XADD","st","1001-0","f1","v3"),("bulk",b"1001-0"))
    expect("xdel 1001-0",cmd(redis_sock,"XDEL","st","1001-0"),("integer",1))
    expect("xadd 1002-0",cmd(redis_sock,"XADD","st","1002-0","f1","v4"),("bulk",b"1002-0"))
    expect("xgroup create",cmd(redis_sock,"XGROUP","CREATE","st","g1","1000-0"),("simple",b"OK"))
    expect("xgroup createconsumer",cmd(redis_sock,"XGROUP","CREATECONSUMER","st","g1","c1"),("integer",1))
    print("xreadgroup:",cmd(redis_sock,"XREADGROUP","GROUP","g1","c1","COUNT","1","STREAMS","st",">"))

    redis_dump=dump(redis_sock,"st")
    expect("SnugKV RESTORE",cmd(snug_sock,"RESTORE","st","0",redis_dump),("simple",b"OK"))
    print("SnugKV XRANGE:",cmd(snug_sock,"XRANGE","st","-","+"))
    print("SnugKV XINFO STREAM:",cmd(snug_sock,"XINFO","STREAM","st"))
    print("SnugKV XINFO GROUPS:",cmd(snug_sock,"XINFO","GROUPS","st"))
    print("SnugKV XPENDING:",cmd(snug_sock,"XPENDING","st","g1"))
    print("SnugKV XINFO CONSUMERS:",cmd(snug_sock,"XINFO","CONSUMERS","st","g1"))

    print("\n=== SnugKV -> Redis entries-only exact bytes ===")
    expect("Redis FLUSHDB",cmd(redis_sock,"FLUSHDB"),("simple",b"OK"))
    expect("SnugKV FLUSHDB",cmd(snug_sock,"FLUSHDB"),("simple",b"OK"))
    expect("SnugKV xadd 1000-0",cmd(snug_sock,"XADD","st","1000-0","f1","v1","f2","v2"),("bulk",b"1000-0"))
    expect("SnugKV xadd 1001-0",cmd(snug_sock,"XADD","st","1001-0","f1","v3"),("bulk",b"1001-0"))
    snug_dump=dump(snug_sock,"st")
    expect("Redis RESTORE entries",cmd(redis_sock,"RESTORE","st","0",snug_dump),("simple",b"OK"))
    print("Redis XRANGE:",cmd(redis_sock,"XRANGE","st","-","+"))

    expect("Redis FLUSHDB",cmd(redis_sock,"FLUSHDB"),("simple",b"OK"))
    expect("Redis xadd 1000-0",cmd(redis_sock,"XADD","st","1000-0","f1","v1","f2","v2"),("bulk",b"1000-0"))
    expect("Redis xadd 1001-0",cmd(redis_sock,"XADD","st","1001-0","f1","v3"),("bulk",b"1001-0"))
    redis_entries_dump=dump(redis_sock,"st")
    print("entries-only Redis len="+str(len(redis_entries_dump))+" SnugKV len="+str(len(snug_dump))+" byte-identical="+str(redis_entries_dump==snug_dump))
    if redis_entries_dump!=snug_dump: raise AssertionError("entries-only STREAM payload mismatch")

    print("\n=== SnugKV -> Redis group metadata ===")
    expect("Redis FLUSHDB",cmd(redis_sock,"FLUSHDB"),("simple",b"OK"))
    expect("SnugKV FLUSHDB",cmd(snug_sock,"FLUSHDB"),("simple",b"OK"))
    expect("SnugKV xadd 1000-0",cmd(snug_sock,"XADD","st","1000-0","f1","v1"),("bulk",b"1000-0"))
    expect("SnugKV xadd 1001-0",cmd(snug_sock,"XADD","st","1001-0","f1","v2"),("bulk",b"1001-0"))
    expect("SnugKV xgroup create",cmd(snug_sock,"XGROUP","CREATE","st","g1","1000-0"),("simple",b"OK"))
    expect("SnugKV createconsumer",cmd(snug_sock,"XGROUP","CREATECONSUMER","st","g1","c1"),("integer",1))
    print("SnugKV xreadgroup:",cmd(snug_sock,"XREADGROUP","GROUP","g1","c1","COUNT","1","STREAMS","st",">"))
    snug_group_dump=dump(snug_sock,"st")
    expect("Redis RESTORE group",cmd(redis_sock,"RESTORE","st","0",snug_group_dump),("simple",b"OK"))
    print("Redis XINFO GROUPS:",cmd(redis_sock,"XINFO","GROUPS","st"))
    print("Redis XPENDING:",cmd(redis_sock,"XPENDING","st","g1"))
    print("Redis XINFO CONSUMERS:",cmd(redis_sock,"XINFO","CONSUMERS","st","g1"))

print("\nSTREAM CROSS-RESTORE PASS")
