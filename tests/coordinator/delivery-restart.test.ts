// A coordinator restart re-runs the delivery advance on start (onReady). Records that did not change must not be written:
// before this fix every restart set updated_at on every delivery task, so the operator list showed them as just updated.
import {test} from 'node:test';import assert from 'node:assert/strict';import {mkdtempSync} from 'node:fs';import {tmpdir} from 'node:os';
import {createApp} from '../../packages/coordinator/app.js';

test('restarting the coordinator leaves unchanged delivery tasks untouched (updated_at, change rows)',async()=>{
 const database=mkdtempSync(tmpdir()+'/fleet-restart-')+'/db',config={database,browserSecret:'b',machineSecret:'m',hostId:'mac'};
 let app=createApp(config);await app.ready();let s=(app as any).store;
 await s.tx(async()=>{await s.put('execution',{id:'g1',executionId:'g1',sessionId:'s1',hostId:'mac',state:'completed',startedAt:1,finishedAt:2,deliveryId:'d-merged'});
  await s.put('execution',{id:'g2',executionId:'g2',sessionId:'s2',hostId:'mac',state:'failed',startedAt:1,finishedAt:2,deliveryId:'d-failed'});
  await s.put('delivery',{id:'d-merged',projectId:'p',requestId:'r1',task:'Merged page',state:'merged',steps:[{kind:'generate',executionId:'g1',state:'completed'}]});
 await s.put('delivery',{id:'d-failed',projectId:'p',requestId:'r2',task:'Failed page',state:'failed',steps:[{kind:'generate',executionId:'g2',state:'failed'}]})});
 await app.close();
 // A first restart may settle what the advance finds (the records above were written outside it); after that nothing moves.
 app=createApp(config);await app.ready();await app.close();
 app=createApp(config);await app.ready();s=(app as any).store;
 const before=await s.query("SELECT id,updated_at FROM task WHERE legacy_kind='delivery' ORDER BY id");
 const [{c}]=await s.query('SELECT max(seq) AS c FROM change');await app.close();
 await new Promise(r=>setTimeout(r,20));
 app=createApp(config);await app.ready();s=(app as any).store;
 const after=await s.query("SELECT id,updated_at FROM task WHERE legacy_kind='delivery' ORDER BY id");
 assert.equal(before.length,2);
 assert.deepEqual(after.map((r:any)=>[r.id,+r.updated_at]),before.map((r:any)=>[r.id,+r.updated_at]),'a restart moved updated_at');
 const moved=await s.query("SELECT kind,id FROM change WHERE seq>$1 AND kind='task'",[c]);
 assert.deepEqual(moved,[],'a restart wrote task change rows');
 await app.close()});
