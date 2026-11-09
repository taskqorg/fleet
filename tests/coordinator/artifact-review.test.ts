// Review fixes of PR #25: idempotent commit, final receipts, required declarations, failed-copy backoff and retry, invalid
// rules reported, lease renewal.
import {test} from 'node:test';import assert from 'node:assert/strict';import {createHash} from 'node:crypto';import {mkdtempSync,mkdirSync,writeFileSync} from 'node:fs';import {tmpdir} from 'node:os';
import {createApp} from '../../packages/coordinator/app.js';import {fakeStore} from './s3-fake.js';import {upload} from './artifact-helpers.js';
const sha=(b:Buffer)=>createHash('sha256').update(b).digest('hex');
async function setup(){const dir=mkdtempSync(tmpdir()+'/fleet-review-');
 const project=(id:string,locations:string[],extra:any={})=>{mkdirSync(dir+'/projects/'+id,{recursive:true});writeFileSync(dir+'/projects/'+id+'/project.json',JSON.stringify({id,name:id,contextFiles:[],artifact_locations:locations,...extra}))};
 project('win-only',['windows']);project('both',['windows','mac'],{artifact_rules:[{scope:'prod',older_than_days:0,action:'move'}]});
 const app=createApp({database:'memory://',browserSecret:'b',machineSecret:'m',hostId:'mac',nativeHostIds:['windows'],projectConfigDirectory:dir,placementIntervalMs:0,storeEndpointHosts:['127.0.0.1']});await app.ready();
 const s=(app as any).store,P=(app as any).placement;const mac=await fakeStore(),win=await fakeStore();
 const call=(url:string,payload:any)=>app.inject({method:'POST',url,headers:{authorization:'Bearer m'},payload});
 const op=async(name:string,payload:any)=>app.inject({method:'POST',url:'/api/ops/'+name,headers:{authorization:'Bearer b'},payload});
 for(const [host,f] of [['mac',mac],['windows',win]] as const)await call('/api/hosts/stores',{hostId:host,store:{id:host+'-store',bucket:'fleet',endpoint:f.url,localEndpoint:f.url,accessKeyId:'k',secretAccessKey:'s',capacityBytes:1e12}});
 const run=async(projectId:string,id:string)=>{await s.put('session',{id:'s-'+id,createdAt:1,projectId});await s.put('execution',{id,executionId:id,sessionId:'s-'+id,hostId:'mac',state:'started',createdAt:1});
  await s.put('command',{id:'c-'+id,executionId:id,sessionId:'s-'+id,hostId:'mac',kind:'start',status:'started',payload:{}})};
 return {app,s,P,call,op,run}}

test('commit is idempotent: a retried commit hands out the verification again and queues it once',async()=>{const {app,s,call,run}=await setup();await run('win-only','e1');
 const data=Buffer.from('remote write');const r=await (await call('/api/hosts/artifacts/reserve',{hostId:'mac',executionId:'e1',name:'a.txt',size:data.length,sha256:sha(data)})).json();
 const [p]=(await (await call('/api/hosts/artifacts/parts',{hostId:'mac',artifactId:r.artifactId,partNumbers:[1]})).json()).parts;const put=await fetch(p.url,{method:'PUT',body:new Uint8Array(data)});
 const parts=[{partNumber:1,etag:put.headers.get('etag')}];
 for(let i=0;i<2;i++){const c=await call('/api/hosts/artifacts/commit',{hostId:'mac',artifactId:r.artifactId,parts});assert.equal(c.statusCode,200,c.body);assert.equal(c.json().state,'verifying')}
 const verifies=(await (await call('/api/hosts/poll',{hostId:'windows'})).json()).commands.filter((c:any)=>c.kind==='artifact-verify');assert.equal(verifies.length,1);
 // The writer is the store host: each commit answers with the verification itself.
 await run('win-only','e2');await s.put('execution',{...(await s.get('e2','execution')),hostId:'windows'});
 const r2=await (await call('/api/hosts/artifacts/reserve',{hostId:'windows',executionId:'e2',name:'b.txt',size:data.length,sha256:sha(data)})).json();
 const [p2]=(await (await call('/api/hosts/artifacts/parts',{hostId:'windows',artifactId:r2.artifactId,partNumbers:[1]})).json()).parts;const put2=await fetch(p2.url,{method:'PUT',body:new Uint8Array(data)});
 for(let i=0;i<2;i++)assert.ok((await (await call('/api/hosts/artifacts/commit',{hostId:'windows',artifactId:r2.artifactId,parts:[{partNumber:1,etag:put2.headers.get('etag')}]})).json()).verify);
 // Lease renewal extends a committed upload's lease.
 await s.query("UPDATE artifact_copy SET lease_expires_at=now()+interval '1 second' WHERE artifact_id=$1",[r2.artifactId]);
 assert.equal((await call('/api/hosts/artifacts/renew',{hostId:'windows',artifactId:r2.artifactId})).statusCode,200);
 assert.ok((await s.query("SELECT lease_expires_at > now()+interval '50 minutes' AS ok FROM artifact_copy WHERE artifact_id=$1",[r2.artifactId]))[0].ok);
 await app.close()});

