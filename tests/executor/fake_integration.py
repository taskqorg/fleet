"""No model calls. HTTP exchange, live child, stop, retries and recovery."""
import base64,hashlib,json,os,pathlib,signal,sqlite3,subprocess,tempfile,threading,time
from http.server import ThreadingHTTPServer,BaseHTTPRequestHandler
root=pathlib.Path(tempfile.mkdtemp(prefix='fleet-fake-'))
fake=root/'codex';fake.write_text('''#!/usr/bin/env python3
import json,os,pathlib,subprocess,time,sys,builtins
if 'app-server' in sys.argv:
 original_print=builtins.print
 for line in sys.stdin:
  q=json.loads(line)
  if q.get('method')=='initialize':original_print(json.dumps({'id':q['id'],'result':{}}),flush=True)
  elif q.get('method') in ['thread/start','thread/resume']:
   assert 'developerInstructions' in q['params']
   original_print(json.dumps({'id':q['id'],'result':{'thread':{'id':'fake-thread'}}}),flush=True)
  elif q.get('method')=='turn/start':
   sys.argv.append(q['params']['input'][0]['text'])
   original_print(json.dumps({'id':q['id'],'result':{'turn':{'id':'fake-turn'}}}),flush=True)
   break
 def protocol_print(raw,**kw):
  v=json.loads(raw);typ=v.get('type')
  if typ=='thread.started':return
  if typ=='turn.completed':out={'method':'turn/completed','params':{'threadId':'fake-thread','turn':{'id':'fake-turn','status':'completed'}}}
  elif typ in ['item.completed','item.started']:
   out={'method':'item/completed','params':{'threadId':'fake-thread','turnId':'fake-turn','item':v['item']}}
  else:return
  original_print(json.dumps(out),**kw)
 builtins.print=protocol_print
assert 'FLEET_HOST_TOKEN'  not in os.environ
assert 'OPENAI_API_KEY' not in os.environ
pathlib.Path('leader.pid').write_text(str(os.getpid()))
p=pathlib.Path('output');p.mkdir(exist_ok=True);(p/'index.html').write_text('<html>fake</html>')
print(json.dumps({'type':'thread.started','thread_id':'fake-thread'}),flush=True)
print(json.dumps({'type':'item.completed','item':{'text':'partial'}}),flush=True)
if 'lab-call' in __import__('sys').argv:
 for e in json.loads(pathlib.Path(LAB_NATIVE_EVENTS).read_text()): print(json.dumps(e),flush=True)
if 'leaderonly' in __import__('sys').argv:time.sleep(120)
if 'slow' in __import__('sys').argv or 'abnormal' in __import__('sys').argv:
 child=subprocess.Popen(['sleep','120'],start_new_session=True);pathlib.Path('child.pid').write_text(str(child.pid));time.sleep(3 if 'abnormal' in __import__('sys').argv else 120);os._exit(3)
else:
 time.sleep(1);print(json.dumps({'type':'turn.completed','usage':{'input_tokens':1}}),flush=True)
'''.replace('LAB_NATIVE_EVENTS',json.dumps(str(pathlib.Path(__file__).resolve().parent/'lab-native-events.json'))));fake.chmod(0o700)
commands=[];received=[];artifact_attempts=0;lock=threading.Lock()
class Handler(BaseHTTPRequestHandler):
 def log_message(self,*a):pass
 def do_POST(self):
  global artifact_attempts
  b=json.loads(self.rfile.read(int(self.headers['Content-Length'])))
  assert self.headers['Authorization']=='Bearer invented-test'
  with lock:
   if self.path.endswith('/poll'):reply={'commands':commands.copy()}
   else:
    received.append((self.path,b));reply={'ok':True}
    if self.path.endswith('/receipts'):
     commands[:]=[c for c in commands if c['id']!=b['commandId']]
    if self.path.endswith('/artifacts/reserve'):
     # The first reservation fails like an unreachable coordinator; the transfer worker retries the same reservation.
     artifact_attempts+=1
     if artifact_attempts==1:self.send_response(503);self.end_headers();return
     reply={'artifactId':b['artifactId'],'state':'stored'}
  self.send_response(200);self.send_header('Content-Type','application/json');self.end_headers();self.wfile.write(json.dumps(reply).encode())
srv=ThreadingHTTPServer(('127.0.0.1',0),Handler);threading.Thread(target=srv.serve_forever,daemon=True).start()
env={**os.environ,'FLEET_COORDINATOR_URL':f'http://127.0.0.1:{srv.server_port}','FLEET_HOST_ID':'fake','FLEET_HOST_TOKEN':'invented-test','FLEET_DATA_DIR':str(root/'data'),'FLEET_CODEX_BIN':str(fake),'OPENAI_API_KEY':'invented-excluded'}
for name in ['home','account']:
 (root/name).mkdir(mode=0o700)
env['FLEET_NATIVE_HOME']=str(root/'home');env['FLEET_CODEX_HOME']=str(root/'account')
binary=str(pathlib.Path('target/debug/fleet-executor').resolve())
procs=[]
def launch():
 p=subprocess.Popen([binary],env=env,stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL);procs.append(p);return p
