#!/usr/bin/env python3
import json, os, socket, threading, time

HOST=os.environ.get("SNUG_HOST","127.0.0.1")
PORT=int(os.environ.get("SNUG_PORT","6383"))
DOCS=int(os.environ.get("DOCS","20000"))

def enc(args):
    out=[f"*{len(args)}\r\n".encode()]
    for a in args:
        if isinstance(a,str):
            a=a.encode()
        out.append(f"${len(a)}\r\n".encode()); out.append(a); out.append(b"\r\n")
    return b"".join(out)

def read(sock):
    def line():
        b=b""
        while not b.endswith(b"\r\n"):
            x=sock.recv(1)
            if not x: raise EOFError
            b+=x
        return b[:-2]
    p=sock.recv(1)
    if p in (b"+",b"-",b":"):
        return p.decode()+line().decode(errors="replace")
    if p==b"$":
        n=int(line())
        if n<0:return None
        data=b""
        while len(data)<n:data+=sock.recv(n-len(data))
        sock.recv(2); return data
    if p==b"*":
        n=int(line()); return [read(sock) for _ in range(n)]
    raise RuntimeError("unknown RESP prefix "+repr(p))

def cmd(sock,*args):
    sock.sendall(enc(args))
    r=read(sock)
    if isinstance(r,str) and r.startswith("-"):
        raise RuntimeError(r)
    return r

def newconn():
    return socket.create_connection((HOST,PORT))

admin=newconn()
try:
    try: cmd(admin,"FT.DROPINDEX","rebuild")
    except Exception: pass
    cmd(admin,"FLUSHDB")

    print(f"seeding {DOCS} JSON docs")
    for i in range(DOCS):
        state="old"
        category="keep" if i%2==0 else "drop"
        raw=json.dumps({"state":state,"category":category,"n":i}, separators=(",",":"))
        cmd(admin,"JSON.SET",f"rebuild:{i}","$",raw)

    start_evt=threading.Event()
    done_evt=threading.Event()
    result={"err":None,"elapsed":None}

    def build():
        s=newconn()
        try:
            start_evt.set()
            t=time.time()
            cmd(s,"FT.CREATE","rebuild","ON","JSON","PREFIX","1","rebuild:",
                "SCHEMA","$.state","AS","state","TAG","$.category","AS","category","TAG",
                "$.n","AS","n","NUMERIC")
            result["elapsed"]=time.time()-t
        except Exception as e:
            result["err"]=repr(e)
        finally:
            done_evt.set()
            s.close()

    t=threading.Thread(target=build,daemon=True)
    t.start()
    start_evt.wait()

    writer=newconn()
    changed=[]
    deleted=[]
    created=[]
    try:
        # Keep mutating until the build finishes, but ensure at least one pass.
        rounds=0
        while not done_evt.is_set() or rounds==0:
            base=(rounds*200)%max(1,DOCS-200)
            for i in range(base, min(base+100,DOCS)):
                raw=json.dumps({"state":"new","category":"keep","n":i+1000000}, separators=(",",":"))
                cmd(writer,"JSON.SET",f"rebuild:{i}","$",raw)
                changed.append(i)
            for i in range(base+100, min(base+150,DOCS)):
                cmd(writer,"DEL",f"rebuild:{i}")
                deleted.append(i)
            for i in range(50):
                key=f"rebuild:new:{rounds}:{i}"
                raw=json.dumps({"state":"new","category":"keep","n":2000000+rounds*100+i}, separators=(",",":"))
                cmd(writer,"JSON.SET",key,"$",raw)
                created.append(key)
            rounds+=1
            if rounds>=50:
                break
    finally:
        writer.close()

    t.join()
    if result["err"]:
        raise RuntimeError("FT.CREATE failed: "+result["err"])

    print(f"FT.CREATE elapsed={result['elapsed']:.3f}s rounds={rounds} changed={len(set(changed))} deleted={len(set(deleted))} created={len(created)}")

    # Validate final primary state against the published generation.
    changed_keys=sorted({f"rebuild:{i}" for i in changed if i not in set(deleted)})
    for key in changed_keys[:200]:
        r=cmd(admin,"FT.SEARCH","rebuild",f"@state:{{new}} @n:[1000000 +inf]","NOCONTENT","LIMIT","0",str(DOCS+len(created)))
        flat=set(x.decode() if isinstance(x,bytes) else x for x in r[1:])
        if key not in flat:
            raise AssertionError(f"updated key missing from index: {key}")
        break

    r=cmd(admin,"FT.SEARCH","rebuild","@state:{new}","NOCONTENT","LIMIT","0",str(DOCS+len(created)+1000))
    indexed=set(x.decode() if isinstance(x,bytes) else x for x in r[1:])

    for key in created:
        if key not in indexed:
            raise AssertionError(f"created-during-build key missing: {key}")

    for i in set(deleted):
        key=f"rebuild:{i}"
        if key in indexed:
            raise AssertionError(f"deleted-during-build key remained indexed: {key}")

    sample=[k for k in changed_keys if k not in set(created)][:200]
    missing=[k for k in sample if k not in indexed]
    if missing:
        raise AssertionError(f"updated-during-build keys missing, sample={missing[:5]}")

    print("PASS: published generation reflects concurrent updates, deletes, and creates")
finally:
    try: cmd(admin,"FT.DROPINDEX","rebuild")
    except Exception: pass
    try: cmd(admin,"FLUSHDB")
    except Exception: pass
    admin.close()
