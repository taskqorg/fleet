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