test('a required declaration counts before its upload is reserved; repeated terminal receipts change nothing',async()=>{const {app,s,call,run}=await setup();await run('both','e1');
 assert.equal((await call('/api/hosts/artifacts/declare',{hostId:'mac',executionId:'e1',name:'model.glb'})).statusCode,200);
 const receipt=()=>call('/api/hosts/receipts',{hostId:'mac',commandId:'c-e1',executionId:'e1',status:'completed'});
 assert.equal((await receipt()).statusCode,200);const w=await s.get('e1','execution');assert.equal(w.state,'waiting');assert.deepEqual(w.pendingArtifacts,['model.glb']);
 await new Promise(r=>setTimeout(r,5));assert.equal((await receipt()).statusCode,200);assert.equal((await s.get('e1','execution')).waitingSince,w.waitingSince);
 await upload(app,'m',{hostId:'mac',executionId:'e1',path:'model.glb',content:'glb'});
 const done=await s.get('e1','execution');assert.equal(done.state,'completed');assert.equal((await s.query("SELECT required FROM artifact WHERE name='model.glb'"))[0].required,true);
 // A failed execution stays failed when the completed receipt is repeated.
 await run('both','e3');await s.put('execution',{...(await s.get('e3','execution')),state:'failed'});await s.put('command',{...(await s.get('c-e3','command')),status:'completed'});
 assert.equal((await call('/api/hosts/receipts',{hostId:'mac',commandId:'c-e3',executionId:'e3',status:'completed'})).statusCode,200);assert.equal((await s.get('e3','execution')).state,'failed');
 await app.close()});

test('a failed copy waits for its backoff and an operator retry; an invalid rule is reported and never stops the pass',async()=>{const {app,s,P,call,op,run}=await setup();await run('both','e1');
 const id=await upload(app,'m',{hostId:'mac',executionId:'e1',path:'x.bin',content:'bytes'});
 await s.query("INSERT INTO artifact_copy(artifact_id,store_id,project_id,state,last_error,attempts,next_attempt_at) VALUES($1,'windows-store','both','failed','store 500',3,now()+interval '1 hour') ON CONFLICT(artifact_id,store_id) DO UPDATE SET state='failed',last_error='store 500',attempts=3,next_attempt_at=now()+interval '1 hour'",[id]);
 const copies=async()=>(await (await call('/api/hosts/poll',{hostId:'windows'})).json()).commands.filter((c:any)=>c.kind==='artifact-copy').length;
 assert.equal((await P.runPlacement()).queued,0);assert.equal(await copies(),0);
 const problems=(await (await op('stores_list',{})).json()).problems;
 assert.ok(problems.some((p:any)=>p.kind==='artifact-copy-failed'&&p.artifactId===id&&p.attempts===3&&p.actions.includes('artifact_copy_retry')));
 assert.ok(problems.some((p:any)=>p.kind==='artifact-rule-invalid'&&p.projectId==='both'));
 assert.equal((await op('artifact_copy_retry',{artifactId:id,storeId:'windows-store'})).statusCode,200);
 assert.equal((await P.runPlacement()).queued,1);assert.equal(await copies(),1);
 // Confirming the invalid rule is refused.
 assert.equal((await op('artifact_rule_confirm',{projectId:'both',rule:{scope:'prod',older_than_days:0,action:'move'}})).statusCode,400);
 await app.close()});

test('a host reports edge redirects with its poll; hosts_list shows them until a poll without them',async()=>{const {app,call,op}=await setup();
 const hosts=async()=>(await (await op('hosts_list',{})).json()).hosts.find((h:any)=>h.id==='mac').problems;
 await call('/api/hosts/poll',{hostId:'mac',problems:[{kind:'edge-redirect',route:'/api/hosts/stores',status:302,location:'https://login.example/cdn-cgi/access/login'},{kind:'other'}]});
 assert.deepEqual(await hosts(),[{kind:'edge-redirect',route:'/api/hosts/stores',status:302,location:'https://login.example/cdn-cgi/access/login'}]);
 await call('/api/hosts/poll',{hostId:'mac',problems:[]});assert.deepEqual(await hosts(),[]);
 await app.close()});