def wait(predicate):
 end=time.monotonic()+15
 while time.monotonic()<end:
  if predicate():return
  time.sleep(.05)
 raise AssertionError('timed out: '+str(received))
def status(cid,s):return any(p.endswith('/receipts') and b['commandId']==cid and b['status']==s for p,b in received)
def command(cid,kind='start',text='slow',execution=None):return {'id':cid,'executionId':execution or cid,'sessionId':cid if kind!='stop' else execution,'kind':kind,'payload':{'text':text,'model':'gpt-6.1-sol','effort':'low','instructions':'No subagents.'}}
unrelated=subprocess.Popen(['sleep','120'],start_new_session=True)
p=launch()
try:
 commands.append(command('one'))
 wait(lambda:any(p.endswith('/events') and b['executionId']=='one' for p,b in received))
 duplicate=launch();assert duplicate.wait(timeout=3)!=0
 assert not status('one','completed')
 wait(lambda:(root/'data/sessions/one/child.pid').exists())
 child=int((root/'data/sessions/one/child.pid').read_text())
 wait(lambda:any(route.endswith('/events') and any(e['type']=='resource.observation' for e in b['events']) for route,b in received))
 commands.append(command('stop','stop',execution='one'))
 wait(lambda:status('stop','stopped'))
 state=subprocess.run(['ps','-p',str(child),'-o','stat='],capture_output=True,text=True).stdout.strip()
 assert not state or state.startswith('Z'),state
 assert unrelated.poll() is None
 commands.append(command('abnormal','start','abnormal'))
 wait(lambda:status('abnormal','failed'))
 orphan=int((root/'data/sessions/abnormal/child.pid').read_text())
 state=subprocess.run(['ps','-p',str(orphan),'-o','stat='],capture_output=True,text=True).stdout.strip()
 assert not state or state.startswith('Z'),state
 assert unrelated.poll() is None
 wait(lambda:artifact_attempts>=2)
 artifacts=[b for route,b in received if route.endswith('/artifacts/reserve')]
 assert artifacts[0]==artifacts[1]
 assert artifacts[0]['name']=='index.html' and artifacts[0]['sha256']==hashlib.sha256(b'<html>fake</html>').hexdigest()
 commands.append(command('two','start','quick'))
 wait(lambda:status('two','completed'))
 # Kill executor after native spawn; reconciliation must not replay native work.
 commands.append(command('crash','start','leaderonly'))
 wait(lambda:(root/'data/sessions/crash/leader.pid').exists())
 leader=int((root/'data/sessions/crash/leader.pid').read_text())
 p.kill();p.wait(timeout=5)
 # Fixture owns this unreaped process group; production recovery reports unknown.
 os.killpg(leader,signal.SIGKILL)
 p=launch();wait(lambda:status('crash','unknown'))
 assert (root/'data/sessions/crash/leader.pid').read_text()==str(leader)
 # Restart boundary: accepted obligation survived but no terminal evidence.
 p.terminate();p.wait(timeout=5)
 db=sqlite3.connect(root/'data/journal.sqlite');c=command('uncertain');db.execute('INSERT INTO commands(id,body) VALUES(?,?)',(c['id'],json.dumps(c)));db.commit();db.close()
 p=launch();wait(lambda:status('uncertain','unknown'));assert not (root/'data/sessions/uncertain').exists()
 p.terminate();p.wait(timeout=5)
 p=launch()
 profile=json.loads(pathlib.Path('config/agents/design-rules.json').read_text())
 c=command('lab','start','lab-call');c['payload']['agentConfig']=profile;commands.append(c)
 wait(lambda:status('lab','completed'))
 ev=[e for route,b in received if route.endswith('/events') and b['executionId']=='lab' for e in b['events']]
 assert any(e['type']=='preparation' and e['data']['requested']==profile for e in ev),ev
 assert any(e['data'].get('item',{}).get('tool')=='get_design_rules' and e['data']['item']['result'] for e in ev),ev
 raw=(root/'data/sessions/lab/lab.native.jsonl').read_text();assert 'get_design_rules' in raw and 'Quiet systems' in raw
 bad=command('badprofile','start','quick');bad['payload']['agentConfig']={**profile,'model':'unsupported'};commands.append(bad);wait(lambda:status('badprofile','failed'));assert not (root/'data/sessions/badprofile').exists()
 p.terminate();p.wait(timeout=5)
 env['FLEET_CODEX_BIN']=str(root/'missing-codex')
 p=launch();commands.append(command('spawnfailure','start','quick'));wait(lambda:status('spawnfailure','failed'))
 print(json.dumps({'result':'passed','evidenceDir':str(root),'checks':['live events before exit','owned stop separate-session child','abnormal exit descendant cleanup','unrelated process survives','singleton lock','artifact retry and sha256','completion','restart unknown without spawn','native environment excludes secrets']}))
finally:
 unrelated.terminate();unrelated.wait()
 for p in procs:
  if p.poll() is None:p.terminate()
 for p in procs:
  try:p.wait(timeout=5)
  except subprocess.TimeoutExpired:p.kill();p.wait()
 srv.shutdown()
