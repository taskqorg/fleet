import {test} from 'node:test';import assert from 'node:assert/strict';import {mkdtempSync,mkdirSync,writeFileSync,readFileSync,existsSync,statSync} from 'node:fs';import {join} from 'node:path';
import {createApp,peopleFile,OWNER,sha256} from './test-app.ts';
const VALUE='tvly-test-0123456789abcdef',FP=sha256(VALUE).slice(0,12);
const setup=(extra:any={})=>{const secretDir=mkdtempSync('/tmp/fleet-secret-'),gatewayDir=mkdtempSync('/tmp/fleet-gateway-');
 const config={database:mkdtempSync('/tmp/fleet-connections-')+'/db',browserSecret:'browser',hostId:'mac',secretDir,gatewayDir,...extra};const app=createApp(config);
 const op=async(name:string,b:any={},code=200,person?:string)=>{const r=await app.inject({method:'POST',url:'/api/ops/'+name,headers:{authorization:'Bearer browser',...(person?{'x-fleet-person':person}:{})},payload:b});assert.equal(r.statusCode,code,r.body);return r.json()};
 const snapshot=()=>JSON.parse(readFileSync(join(gatewayDir,'snapshot.json'),'utf8'));
 return {app,op,secretDir,gatewayDir,snapshot}};
const settle=()=>new Promise(r=>setTimeout(r,20));

test('import: a key file in the secret directory registers the tavily connection held by the owner',async()=>{
 const secretDir=mkdtempSync('/tmp/fleet-secret-');mkdirSync(join(secretDir,'connections','tavily'),{recursive:true});
 writeFileSync(join(secretDir,'connections','tavily',FP),VALUE+'\n',{mode:0o600});writeFileSync(join(secretDir,'connections','tavily','0123456789ab'),'not this key\n');
 const f=setup({secretDir});try{await f.app.ready();
  const [c]=(await f.op('connections_list')).connections;
  assert.equal(c.name,'tavily');assert.equal(c.holder,OWNER);assert.deepEqual(c.access.projects,['*']);assert.deepEqual(c.tools,['web_search','web_extract']);
  assert.deepEqual(c.keys.map((k:any)=>[k.fingerprint,k.state]),[[FP,'active']]);// the mismatched file is ignored
  assert.ok(!JSON.stringify(c).includes(VALUE));
  const s=f.snapshot();assert.equal(s.schema,2);assert.deepEqual(s.connections.tavily.keys,[{id:FP,state:'active'}]);assert.equal(s.providers.tavily.tools.web_search.path,'/search');
  assert.ok(!readFileSync(join(f.gatewayDir,'snapshot.json'),'utf8').includes(VALUE));
 }finally{await f.app.close()}});

test('key add returns only the fingerprint, writes a 0600 file; rotation and retire; snapshot follows',async()=>{const f=setup();try{
 await f.op('connections_create',{name:'tavily',provider:'tavily'});
 await f.op('connections_create',{name:'tavily',provider:'tavily'},409);await f.op('connections_create',{name:'x',provider:'nope'},400);
 const k=await f.op('connections_key_add',{connection:'tavily',value:VALUE});
 assert.deepEqual([k.fingerprint,k.state],[FP,'active']);assert.ok(!JSON.stringify(k).includes(VALUE));
 const file=join(f.secretDir,'connections','tavily',FP);assert.equal(readFileSync(file,'utf8').trim(),VALUE);assert.equal(statSync(file).mode&0o777,0o600);
 await f.op('connections_key_add',{connection:'tavily',value:VALUE},409);await f.op('connections_key_add',{connection:'tavily',value:'has space here'},400);
 await f.op('connections_key_state',{keyId:FP,state:'draining'});await settle();assert.deepEqual(f.snapshot().connections.tavily.keys,[{id:FP,state:'draining'}]);
 await f.op('connections_key_state',{keyId:FP,state:'disabled'},400);
 await f.op('connections_key_state',{keyId:FP,state:'retired'});await settle();assert.ok(!existsSync(file));assert.deepEqual(f.snapshot().connections.tavily.keys,[]);
 await f.op('connections_key_state',{keyId:FP,state:'active'},409);
 const [c]=(await f.op('connections_list')).connections;assert.equal(c.keys[0].state,'retired');assert.deepEqual(c.history.map((h:any)=>h.value),['draining','retired']);
 await f.op('connections_set_access',{connection:'tavily',access:{projects:['p1'],people:['A@x.test']}});
 await f.op('connections_set_intake',{connection:'tavily',intake:'paused'});await settle();assert.equal(f.snapshot().connections.tavily.intake,'paused');
 assert.deepEqual((await f.op('connections_list')).connections[0].access,{people:['a@x.test'],projects:['p1'],experiments:[]});
}finally{await f.app.close()}});

