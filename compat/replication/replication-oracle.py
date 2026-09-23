#!/usr/bin/env python3
import os
import socket
import time

HOST = os.environ.get("REDIS_HOST", "127.0.0.1")
PRIMARY_PORT = int(os.environ.get("PRIMARY_PORT", "6393"))
REPLICA_PORT = int(os.environ.get("REPLICA_PORT", "6394"))

class RedisConn:
    def __init__(self, port):
        self.s = socket.create_connection((HOST, port), timeout=3)
        self.f = self.s.makefile("rb")

    def close(self):
        self.f.close()
        self.s.close()

    def command(self, *args):
        out=[f"*{len(args)}\r\n".encode()]
        for arg in args:
            if not isinstance(arg,(bytes,bytearray)):
                arg=str(arg).encode()
            out += [f"${len(arg)}\r\n".encode(), bytes(arg), b"\r\n"]
        self.s.sendall(b"".join(out))
        return self.read()

    def line(self):
        b=self.f.readline()
        if not b:
            raise EOFError("closed")
        return b[:-2]

    def read(self):
        p=self.f.read(1)
        if p==b"+": return ("simple",self.line().decode(errors="replace"))
        if p==b"-": return ("error",self.line().decode(errors="replace"))
        if p==b":": return ("int",int(self.line()))
        if p==b"$":
            n=int(self.line())
            if n<0:return ("bulk",None)
            d=self.f.read(n); self.f.read(2)
            return ("bulk",d.decode(errors="replace"))
        if p==b"*":
            n=int(self.line())
            if n<0:return ("array",None)
            return ("array",[self.read() for _ in range(n)])
        raise RuntimeError(repr(p))

def scalar(r):
    if r[0] in ("simple","bulk","int"): return r[1]
    raise TypeError(r)

def array(r):
    if r[0]!="array": raise TypeError(r)
    return r[1]

def cmd(c,*args):
    r=c.command(*args)
    print("> "+" ".join(map(str,args)))
    print(repr(r))
    return r

def info(c):
    body=scalar(c.command("INFO","replication"))
    out={}
    for line in body.splitlines():
        if ":" in line and not line.startswith("#"):
            k,v=line.split(":",1); out[k]=v
    return out

def role(c):
    r=array(c.command("ROLE"))
    name=scalar(r[0])
    if name=="master":
        return {"role":"master","replicas":len(array(r[2]))}
    return {"role":name,"master_host":scalar(r[1]),"master_port":scalar(r[2]),"state":scalar(r[3])}

def wait_for(fn,timeout=10):
    end=time.time()+timeout
    last=None
    while time.time()<end:
        try:
            last=fn()
            if last:return last
        except Exception:
            pass
        time.sleep(.05)
    raise RuntimeError(f"timeout last={last!r}")

def psync_probe(port):
    s=socket.create_connection((HOST,port),timeout=3)
    f=s.makefile("rb")
    try:
        s.sendall(b"*3\r\n$5\r\nPSYNC\r\n$1\r\n?\r\n$2\r\n-1\r\n")
        first=f.readline().decode(errors="replace").strip()
        second=f.readline().decode(errors="replace").strip()
        parts=first.split()
        print("PSYNC ? -1 -> "+repr({
            "kind": parts[0].lstrip("+") if parts else "",
            "runid_len": len(parts[1]) if len(parts)>1 else -1,
            "offset_is_integer": len(parts)>2 and parts[2].lstrip("-").isdigit(),
            "rdb_bulk_header": second.startswith("$") and second[1:].isdigit(),
        }))
    finally:
        f.close(); s.close()

p=RedisConn(PRIMARY_PORT)
r=RedisConn(REPLICA_PORT)
try:
    cmd(p,"FLUSHALL")
    cmd(r,"REPLICAOF","NO","ONE")
    cmd(r,"FLUSHALL")

    print("\n=== standalone ===")
    print("primary_role="+repr(role(p)))
    print("replica_role="+repr(role(r)))

    print("\n=== seed primary ===")
    cmd(p,"SET","initial","alpha")
    cmd(p,"HSET","hash","a","1","b","2")
    cmd(p,"SET","expiring","value","PX","60000")

    print("\n=== PSYNC handshake shape ===")
    psync_probe(PRIMARY_PORT)

    print("\n=== attach replica ===")
    cmd(r,"REPLICAOF",HOST,str(PRIMARY_PORT))
    wait_for(lambda: info(r).get("master_link_status")=="up")
    wait_for(lambda: scalar(r.command("GET","initial"))=="alpha")
    print("primary_role="+repr(role(p)))
    print("replica_role="+repr(role(r)))
    pi,ri=info(p),info(r)
    print("primary_info="+repr({"role":pi.get("role"),"connected_slaves":pi.get("connected_slaves")}))
    print("replica_info="+repr({
        "role":ri.get("role"),
        "master_host":ri.get("master_host"),
        "master_port":ri.get("master_port"),
        "master_link_status":ri.get("master_link_status"),
        "master_sync_in_progress":ri.get("master_sync_in_progress"),
        "slave_read_only":ri.get("slave_read_only"),
    }))

    print("\n=== initial full sync ===")
    cmd(r,"GET","initial")
    cmd(r,"HGETALL","hash")
    a=scalar(p.command("PTTL","expiring"))
    b=scalar(r.command("PTTL","expiring"))
    print("ttl_synced="+repr(isinstance(a,int) and isinstance(b,int) and a>0 and b>0 and abs(a-b)<=1500))

    print("\n=== live propagation ===")
    cmd(p,"SET","live","beta")
    cmd(p,"INCR","counter")
    cmd(p,"HSET","hash","c","3")
    wait_for(lambda: scalar(r.command("GET","live"))=="beta")
    wait_for(lambda: scalar(r.command("GET","counter"))=="1")
    cmd(r,"GET","live")
    cmd(r,"GET","counter")
    cmd(r,"HGETALL","hash")

    print("\n=== replica read-only ===")
    cmd(r,"SET","forbidden","x")

    print("\n=== detach ===")
    cmd(r,"REPLICAOF","NO","ONE")
    wait_for(lambda: role(r).get("role")=="master")
    print("replica_after_detach_role="+repr(role(r)))
    cmd(r,"SET","after-detach","ok")
    cmd(r,"GET","after-detach")
finally:
    p.close(); r.close()
