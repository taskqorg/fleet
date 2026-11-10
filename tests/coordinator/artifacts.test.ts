import {test} from 'node:test';import assert from 'node:assert/strict';import {createHash} from 'node:crypto';import {mkdtempSync,mkdirSync,writeFileSync} from 'node:fs';import {tmpdir} from 'node:os';
import {createApp} from './test-app.ts';import {fakeStore} from './s3-fake.ts';import {upload} from './artifact-helpers.ts';
const sha=(b:Buffer)=>createHash('sha256').update(b).digest('hex');
async function setup(opts:{access?:boolean,taskArtifacts?:string[]}={}){const dir=mkdtempSync(tmpdir()+'/fleet-artifacts-');mkdirSync(dir+'/projects/p1',{recursive:true});
 writeFileSync(dir+'/projects/p1/project.json',JSON.stringify({id:'p1',name:'P1',contextFiles:[]}));
 let accessFile:string|undefined;if(opts.access){accessFile=dir+'/cf-access.env';writeFileSync(accessFile,"export CF_ACCESS_CLIENT_ID='cid'\nexport CF_ACCESS_CLIENT_SECRET='csecret'\n")}
 const app=createApp({database:'memory://',browserSecret:'b',machineSecret:'m',hostId:'mac',projectConfigDirectory:dir,placementIntervalMs:0,cfAccessFile:accessFile,defaultArtifactLocations:['mac'],nativeHostIds:['pc'],storeEndpointHosts:['127.0.0.1']});await app.ready();const s=(app as any).store,A=(app as any).artifacts;
 const fake=await fakeStore(opts.access?{requireAccess:{id:'cid',secret:'csecret'}}:{});
 await s.put('session',{id:'s1',createdAt:1,projectId:'p1'});await s.put('execution',{id:'e1',executionId:'e1',sessionId:'s1',hostId:'mac',state:'started',createdAt:1});
 await s.put('command',{id:'c1',executionId:'e1',sessionId:'s1',hostId:'mac',kind:'start',status:'started',payload:{}});
 const call=(url:string,payload:any)=>app.inject({method:'POST',url,headers:{authorization:'Bearer m'},payload});
 return {app,s,A,fake,call,dir}}
// Registers the fake as the mac store with a tunnel endpoint distinct from loopback when asked.
const register=(call:any,fake:any,localEndpoint?:string)=>call('/api/hosts/stores',{hostId:'mac',store:{id:'mac-store',bucket:'fleet',endpoint:fake.url,localEndpoint:localEndpoint??fake.url,accessKeyId:'k',secretAccessKey:'s',capacityBytes:1e12,usedBytes:0}});

test('upload: staging, re-hash by the store host, promotion to the content key; multipart; dedupe; reads stream',async()=>{const {app,s,A,fake,call}=await setup();await register(call,fake);
 const big=Buffer.alloc(70*1024*1024,7);big.write('fleet',0);
 const id=await upload(app,'m',{hostId:'mac',executionId:'e1',path:'model.glb',content:big,mediaType:'model/gltf-binary'});
 const row=await A.get(id);assert.deepEqual([row.state,row.size,row.sha256,row.availability,row.scope],['stored',big.length,sha(big),'available','prod']);
 // An empty file is stored at once by the coordinator (stores refuse an empty multipart part).
 await s.put('execution',{id:'e2',executionId:'e2',sessionId:'s1',hostId:'mac',state:'started',createdAt:1});const empty=await upload(app,'m',{hostId:'mac',executionId:'e2',path:'script.stderr',content:''});assert.equal((await A.get(empty)).state,'stored');assert.ok(fake.objects.get(`p1/prod/${sha(Buffer.alloc(0))}`)?.length===0);
 const key=`p1/prod/${sha(big)}`;assert.ok(fake.objects.get(key)?.equals(big));assert.ok(![...fake.objects.keys()].some(k=>k.includes('.pending/')),'staging removed');
 assert.ok(fake.requests.filter(r=>r.method==='PUT'&&r.url.includes('partNumber=')).length>=3,'three 32 MiB parts');
 assert.ok((await A.readBuffer(id)).equals(big));
 // The same content again: stored at once, nothing uploaded.
 const puts=fake.requests.length;const again=await (await call('/api/hosts/artifacts/reserve',{hostId:'mac',executionId:'e1',name:'copy.glb',size:big.length,sha256:sha(big)})).json();
 assert.equal(again.state,'stored');assert.equal(fake.requests.length,puts);
 // Results stream with the old sandbox headers; the legacy record shape serves session reads.
 const small=await upload(app,'m',{hostId:'mac',executionId:'e1',path:'index.html',content:'<h1>x</h1>',mediaType:'text/html'});
 const r=await app.inject({url:'/api/results/'+small,headers:{authorization:'Bearer b'}});assert.equal(r.statusCode,200);assert.equal(r.body,'<h1>x</h1>');assert.match(String(r.headers['content-security-policy']),/sandbox/);
 assert.deepEqual((await (app as any).store.all('artifact',{sessionId:'s1'})).map((x:any)=>x.path),['model.glb','script.stderr','copy.glb','index.html']);
 await app.close()});

