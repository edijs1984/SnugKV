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
        out.append(("$%d\r\n"%len(b)).encode()); out.append(b+b"\r\n")
    return b"".join(out)

def recv_some(sock, label, idle=0.12, total=1.5):
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
    sock.sendall(resp(*parts)); return recv_some(sock,label)

print("CLIENT tracking differential %s:%d"%(HOST,PORT))

with socket.create_connection((HOST,PORT),timeout=3) as a, socket.create_connection((HOST,PORT),timeout=3) as b:
    cmd(a,"A HELLO 3","HELLO","3")
    cmd(b,"B HELLO 3","HELLO","3")
    cmd(a,"A CLIENT ID","CLIENT","ID")
    cmd(b,"B CLIENT ID","CLIENT","ID")

    cmd(a,"TRACKING OFF baseline","CLIENT","TRACKING","OFF")
    cmd(a,"GETREDIR baseline","CLIENT","GETREDIR")

    for parts in [
        ("CLIENT","TRACKING","ON"),
        ("CLIENT","TRACKING","OFF"),
        ("CLIENT","TRACKING","ON","BCAST"),
        ("CLIENT","TRACKING","ON","OPTIN"),
        ("CLIENT","TRACKING","ON","OPTOUT"),
        ("CLIENT","TRACKING","ON","NOLOOP"),
        ("CLIENT","TRACKING","ON","BCAST","PREFIX","foo:"),
        ("CLIENT","TRACKING","ON","BCAST","PREFIX","foo:","PREFIX","bar:"),
    ]:
        cmd(a," ".join(parts),*parts)
        cmd(a,"TRACKING OFF reset","CLIENT","TRACKING","OFF")

    for parts in [
        ("CLIENT","TRACKING"),
        ("CLIENT","TRACKING","MAYBE"),
        ("CLIENT","TRACKING","ON","OPTIN","OPTOUT"),
        ("CLIENT","TRACKING","ON","BCAST","OPTIN"),
        ("CLIENT","TRACKING","ON","PREFIX","foo:"),
        ("CLIENT","CACHING"),
        ("CLIENT","CACHING","MAYBE"),
        ("CLIENT","CACHING","YES"),
        ("CLIENT","GETREDIR","EXTRA"),
    ]:
        cmd(a,"invalid "+" ".join(parts),*parts)

    # RESP3 default tracking: read establishes key tracking.
    cmd(a,"TRACKING ON default","CLIENT","TRACKING","ON")
    cmd(a,"GET track:key","GET","track:key")
    cmd(b,"B SET track:key v1","SET","track:key","v1")
    recv_some(a,"A invalidation after B SET")
    cmd(a,"TRACKING OFF","CLIENT","TRACKING","OFF")

    # NOLOOP: own write should not invalidate, other-client write should.
    cmd(a,"TRACKING ON NOLOOP","CLIENT","TRACKING","ON","NOLOOP")
    cmd(a,"GET noloop:key","GET","noloop:key")
    cmd(a,"A SET noloop:key mine","SET","noloop:key","mine")
    recv_some(a,"A after own SET (NOLOOP)",total=0.35)
    cmd(b,"B SET noloop:key other","SET","noloop:key","other")
    recv_some(a,"A after B SET (NOLOOP)")
    cmd(a,"TRACKING OFF","CLIENT","TRACKING","OFF")

    # OPTIN + CACHING YES
    cmd(a,"TRACKING ON OPTIN","CLIENT","TRACKING","ON","OPTIN")
    cmd(a,"CACHING YES","CLIENT","CACHING","YES")
    cmd(a,"GET optin:key","GET","optin:key")
    cmd(b,"B SET optin:key x","SET","optin:key","x")
    recv_some(a,"A invalidation OPTIN")
    cmd(a,"TRACKING OFF","CLIENT","TRACKING","OFF")

    # OPTOUT + CACHING NO
    cmd(a,"TRACKING ON OPTOUT","CLIENT","TRACKING","ON","OPTOUT")
    cmd(a,"CACHING NO","CLIENT","CACHING","NO")
    cmd(a,"GET optout:key","GET","optout:key")
    cmd(b,"B SET optout:key x","SET","optout:key","x")
    recv_some(a,"A after excluded OPTOUT key",total=0.35)
    cmd(a,"TRACKING OFF","CLIENT","TRACKING","OFF")

    # BCAST prefix invalidation without prior GET.
    cmd(a,"TRACKING ON BCAST PREFIX foo:","CLIENT","TRACKING","ON","BCAST","PREFIX","foo:")
    cmd(b,"B SET foo:a 1","SET","foo:a","1")
    recv_some(a,"A BCAST foo invalidation")
    cmd(b,"B SET other:a 1","SET","other:a","1")
    recv_some(a,"A after non-prefix BCAST write",total=0.35)
    cmd(a,"TRACKING OFF","CLIENT","TRACKING","OFF")

print("\ndone")