test('key values are owner only; API keys are architect views',async()=>{
 const people=peopleFile([{email:OWNER,name:'Owner',roles:['operator','architect'],owner:true},{email:'arch@fleet.test',roles:['architect']},{email:'op@fleet.test',roles:['operator']}]);
 const f=setup({peopleFile:people});try{await f.op('connections_create',{name:'tavily',provider:'tavily'});
  await f.op('connections_key_add',{connection:'tavily',value:VALUE},403,'arch@fleet.test');
  await f.op('connections_list',{},403,'op@fleet.test');
  assert.equal((await f.op('connections_list',{},200,'arch@fleet.test')).connections.length,1);
  await f.op('connections_set_intake',{connection:'tavily',intake:'paused'},403,'arch@fleet.test');// not the holder
  // A key the gateway disabled comes back only through the owner, also by way of draining.
  await f.op('connections_key_add',{connection:'tavily',value:VALUE});await f.op('connections_set_access',{connection:'tavily',access:{}});
  const store=(f.app as any).store;await store.tx(async()=>store.put('connection-key',{...(await store.get('key:'+FP,'connection-key')),state:'disabled',disabledReason:'401'}));
  // holder change: give the architect the connection
  await store.tx(async()=>store.put('api-connection',{...(await store.get('connection:tavily','api-connection')),holder:'arch@fleet.test'}));
  await f.op('connections_key_state',{keyId:FP,state:'draining'},403,'arch@fleet.test');await f.op('connections_key_state',{keyId:FP,state:'active'},403,'arch@fleet.test');
  await f.op('connections_set_intake',{connection:'tavily',intake:'paused'},200,'arch@fleet.test');// the holder may
  await f.op('connections_key_state',{keyId:FP,state:'active'});
  await settle();
 }finally{await f.app.close()}});