test('a wrong hash fails and never overwrites a verified object; sizes, leases and stores are checked',async()=>{const {app,s,A,fake,call}=await setup();await register(call,fake);
 const good=Buffer.from('good content'),id=await upload(app,'m',{hostId:'mac',executionId:'e1',path:'a.txt',content:good});
 // A second artifact claims the same hash but uploads other bytes: the store host's digest differs, it fails, the object stays.
 const r=await (await call('/api/hosts/artifacts/reserve',{hostId:'mac',executionId:'e1',name:'b.txt',size:good.length,sha256:sha(Buffer.from('other'))})).json();
 const liar=Buffer.from('bad content!');const fakeHash=sha(good);
 const r2=await (await call('/api/hosts/artifacts/reserve',{hostId:'mac',executionId:'e1',name:'c.txt',size:liar.length,sha256:sha(Buffer.from('never'))})).json();
 const [p]=(await (await call('/api/hosts/artifacts/parts',{hostId:'mac',artifactId:r2.artifactId,partNumbers:[1]})).json()).parts;const put=await fetch(p.url,{method:'PUT',body:new Uint8Array(liar)});
 const c=await (await call('/api/hosts/artifacts/commit',{hostId:'mac',artifactId:r2.artifactId,parts:[{partNumber:1,etag:put.headers.get('etag')}]})).json();
 const v=await (await call('/api/hosts/artifacts/verified',{hostId:'mac',artifactId:r2.artifactId,storeId:c.verify.storeId,sha256:sha(liar)})).json();assert.equal(v.state,'failed');
 assert.equal((await A.get(r2.artifactId)).state,'failed');assert.ok(fake.objects.get(`p1/prod/${fakeHash}`)?.equals(good));assert.ok(![...fake.objects.keys()].some(k=>k.includes('.pending/')));
 // A digest for an already verified copy changes nothing.
 assert.equal((await (await call('/api/hosts/artifacts/verified',{hostId:'mac',artifactId:id,storeId:'mac-store',sha256:fakeHash})).json()).state,'verified');
 // A size that differs from the reservation fails at commit.
 const r3=await (await call('/api/hosts/artifacts/reserve',{hostId:'mac',executionId:'e1',name:'d.txt',size:100,sha256:sha(Buffer.from('x'))})).json();
 const [p3]=(await (await call('/api/hosts/artifacts/parts',{hostId:'mac',artifactId:r3.artifactId,partNumbers:[1]})).json()).parts;const put3=await fetch(p3.url,{method:'PUT',body:new Uint8Array(Buffer.from('x'))});
 assert.equal((await (await call('/api/hosts/artifacts/commit',{hostId:'mac',artifactId:r3.artifactId,parts:[{partNumber:1,etag:put3.headers.get('etag')}]})).json()).state,'failed');
 // An expired lease aborts the upload.
 await s.query("UPDATE artifact_copy SET lease_expires_at=now()-interval '1 minute' WHERE artifact_id=$1",[r.artifactId]);assert.equal(await A.expireLeases(),1);assert.equal((await A.get(r.artifactId)).state,'failed');
 // Unreachable or full stores take no uploads.
 await call('/api/hosts/poll',{hostId:'mac',stores:[{id:'mac-store',reachable:false}]});
 assert.equal((await call('/api/hosts/artifacts/reserve',{hostId:'mac',executionId:'e1',name:'e.txt',size:1,sha256:sha(Buffer.from('y'))})).statusCode,409);
 // Full only below the operator's store.free_space_warning: none set, never full; set, capacity left 10 < 100 is full.
 await call('/api/hosts/poll',{hostId:'mac',stores:[{id:'mac-store',reachable:true,capacityBytes:10,usedBytes:11}]});
 assert.equal((await (await (app as any).stores.get('mac-store'))).state,'reachable');
 const op=(name:string,payload:any)=>app.inject({method:'POST',url:'/api/ops/'+name,headers:{authorization:'Bearer b'},payload});
 assert.equal((await (await op('store_set_free_space_warning',{storeId:'mac-store',bytes:100})).json()).state,'full');
 await call('/api/hosts/poll',{hostId:'mac',stores:[{id:'mac-store',reachable:true,capacityBytes:10}]});
 assert.equal((await (await (app as any).stores.get('mac-store'))).state,'full');
 assert.ok((await (await op('stores_list',{})).json()).problems.some((p:any)=>p.kind==='store-full'&&p.storeId==='mac-store'));
 assert.equal((await call('/api/hosts/artifacts/reserve',{hostId:'mac',executionId:'e1',name:'e.txt',size:1,sha256:sha(Buffer.from('y'))})).statusCode,409);
 await app.close()});

