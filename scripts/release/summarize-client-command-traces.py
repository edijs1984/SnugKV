#!/usr/bin/env python3
import argparse, collections, json, pathlib

ap=argparse.ArgumentParser()
ap.add_argument('--trace',action='append',required=True,type=pathlib.Path)
ap.add_argument('--candidates',required=True,type=pathlib.Path)
ap.add_argument('--markdown',required=True,type=pathlib.Path)
args=ap.parse_args()

cand=json.loads(args.candidates.read_text())
candidate={x['name'] for x in cand['entries'] if x['classification']=='CANDIDATE_GAP'}
counts=collections.Counter()
sources=collections.defaultdict(set)
for path in args.trace:
    if not path.exists(): continue
    for line in path.read_text(errors='replace').splitlines():
        try: rec=json.loads(line)
        except Exception: continue
        canon=rec.get('canonical','').upper()
        counts[canon]+=1
        sources[canon].add(path.name)

observed=sorted(candidate & set(counts))
not_observed=sorted(candidate-set(observed))
lines=['# Client Command Trace','',
'Candidate-gap commands observed automatically or during supported client workflows.','',
'## Candidate gaps observed','',
'| Command | Count | Trace files |','|---|---:|---|']
for name in observed:
    lines.append(f'| `{name}` | {counts[name]} | ' + ', '.join(sorted(sources[name])) + ' |')
lines += ['','## Candidate gaps not observed','']
for name in not_observed: lines.append(f'- `{name}`')
lines += ['','## All observed commands','', '| Command | Count |','|---|---:|']
for name,count in sorted(counts.items()): lines.append(f'| `{name}` | {count} |')
args.markdown.write_text('\n'.join(lines)+'\n')
print(json.dumps({'candidate_total':len(candidate),'candidate_observed':len(observed),'candidate_not_observed':len(not_observed),'observed':observed},indent=2))
