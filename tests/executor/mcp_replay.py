"""fleet-stack L4: replay captured stdio behavior; protects tools, errors and concurrent journal effects."""
import json, pathlib, sqlite3, subprocess, tempfile, threading, time, sys, re, select
ROOT = pathlib.Path(__file__).resolve().parents[2]
FIXTURE = ROOT/'tests/executor/fixtures/mcp.json'
CAPTURE = '--capture' in sys.argv
REVISION = sys.argv[2] if CAPTURE and len(sys.argv)>2 else '3d1a7aa'
BINARY = sys.argv[1] if len(sys.argv)>1 and not CAPTURE else None

def normalize(v, root):
    if isinstance(v, str):
        return re.sub(r'[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}', '<uuid>', v.replace(str(root), '<root>'))
    if isinstance(v, list): return [normalize(x,root) for x in v]
    if isinstance(v, dict):
        result={k:normalize(x,root) for k,x in v.items()}
        if v.get('type')=='text' and isinstance(v.get('text'),str):
            try:result['text']=normalize(json.loads(v['text']),root)
            except ValueError:pass
        return result
    return v

def run(server, context, requests):
    with tempfile.TemporaryDirectory() as tmp:
        root=pathlib.Path(tmp);(root/'work/sub').mkdir(parents=True);(root/'spool').mkdir();(root/'work/sub/a.txt').write_text('hello');(root/'outside').write_text('outside');(root/'work/link').symlink_to(root/'outside');(root/'work/hard').hardlink_to(root/'outside')
        for name in ['.txt','note.MD','archive.tgz','blob.unknown']:(root/'work'/name).write_text('fixture')
        ctx=json.loads(json.dumps(context).replace('<root>',str(root)))
        db=sqlite3.connect(root/'journal.sqlite');db.executescript("PRAGMA journal_mode=WAL; CREATE TABLE delegation_requests(id TEXT PRIMARY KEY,body TEXT,response TEXT);CREATE TABLE outbox(route TEXT,body TEXT);CREATE TABLE transfers(id TEXT PRIMARY KEY,kind TEXT,body TEXT,state TEXT DEFAULT 'queued',response TEXT,created_at INTEGER);");db.close()
        stop=threading.Event();errors=[]
        def respond():
            c=sqlite3.connect(root/'journal.sqlite')
            try:
                while not stop.is_set():
                    with c:
                        c.execute('UPDATE delegation_requests SET response=? WHERE response IS NULL',(json.dumps(ctx.get('_response',{'childExecutionId':'child','result':{'content':[{'type':'text','text':'reply'}]}})),))
                        c.execute('UPDATE transfers SET state=?,response=? WHERE response IS NULL',(ctx.get('_transferState','done'),json.dumps(ctx.get('_transferResponse',{'artifactId':'artifact','state':'done'}))))
                    stop.wait(.005)
            except Exception as e: errors.append(e)
            finally:c.close()
        t=threading.Thread(target=respond,daemon=True);t.start()
        args=([sys.executable,'-c',subprocess.check_output(['git','show',f'{REVISION}:crates/executor/src/{server.replace("-","_")}.py'],cwd=ROOT,text=True)] if CAPTURE else [BINARY,'mcp',server])+([str(root/'journal.sqlite'),json.dumps(ctx)] if server=='delegation' else [json.dumps(ctx)])
        p=subprocess.Popen(args,stdin=subprocess.PIPE,stdout=subprocess.PIPE,stderr=subprocess.PIPE,text=True)
        replies=[]
        try:
            for req in requests:
                p.stdin.write((req if isinstance(req,str) else json.dumps(req))+'\n');p.stdin.flush()
                if isinstance(req,dict) and 'id' not in req:continue
                assert select.select([p.stdout],[],[],10)[0], 'MCP reply did not arrive within 10 seconds'
                line=p.stdout.readline();assert line, "MCP exited without a reply";replies.append(normalize(json.loads(line),root))
            p.stdin.close();assert p.wait(timeout=5)==0,p.stderr.read()
            db=sqlite3.connect(root/'journal.sqlite');effects={name:[normalize(json.loads(r[0]),root) for r in db.execute(f'SELECT body FROM {name} ORDER BY rowid')] for name in ['outbox','delegation_requests','transfers']};db.close()
        finally:
            if p.poll() is None:p.kill();p.wait()
            stop.set();t.join()
        assert not errors,errors
        return {'replies':replies,'effects':effects}

