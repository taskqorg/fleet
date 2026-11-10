"""Actual Fleet executor + fake installed ACP executable; real Fleet read-only MCP. Zero model calls."""
import json,os,pathlib,subprocess,tempfile,threading,time
import sys as _sys;_sys.path.insert(0,os.path.dirname(os.path.abspath(__file__)));from executor_binary import executor_binary,secret_file
from http.server import ThreadingHTTPServer,BaseHTTPRequestHandler
root=pathlib.Path(tempfile.mkdtemp(prefix='fleet-acp-fake-'));root.chmod(0o700)
for d in ['home','native','host']:(root/d).mkdir(mode=0o700)
(root/'host').chmod(0o755)
fake=root/'agy';fake.write_text('''#!/usr/bin/env python3
import json,sys,os,pathlib,subprocess,time
assert sys.argv[1]=='acp';assert '--include-default-customizations=false' in sys.argv
assert sys.argv[sys.argv.index('--app-data-dir')+1]=='antigravity-cli';assert sys.argv[sys.argv.index('--gemini-dir')+1]==os.environ['GEMINI_HOME']
assert pathlib.Path(os.environ['HOME']).name=='host';assert 'GOOGLE_API_KEY' not in os.environ and 'FLEET_HOST_SECRET_FILE' not in os.environ
servers=[];model=False;mode=False
wire=pathlib.Path('fake-wire.jsonl').open('ab')
def send(v):print(json.dumps(v),flush=True)
def update(v):send({'jsonrpc':'2.0','method':'session/update','params':{'sessionId':'fake-native','update':v}})
for raw in sys.stdin.buffer:
 wire.write(raw);wire.flush();q=json.loads(raw);m=q.get('method');i=q.get('id');params=q.get('params',{})
 if m=='initialize':send({'id':i,'result':{'protocolVersion':1,'agentInfo':{'version':'1.5.3'},'agentCapabilities':{'loadSession':True,'sessionCapabilities':{'resume':{}}}}})
 elif m in ['session/new','session/load','session/resume']:
  assert not params.get('_meta');servers=params['mcpServers'];assert [s['name'] for s in servers]==['design-rules'];assert servers[0]['env']==[]
  if m!='session/new':assert params['sessionId']=='fake-native'
  send({'id':i,'result':{'sessionId':'fake-native','models':{'availableModels':[{'modelId':'gemini-3.8-flash-low'}]},'modes':{'availableModes':[{'id':'accept-edits'}]}}})
 elif m=='session/set_model':assert params['modelId']=='gemini-3.8-flash-low';model=True;send({'id':i,'result':{}})
 elif m=='session/set_mode':assert params['modeId']=='accept-edits';mode=True;send({'id':i,'result':{}})
 elif m=='session/prompt':
  assert model and mode and params['sessionId']=='fake-native';text=params['prompt'][0]['text'];assert 'Explicit Fleet instructions (user role)' in text
  update({'sessionUpdate':'agent_message_chunk','content':{'type':'text','text':'partial native text'}})
  if 'unknown-fixture' in text:break
  if 'stop-fixture' in text:time.sleep(30);break
  send({'id':'permission','method':'session/request_permission','params':{'sessionId':'fake-native','toolCall':{'kind':'fetch'},'options':[{'optionId':'allow','kind':'allow_once'}]}})
  reply=json.loads(sys.stdin.buffer.readline());wire.write((json.dumps(reply,separators=(',',':'),sort_keys=True)+'\\n').encode());wire.flush();assert reply['result']['outcome']['outcome']=='cancelled'
  s=servers[0];p=subprocess.Popen([s['command'],*s['args']],stdin=subprocess.PIPE,stdout=subprocess.PIPE,text=True)
  def call(i,m,params):p.stdin.write(json.dumps({'id':i,'method':m,'params':params})+'\\n');p.stdin.flush();return json.loads(p.stdout.readline())
  call(1,'initialize',{'protocolVersion':'2024-11-05'});assert call(2,'tools/list',{})['result']['tools'][0]['name']=='get_design_rules'
  result=call(3,'tools/call',{'name':'get_design_rules','arguments':{}})['result'];p.stdin.close();p.wait(timeout=3)
  update({'sessionUpdate':'tool_call','toolCallId':'tool','title':'design-rules/get_design_rules','kind':'other','status':'in_progress','rawInput':{}})
  update({'sessionUpdate':'tool_call_update','toolCallId':'tool','status':'completed','rawOutput':result})
  out=pathlib.Path('output');out.mkdir(exist_ok=True);(out/'marker.txt').write_text('ACP')
  update({'sessionUpdate':'agent_message_chunk','content':{'type':'text','text':'ACP'}})
  send({'id':i,'result':{'stopReason':'end_turn','usage':{'inputTokens':13,'outputTokens':2}}})
''');fake.chmod(0o700)
registry=root/'registry.json';registry.write_text(json.dumps({'current':{'harness':'antigravity','home':str(root/'home'),'nativeHome':str(root/'native'),'credentialAuthority':'native-current','credentialHome':str(root/'host'),'executable':str(fake),'readiness':'ready'}}));registry.chmod(0o600)
profile=json.loads(pathlib.Path('config/agents/antigravity-worker.json').read_text());commands=[];received=[];seen_events={}
class Handler(BaseHTTPRequestHandler):
 def log_message(self,*a):pass
 def do_POST(self):
  b=json.loads(self.rfile.read(int(self.headers['Content-Length'])))
  if self.path.endswith('/poll'):reply={'commands':commands.copy()}
  else:
   if self.path.endswith('/events'):
    for event in b['events']:
     key=(b['executionId'],event['seq']);assert key not in seen_events or seen_events[key]==event,'conflicting retained event sequence';seen_events[key]=event
   received.append((self.path,b));reply={'ok':True}
   if self.path.endswith('/receipts'):commands[:]=[c for c in commands if c['id']!=b['commandId']]
  self.send_response(200);self.end_headers();self.wfile.write(json.dumps(reply).encode())
