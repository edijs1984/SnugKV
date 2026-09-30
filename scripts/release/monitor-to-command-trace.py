#!/usr/bin/env python3
import argparse, json, pathlib, re, shlex
ap=argparse.ArgumentParser()
ap.add_argument('--input',action='append',required=True,type=pathlib.Path)
ap.add_argument('--output',required=True,type=pathlib.Path)
args=ap.parse_args()
with args.output.open('a') as out:
    for path in args.input:
        if not path.exists(): continue
        for line in path.read_text(errors='replace').splitlines():
            try:
                first=line.find(']')
                if first < 0: continue
                payload=line[first+1:].strip()
                vals=shlex.split(payload)
                if not vals: continue
                cmd=vals[0].upper()
                sub=vals[1].upper() if len(vals)>1 else ''
                rec={'source':path.name,'command':cmd,'subcommand':sub,'canonical':cmd + ('|' + sub if sub else '')}
                out.write(json.dumps(rec)+'\n')
            except Exception:
                continue
