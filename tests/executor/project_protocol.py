"""Actual stdio MCP and durable host envelope; no native executable/model calls."""
import json,pathlib,sqlite3,subprocess,tempfile,threading,time
root=pathlib.Path(tempfile.mkdtemp(prefix='fleet-project-mcp-'));path=root/'journal.sqlite'
db=sqlite3.connect(path);db.executescript('CREATE TABLE delegation_requests(id TEXT PRIMARY KEY,body TEXT,response TEXT);CREATE TABLE outbox(route TEXT,body TEXT);');db.close()
context={'hostId':'mac','commandId':'command','executionId':'run','profiles':[],'projectId':'hello-world'}
p=subprocess.Popen(['python3','crates/executor/src/delegation.py',str(path),json.dumps(context)],stdin=subprocess.PIPE,stdout=subprocess.PIPE,text=True)
def call(i,method,params={}):
 p.stdin.write(json.dumps({'jsonrpc':'2.0','id':i,'method':method,'params':params})+'\n');p.stdin.flush();return json.loads(p.stdout.readline())
try:
 assert 'result' in call(1,'initialize');tools=call(2,'tools/list')['result']['tools'];names=[x['name'] for x in tools];assert 'project_context' in names and 'pipeline_run' in names and 'request_child' not in names
 assert 'error' in call(3,'tools/call',{'name':'pipeline_run','arguments':{'projectId':'other','requestId':'one','query':'hello'}})
 def reply():
  c=sqlite3.connect(path)
  for _ in range(100):
   row=c.execute('SELECT body FROM outbox').fetchone()
   if row:
    b=json.loads(row[0]);assert b=={'hostId':'mac','commandId':'command','executionId':'run','delegation':{'operation':'pipeline_run','arguments':{'requestId':'one','query':'hello'},'requestId':'one','projectId':'hello-world'}}
    with c:c.execute('UPDATE delegation_requests SET response=?',(json.dumps({'id':'retained-run','state':'completed'}),))
    c.close();return
   time.sleep(.02)
  raise AssertionError('No durable request')
 t=threading.Thread(target=reply);t.start();r=call(4,'tools/call',{'name':'pipeline_run','arguments':{'requestId':'one','query':'hello'}});t.join();assert 'retained-run' in r['result']['content'][0]['text'];assert not r['result']['isError'];print('Project MCP protocol passed')
finally:p.stdin.close();p.wait(timeout=3)
