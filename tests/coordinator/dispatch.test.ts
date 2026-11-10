// Slice 6 Dispatch (docs/designs/fleet-people-access.md §6-7, CUJ 5): late choice on a host poll, wait reasons, one question per
// cause closed when the cause clears, route decisions, budgets as skip reasons.
import {test} from 'node:test';import assert from 'node:assert/strict';import {mkdtempSync} from 'node:fs';
import {createApp} from './test-app.ts';

test('a Paused host makes two tasks wait with one question; resuming starts them and closes it; a reached pause budget waits',async()=>{
 const app=createApp({database:mkdtempSync('/tmp/fleet-dispatch-')+'/db',browserSecret:'b',machineSecret:'m',hostId:'mac'});
 try{
  const call=(url:string,payload?:any,machine=false)=>app.inject({method:payload?'POST':'GET',url,headers:{authorization:'Bearer '+(machine?'m':'b')},payload});
  const poll=async()=>(await call('/api/hosts/poll',{hostId:'mac'},true)).json().commands.filter((c:any)=>c.kind==='start');
  const exec=(id:string)=>(app as any).store.get(id,'execution');
  const open=async()=>(await call('/api/ops/questions_list',{state:'open'})).json().questions.filter((q:any)=>/Work is waiting/.test(q.text));
  await poll();
  assert.equal((await call('/api/ops/hosts_update',{id:'mac',intake:'paused'})).statusCode,200);
  const ids:string[]=[];for(const r of ['a','b']){const s=(await call('/api/sessions',{})).json().id;ids.push((await call('/api/sessions/'+s+'/messages',{requestId:r,text:'x'})).json().id)}
  assert.equal((await poll()).length,0);
  for(const id of ids)assert.equal((await exec(id)).waitReason,'mac is Paused');
  assert.equal((await open()).length,1);
  const rd=(await call('/api/ops/route_decisions',{executionId:ids[0]})).json().decisions;assert.equal(rd.length,1);assert.equal(rd[0].winner,null);assert.equal(rd[0].candidates[0].skipped,'mac is Paused');
  await poll();assert.equal((await call('/api/ops/route_decisions',{executionId:ids[0]})).json().decisions.length,1);// recorded on change, not every poll
  // Answering the question does not bring a new one while the cause lasts.
  const q=(await open())[0];assert.equal((await call('/api/ops/questions_answer',{questionId:q.id,answer:'ok'})).statusCode,200);await poll();assert.equal((await open()).length,0);
  assert.equal((await call('/api/ops/hosts_update',{id:'mac',intake:'all'})).statusCode,200);
  assert.equal((await poll()).length,2);assert.equal((await open()).length,0);
  // A reached pause budget on the director skips every candidate; warn would only warn.
  const owner=(await call('/api/ops/me',{})).json().email;
  assert.equal((await call('/api/ops/budget_set',{scope:'person',scopeId:owner,unit:'wall_ms',limit:0,action:'pause'})).statusCode,200);
  const s=(await call('/api/sessions',{})).json().id,e=(await call('/api/sessions/'+s+'/messages',{requestId:'c',text:'x'})).json().id;
  assert.equal((await poll()).filter((c:any)=>c.executionId===e).length,0);assert.match((await exec(e)).waitReason,/budget reached: wall_ms 0 of 0/);
  assert.equal((await call('/api/ops/budgets_list',{})).json().budgets[0].spend.basis,'measured');
  // A reached stop budget ends new work instead of keeping it waiting.
  assert.equal((await call('/api/ops/budget_set',{scope:'person',scopeId:owner,unit:'wall_ms',limit:0,action:'stop'})).statusCode,200);
  const s2=(await call('/api/sessions',{})).json().id,e2=(await call('/api/sessions/'+s2+'/messages',{requestId:'d',text:'x'})).json().id;
  await poll();assert.equal((await exec(e2)).state,'stopped');
  // Stopping work that never started settles it at once.
  assert.equal((await exec((await call('/api/executions/'+e+'/stop',{})).json().executionId)).state,'stopped');
 }finally{await app.close()}
});