def req(method,params=None):return {'jsonrpc':'2.0','id':1,'method':method,**({'params':params} if params is not None else {})}
def call(name,args={}):return req('tools/call',{'name':name,'arguments':args})
if CAPTURE:
    common=[req('initialize'),req('initialize',{'protocolVersion':'custom'}),{'method':'notifications/initialized'},req('ping'),req('tools/list'),req('unknown'),call('unknown')]
    cases=[{'server':'design-rules','context':{'z':'Unicode: Ж','a':[1,True,None]},'requests':common+[call('get_design_rules'),call('get_design_rules',{'bad':1}),'broken']}, {'server':'delegation','context':{'hostId':'host','commandId':'command','executionId':'execution','profiles':[]},'requests':common}]
    ctx={'hostId':'host','commandId':'command','executionId':'execution','projectId':'project','profiles':['reviewer'],'workdir':'<root>/work','inputDir':'<root>/in','spool':'<root>/spool'}
    requests=common+[call('request_child',{'requestId':'child','profileId':'reviewer','input':'review'}),call('request_child',{'requestId':'child','profileId':'reviewer','input':'review'}),call('request_child',{'requestId':'child','profileId':'reviewer','input':'changed'}),call('request_child',{}),call('request_child',{'requestId':'bad','profileId':'wrong','input':'review'})]
    for name in ['project_context','catalogue','pipeline_run','direct_search','pipeline_history','pipeline_get','question_ask','question_history','account_list']:
        requests.extend([call(name,{'requestId':name}),call(name,{'invalid':True})])
    for name in ['session_start','session_deliver','session_transition','session_fork','session_resume','session_handoffs','message_send','queue_list','ask','reply','questions','worker_done','execution_status']: requests.append(call(name))
    requests += [call('artifact_save',{'path':p}) for p in ['../outside','link','hard','missing','sub/a.txt','.txt','note.MD','archive.tgz','blob.unknown']]+[call('artifact_save',{'path':'sub/a.txt','required':True}),call('artifact_save',{}),call('artifact_save',{'path':3}),call('artifact_list'),call('artifact_get',{'artifact_id':'artifact'}),'broken']
    requests += [call('pipeline_run',{'requestId':'pass-through','query':{'native':'argument'}}),call('pipeline_run'),call('artifact_get'),call('artifact_list',{'extra':'no'}),call('artifact_save',{'path':''}),call('request_child',{'requestId':'','profileId':'reviewer','input':'x'})]
    cases.append({'server':'delegation','context':ctx,'requests':requests})
    rejected=[call('request_child',{'requestId':'refused','profileId':'reviewer','input':'x'})]
    rejected += [call(name,{'requestId':name}) for name in ['project_context','catalogue','pipeline_run','direct_search','pipeline_history','pipeline_get','question_ask','question_history','account_list']]
    rejected += [call(name) for name in ['session_start','session_deliver','session_transition','session_fork','session_resume','session_handoffs','message_send','queue_list','ask','reply','questions','worker_done','execution_status']]
    rejected += [call('artifact_save',{'path':'sub/a.txt'}),call('artifact_list'),call('artifact_get',{'artifact_id':'failed'})]
    cases.append({'server':'delegation','context':{**ctx,'_response':{'error':'rejected'},'_transferState':'failed','_transferResponse':{'error':'rejected','state':'failed'}},'requests':rejected})
    for case in cases:case['requests'] += ['{','[','{"id":}','{"id":1,}','{"id":1 "method":"x"}','{"id" 1}','{"id":1}:','{"id":"abc}','{"id":"\\q"}','{"id":1e}','{"id":01}']
    for case in cases:case['expected']=run(case['server'],case['context'],case['requests'])
    FIXTURE.write_text(json.dumps(cases,indent=2)+'\n');print('Captured',sum(len(c['expected']['replies']) for c in cases),'replies')
else:
    for case in json.loads(FIXTURE.read_text()):
        actual=run(case['server'],case['context'],case['requests'])
        for group in actual:
            if actual[group] != case['expected'][group]:
                if isinstance(actual[group],list):
                    for index,(a,b) in enumerate(zip(actual[group],case['expected'][group])):
                        assert a==b, f"{case['server']} {group}[{index}]: {a!r} != {b!r}"
                raise AssertionError(f"{case['server']} {group} mismatch: {actual[group]!r} != {case['expected'][group]!r}")
    print('All captured MCP exchanges and journal effects matched')