srv=ThreadingHTTPServer(('127.0.0.1',0),Handler);threading.Thread(target=srv.serve_forever,daemon=True).start()
env={**os.environ,'FLEET_COORDINATOR_URL':f'http://127.0.0.1:{srv.server_port}','FLEET_HOST_ID':'fake','FLEET_HOST_SECRET_FILE':secret_file('fake-host-secret-0123456789abcdef'),'FLEET_DATA_DIR':str(root/'data'),'FLEET_NATIVE_ACCOUNTS':str(registry)}
p=subprocess.Popen([executor_binary()],env=env,stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL)
def issue(cid,text,session='session',kind='start',execution=None):commands.append({'id':cid,'executionId':execution or cid,'sessionId':session,'kind':kind,'payload':{} if kind=='stop' else {'harness':'antigravity','account':'current','model':profile['model'],'effort':None,'text':text,'agentConfig':profile}})
def wait(fn):
 end=time.monotonic()+20
 while time.monotonic()<end:
  if fn():return
  time.sleep(.05)
 raise AssertionError('fixture timeout')
def status(cid,s):return any(path.endswith('/receipts') and b['commandId']==cid and b.get('status')==s for path,b in received)
try:
 for cid in ['fresh','resume']:
  issue(cid,'local fixture');wait(lambda:status(cid,'completed'))
  w=root/'data/sessions/session';frames=[json.loads(l) for l in (w/(cid+'.protocol-out.jsonl')).read_text().splitlines()];methods=[v.get('method') for v in frames];assert methods.count('session/prompt')==1;assert ('session/new' if cid=='fresh' else 'session/resume') in methods;assert methods.index('session/set_model')<methods.index('session/prompt');assert (w/(cid+'.protocol-out.jsonl')).stat().st_mode&0o777==0o600
 issue('unknown','unknown-fixture','unknown');wait(lambda:status('unknown','unknown'));frames=[json.loads(l) for l in (root/'data/sessions/unknown/unknown.protocol-out.jsonl').read_text().splitlines()];assert sum(v.get('method')=='session/prompt' for v in frames)==1
 issue('stopping','stop-fixture','stopping');wait(lambda:any(path.endswith('/events') and b['executionId']=='stopping' and any(e['type']=='acp.update' for e in b['events']) for path,b in received));issue('stop','',session='stopping',kind='stop',execution='stopping');wait(lambda:status('stop','stopped'))
 events=[e for path,b in received if path.endswith('/events') for e in b['events']];assert any(e['data'].get('fleetNormalized',{}).get('messages') for e in events);assert any(e['data'].get('fleetNormalized',{}).get('tools') for e in events);assert any(e['data'].get('fleetNormalized',{}).get('usage',{}).get('input_tokens')==13 for e in events if isinstance(e['data'].get('fleetNormalized',{}).get('usage'),dict))
 print(json.dumps({'result':'passed','root':str(root),'checks':['native-current HOME with private explicit behavior roots','new/resume MCP','model/mode before sole user-role prompt','real read-only Fleet MCP','permission denied','text/tools/results/usage','unknown EOF no duplicate','owned stop partial history']}))
finally:p.terminate();p.wait(timeout=5);srv.shutdown()
