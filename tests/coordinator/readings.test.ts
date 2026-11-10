// Slice 7 Readings (docs/designs/fleet-people-access.md §8, CUJ 6): saved Codex and Claude limit_reading samples are stored with
// their age, a reading past its reset counts as unknown, and the switch point skips a subscription over it.
import {test} from 'node:test';import assert from 'node:assert/strict';import {mkdtempSync} from 'node:fs';
import {createApp} from './test-app.ts';

test('limit readings per subscription with age; reset readings are unknown; switch point 90 % skips a 92 % subscription',async()=>{
 const app=createApp({database:mkdtempSync('/tmp/fleet-readings-')+'/db',browserSecret:'b',machineSecret:'m',hostId:'mac'});
 try{
  const call=(url:string,payload?:any,machine=false)=>app.inject({method:payload?'POST':'GET',url,headers:{authorization:'Bearer '+(machine?'m':'b')},payload});
  const poll=async()=>(await call('/api/hosts/poll',{hostId:'mac'},true)).json().commands.filter((c:any)=>c.kind==='start');
  const sub=async(name:string,harness:string)=>{const a=(await call('/api/ops/account_create',{name,harness})).json();await call('/api/hosts/accounts',{hostId:'mac',accountId:a.id,binding:name,platform:'darwin',readiness:'ready'},true);return a};
  const codex=await sub('codex-one','codex'),claude=await sub('claude-one','claude');
  await poll();
  const run=async(account:string,profileId:string,data:any)=>{const s=(await call('/api/sessions',{})).json().id;await call('/api/sessions/'+s+'/profile',{profileId,account});
   const e=(await call('/api/sessions/'+s+'/messages',{requestId:'r',text:'x'})).json().id;assert.ok((await poll()).some((c:any)=>c.executionId===e));
   assert.equal((await call('/api/hosts/events',{hostId:'mac',executionId:e,events:[{id:'l1',seq:1,type:'limit_reading',timestamp:'now',data}]},true)).statusCode,200);return e};
  const soon=Date.now()+3600_000;
  await run('codex-one','baseline',{type:'limit_reading',harness:'codex',readings:[{period:'weekly',pct:92,resetsAt:soon},{period:'5h',pct:12,resetsAt:soon}]});
  await run('claude-one','claude-worker',{type:'limit_reading',harness:'claude',readings:[{period:'5h',status:'allowed_warning',resetsAt:Date.now()-1000}]});
  const limits=(await call('/api/ops/subscription_limits',{})).json().subscriptions;
  const of=(id:string)=>limits.find((x:any)=>x.id===id).readings;
  assert.deepEqual(of(codex.id).map((r:any)=>[r.period,r.pct,r.state]).sort(),[['5h',12,'known'],['weekly',92,'known']]);
  assert.ok(of(codex.id).every((r:any)=>typeof r.at==='number'&&r.hostId==='mac'));
  assert.deepEqual(of(claude.id).map((r:any)=>[r.period,r.pct,r.status,r.state]),[['5h',null,'allowed_warning','unknown']]);
  // Switch point 90 %: the codex subscription is skipped with the reading and its age.
  assert.equal((await call('/api/ops/account_update',{accountId:codex.id,switchPct:90})).statusCode,200);
  const s=(await call('/api/sessions',{})).json().id;await call('/api/sessions/'+s+'/profile',{profileId:'baseline',account:'codex-one'});
  const e=(await call('/api/sessions/'+s+'/messages',{requestId:'r2',text:'x'})).json().id;await poll();
  assert.match((await (app as any).store.get(e,'execution')).waitReason,/^switch point 90 %; reading 92 % \(weekly\), 0 min old$/);
 }finally{await app.close()}
});
