// A run bound to a host that never registered must not stay open forever (example: tool_run 6b00f0fb on host 'pds18').
import {test} from 'node:test';
import assert from 'node:assert/strict';
import {dbApp} from './pg-test.ts';

test('stop settles an execution whose host never registered; a known host still waits for its receipt',async()=>{
 const app:any=await dbApp({browserSecret:'b',machineSecret:'m',hostId:'mac'});await app.ready();
 try{const s=app.store,op=(n:string,p:any)=>app.inject({method:'POST',url:'/api/ops/'+n,headers:{authorization:'Bearer b'},payload:p});
  await s.tx(async()=>{for(const [id,hostId] of [['ghost-run','ghost'],['mac-run','mac']]){await s.put('session',{id:'s-'+id,createdAt:Date.now(),actor:'program-worker'});
   await s.put('execution',{id,executionId:id,sessionId:'s-'+id,hostId,state:'queued',createdAt:Date.now(),actor:'program-worker'});
   await s.put('command',{id:'c-'+id,executionId:id,sessionId:'s-'+id,hostId,kind:'readiness',status:'pending',payload:{}})}});
  const r=await op('execution_stop',{executionId:'ghost-run'});assert.equal(r.statusCode,200,r.body);
  assert.equal(r.json().stop.outcome,'proven');
  assert.equal((await s.query("SELECT state FROM execution WHERE id='ghost-run'"))[0].state,'cancelled');
  assert.equal((await s.get('c-ghost-run','command')).status,'stopped','its pending command is not left for a host');
  assert.equal((await op('execution_stop',{executionId:'mac-run'})).json().stop.outcome,'unknown');
  assert.equal((await s.query("SELECT state FROM execution WHERE id='mac-run'"))[0].state,'preparing');
 }finally{await app.close()}});