test('purge removes every copy, refuses the hash afterwards, and waits for offline stores',async()=>{const {app,s,A,fake,call}=await setup();await register(call,fake);
 const leak=Buffer.from('token=secret'),id=await upload(app,'m',{hostId:'mac',executionId:'e1',path:'leak.txt',content:leak});
 await call('/api/hosts/poll',{hostId:'mac',stores:[{id:'mac-store',reachable:false}]});
 const r=await (await app.inject({method:'POST',url:'/api/ops/artifact_purge',headers:{authorization:'Bearer b'},payload:{contentHash:sha(leak),reason:'leaked token'}})).json();assert.deepEqual(r,{artifacts:1,deleted:0,pending:1});
 assert.equal((await A.get(id)).state,'deleted');assert.ok(fake.objects.has(`p1/prod/${sha(leak)}`),'offline store not touched yet');
 await call('/api/hosts/poll',{hostId:'mac',stores:[{id:'mac-store',reachable:true}]});await (app as any).placement.runPlacement();
 assert.ok(!fake.objects.has(`p1/prod/${sha(leak)}`),'deleted when the store came back');
 assert.equal((await call('/api/hosts/artifacts/reserve',{hostId:'mac',executionId:'e1',name:'leak2.txt',size:leak.length,sha256:sha(leak)})).statusCode,409);await app.close()});

test('Access headers go to tunnel requests after signing and are never signed; loopback URLs carry none',async()=>{const {app,call,fake}=await setup({access:true});
 const local=await fakeStore();await register(call,fake,local.url);
 const data=Buffer.from('through the tunnel');const r=await (await call('/api/hosts/artifacts/reserve',{hostId:'mac',executionId:'e1',name:'t.txt',size:data.length,sha256:sha(data)})).json();assert.equal(r.state,'pending');
 const create=fake.requests.find(x=>x.method==='POST'&&x.url.includes('uploads'))!;
 assert.equal(create.headers['cf-access-client-id'],'cid');assert.equal(create.headers['cf-access-client-secret'],'csecret');
 assert.doesNotMatch(String(create.headers.authorization),/cf-access/i);assert.match(String(create.headers.authorization),/SignedHeaders=/);
 // The store host gets loopback part URLs; another host gets the tunnel.
 const mine=(await (await call('/api/hosts/artifacts/parts',{hostId:'mac',artifactId:r.artifactId,partNumbers:[1]})).json()).parts[0].url;assert.ok(mine.startsWith(local.url));
 assert.doesNotMatch(mine,/cf-access/i);await app.close()});

