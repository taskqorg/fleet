"""Real isolated worker crash at a controlled transport boundary; no native/model/provider calls."""
import json,os,pathlib,subprocess,tempfile,threading,time
import sys as _sys;_sys.path.insert(0,os.path.dirname(os.path.abspath(__file__)));from executor_binary import secret_file
from http.server import ThreadingHTTPServer,BaseHTTPRequestHandler
root=pathlib.Path(tempfile.mkdtemp(prefix='fleet-program-recovery-'));root.chmod(0o700);started=threading.Event();release=threading.Event();received=[];pending=True
command={'id':'isolated-command','executionId':'isolated-execution','sessionId':'isolated-session','kind':'delivery-step','payload':{'kind':'publish','projectId':'hello-world','deliveryId':'isolated-delivery','files':[]}}
class Handler(BaseHTTPRequestHandler):
 def log_message(self,*args):pass
 def do_POST(self):
  global pending
  b=json.loads(self.rfile.read(int(self.headers['Content-Length'])));received.append((self.path,b))
  if self.path.endswith('/poll'):reply={'commands':[command] if pending else []}
  else:
   if b.get('status')=='started':started.set();release.wait(10)
   if b.get('status')=='unknown':pending=False
   reply={'ok':True}
  try:self.send_response(200);self.end_headers();self.wfile.write(json.dumps(reply).encode())
  except BrokenPipeError:pass
server=ThreadingHTTPServer(('127.0.0.1',0),Handler);threading.Thread(target=server.serve_forever,daemon=True).start();config=root/'worker.json';config.write_text(json.dumps({'hostId':'isolated-mac','coordinator':f'http://127.0.0.1:{server.server_port}','hostSecretFile':secret_file('invented-fixture-only-host-secret-0123456789'),'journal':str(root/'journal'),'projects':{},'kinds':['publish']}));config.chmod(0o600);prior=root/'retained-result.txt';prior.write_text('surviving result')
argv=['python3','packages/coordinator/delivery_worker.py',str(config),'--once'];p=subprocess.Popen(argv,stdout=subprocess.PIPE,stderr=subprocess.PIPE)
try:
 assert started.wait(5),'No observed started transport boundary';p.kill();p.wait(timeout=5);release.set()
 q=subprocess.run(argv,stdout=subprocess.PIPE,stderr=subprocess.PIPE,timeout=8);assert q.returncode==0
 statuses=[b.get('status') for route,b in received if route.endswith('/receipts')];assert statuses==['accepted','started','unknown'],statuses
 assert not any(route.endswith('/artifacts') for route,b in received)
 assert not (root/'journal/isolated-command').exists();assert (root/'journal/isolated-command.json').exists();assert prior.read_text()=='surviving result'
 result={'proof':'isolated actual program-worker process and fake HTTP transport','platform':os.uname().sysname,'killedAfterStartedRequestBeforeEffect':True,'statuses':statuses,'effectReplayCount':0,'workAreaAbsent':True,'priorResultSurvived':True,'liveServiceDisrupted':False,'nativeModelCalls':0,'providerCalls':0}
 pathlib.Path('evidence/program-worker-recovery.json').write_text(json.dumps(result,indent=2)+'\n');print(json.dumps(result))
finally:
 release.set()
 if p.poll() is None:p.kill();p.wait()
 server.shutdown()
