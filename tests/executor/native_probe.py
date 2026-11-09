"""Opt-in bounded subscription probe; never run by cargo test."""
import json, pathlib, subprocess, tempfile
root=pathlib.Path(tempfile.mkdtemp(prefix='fleet-native-probe-'))
base=['codex','exec','--ignore-user-config','--ignore-rules','--skip-git-repo-check','--json','-m','gpt-6.1-sol','-c','model_reasoning_effort=low','-c','forced_login_method="chatgpt"','-c','approval_policy="never"','-c','sandbox_mode="read-only"','--disable','multi_agent']
import os
env=dict(os.environ)
for k in ('OPENAI_API_KEY','CODEX_API_KEY'):env.pop(k,None)
def turn(args,name):
 p=subprocess.run(args,cwd=root,env=env,capture_output=True,text=True,timeout=60)
 (root/(name+'.jsonl')).write_text(p.stdout)
 (root/(name+'.stderr')).write_text(p.stderr)
 events=[json.loads(s) for s in p.stdout.splitlines() if s.startswith('{')]
 assert p.returncode==0,(p.returncode,str(root))
 assert any(e.get('type')=='turn.completed' for e in events),str(root)
 return events
first=turn(base+['Remember marker FLEET_PROBE_731. Reply only ACK. Do not use tools or modify files.'],'launch')
native=next(e['thread_id'] for e in first if e.get('type')=='thread.started')
second=turn(base+['resume',native,'Reply only with the marker from the prior message. Do not use tools or modify files.'],'followup')
assert any('FLEET_PROBE_731' in str(e.get('item',{}).get('text','')) for e in second)
print(json.dumps({'evidenceDir':str(root),'nativeSessionId':native,'launch':'completed','followup':'marker retained','modelRequested':'gpt-6.1-sol','effortRequested':'low'}))
