import {test} from 'node:test';import assert from 'node:assert/strict';import {createHash} from 'node:crypto';import {mkdtempSync,mkdirSync,writeFileSync} from 'node:fs';import {tmpdir} from 'node:os';
import {createApp} from '../../packages/coordinator/app.ts';import {fakeStore} from './s3-fake.ts';import {upload} from './artifact-helpers.ts';
const sha=(b:Buffer)=>createHash('sha256').update(b).digest('hex');
// CUJ 5 shape: a Mac and a Windows host, each with its own store; projects keep artifacts on [windows] or on [windows, mac].
async function setup(){const dir=mkdtempSync(tmpdir()+'/fleet-placement-');
 const project=(id:string,locations:string[],extra:any={})=>{mkdirSync(dir+'/projects/'+id,{recursive:true});writeFileSync(dir+'/projects/'+id+'/project.json',JSON.stringify({id,name:id,contextFiles:[],artifact_locations:locations,...extra}))};
 project('win-only',['windows']);project('both',['windows','mac']);project('lab-rules',['mac'],{artifact_rules:[{scope:'prod',older_than_days:0,action:'delete'}]});
 const app=createApp({database:'memory://',browserSecret:'b',machineSecret:'m',hostId:'mac',nativeHostIds:['windows'],projectConfigDirectory:dir,placementIntervalMs:0,storeEndpointHosts:['127.0.0.1']});await app.ready();
 const s=(app as any).store,P=(app as any).placement,A=(app as any).artifacts;const mac=await fakeStore(),win=await fakeStore();
 const call=(url:string,payload:any)=>app.inject({method:'POST',url,headers:{authorization:'Bearer m'},payload});
 for(const [host,f] of [['mac',mac],['windows',win]] as const)await call('/api/hosts/stores',{hostId:host,store:{id:host+'-store',bucket:'fleet',endpoint:f.url,localEndpoint:f.url,accessKeyId:'k',secretAccessKey:'s',capacityBytes:1e12,usedBytes:0,requiresLogon:host==='windows'}});
 const run=async(projectId:string,id:string)=>{await s.put('session',{id:'s-'+id,createdAt:1,projectId});await s.put('execution',{id,executionId:id,sessionId:'s-'+id,hostId:'mac',state:'started',createdAt:1})};
 // Plays the target host's executor for queued artifact-copy commands: download, upload to its own store, commit, re-hash.
 const executeCopies=async(host:string)=>{const commands=(await (await call('/api/hosts/poll',{hostId:host})).json()).commands.filter((c:any)=>c.kind==='artifact-copy');
  for(const c of commands){const p=c.payload,bytes=Buffer.from(await (await fetch(p.source.url)).arrayBuffer());
   const urls=(await (await call('/api/hosts/artifacts/parts',{hostId:host,artifactId:p.artifactId,storeId:p.storeId,partNumbers:[1]})).json()).parts;
   const put=await fetch(urls[0].url,{method:'PUT',body:new Uint8Array(bytes)});
   const done=await (await call('/api/hosts/artifacts/commit',{hostId:host,artifactId:p.artifactId,storeId:p.storeId,parts:[{partNumber:1,etag:put.headers.get('etag')}]})).json();
   const got=Buffer.from(await (await fetch(done.verify.url)).arrayBuffer());
   await call('/api/hosts/artifacts/verified',{hostId:host,artifactId:p.artifactId,storeId:p.storeId,sha256:sha(got)});
   await call('/api/hosts/receipts',{hostId:host,commandId:c.id,executionId:c.executionId,status:'completed'})}
  return commands.length};
 // Plays a store host's executor for artifact-verify commands: re-hash the staging object over loopback and report it.
 const executeVerifies=async(host:string)=>{const commands=(await (await call('/api/hosts/poll',{hostId:host})).json()).commands.filter((c:any)=>c.kind==='artifact-verify');
  for(const c of commands){const got=Buffer.from(await (await fetch(c.payload.url)).arrayBuffer());await call('/api/hosts/artifacts/verified',{hostId:host,artifactId:c.payload.artifactId,storeId:c.payload.storeId,sha256:sha(got)});await call('/api/hosts/receipts',{hostId:host,commandId:c.id,executionId:c.executionId,status:'completed'})}
  return commands.length};
 return {app,s,P,A,mac,win,call,run,executeCopies,executeVerifies,dir}}
const placed=async(A:any,id:string)=>(await A.get(id)).copies.filter((c:any)=>c.state==='verified').map((c:any)=>c.storeId).sort();

