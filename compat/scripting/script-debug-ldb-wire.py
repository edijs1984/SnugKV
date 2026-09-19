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

def recv_idle(sock,label,idle=0.12,total=1.5):
    sock.setblocking(False)
    chunks=[]
    end=time.time()+total
    last=time.time()
    while time.time()<end:
        try:
            d=sock.recv(65536)
            if not d:
                break
            chunks.append(d)
            last=time.time()
        except BlockingIOError:
            if chunks and time.time()-last>=idle:
                break
            time.sleep(0.01)
    raw=b"".join(chunks)
    print("\n--- %s (%d bytes) ---"%(label,len(raw)))
    print(repr(raw))
    print(raw.decode("utf-8","replace"))
    return raw

def send(sock,label,*parts):
    sock.sendall(resp(*parts))
    return recv_idle(sock,label)

def dbg(sock,label,*parts):
    sock.sendall(resp(*parts))
    return recv_idle(sock,label)

print("SCRIPT DEBUG LDB differential %s:%d"%(HOST,PORT))

# Script intentionally has locals, nested function, redis.debug(), and
# redis.breakpoint() so we can capture the debugger protocol around each.
SCRIPT = """local x = 10
local function add(a, b)
  local c = a + b
  redis.debug('inside-add', c)
  return c
end
local y = add(x, 5)
redis.breakpoint()
local z = y * 2
return z"""

with socket.create_connection((HOST,PORT),timeout=3) as s:
    send(s,"SCRIPT DEBUG YES","SCRIPT","DEBUG","YES")
    s.sendall(resp("EVAL",SCRIPT,"0"))
    recv_idle(s,"initial stop")

    # Source listing and trace at the first stop.
    dbg(s,"L source list","L")
    dbg(s,"T stack trace","T")

    # Variable inspection.
    dbg(s,"print x","P","x")
    dbg(s,"print missing","P","missing_name")

    # Single-step. This should advance to the next executable line.
    dbg(s,"S step","S")

    # NEXT behavior, especially around function call boundaries.
    dbg(s,"N next","N")

    # Breakpoint management grammar.
    dbg(s,"B list","B")
    dbg(s,"B set line 4","B","4")
    dbg(s,"B list after set","B")
    dbg(s,"B clear line 4","B","-4")
    dbg(s,"B list after clear","B")

    # Continue through redis.debug() and redis.breakpoint().
    dbg(s,"C continue to runtime debug/breakpoint","C")
    recv_idle(s,"runtime debug or breakpoint follow-up")

    # Inspect variables at breakpoint if session is paused.
    dbg(s,"P y at breakpoint","P","y")
    dbg(s,"L around breakpoint","L")
    dbg(s,"T at breakpoint","T")

    # Continue to end.
    dbg(s,"C finish","C")
    recv_idle(s,"final trailing frame")

# Dedicated breakpoint-by-line session.
with socket.create_connection((HOST,PORT),timeout=3) as s:
    send(s,"SCRIPT DEBUG YES second","SCRIPT","DEBUG","YES")
    s.sendall(resp("EVAL",SCRIPT,"0"))
    recv_idle(s,"second initial stop")
    dbg(s,"set breakpoint line 8","B","8")
    dbg(s,"continue to line breakpoint","C")
    dbg(s,"P y line breakpoint","P","y")
    dbg(s,"continue second finish","C")
    recv_idle(s,"second final trailing frame")

# Syntax/error behavior for debugger commands.
with socket.create_connection((HOST,PORT),timeout=3) as s:
    send(s,"SCRIPT DEBUG YES errors","SCRIPT","DEBUG","YES")
    s.sendall(resp("EVAL","local a=1\nreturn a","0"))
    recv_idle(s,"error-session initial")
    for label,cmd in [
        ("unknown debugger command","QWERTY"),
        ("bad breakpoint","B nope"),
        ("bad print expression","P )"),
        ("empty command",""),
    ]:
        dbg(s,label,*([cmd] if cmd else [""]))
    dbg(s,"finish error session","C")
    recv_idle(s,"error-session final trailing frame")

print("\ndone")
