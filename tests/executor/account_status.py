"""Read-only native readiness fixtures; invented metadata, zero real logins/models."""
import importlib.util,pathlib,tempfile,os,json
spec=importlib.util.spec_from_file_location('worker','packages/coordinator/delivery_worker.py');m=importlib.util.module_from_spec(spec);spec.loader.exec_module(m)
r=pathlib.Path(tempfile.mkdtemp(prefix='fleet-account-status-'));exe=r/'fake';b={'home':str(r),'nativeHome':str(r),'executable':str(exe),'readiness':'ready'}
assert m.account_status(b,'codex')['reason']=='executable-missing'
for harness,output,code,reason,login in [('codex','Not logged in',1,'login-absent',False),('codex','Logged in using ChatGPT',0,'ready',True),('codex','connected',0,'status-unknown',None),('claude','{"loggedIn":false}',0,'login-absent',False),('claude','{"loggedIn":true}',0,'ready',True),('antigravity','gemini-3.8-flash-low',0,'catalogue-only',None)]:
 exe.write_text('#!/usr/bin/env python3\nimport sys\nprint('+repr(output)+')\nsys.exit('+str(code)+')\n');exe.chmod(0o700);v=m.account_status(b,harness);assert v['reason']==reason and v['nativeLoginObserved'] is login and v['quota']=='unknown',v
b['readiness']='unprovisioned';assert m.account_status(b,'claude')['readiness']!='ready'
print(json.dumps({'fakeNativeStatuses':6,'missingExecutable':True,'connectivityNotAuth':True,'catalogueNotAuth':True,'quotaUnknown':True,'realCalls':0}))