test('a [windows] project written from the Mac never lands on the Mac store',async()=>{const {app,P,A,mac,win,run,executeVerifies}=await setup();await run('win-only','e1');
 const data=Buffer.alloc(3*1024*1024,1),id=await upload(app,'m',{hostId:'mac',executionId:'e1',path:'bundle.bin',content:data});
 // The writer is not the store's host, so the Windows executor re-hashes it (P3-D1) before it counts as stored.
 assert.equal((await A.get(id)).state,'pending');assert.equal(await executeVerifies('windows'),1);
 assert.deepEqual(await placed(A,id),['windows-store']);await P.runPlacement();
 assert.equal([...mac.objects.keys()].length,0,'nothing on the Mac store');assert.ok(win.objects.get(`win-only/prod/${sha(data)}`)?.equals(data));
 // Windows logged out: no store in the list is reachable, so the upload waits (no fallback to the Mac).
 await app.inject({method:'POST',url:'/api/hosts/poll',headers:{authorization:'Bearer m'},payload:{hostId:'windows',stores:[{id:'windows-store',reachable:false}]}});
 assert.equal((await app.inject({method:'POST',url:'/api/hosts/artifacts/reserve',headers:{authorization:'Bearer m'},payload:{hostId:'mac',executionId:'e1',name:'x',size:1,sha256:sha(Buffer.from('z'))}})).statusCode,409);
 assert.equal([...mac.objects.keys()].length,0);await app.close()});

test('a [windows, mac] project reaches placed through a copy by the Windows executor; offline copies wait',async()=>{const {app,P,A,win,call,run,executeCopies}=await setup();await run('both','e1');
 const data=Buffer.from('placed on both'),id=await upload(app,'m',{hostId:'mac',executionId:'e1',path:'a.txt',content:data});
 assert.deepEqual(await placed(A,id),['mac-store']);
 await call('/api/hosts/poll',{hostId:'windows',stores:[{id:'windows-store',reachable:false}]});await P.runPlacement();
 assert.equal(await executeCopies('windows'),0,'no copy while Windows is offline');
 await call('/api/hosts/poll',{hostId:'windows',stores:[{id:'windows-store',reachable:true}]});await P.runPlacement();
 assert.equal(await executeCopies('windows'),1);assert.deepEqual(await placed(A,id),['mac-store','windows-store']);
 assert.ok(win.objects.get(`both/prod/${sha(data)}`)?.equals(data));
 await P.runPlacement();assert.equal(await executeCopies('windows'),0,'placed: nothing more to copy');await app.close()});

test('removing a location deletes copies there only behind a verified surviving copy; never the last one',async()=>{const {app,P,A,mac,win,call,run,executeCopies,dir}=await setup();await run('both','e1');
 const data=Buffer.from('moving home'),id=await upload(app,'m',{hostId:'mac',executionId:'e1',path:'m.txt',content:data});
 await P.runPlacement();await executeCopies('windows');assert.deepEqual(await placed(A,id),['mac-store','windows-store']);
 writeFileSync(dir+'/projects/both/project.json',JSON.stringify({id:'both',name:'both',contextFiles:[],artifact_locations:['windows']}));
 // The surviving Windows copy must be reachable and present: while it is offline the Mac copy stays.
 await call('/api/hosts/poll',{hostId:'windows',stores:[{id:'windows-store',reachable:false}]});await P.runPlacement();assert.deepEqual(await placed(A,id),['mac-store','windows-store']);
 await call('/api/hosts/poll',{hostId:'windows',stores:[{id:'windows-store',reachable:true}]});
 win.objects.clear();await P.runPlacement();assert.ok(mac.objects.has(`both/prod/${sha(data)}`),'a missing surviving object keeps the Mac copy');
 win.objects.set(`both/prod/${sha(data)}`,data);await P.runPlacement();
 assert.deepEqual(await placed(A,id),['windows-store']);assert.ok(!mac.objects.has(`both/prod/${sha(data)}`));await app.close()});

test('retention rules run only after the operator confirms their preview',async()=>{const {app,P,A,mac,run}=await setup();await run('lab-rules','e1');
 const data=Buffer.from('old output'),id=await upload(app,'m',{hostId:'mac',executionId:'e1',path:'old.txt',content:data});
 const rule={scope:'prod',older_than_days:0,action:'delete'};
 await P.runPlacement();assert.equal((await A.get(id)).state,'stored','unconfirmed rules do nothing');
 const preview=(await app.inject({method:'POST',url:'/api/ops/artifact_rule_preview',headers:{authorization:'Bearer b'},payload:{projectId:'lab-rules',rule}})).json();
 assert.deepEqual([preview.count,preview.bytes],[1,data.length]);
 assert.equal((await app.inject({method:'POST',url:'/api/ops/artifact_rule_confirm',headers:{authorization:'Bearer b'},payload:{projectId:'lab-rules',rule:{...rule,older_than_days:5}}})).statusCode,404,'only a rule from the project settings');
 assert.equal((await app.inject({method:'POST',url:'/api/ops/artifact_rule_confirm',headers:{authorization:'Bearer b'},payload:{projectId:'lab-rules',rule}})).statusCode,200);
 await P.runPlacement();const a=await A.get(id);assert.equal(a.state,'deleted');assert.equal(mac.objects.size,0);await app.close()});
