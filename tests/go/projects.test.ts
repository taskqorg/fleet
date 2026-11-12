// Shared project management on a fresh real coordinator/Postgres, no providers or native launches.
import {test} from 'node:test';
import assert from 'node:assert/strict';
import {randomUUID} from 'node:crypto';
const BASE=process.env.FLEET_TRIAL_URL!,SECRET=process.env.FLEET_TRIAL_BROWSER_SECRET!;
async function op(name:string,body:object={},person='member@trial.test'){
 const r=await fetch(BASE+'/api/ops/'+name,{method:'POST',headers:{authorization:'Bearer '+SECRET,'x-fleet-person':person,'content-type':'application/json'},body:JSON.stringify(body)});
 return {status:r.status,body:await r.json() as any};
}
const ok=(r:{status:number,body:any})=>{assert.equal(r.status,200,JSON.stringify(r.body));return r.body};
const bundle=(id:string,text='initial')=>({settings:{id,name:'Trial project',contextFiles:['rules.md'],roles:{pm:'pm'}},contextFiles:[{path:'rules.md',text}]});
// R2, R3; risk: unauthorized scope, stale authority or inconsistent retained project/session state.
test('Ordinary creator saves, reads, updates and replays one project; other actor cannot self-grant',async()=>{
 const id='project-'+randomUUID();const req={id,requestId:randomUUID(),content:bundle(id)};
 const created=ok(await op('project_create',req));assert.equal(created.revision,1);
 const replay=ok(await op('project_create',req));assert.equal(replay.revision,1);
 assert.equal((await op('project_create',{...req,content:bundle(id,'changed')})).status,409);
 const list=ok(await op('project_list'));assert.ok(list.projects.some((p:any)=>p.id===id));
 assert.equal((await op('project_context',{projectId:id},'outsider@trial.test')).status,404);
 const read=ok(await op('project_context',{projectId:id}));assert.equal(read.context[0].text,'initial');
 const update={projectId:id,requestId:randomUUID(),baseRevision:1,content:bundle(id,'next')};
 assert.equal(ok(await op('project_update',update)).revision,2);
 assert.equal(ok(await op('project_update',update)).revision,2);
 assert.equal((await op('project_update',{...update,requestId:randomUUID()})).status,409);
 assert.equal(ok(await op('project_context',{projectId:id})).context[0].text,'next');
 assert.equal((await op('project_initial_access_revoke',{projectId:id,baseAccessVersion:1})).status,403);
 ok(await op('project_initial_access_revoke',{projectId:id,baseAccessVersion:1},'owner@trial.test'));
 assert.equal((await op('project_context',{projectId:id})).status,404);
 assert.equal((await op('project_create',req)).status,404,'replay does not resurrect revoked membership');
});
// R2, R3; risk: unauthorized scope, stale authority or inconsistent retained project/session state.
test('Create race conflicts without partial content or revision, no hidden metadata accepted',async()=>{
 const id='race-'+randomUUID(),req={id,content:bundle(id)};
 const results=await Promise.all([op('project_create',{...req,requestId:randomUUID()}),op('project_create',{...req,requestId:randomUUID()})]);
 assert.deepEqual(results.map(r=>r.status).sort(),[200,409]);
 const read=ok(await op('project_context',{projectId:id}));assert.equal(read.revision,1);
 assert.equal((await op('project_update',{projectId:id,requestId:randomUUID(),baseRevision:1,content:{...bundle(id),createdBy:'outsider@trial.test'}})).status,400);
});
// R2, R3; risk: unauthorized scope, stale authority or inconsistent retained project/session state.
test('People projection reload preserves membership, removal and email re-add never resurrect it',async()=>{
 const id='reload-'+randomUUID();ok(await op('project_create',{id,requestId:randomUUID(),content:bundle(id)}));
 const people=ok(await op('people_list',{},'owner@trial.test')).people;
 ok(await op('people_set',{people},'owner@trial.test'));
 assert.equal((await op('project_context',{projectId:id})).status,200);
 ok(await op('people_set',{people:people.filter((p:any)=>p.email!=='member@trial.test')},'owner@trial.test'));
 assert.equal((await op('project_context',{projectId:id})).status,403);
 ok(await op('people_set',{people},'owner@trial.test'));
 assert.equal((await op('project_context',{projectId:id})).status,404);
});
// R2, R3; risk: unauthorized scope, stale authority or inconsistent retained project/session state.
test('Revoking last initial access resynchronizes an already-open stream without a later project event',async()=>{
 const id='stream-'+randomUUID();ok(await op('project_create',{id,requestId:randomUUID(),content:bundle(id)}));
 const previous=ok(await op('me')).grantsVersion;
 const ac=new AbortController();const r=await fetch(BASE+'/api/stream',{headers:{authorization:'Bearer '+SECRET,'x-fleet-person':'member@trial.test'},signal:ac.signal});
 assert.equal(r.status,200);const reader=r.body!.getReader();const decode=new TextDecoder();let text='';
 const timer=setTimeout(()=>ac.abort(),5000);
 try{
  while(!text.includes('event: ready')){const next=await reader.read();assert.equal(next.done,false);text+=decode.decode(next.value);}
  ok(await op('project_initial_access_revoke',{projectId:id,baseAccessVersion:1},'owner@trial.test'));
  while(!text.includes('event: resync')){const next=await reader.read();if(next.done)break;text+=decode.decode(next.value);}
  assert.ok(text.includes('event: resync'));assert.notEqual(ok(await op('me')).grantsVersion,previous);
 }finally{clearTimeout(timer);ac.abort();}
});
// R3; risk: a scoped architect or project creator borrows another host, shared credentials, or generic definition writes.
test('New binding authority is checked atomically and scoped architect does not gain fullAccess',async()=>{
 const people=ok(await op('people_list',{},'owner@trial.test')).people;
 ok(await op('people_set',{people:[...people,{email:'architect@trial.test',name:'Scoped architect',roles:['architect'],grants:[]}]},'owner@trial.test'));
 const id='authority-'+randomUUID(),content=bundle(id);
 assert.equal((await op('project_create',{id,requestId:randomUUID(),content},'architect@trial.test')).status,403);
 const denied={...content,settings:{...content.settings,environments:[{hostId:'foreign-host',checkoutPath:'/tmp/foreign'}]}};
 assert.equal((await op('project_create',{id,requestId:randomUUID(),content:denied})).status,403);
 assert.equal((await op('project_context',{projectId:id})).status,404,'failed create publishes no partial head or membership');
 ok(await op('project_create',{id,requestId:randomUUID(),content}));
 assert.equal((await op('definition_get',{kind:'project',id},'architect@trial.test')).status,404);
 assert.equal((await op('definition_create',{kind:'project',id:'forged-'+randomUUID(),content})).status,403);
 assert.equal((await op('project_initial_access_revoke',{projectId:id,baseAccessVersion:0},'owner@trial.test')).status,409);
 assert.equal((await op('project_context',{projectId:id})).status,200);
});

