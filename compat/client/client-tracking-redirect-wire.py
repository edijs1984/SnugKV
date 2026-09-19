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

def recv_some(sock,label,idle=0.12,total=1.5):
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

def integer_reply(raw):
    text=raw.decode("ascii","replace").strip()
    if not text.startswith(":"):
        raise RuntimeError("expected integer reply, got %r"%raw)
    return int(text[1:])

print("CLIENT tracking REDIRECT differential %s:%d"%(HOST,PORT))

# RESP3 redirect target.
with socket.create_connection((HOST,PORT),timeout=3) as tracked,      socket.create_connection((HOST,PORT),timeout=3) as redirect,      socket.create_connection((HOST,PORT),timeout=3) as writer:

    cmd(tracked,"tracked HELLO 3","HELLO","3")
    cmd(redirect,"redirect HELLO 3","HELLO","3")
    cmd(writer,"writer HELLO 3","HELLO","3")

    tracked_id=integer_reply(cmd(tracked,"tracked ID","CLIENT","ID"))
    redirect_id=integer_reply(cmd(redirect,"redirect ID","CLIENT","ID"))
    print("tracked_id=%d redirect_id=%d"%(tracked_id,redirect_id))

    cmd(tracked,"TRACKING ON REDIRECT","CLIENT","TRACKING","ON","REDIRECT",redirect_id)
    cmd(tracked,"GETREDIR","CLIENT","GETREDIR")
    cmd(tracked,"GET redirect:key","GET","redirect:key")
    cmd(writer,"writer SET redirect:key v1","SET","redirect:key","v1")
    recv_some(tracked,"tracked client after redirected invalidation",total=0.35)
    recv_some(redirect,"redirect target invalidation")

    cmd(tracked,"TRACKING OFF","CLIENT","TRACKING","OFF")
    cmd(tracked,"GETREDIR after OFF","CLIENT","GETREDIR")

# RESP2 redirect target.
with socket.create_connection((HOST,PORT),timeout=3) as tracked,      socket.create_connection((HOST,PORT),timeout=3) as redirect2,      socket.create_connection((HOST,PORT),timeout=3) as writer:

    cmd(tracked,"tracked2 HELLO 3","HELLO","3")
    redirect2_id=integer_reply(cmd(redirect2,"redirect2 ID RESP2","CLIENT","ID"))
    cmd(writer,"writer2 HELLO 3","HELLO","3")

    cmd(tracked,"TRACKING ON REDIRECT RESP2","CLIENT","TRACKING","ON","REDIRECT",redirect2_id)
    cmd(tracked,"GET redirect2:key","GET","redirect2:key")
    cmd(writer,"writer SET redirect2:key v1","SET","redirect2:key","v1")
    recv_some(redirect2,"RESP2 redirect target invalidation")

# Option combinations and target validation.
with socket.create_connection((HOST,PORT),timeout=3) as a,      socket.create_connection((HOST,PORT),timeout=3) as target:

    cmd(a,"A HELLO 3","HELLO","3")
    target_id=integer_reply(cmd(target,"target ID","CLIENT","ID"))

    cases=[
        ("missing redirect id",("CLIENT","TRACKING","ON","REDIRECT")),
        ("invalid redirect integer",("CLIENT","TRACKING","ON","REDIRECT","abc")),
        ("missing redirect client",("CLIENT","TRACKING","ON","REDIRECT","999999999")),
        ("redirect self",("CLIENT","TRACKING","ON","REDIRECT","0")),
        ("redirect bcast",("CLIENT","TRACKING","ON","BCAST","REDIRECT",str(target_id))),
        ("redirect optin",("CLIENT","TRACKING","ON","OPTIN","REDIRECT",str(target_id))),
        ("redirect noloop",("CLIENT","TRACKING","ON","NOLOOP","REDIRECT",str(target_id))),
    ]
    for label,parts in cases:
        cmd(a,label,*parts)
        cmd(a,"reset after "+label,"CLIENT","TRACKING","OFF")

# Redirect target disconnect behavior.
tracked=socket.create_connection((HOST,PORT),timeout=3)
target=socket.create_connection((HOST,PORT),timeout=3)
writer=socket.create_connection((HOST,PORT),timeout=3)
try:
    cmd(tracked,"disconnect tracked HELLO 3","HELLO","3")
    target_id=integer_reply(cmd(target,"disconnect target ID","CLIENT","ID"))
    cmd(writer,"disconnect writer HELLO 3","HELLO","3")
    cmd(tracked,"TRACKING ON REDIRECT disconnect target","CLIENT","TRACKING","ON","REDIRECT",target_id)
    cmd(tracked,"GET deadredir:key","GET","deadredir:key")
    target.close()
    time.sleep(0.1)
    cmd(writer,"writer SET after target disconnect","SET","deadredir:key","v")
    cmd(tracked,"GETREDIR after target disconnect","CLIENT","GETREDIR")
finally:
    try: tracked.close()
    except: pass
    try: writer.close()
    except: pass

print("\ndone")
