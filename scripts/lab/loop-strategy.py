#!/usr/bin/env python3
"""Built-in list/grid strategy, executed as an ordinary retained pipeline step."""
import itertools,json,os,pathlib
root=pathlib.Path(os.environ['FLEET_INPUT_DIR'])/'example'
s=json.loads((root/'snapshot.json').read_text())
g=s['generator'];p=g['params'];kind=g['kind']
if kind=='list':
 variants=p['variants']
elif kind=='grid':
 axes=p['overrides'];keys=sorted(axes)
 variants=[{'label':'grid '+str(i),'overrides':dict(zip(keys,vs))} for i,vs in enumerate(itertools.product(*(axes[k] for k in keys)))]
else:
 raise ValueError('script strategy supports list or grid')
result={'action':'next' if variants else 'stop','variants':variants,'reason':'configured '+kind+' generator' if variants else 'no configured variants'}
out=pathlib.Path(os.environ['FLEET_OUTPUT_DIR'])
for name in ['strategy.json','output.json']:(out/name).write_text(json.dumps(result)+'\n')
