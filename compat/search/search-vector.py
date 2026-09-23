#!/usr/bin/env python3
import os, socket, struct

HOST=os.environ.get("REDIS_HOST","127.0.0.1")
PORT=int(os.environ.get("REDIS_PORT","6392"))
TARGET=os.environ.get("TARGET_NAME","redis82")

def enc(args):
    out=[f"*{len(args)}\r\n".encode()]
    for a in args:
        if isinstance(a,str): a=a.encode()
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
    if p in (b"+",b"-",b":"): return p.decode()+line().decode(errors="replace")
    if p==b"$":
        n=int(line())
        if n<0:return "$-1"
        data=b""
        while len(data)<n:data+=sock.recv(n-len(data))
        sock.recv(2)
        try:return data.decode()
        except:return data.hex()
    if p==b"*":
        n=int(line())
        return [read(sock) for _ in range(n)]
    return "?"

def run(sock,*args):
    printable=[]
    for a in args:
        if isinstance(a,bytes): printable.append(f"<{len(a)} binary bytes>")
        else: printable.append(str(a))
    print(">"," ".join(printable))
    sock.sendall(enc(args))
    print(read(sock)); print()

def vec(*xs): return struct.pack("<"+"f"*len(xs),*xs)

s=socket.create_connection((HOST,PORT))
print(f"target={TARGET} port={PORT}\n")

run(s,"FLUSHDB")
run(s,"FT.DROPINDEX","v")

for k,doc in [
 ("vec:1",'{"name":"a","v":[1,0,0]}'),
 ("vec:2",'{"name":"b","v":[0.9,0.1,0]}'),
 ("vec:3",'{"name":"c","v":[0,1,0]}'),
 ("vec:4",'{"name":"d","v":[0,0,1]}'),
]:
    run(s,"JSON.SET",k,"$",doc)

print("=== create ===")
run(s,"FT.CREATE","v","ON","JSON","PREFIX","1","vec:","SCHEMA",
    "$.name","AS","name","TEXT",
    "$.v","AS","v","VECTOR","FLAT","6","TYPE","FLOAT32","DIM","3","DISTANCE_METRIC","COSINE")

print("=== info ===")
run(s,"FT.INFO","v")

print("=== knn ===")
q=vec(1,0,0)
run(s,"FT.SEARCH","v","(*)=>[KNN 2 @v $q AS score]","PARAMS","2","q",q,"SORTBY","score","ASC","RETURN","2","name","score","DIALECT","2")
run(s,"FT.SEARCH","v","(*)=>[KNN 3 @v $q]","PARAMS","2","q",q,"NOCONTENT","DIALECT","2")

print("=== range ===")
run(s,"FT.SEARCH","v","@v:[VECTOR_RANGE 0.02 $q]","PARAMS","2","q",q,"RETURN","1","name","DIALECT","2")

print("=== invalid ===")
run(s,"FT.SEARCH","v","(*)=>[KNN 2 @v $q]","DIALECT","2")
run(s,"FT.SEARCH","v","(*)=>[KNN 0 @v $q]","PARAMS","2","q",q,"DIALECT","2")
run(s,"FT.SEARCH","v","(*)=>[KNN 2 @missing $q]","PARAMS","2","q",q,"DIALECT","2")
run(s,"FT.SEARCH","v","(*)=>[KNN 2 @v $q]","PARAMS","2","q",vec(1,0),"DIALECT","2")

run(s,"FT.DROPINDEX","v")
run(s,"FLUSHDB")
s.close()
