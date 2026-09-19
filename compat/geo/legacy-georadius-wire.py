#!/usr/bin/env python3
import os
import socket
import time

HOST=os.environ.get("REDIS_HOST","127.0.0.1")
PORT=int(os.environ.get("REDIS_PORT","6390"))

def resp(*parts):
    out=[("*%d\r\n"%len(parts)).encode()]
    for p in parts:
        b=str(p).encode()
        out.append(("$%d\r\n"%len(b)).encode())
        out.append(b+b"\r\n")
    return b"".join(out)

def recv_some(sock,label,idle=0.12,total=1.2):
    sock.setblocking(False)
    chunks=[]; end=time.time()+total; last=time.time()
    while time.time()<end:
        try:
            d=sock.recv(65536)
            if not d: break
            chunks.append(d); last=time.time()
        except BlockingIOError:
            if chunks and time.time()-last>=idle: break
            time.sleep(0.01)
    raw=b"".join(chunks)
    print("\n--- %s (%d bytes) ---"%(label,len(raw)))
    print(repr(raw))
    print(raw.decode("utf-8","replace"))
    return raw

def cmd(sock,label,*parts):
    sock.sendall(resp(*parts))
    return recv_some(sock,label)

print("legacy GEORADIUS differential %s:%d"%(HOST,PORT))

with socket.create_connection((HOST,PORT),timeout=3) as s:
    cmd(s,"DEL setup","DEL","geo:src","geo:store","geo:dist","geo:wrong")
    cmd(s,"setup wrongtype","SET","geo:wrong","x")
    cmd(s,"setup GEOADD","GEOADD","geo:src",
        "13.361389","38.115556","Palermo",
        "15.087269","37.502669","Catania",
        "12.496366","41.902782","Rome",
        "9.190000","45.464200","Milan")

    cases=[
        ("GEORADIUS basic",("GEORADIUS","geo:src","15","37","200","km")),
        ("GEORADIUS ASC",("GEORADIUS","geo:src","15","37","2000","km","ASC")),
        ("GEORADIUS DESC COUNT",("GEORADIUS","geo:src","15","37","2000","km","DESC","COUNT","2")),
        ("GEORADIUS WITHDIST",("GEORADIUS","geo:src","15","37","2000","km","WITHDIST","ASC")),
        ("GEORADIUS WITHCOORD",("GEORADIUS","geo:src","15","37","2000","km","WITHCOORD","ASC")),
        ("GEORADIUS WITHHASH",("GEORADIUS","geo:src","15","37","2000","km","WITHHASH","ASC")),
        ("GEORADIUS all enrich",("GEORADIUS","geo:src","15","37","2000","km","WITHDIST","WITHHASH","WITHCOORD","ASC","COUNT","3")),
        ("GEORADIUS COUNT ANY",("GEORADIUS","geo:src","15","37","2000","km","COUNT","2","ANY")),
        ("GEORADIUS STORE",("GEORADIUS","geo:src","15","37","2000","km","STORE","geo:store")),
        ("ZRANGE stored",("ZRANGE","geo:store","0","-1","WITHSCORES")),
        ("GEORADIUS STOREDIST",("GEORADIUS","geo:src","15","37","2000","km","STOREDIST","geo:dist")),
        ("ZRANGE storedist",("ZRANGE","geo:dist","0","-1","WITHSCORES")),
        ("GEORADIUSBYMEMBER basic",("GEORADIUSBYMEMBER","geo:src","Palermo","200","km")),
        ("GEORADIUSBYMEMBER all enrich",("GEORADIUSBYMEMBER","geo:src","Palermo","2000","km","WITHDIST","WITHHASH","WITHCOORD","ASC")),
        ("GEORADIUS missing key",("GEORADIUS","geo:missing","15","37","200","km")),
        ("GEORADIUSBYMEMBER missing member",("GEORADIUSBYMEMBER","geo:src","Nope","200","km")),
        ("GEORADIUS wrongtype",("GEORADIUS","geo:wrong","15","37","200","km")),
        ("GEORADIUSBYMEMBER wrongtype",("GEORADIUSBYMEMBER","geo:wrong","Palermo","200","km")),
        ("bad unit",("GEORADIUS","geo:src","15","37","200","wat")),
        ("bad lon",("GEORADIUS","geo:src","x","37","200","km")),
        ("bad lat",("GEORADIUS","geo:src","15","x","200","km")),
        ("bad radius",("GEORADIUS","geo:src","15","37","x","km")),
        ("negative radius",("GEORADIUS","geo:src","15","37","-1","km")),
        ("COUNT zero",("GEORADIUS","geo:src","15","37","200","km","COUNT","0")),
        ("COUNT invalid",("GEORADIUS","geo:src","15","37","200","km","COUNT","x")),
        ("ANY without COUNT",("GEORADIUS","geo:src","15","37","200","km","ANY")),
        ("STORE with WITHDIST",("GEORADIUS","geo:src","15","37","200","km","WITHDIST","STORE","geo:store")),
        ("clear dual-store targets",("DEL","geo:store2","geo:dist2")),
        ("STORE plus STOREDIST",("GEORADIUS","geo:src","15","37","200","km","STORE","geo:store2","STOREDIST","geo:dist2")),
        ("dual-store geo target",("ZRANGE","geo:store2","0","-1","WITHSCORES")),
        ("dual-store dist target",("ZRANGE","geo:dist2","0","-1","WITHSCORES")),
        ("missing args",("GEORADIUS","geo:src")),
        ("extra junk",("GEORADIUS","geo:src","15","37","200","km","JUNK")),
    ]
    for label,parts in cases:
        cmd(s,label,*parts)

print("\ndone")
