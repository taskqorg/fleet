"""Actual Fleet executor with a fake Claude running the existing real MCP transports; no model calls."""
import os,json,pathlib,tempfile,subprocess,threading,time
from http.server import ThreadingHTTPServer,BaseHTTPRequestHandler
root=pathlib.Path(tempfile.mkdtemp(prefix='fleet-claude-mcp-'));root.chmod(0o700)
for name in ['home','native']:(root/name).mkdir(mode=0o700)
fake=root/'claude';fake.write_text('''#!/usr/bin/env python3
import os,sys,json,subprocess
args=sys.argv[1:];get=lambda k:args[args.index(k)+1]
assert get('--permission-mode')=='dontAsk' and '--strict-mcp-config' in args
assert get('--setting-sources')=='' and get('--tools')=='Read'
assert 'FLEET_HOST_TOKEN' not in os.environ and 'ANTHROPIC_API_KEY' not in os.environ
assert os.environ.get('USER') and os.environ.get('LOGNAME')==os.environ['USER']
assert get('--system-prompt')=='explicit system'
servers=json.loads(get('--mcp-config'))['mcpServers'];assert set(servers)=={'design-rules','fleet'}
allowed=get('--allowedTools').split(',');assert set(allowed)=={'Read','mcp__design-rules__get_design_rules','mcp__fleet__request_child'}
assert '--dangerously-skip-permissions' not in args
for name,s in servers.items():
 p=subprocess.Popen([s['command'],*s['args']],stdin=subprocess.PIPE,stdout=subprocess.PIPE,text=True)
 def call(i,m,params):
  p.stdin.write(json.dumps({'jsonrpc':'2.0','id':i,'method':m,'params':params})+'\\n');p.stdin.flush();return json.loads(p.stdout.readline())
 assert 'result' in call(1,'initialize',{'protocolVersion':'2024-11-05'})
 tools=call(2,'tools/list',{})['result']['tools'];assert len(tools)==1
 if name=='design-rules':
  assert tools[0]['name']=='get_design_rules';result=call(3,'tools/call',{'name':'get_design_rules','arguments':{}});assert 'fixture-heading' in json.dumps(result)
 else:
  assert tools[0]['name']=='request_child';assert tools[0]['inputSchema']['properties']['profileId']['enum']==['claude-reviewer']
  assert 'error' in call(3,'tools/call',{'name':'request_child','arguments':{'requestId':'bad','profileId':'ungranted','input':'review'}})
  result=call(4,'tools/call',{'name':'request_child','arguments':{'requestId':'child','profileId':'claude-reviewer','input':'review'}});assert 'accepted' in json.dumps(result)
 p.stdin.close();p.wait(timeout=3)
print(json.dumps({'type':'system','session_id':'claude-fixture'}),flush=True)
print(json.dumps({'type':'result','session_id':'claude-fixture','is_error':False,'result':'MCP passed'}),flush=True)
''');fake.chmod(0o700)
registry=root/'accounts.json';registry.write_text(json.dumps({'claude':{'harness':'claude','home':str(root/'home'),'nativeHome':str(root/'native'),'executable':str(fake),'readiness':'ready'}}));registry.chmod(0o600)
profile={'id':'fixture','harness':'claude','model':'fixture','effort':None,'instructionRole':'system','instructions':'explicit system','tools':['Read'],'mcpServers':[{'id':'design-rules','data':{'heading':'fixture-heading'}}],'delegationProfiles':['claude-reviewer']}
commands=[{'id':'mcp','executionId':'mcp','sessionId':'mcp','kind':'start','payload':{'harness':'claude','account':'claude','model':'fixture','effort':None,'text':'fixture','agentConfig':profile}}];received=[]
class Handler(BaseHTTPRequestHandler):
 def log_message(self,*a):pass
 def do_POST(self):
  b=json.loads(self.rfile.read(int(self.headers['Content-Length'])))
  if self.path.endswith('/poll'):reply={'commands':commands.copy()}
  else:
   received.append((self.path,b));reply={'ok':True}
   if b.get('delegation'):reply={'childExecutionId':'fixture-child'}
   elif self.path.endswith('/receipts'):commands[:]=[c for c in commands if c['id']!=b['commandId']]
  self.send_response(200);self.end_headers();self.wfile.write(json.dumps(reply).encode())
srv=ThreadingHTTPServer(('127.0.0.1',0),Handler);threading.Thread(target=srv.serve_forever,daemon=True).start()
env={**os.environ,'FLEET_COORDINATOR_URL':f'http://127.0.0.1:{srv.server_port}','FLEET_HOST_ID':'fake','FLEET_HOST_TOKEN':'fixture','FLEET_DATA_DIR':str(root/'data'),'FLEET_NATIVE_ACCOUNTS':str(registry)}
p=subprocess.Popen([str(pathlib.Path('target/debug/fleet-executor').resolve())],env=env,stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL)
try:
 end=time.monotonic()+20;terminal=[]
 while time.monotonic()<end:
  terminal=[b for path,b in received if path.endswith('/receipts') and b.get('status') in ['completed','failed','unknown']]
  if terminal:break
  time.sleep(.1)
 assert terminal and terminal[-1]['status']=='completed',received
 assert len([b for _,b in received if b.get('delegation')])==1
 print(json.dumps({'result':'passed','root':str(root),'checks':['exact native tools/MCP allowlist','strict config/no bypass','real design-rules MCP','real durable Fleet delegation','ungranted child refused','native environment excludes secrets']}))
finally:p.terminate();p.wait(timeout=5);srv.shutdown()
