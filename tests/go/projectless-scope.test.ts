import {test} from 'node:test';
import assert from 'node:assert/strict';
import {randomUUID} from 'node:crypto';
const BASE=process.env.FLEET_TRIAL_URL!,SECRET=process.env.FLEET_TRIAL_BROWSER_SECRET!;
const a='member@trial.test',b='outsider@trial.test',owner='owner@trial.test';
async function request(path:string,person:string,body?:object){const r=await fetch(BASE+path,{method:body===undefined?'GET':'POST',headers:{authorization:'Bearer '+SECRET,'x-fleet-person':person,'content-type':'application/json'},body:body===undefined?undefined:JSON.stringify(body)});return{status:r.status,body:await r.json() as any};}
const ok=(r:{status:number,body:any})=>{assert.equal(r.status,200,JSON.stringify(r.body));return r.body};
// R2, R3; risk: unauthorized scope, stale authority or inconsistent retained project/session state.
test('Two ordinary actors can own projectless sessions but cannot substitute foreign identity/session/execution',async()=>{
 assert.equal((await request('/api/ops/session_create',a,{directedBy:b})).status,400);
 const own=ok(await request('/api/ops/session_create',a,{})),foreign=ok(await request('/api/ops/session_create',b,{}));
 assert.equal(ok(await request('/api/sessions/'+own.sessionId,a)).directedBy,a);
 for(const [who,sid]of[[a,foreign.sessionId],[b,own.sessionId]]){
  assert.equal((await request('/api/sessions/'+sid,who)).status,404);
  assert.equal((await request('/api/sessions/'+sid+'/profile',who,{profileId:'pm'})).status,404);
  assert.equal((await request('/api/sessions/'+sid+'/messages',who,{requestId:randomUUID(),text:'foreign'})).status,404);
  assert.equal((await request('/api/ops/session_fork',who,{sessionId:sid,requestId:randomUUID()})).status,404);
 }
 const fork=ok(await request('/api/ops/session_fork',a,{sessionId:own.sessionId,requestId:randomUUID(),strategy:'context'}));
 assert.equal(ok(await request('/api/sessions/'+fork.sessionId,a)).directedBy,a);
 assert.equal((await request('/api/sessions/'+fork.sessionId,b)).status,404);
 ok(await request('/api/sessions/'+own.sessionId+'/profile',a,{profileId:'pm'}));
 assert.equal((await request('/api/sessions/'+own.sessionId+'/messages',a,{requestId:randomUUID(),text:'own',directedBy:b})).status,400);
 const execution=ok(await request('/api/sessions/'+own.sessionId+'/messages',a,{requestId:randomUUID(),text:'Owned transcript canary'}));
 assert.equal(execution.directedBy,a);
 const ownHistory=ok(await request('/api/threads/session/'+own.sessionId+'/entries?limit=10',a));assert.ok(JSON.stringify(ownHistory).includes('Owned transcript canary'));
 assert.equal((await request('/api/executions/'+execution.id+'/events',b)).status,404);
 const lists=ok(await request('/api/ops/sessions_list',b,{}));assert.ok(!JSON.stringify(lists).includes(own.sessionId));
 const ownRead=ok(await request('/api/sessions/'+own.sessionId,a));assert.ok(JSON.stringify(ownRead).includes(execution.id));
 const people=ok(await request('/api/ops/people_list',owner,{})).people;
 ok(await request('/api/ops/people_set',owner,{people:people.filter((p:any)=>p.email!==a)}));
 assert.equal((await request('/api/sessions/'+own.sessionId,a)).status,403);
 ok(await request('/api/ops/people_set',owner,{people}));assert.equal((await request('/api/sessions/'+own.sessionId,a)).status,404);
 assert.equal((await request('/api/sessions/'+own.sessionId,owner)).status,200,'privileged history stays available');
});