test('success rule: a completed run waits for its required artifacts, then completes; missing or failed ones fail it',async()=>{const {app,s,A,fake,call}=await setup();await register(call,fake);
 const data=Buffer.from('required output');
 const r=await (await call('/api/hosts/artifacts/reserve',{hostId:'mac',executionId:'e1',name:'out.bin',size:data.length,sha256:sha(data),required:true})).json();
 await call('/api/hosts/receipts',{hostId:'mac',commandId:'c1',executionId:'e1',status:'completed'});
 let e=await s.get('e1','execution');assert.deepEqual([e.state,e.waitReason],['waiting','artifacts']);assert.equal((await s.executions.get('e1')).state,'waiting');
 const [p]=(await (await call('/api/hosts/artifacts/parts',{hostId:'mac',artifactId:r.artifactId,partNumbers:[1]})).json()).parts;const put=await fetch(p.url,{method:'PUT',body:new Uint8Array(data)});
 const c=await (await call('/api/hosts/artifacts/commit',{hostId:'mac',artifactId:r.artifactId,parts:[{partNumber:1,etag:put.headers.get('etag')}]})).json();
 await call('/api/hosts/artifacts/verified',{hostId:'mac',artifactId:r.artifactId,storeId:c.verify.storeId,sha256:sha(data)});
 e=await s.get('e1','execution');assert.equal(e.state,'completed');assert.equal(e.waitReason,undefined);assert.equal((await s.executions.get('e1')).state,'succeeded');
 // A task pattern with no matching artifact fails the run; a window that passes fails a waiting one.
 await s.tx(async()=>{await s.tasks.create({id:'t1',title:'needs a model',projectId:'p1',completionRequirements:{artifacts:['*.glb']}});await s.put('session',{id:'s2',createdAt:1,projectId:'p1',deliveryId:'t1'});
  await s.put('execution',{id:'e2',executionId:'e2',sessionId:'s2',hostId:'mac',state:'started',createdAt:1});await s.put('command',{id:'c2',executionId:'e2',sessionId:'s2',hostId:'mac',kind:'start',status:'started',payload:{}})});
 await call('/api/hosts/receipts',{hostId:'mac',commandId:'c2',executionId:'e2',status:'completed'});
 e=await s.get('e2','execution');assert.equal(e.state,'failed');assert.deepEqual(e.error.missing,['*.glb']);
 // An operator acceptance of a task's stored model completes the task.
 await s.tx(async()=>{await s.put('session',{id:'s4',createdAt:1,projectId:'p1',deliveryId:'t1'});await s.put('execution',{id:'e4',executionId:'e4',sessionId:'s4',hostId:'mac',state:'started',createdAt:1,startedAt:1});await s.put('command',{id:'c4',executionId:'e4',sessionId:'s4',hostId:'mac',kind:'start',status:'started',payload:{}})});
 const glb=await upload(app,'m',{hostId:'mac',executionId:'e4',path:'model.glb',content:Buffer.alloc(1024,3),mediaType:'model/gltf-binary'});
 await call('/api/hosts/receipts',{hostId:'mac',commandId:'c4',executionId:'e4',status:'completed',artifacts:['model.glb']});assert.equal((await s.get('e4','execution')).state,'completed');
 const ask=await (await app.inject({method:'POST',url:'/api/ops/task_request_acceptance',headers:{authorization:'Bearer b'},payload:{taskId:'t1'}})).json();assert.deepEqual(ask.artifactIds,[glb]);
 await app.inject({method:'POST',url:'/api/ops/questions_answer',headers:{authorization:'Bearer b'},payload:{questionId:ask.id,answer:'accepted'}});
 const task=await s.tasks.get('t1');assert.deepEqual([task.state,task.acceptedArtifactIds],['completed',[glb]]);
 await s.tx(async()=>{await s.put('execution',{id:'e3',executionId:'e3',sessionId:'s1',hostId:'mac',state:'started',createdAt:1});await s.put('command',{id:'c3',executionId:'e3',sessionId:'s1',hostId:'mac',kind:'start',status:'started',payload:{}})});
 await call('/api/hosts/artifacts/reserve',{hostId:'mac',executionId:'e3',name:'late.bin',size:3,sha256:sha(Buffer.from('abc')),required:true});
 await call('/api/hosts/receipts',{hostId:'mac',commandId:'c3',executionId:'e3',status:'completed'});assert.equal((await s.get('e3','execution')).state,'waiting');
 await s.query("UPDATE execution SET data=jsonb_set(data,'{waitingSince}','1') WHERE id='e3'");await (app as any).placement.runPlacement();
 e=await s.get('e3','execution');assert.equal(e.state,'failed');assert.equal(e.error.message,'artifact retry window passed');await app.close()});

test('backfill moves base64 documents to a store; readers see them before and after',async()=>{const {app,s,A,fake,call}=await setup();
 const html=Buffer.from('<h1>old</h1>');await s.put('artifact',{id:'artifact:e1:index.html',executionId:'e1',sessionId:'s1',path:'index.html',mediaType:'text/html',sha256:sha(html),size:html.length,contentBase64:html.toString('base64')});
 assert.equal((await app.inject({url:'/api/results/artifact:e1:index.html',headers:{authorization:'Bearer b'}})).body,'<h1>old</h1>');
 await register(call,fake);assert.equal(await A.backfill(),1);assert.equal(await A.backfill(),0);
 assert.ok(fake.objects.get(`p1/prod/${sha(html)}`)?.equals(html));
 assert.equal((await app.inject({url:'/api/results/artifact:e1:index.html',headers:{authorization:'Bearer b'}})).body,'<h1>old</h1>');
 assert.equal((await s.query("SELECT count(*)::int AS n FROM document WHERE kind='artifact'"))[0].n,0);
 assert.deepEqual((await s.all('artifact',{executionId:'e1'})).map((a:any)=>[a.id,a.path]),[['artifact:e1:index.html','index.html']]);await app.close()});

