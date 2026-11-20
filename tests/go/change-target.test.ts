// Requirement: audit target metadata is captured atomically and follows existing authorized feed visibility.
// Risk: actor is mistaken for target, rollback/delete loses scope, or metadata leaks another person's audit.
import {test} from 'node:test';
import assert from 'node:assert/strict';
import {startCoordinator,lit} from '../harness.ts';
// Requirement: scoped audit/definition changes retain authorized current data and legacy behavior.
// Risk: wrong target filtering leaks metadata, loses remote updates or repeats irrelevant reads.
test('committed setting target survives audit deletion, rollback and legacy rows without changing RLS',{timeout:30000},async()=>{
 const c=await startCoordinator({name:'change-target'});
 const call=async(actor:string,name:string,body:object={})=>{const r=await fetch(c.base+'/api/ops/'+name,{method:'POST',headers:{authorization:'Bearer '+c.secret,'x-fleet-person':actor,'content-type':'application/json'},body:JSON.stringify(body)});assert.equal(r.status,200,await r.clone().text());return r.json() as Promise<any>;};
 const read=async(cursor:number,predicate:(row:any)=>boolean,actor='member@trial.test')=>{const end=Date.now()+10000;for(;;){const r=await call(actor,'changes_since',{cursor});if(r.changes.some(predicate))return r.changes;assert.ok(Date.now()<end,'committed feed checkpoint');await new Promise(r=>setTimeout(r,10));}};
 try{
  let cursor=(await call('member@trial.test','changes_since')).cursor;
  await call('owner@trial.test','person_settings_set',{email:'member@trial.test',navCollapsed:true});
  const rows=await read(cursor,r=>r.kind==='setting-change'&&r.settingTarget==='person:member@trial.test'),personal=rows.find((r:any)=>r.kind==='setting-change'&&r.settingTarget==='person:member@trial.test');assert.ok(personal);
  const audit=JSON.parse(c.sql(`SELECT json_build_object('actor',actor,'holder',holder) FROM audit_log WHERE id=${lit(personal.id)}`));assert.equal(audit.actor,'owner@trial.test');assert.equal(audit.holder,'member@trial.test');
  assert.equal((await call('outsider@trial.test','changes_since',{cursor})).changes.some((r:any)=>r.id===personal.id),false);
  cursor=(await call('member@trial.test','changes_since')).cursor;
  c.sql(`DELETE FROM audit_log WHERE id=${lit(personal.id)}`);
  const deleted=(await read(cursor,r=>r.id===personal.id&&r.op==='D')).find((r:any)=>r.id===personal.id&&r.op==='D');assert.equal(deleted?.settingTarget,'person:member@trial.test','OLD audit data remains authoritative after body removal');
  c.sql(`BEGIN; INSERT INTO audit_log(id,target,field,actor,holder) VALUES('rolled-target','person:member@trial.test','x','owner@trial.test','member@trial.test'); ROLLBACK`);assert.equal(c.sql("SELECT count(*) FROM change WHERE id='rolled-target'").trim(),'0');
  cursor=(await call('member@trial.test','changes_since')).cursor;
  c.sql("BEGIN; INSERT INTO change(kind,id,op,holder) VALUES('setting-change','legacy-target','I','member@trial.test'); SELECT fleet_enqueue_wake('fleet_change',''); COMMIT");
  const legacy=(await read(cursor,r=>r.id==='legacy-target')).find((r:any)=>r.id==='legacy-target');assert.ok(legacy);assert.ok(!('settingTarget'in legacy),'legacy metadata is unknown, not fabricated');
 }finally{await c.stop();}
});
