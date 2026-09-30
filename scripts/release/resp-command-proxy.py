#!/usr/bin/env python3
import argparse, asyncio, json, time

def parse_resp_commands(buf):
    out=[]
    pos=0
    n=len(buf)
    while pos<n:
        if buf[pos:pos+1] != b'*':
            nl=buf.find(b'\n',pos)
            if nl<0: break
            line=buf[pos:nl+1]
            parts=line.strip().split()
            if parts:
                out.append((parts[0].decode('utf-8','replace').upper(), parts[1].decode('utf-8','replace').upper() if len(parts)>1 else ''))
            pos=nl+1
            continue
        nl=buf.find(b'\r\n',pos)
        if nl<0: break
        try: argc=int(buf[pos+1:nl])
        except ValueError: break
        p=nl+2
        vals=[]
        complete=True
        for _ in range(argc):
            if p>=n or buf[p:p+1] != b'$': complete=False; break
            ln=buf.find(b'\r\n',p)
            if ln<0: complete=False; break
            try: size=int(buf[p+1:ln])
            except ValueError: complete=False; break
            start=ln+2; end=start+size
            if end+2>n: complete=False; break
            vals.append(buf[start:end])
            p=end+2
        if not complete: break
        if vals:
            cmd=vals[0].decode('utf-8','replace').upper()
            sub=vals[1].decode('utf-8','replace').upper() if len(vals)>1 else ''
            out.append((cmd,sub))
        pos=p
    return out, buf[pos:]

async def handle(reader, writer, args, logfh):
    peer=writer.get_extra_info('peername')
    upstream_r, upstream_w = await asyncio.open_connection(args.target_host,args.target_port)
    conn=f'{peer[0]}:{peer[1]}' if peer else 'unknown'
    async def c2s():
        buf=b''
        while True:
            data=await reader.read(65536)
            if not data: break
            buf += data
            cmds, buf = parse_resp_commands(buf)
            for cmd,sub in cmds:
                rec={'ts':time.time(),'connection':conn,'command':cmd,'subcommand':sub,'canonical':cmd + ('|' + sub if sub else '')}
                logfh.write(json.dumps(rec)+'\n'); logfh.flush()
            upstream_w.write(data); await upstream_w.drain()
        try: upstream_w.write_eof()
        except Exception: pass
    async def s2c():
        while True:
            data=await upstream_r.read(65536)
            if not data: break
            writer.write(data); await writer.drain()
    try:
        await asyncio.gather(c2s(),s2c())
    finally:
        upstream_w.close(); writer.close()
        await upstream_w.wait_closed()
        await writer.wait_closed()

async def main_async(args):
    with open(args.log,'a',buffering=1) as logfh:
        server=await asyncio.start_server(lambda r,w: handle(r,w,args,logfh),args.listen_host,args.listen_port)
        async with server:
            await server.serve_forever()

def main():
    ap=argparse.ArgumentParser()
    ap.add_argument('--listen-host',default='127.0.0.1')
    ap.add_argument('--listen-port',type=int,default=6380)
    ap.add_argument('--target-host',default='127.0.0.1')
    ap.add_argument('--target-port',type=int,default=6398)
    ap.add_argument('--log',required=True)
    args=ap.parse_args()
    asyncio.run(main_async(args))

if __name__=='__main__': main()
