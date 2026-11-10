# fleet-stack L4: preserve MCP grants and durable transport after replacing host Python.
from executor_binary import executor_binary
"""Actual stdio MCP and durable host envelopes; no native executable/model calls."""
import json,pathlib,sqlite3,subprocess,tempfile,threading,time
root=pathlib.Path(tempfile.mkdtemp(prefix='fleet-project-mcp-'));path=root/'journal.sqlite'
db=sqlite3.connect(path);db.executescript('CREATE TABLE delegation_requests(id TEXT PRIMARY KEY,body TEXT,response TEXT);CREATE TABLE outbox(route TEXT,body TEXT);');db.close()
context={'hostId':'mac','commandId':'command','executionId':'run','profiles':[],'projectId':'hello-world'}
p=subprocess.Popen([executor_binary(),'mcp','delegation',str(path),json.dumps(context)],stdin=subprocess.PIPE,stdout=subprocess.PIPE,text=True)
def call(i,method,params={}):
 p.stdin.write(json.dumps({'jsonrpc':'2.0','id':i,'method':method,'params':params})+'\n');p.stdin.flush();return json.loads(p.stdout.readline())
try:
 assert 'result' in call(1,'initialize');tools=call(2,'tools/list')['result']['tools'];names=[x['name'] for x in tools];assert 'project_context' in names and 'pipeline_run' in names and 'pipeline_get' in names and 'request_child' not in names
 assert 'error' in call(3,'tools/call',{'name':'pipeline_run','arguments':{'projectId':'other','requestId':'one','query':'hello'}})
 received=[];errors=[]
 def reply():
  try:
   c=sqlite3.connect(path)
   for index in range(3):
    for _ in range(100):
     row=c.execute('SELECT rowid,body FROM outbox ORDER BY rowid LIMIT 1').fetchone()
     if row:break
     time.sleep(.02)
    assert row,'No durable request';b=json.loads(row[1]);received.append(b);q=b['delegation'];assert b['hostId']=='mac' and b['commandId']=='command' and b['executionId']=='run' and q['projectId']=='hello-world'
    if index==0:assert q=={'operation':'pipeline_run','arguments':{'requestId':'one','query':'hello'},'requestId':'one','projectId':'hello-world'}
    else:assert q['operation']=='pipeline_get' and q['arguments']=={'runId':'retained-run'}
    response={'id':'retained-run','state':'completed' if index==2 else 'running'}
    with c:
     c.execute('UPDATE delegation_requests SET response=? WHERE id=?',(json.dumps(response),'command:'+q['requestId']));c.execute('DELETE FROM outbox WHERE rowid=?',(row[0],))
   c.close()
  except BaseException as error:errors.append(str(error))
 t=threading.Thread(target=reply);t.start();r=call(4,'tools/call',{'name':'pipeline_run','arguments':{'requestId':'one','query':'hello'}});assert json.loads(r['result']['content'][0]['text'])['state']=='running'
 for i,state in [(5,'running'),(6,'completed')]:assert json.loads(call(i,'tools/call',{'name':'pipeline_get','arguments':{'runId':'retained-run'}})['result']['content'][0]['text'])['state']==state
 t.join();assert not errors,errors;assert len({q['delegation']['requestId'] for q in received})==3;print('Project MCP protocol passed: durable effect identity and fresh shared result reads')
finally:p.stdin.close();p.wait(timeout=3)