test('run tokens: access filters connections, the snapshot holds only the hash, ended runs are revoked; spool ingest is idempotent and disables refused keys',async()=>{const f=setup({connectionTickMs:0});try{await f.app.ready();
 const c=(f.app as any).connections,store=(f.app as any).store;
 await f.op('connections_create',{name:'tavily',provider:'tavily'});await f.op('connections_key_add',{connection:'tavily',value:VALUE});
 await f.op('connections_create',{name:'other',provider:'tavily'});await f.op('connections_set_access',{connection:'other',access:{projects:['p2']}});
 await f.op('connections_set_access',{connection:'tavily',access:{projects:['*']}});
 await store.tx(()=>store.put('execution',{id:'exec-1',executionId:'exec-1',sessionId:'s',hostId:'mac',state:'running',createdAt:Date.now()}));
 const run=await store.tx(()=>c.mint({executionId:'exec-1',directedBy:OWNER,projectId:'p1',experimentId:null},['tavily','other','absent']));
 assert.deepEqual(run.connections.map((x:any)=>x.name),['tavily']);assert.deepEqual(run.connections[0].tools.map((t:any)=>t.name),['web_search','web_extract']);
 await c.writeSnapshot();const tok=f.snapshot().tokens;assert.equal(tok.length,1);assert.equal(tok[0].sha256,sha256(run.token));assert.ok(!JSON.stringify(f.snapshot()).includes(run.token));
 assert.deepEqual(await store.tx(()=>c.mint({executionId:'exec-1',directedBy:'x@y.z',projectId:null},['other'])),{token:null,connections:[],refused:['other']});assert.deepEqual(run.refused,['other','absent']);
 // Spool: one ok call and one that disabled the key; read twice, recorded once.
 mkdirSync(join(f.gatewayDir,'state','spool'),{recursive:true});const sp=join(f.gatewayDir,'state','spool',new Date().toISOString().slice(0,10)+'.jsonl');
 const line=(callId:string,extra:any)=>JSON.stringify({callId,executionId:'exec-1',tokenSha256:sha256(run.token),connection:'tavily',tool:'web_search',status:200,outcome:'ok',usage:{unit:'credits',amount:2},...extra});
 writeFileSync(sp,line('a',{keyProblem:{key:FP,status:403,message:'forbidden domain'}})+'\n'+line('b',{status:502,outcome:'error',errorCode:'key-disabled-401',usage:null,disabled:{key:FP,status:401}})+'\n');
 await store.atomic(()=>c.tick());await store.atomic(()=>c.tick());
 const rows=await store.query("SELECT e.id,e.parent_execution_id AS parent,e.project_id,t.connection_cost,t.connection_cost_basis AS basis FROM execution e JOIN execution_totals t ON t.execution_id=e.id WHERE e.kind='tool_run' ORDER BY e.id");
 assert.deepEqual(rows.map((r:any)=>[r.id,r.parent,r.project_id,r.basis]),[['call:a','exec-1','p1','measured'],['call:b','exec-1','p1','unavailable']]);assert.equal(rows[0].connection_cost.credits,2);
 const k=(await f.op('connections_list')).connections.find((x:any)=>x.name==='tavily').keys[0];assert.equal(k.state,'disabled');assert.match(k.disabledReason,/401/);assert.equal(k.problem.message,'forbidden domain');
 const qs=(await f.op('questions_list',{})).questions.filter((q:any)=>q.cause==='key:'+FP+':disabled');assert.equal(qs.length,1);
 // The run ends: the next tick revokes its token.
 await store.tx(async()=>store.put('execution',{...(await store.get('exec-1','execution')),state:'completed'}));await store.atomic(()=>c.tick());await settle();
 assert.deepEqual(f.snapshot().tokens,[]);
}finally{await settle();await f.app.close()}});

test('reconcile: provider usage since the first check minus credits recorded for the key; unknown stays unknown',async()=>{
 const {createServer}=await import('node:http');let usage=100,fail=false;
 const srv=createServer((_q,r)=>{r.setHeader('content-type','application/json');r.end(JSON.stringify({at:Date.now(),keys:[fail?{connection:'tavily',key:FP,usage:null,unknown:'usage HTTP 500'}:{connection:'tavily',key:FP,usage}]}))});
 await new Promise<void>(r=>srv.listen(0,'127.0.0.1',r));
 const f=setup({gatewayUrl:`http://127.0.0.1:${(srv.address() as any).port}`,connectionTickMs:0,reconcileMs:0});try{await f.app.ready();const c=(f.app as any).connections,store=(f.app as any).store;
  await f.op('connections_create',{name:'tavily',provider:'tavily'});await f.op('connections_key_add',{connection:'tavily',value:VALUE});
  await c.reconcile();const key=async()=>(await f.op('connections_list')).connections[0].keys[0];
  assert.deepEqual((await key()).reconcile.difference,0);
  // 7 credits used at the provider, 2 of them through Fleet: 5 elsewhere.
  await store.tx(async()=>{await store.executions.createExecution({id:'call:x',kind:'tool_run',state:'succeeded',data:{keyFingerprint:FP,startedAt:Date.now()+1}});
   await store.query(`INSERT INTO execution_totals(execution_id,connection_cost,connection_cost_basis) VALUES('call:x',$1,'measured')`,[JSON.stringify({unit:'credits',credits:2,amount:2})])});
  usage=107;await c.reconcile();const k=await key();assert.deepEqual([k.reconcile.providerDelta,k.reconcile.recorded,k.reconcile.difference],[7,2,5]);
  fail=true;await c.reconcile();assert.equal((await key()).reconcile.unknown,'usage HTTP 500');
 }finally{await settle();await f.app.close();srv.close()}});
