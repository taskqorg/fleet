"""Actual executor pipe bytes against a fake executable. Zero native/model calls."""
import hashlib,json,os,pathlib,subprocess,tempfile,threading,time
from http.server import ThreadingHTTPServer,BaseHTTPRequestHandler
root=pathlib.Path(tempfile.mkdtemp(prefix='fleet-codex-wire-'));root.chmod(0o700)
fake=root/'fake-codex';fake.write_text('''#!/usr/bin/env python3
import sys,json,pathlib
assert 'app-server' in sys.argv
instructions=None;injected=False
wire=pathlib.Path('fake-wire.jsonl').open('ab')
for raw in sys.stdin.buffer:
 wire.write(raw);wire.flush();q=json.loads(raw);method=q.get('method')
 if method=='initialize':print(json.dumps({'id':q['id'],'result':{}}),flush=True)
 elif method in ['thread/start','thread/resume']:
  instructions=q['params']['developerInstructions'];assert instructions in ['marker A','marker B','marker C'];assert q['params']['model']=='gpt-6.1-sol';assert q['params']['config']['model_reasoning_effort']=='low'
  if method=='thread/resume':assert q['params']['threadId']=='wire-thread'
  print(json.dumps({'id':q['id'],'result':{'thread':{'id':'wire-thread'}}}),flush=True)
 elif method=='thread/inject_items':
  item=q['params']['items'][0];assert q['params']['threadId']=='wire-thread';assert item['role']=='developer';assert item['content']==[{'type':'input_text','text':instructions}];injected=True
  if instructions=='marker C':print(json.dumps({'id':q['id'],'error':{'code':-1,'message':'fixture refusal'}}),flush=True);break
  print(json.dumps({'id':q['id'],'result':{}}),flush=True)
 elif method=='turn/start':
  assert q['params']['input'][0]['text']=='task separate from instructions';assert q['params']['effort']=='low'
  assert instructions=='marker A' or injected
  print(json.dumps({'id':q['id'],'result':{'turn':{'id':'wire-turn'}}}),flush=True)
  print(json.dumps({'method':'item/completed','params':{'threadId':'wire-thread','turnId':'wire-turn','item':{'id':'collab','type':'collabAgentToolCall','tool':'spawnAgent','receiverThreadIds':['child-thread'],'agentsStates':{}}}}),flush=True)
  print(json.dumps({'method':'turn/completed','params':{'threadId':'wire-thread','turn':{'id':'wire-turn','status':'completed'}}}),flush=True)
''');fake.chmod(0o700)
for name in ['home','native']: (root/name).mkdir(mode=0o700)
commands=[];received=[]
class Handler(BaseHTTPRequestHandler):
 def log_message(self,*a):pass
 def do_POST(self):
  b=json.loads(self.rfile.read(int(self.headers['Content-Length'])))
  if self.path.endswith('/poll'):reply={'commands':commands.copy()}
  else:
   received.append((self.path,b));reply={'ok':True}
   if self.path.endswith('/receipts'):commands[:]=[c for c in commands if c['id']!=b['commandId']]
  self.send_response(200);self.end_headers();self.wfile.write(json.dumps(reply).encode())
srv=ThreadingHTTPServer(('127.0.0.1',0),Handler);threading.Thread(target=srv.serve_forever,daemon=True).start()
env={**os.environ,'FLEET_COORDINATOR_URL':f'http://127.0.0.1:{srv.server_port}','FLEET_HOST_ID':'fake','FLEET_HOST_TOKEN':'fake-only','FLEET_DATA_DIR':str(root/'data'),'FLEET_CODEX_BIN':str(fake),'FLEET_NATIVE_HOME':str(root/'home'),'FLEET_CODEX_HOME':str(root/'native')}
p=subprocess.Popen([str(pathlib.Path(os.environ.get('CARGO_TARGET_DIR','target'),'debug','fleet-executor').resolve())],env=env,stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL)
try:
 work=root/'data/sessions/wire-session'
 previous_wire=b''
 for letter in ['A','B','C']:
  profile={'id':'wire','model':'gpt-6.1-sol','effort':'low','instructions':'marker '+letter,'mcpServers':[]}
  commands.append({'id':letter,'executionId':letter,'sessionId':'wire-session','kind':'start','payload':{'model':'gpt-6.1-sol','effort':'low','instructions':'','agentConfig':profile,'text':'task separate from instructions',**({} if letter=='A' else {'nativeSessionId':'wire-thread'})}})
  end=time.monotonic()+20
  while time.monotonic()<end:
   done=[b for route,b in received if route.endswith('/receipts') and b['commandId']==letter and b['status'] in ['completed','failed','unknown']]
   if done:break
   time.sleep(.05)
  assert done,letter
  assert done[-1]['status']==('unknown' if letter=='C' else 'completed'),done
  raw=(work/(letter+'.protocol-out.jsonl'));assert raw.stat().st_mode&0o777==0o600
  captured=(work/'fake-wire.jsonl').read_bytes();assert previous_wire+raw.read_bytes()==captured
  emitted=captured[len(previous_wire):];previous_wire=captured
  frames=[json.loads(l) for l in emitted.splitlines()];open_frame=next(v for v in frames if v.get('method') in ['thread/start','thread/resume']);assert open_frame['params']['developerInstructions']=='marker '+letter
  methods=[v.get('method') for v in frames]
  if letter=='A':assert 'thread/inject_items' not in methods
  else:assert 'thread/inject_items' in methods
  assert methods.count('turn/start')==(0 if letter=='C' else 1)
  if letter=='B':assert methods.index('thread/inject_items')<methods.index('turn/start')
  emitted_events=[e for route,b in received if route.endswith('/events') and b['executionId']==letter for e in b['events']];assert len({e['seq'] for e in emitted_events})==len(emitted_events),'Duplicate sequence';assert len({e['id'] for e in emitted_events})==len(emitted_events),'Duplicate event ID'
  closed=[e['data'] for route,b in received if route.endswith('/events') and b['executionId']==letter for e in b['events'] if e['type']=='process.closed'];assert closed and closed[-1]['stdoutClosed'] is True and closed[-1]['exitCode']==0
  if letter!='C':assert any(c['threadId']=='child-thread' and c['parentThreadId']=='wire-thread' and c['binding']['model']=='gpt-6.1-sol' for e in emitted_events for c in json.loads(json.dumps(e)).get('data',{}).get('native',{}).get('children') or e['data'].get('children') or []),'child not recorded'
  metadata=[e['data'] for route,b in received if route.endswith('/events') and b['executionId']==letter for e in b['events'] if e['type']=='protocol.write'];assert metadata
  for m in metadata:assert 'params' not in m and 'marker ' not in json.dumps(m) and 'sha256' in m
 print(json.dumps({'result':'passed','root':str(root),'checks':['actual A/B emitted bytes','private persisted wire equals fake read bytes','developer injection before resumed turn','task input separate','refusal blocks turn without replay','safe projected metadata']}))
finally:
 p.terminate();p.wait(timeout=5);srv.shutdown()