test('store endpoints are allowlisted; hosts read only artifacts of their own command or execution; reads are bounded',async()=>{const {app,s,A,fake,call}=await setup();
 assert.equal((await call('/api/hosts/stores',{hostId:'mac',store:{id:'mac-store',bucket:'fleet',endpoint:'https://attacker.example',accessKeyId:'k',secretAccessKey:'s'}})).statusCode,403);
 assert.equal((await call('/api/hosts/stores',{hostId:'mac',store:{id:'mac-store',bucket:'fleet',endpoint:fake.url,localEndpoint:'http://10.0.0.5:3900',accessKeyId:'k',secretAccessKey:'s'}})).statusCode,400);
 await register(call,fake);const id=await upload(app,'m',{hostId:'mac',executionId:'e1',path:'secret.txt',content:'project p1 only'});
 // Host pc has no command naming the artifact and no execution in its project.
 assert.equal((await call('/api/hosts/artifacts/read',{hostId:'pc',artifactId:id})).statusCode,403);
 await s.put('session',{id:'s9',createdAt:1});await s.put('execution',{id:'e9',executionId:'e9',sessionId:'s9',hostId:'pc',state:'started',createdAt:1});
 assert.equal((await call('/api/hosts/artifacts/read',{hostId:'pc',executionId:'e9',artifactId:id})).statusCode,403);
 await s.put('command',{id:'c9',executionId:'e9',sessionId:'s9',hostId:'pc',kind:'delivery-step',status:'pending',payload:{files:[{artifactId:id}]}});
 const ok=await call('/api/hosts/artifacts/read',{hostId:'pc',commandId:'c9',artifactId:id});assert.equal(ok.statusCode,200);assert.equal(ok.body,'project p1 only');
 assert.equal((await call('/api/hosts/artifacts/read',{hostId:'mac',executionId:'e1',artifactId:id})).statusCode,200);
 // Only structured fields of a command in progress count.
 await s.put('command',{id:'c10',executionId:'e9',sessionId:'s9',hostId:'pc',kind:'delivery-step',status:'pending',payload:{note:'"'+id+'"'}});
 assert.equal((await call('/api/hosts/artifacts/read',{hostId:'pc',commandId:'c10',artifactId:id})).statusCode,403);
 await s.put('command',{id:'c9',executionId:'e9',sessionId:'s9',hostId:'pc',kind:'delivery-step',status:'completed',payload:{files:[{artifactId:id}]}});
 assert.equal((await call('/api/hosts/artifacts/read',{hostId:'pc',commandId:'c9',artifactId:id})).statusCode,403);
 await assert.rejects(A.readBuffer(id,4),(e:any)=>e.statusCode===413);await app.close()});

test('a permanent store refusal reported by the host fails the artifact at once with the reason',async()=>{const {app,s,A,fake,call}=await setup();await register(call,fake);
 const data=Buffer.from('refused bytes');
 const r=(await call('/api/hosts/artifacts/reserve',{hostId:'mac',executionId:'e1',name:'out.txt',mediaType:'text/plain',size:data.length,sha256:sha(data),required:true})).json();assert.equal(r.state,'pending');
 const other=await app.inject({method:'POST',url:'/api/hosts/artifacts/failed',headers:{authorization:'Bearer m'},payload:{hostId:'pds18',artifactId:r.artifactId,error:'x'}});assert.notEqual(other.statusCode,200);
 const done=(await call('/api/hosts/artifacts/failed',{hostId:'mac',artifactId:r.artifactId,error:'store 400 InvalidRequest'})).json();assert.equal(done.state,'failed');
 const row=await A.get(r.artifactId);assert.equal(row.state,'failed');assert.match(row.error,/upload refused: store 400 InvalidRequest/);
 assert.equal((await call('/api/hosts/artifacts/failed',{hostId:'mac',artifactId:r.artifactId,error:'again'})).json().state,'failed');
 await app.close();await fake.close()});