// R1, R2, R3; risk: a source pin is dropped by project projection or replaced in an already queued execution.
test('Pinned context descriptors round-trip and freeze at real session enqueue across project saves',async()=>{
 const id='sources-'+randomUUID();const content:any=bundle(id);content.settings.roles.worker='baseline';content.settings.repos=[{id:'main',remote:'https://github.com/taskqorg/fleet.git'},{id:'specq',remote:'https://github.com/taskqorg/specq.git'}];
 const source={repo:'specq',revision:'1'.repeat(40),core:[{path:'engineering/principles.md',sha256:'a'.repeat(64)}]};content.settings.contextSources=[source];
 ok(await op('project_create',{id,requestId:randomUUID(),content}));const first=ok(await op('project_context',{projectId:id}));assert.deepEqual(first.settings.contextSources,[source]);
 const started=ok(await op('session_start',{projectId:id,role:'worker',requestId:randomUUID(),text:'Fixture only: no executor is polled or provider launched.'}));
 const frozen=ok(await op('session_get',{sessionId:started.sessionId}));assert.equal(frozen.executions.length,1);assert.deepEqual(frozen.executions[0].projectSnapshot.settings.contextSources,[source]);
 content.settings.contextSources=[{...source,revision:'2'.repeat(40)}];ok(await op('project_update',{projectId:id,requestId:randomUUID(),baseRevision:1,content}));
 const old=ok(await op('session_get',{sessionId:started.sessionId}));assert.deepEqual(old.executions[0].projectSnapshot.settings.contextSources,[source]);assert.equal(old.executions[0].projectSnapshot.revision,1);
 const next=ok(await op('session_start',{projectId:id,role:'worker',requestId:randomUUID(),text:'Fixture only; no model.'}));const current=ok(await op('session_get',{sessionId:next.sessionId}));assert.equal(current.executions[0].projectSnapshot.revision,2);assert.equal(current.executions[0].projectSnapshot.settings.contextSources[0].revision,'2'.repeat(40));
 const invalid=structuredClone(content);invalid.settings.contextSources[0].command='unexpected';assert.equal((await op('project_update',{projectId:id,requestId:randomUUID(),baseRevision:2,content:invalid})).status,400);
 const foreign=structuredClone(content);foreign.settings.contextSources[0].repo='unknown';assert.equal((await op('project_update',{projectId:id,requestId:randomUUID(),baseRevision:2,content:foreign})).status,400);
 assert.equal((await op('project_context',{projectId:id},'outsider@trial.test')).status,404);
});
// R2, R7; owner decision 2026-11-12, archived repositories leave Fleet; risk: a dropped project keeps running work or stays listed.
test('Owner archives a project only after its open work is closed; it leaves every list',async()=>{
 const owner='owner@trial.test',id='project-'+randomUUID();
 ok(await op('project_create',{id,requestId:randomUUID(),content:bundle(id)}));
 const task=ok(await op('task_create',{projectId:id,title:'open work'},owner));
 assert.equal((await op('project_archive',{projectId:id})).status,403);
 const busy=await op('project_archive',{projectId:id},owner);assert.equal(busy.status,409);assert.match(JSON.stringify(busy.body),/task /);
 ok(await op('task_cancel',{taskId:task.id??task.taskId},owner));
 ok(await op('project_archive',{projectId:id},owner));
 assert.ok(!ok(await op('project_list',{},owner)).projects.some((p:any)=>p.id===id));
 assert.equal((await op('project_context',{projectId:id},owner)).status,404);
 ok(await op('project_archive',{projectId:id},owner)); // idempotent
 assert.equal((await op('project_archive',{projectId:'no-such-'+randomUUID()},owner)).status,404);
 assert.equal((await op('task_create',{projectId:id,title:'late work'},owner)).status,404);
 assert.equal((await op('session_start',{requestId:randomUUID(),text:'late',projectId:id,profileId:'pm'},owner)).status,404);
 assert.equal((await op('project_update',{projectId:id,requestId:randomUUID(),baseRevision:1,content:bundle(id,'back')},owner)).status,404);
 assert.equal((await op('project_create',{id,requestId:randomUUID(),content:bundle(id)},owner)).status,409);
});
// R2, R3; risk: a finished pipeline run leaves unreached steps 'preparing', and they block project_archive forever.
test('Unreached steps of a finished pipeline run do not block project_archive; a running run does',async t=>{
 const {startCoordinator}=await import('../harness.ts');
 const c=await startCoordinator({name:'archsteps'});t.after(()=>c.stop());
 const call=async(name:string,body:object)=>{const r=await fetch(c.base+'/api/ops/'+name,{method:'POST',headers:{authorization:'Bearer '+c.secret,'x-fleet-person':'owner@trial.test','content-type':'application/json'},body:JSON.stringify(body)});return {status:r.status,body:await r.json() as any}};
 const id='project-'+randomUUID();
 ok(await call('project_create',{id,requestId:randomUUID(),content:bundle(id)}));
 c.sql(`INSERT INTO execution(id,kind,state,project_id,data) VALUES ('run-a','pipeline_run','running','${id}','{}'),('run-a:step:1','pipeline_step','preparing','${id}','{}')`);
 assert.equal((await call('project_archive',{projectId:id})).status,409);
 c.sql(`UPDATE execution SET state='failed' WHERE id='run-a'`);
 ok(await call('project_archive',{projectId:id}));
});
// R2, R3; risk: an archived project's deploy key stays on every host that held it.
test('Archiving the last project that names a git connection queues connection-remove on each host that holds it',async t=>{
 const {startCoordinator}=await import('../harness.ts');
 const c=await startCoordinator({name:'archkeys'});t.after(()=>c.stop());
 const call=async(name:string,body:object)=>{const r=await fetch(c.base+'/api/ops/'+name,{method:'POST',headers:{authorization:'Bearer '+c.secret,'x-fleet-person':'owner@trial.test','content-type':'application/json'},body:JSON.stringify(body)});return {status:r.status,body:await r.json() as any}};
 const withConn=(id:string,conn:string)=>({settings:{id,name:'Keyed',contextFiles:[],repos:[{id:'main',remote:'git@github.com:taskqorg/x.git',forge:'github',defaultBranch:'main',connection:conn}]},contextFiles:[]});
 const a='project-'+randomUUID(),b='project-'+randomUUID();
 ok(await call('project_create',{id:a,requestId:randomUUID(),content:withConn(a,'k-shared')}));
 ok(await call('project_create',{id:b,requestId:randomUUID(),content:withConn(b,'k-shared')}));
 c.sql(`INSERT INTO document(kind,id,data) VALUES ('host-observation','h-keys','{"id":"h-keys","gitConnections":["k-shared","k-other"]}')`);
 assert.deepEqual(ok(await call('project_archive',{projectId:a})).connectionRemovals,[],'another live project still uses it');
 assert.deepEqual(ok(await call('project_archive',{projectId:b})).connectionRemovals,['h-keys/k-shared']);
});
