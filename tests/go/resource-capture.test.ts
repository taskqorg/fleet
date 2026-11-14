import {test} from 'node:test';
import assert from 'node:assert/strict';
import {startCoordinator} from '../harness.ts';

// Requirement: prepared summaries capture every committed canonical fact; risk: lost work, cross-transaction coalescing or old ownership.
test('resource capture is atomic, coalesces only its transaction and aligns moved totals including NULL', async () => {
 const c=await startCoordinator({name:'resource-capture'});
 try {
  const count=()=>Number(c.sql("SELECT count(*) FROM resource_work WHERE execution_id='capture-e'").trim());
  c.sql(`BEGIN; INSERT INTO execution(id,kind,state,project_id,started_at) VALUES('capture-e','agent_run','running','A',now());
   INSERT INTO execution_totals(execution_id,project_id,tokens_input) VALUES('capture-e','A',10);
   UPDATE execution_totals SET tokens_input=20 WHERE execution_id='capture-e'; COMMIT;`);
  assert.equal(count(),1);
  c.sql("UPDATE execution_totals SET computed_at=now() WHERE execution_id='capture-e'");assert.equal(count(),1);
  c.sql("UPDATE execution SET last_seen_at=now(),data=data||'{\"label\":\"unrelated\"}' WHERE id='capture-e'");assert.equal(count(),1);
  c.sql("BEGIN; UPDATE execution_totals SET tokens_input=30 WHERE execution_id='capture-e'; ROLLBACK;");assert.equal(count(),1);
  c.sql("UPDATE execution_totals SET tokens_input=40 WHERE execution_id='capture-e'");assert.equal(count(),2);
  const first=c.sql("SELECT work_id FROM resource_work WHERE execution_id='capture-e' ORDER BY first_pending_at LIMIT 1").trim();
  c.sql("UPDATE execution SET project_id='B' WHERE id='capture-e'");
  assert.equal(c.sql("SELECT project_id FROM execution_totals WHERE execution_id='capture-e'").trim(),'B');
  assert.equal(count(),3);
  assert.equal(c.sql("SELECT string_agg(project_id,',' ORDER BY project_id) FROM resource_work_scope WHERE work_id=(SELECT work_id FROM resource_work WHERE execution_id='capture-e' ORDER BY first_pending_at DESC LIMIT 1)").trim(),'A,B');
  c.sql(`DELETE FROM resource_work WHERE work_id='${first}'`);assert.equal(count(),2,'clearing claimed identity cannot erase a later transaction');
  c.sql("UPDATE execution SET project_id=NULL WHERE id='capture-e'");
  assert.equal(c.sql("SELECT project_id IS NULL FROM execution_totals WHERE execution_id='capture-e'").trim(),'t');
  const before=count();c.sql("BEGIN; DELETE FROM execution_totals WHERE execution_id='capture-e'; DELETE FROM execution WHERE id='capture-e'; COMMIT;");
  assert.equal(count(),before+1,'deletion work has no source cascade');
 } finally {await c.stop();}
});

// Requirement: classification repair retains first old/last new and excludes credentials; risk: stale provider classification or secret capture.
test('metadata repair uses normalized identities, ignores credential changes and cannot leak maintenance work', async()=>{
 const c=await startCoordinator({name:'resource-meta'});
 try {
  c.sql(`BEGIN; INSERT INTO document(kind,id,data) VALUES('api-connection','connection:sample','{"provider":"one","key":"invented-key"}');
   UPDATE document SET data=jsonb_set(data,'{provider}','"two"') WHERE id='connection:sample'; COMMIT;`);
  assert.equal(c.sql("SELECT count(*) FROM resource_metadata_repair WHERE metadata_id='connection:sample'").trim(),'1');
  assert.deepEqual(JSON.parse(c.sql("SELECT new_class FROM resource_metadata_repair WHERE metadata_id='connection:sample'").trim()),{accountId:'connection:sample',connectionName:'sample',kind:'api-connection',provider:'two'});
  assert.equal(c.sql("SELECT old_class FROM resource_metadata_repair WHERE metadata_id='connection:sample'").trim(),'{}');
  c.sql("UPDATE document SET data=jsonb_set(data,'{key}','\"rotated-invented-key\"') WHERE id='connection:sample'");
  assert.equal(c.sql("SELECT count(*) FROM resource_metadata_repair WHERE metadata_id='connection:sample'").trim(),'1');
  c.sql("UPDATE document SET data=jsonb_set(data,'{provider}','\"three\"') WHERE id='connection:sample'");
  assert.equal(c.sql("SELECT count(*) FROM resource_metadata_repair WHERE metadata_id='connection:sample'").trim(),'2');
  assert.equal(c.sql("SET fleet.project_ids='A'; SELECT count(*) FROM resource_metadata_repair").trim(),'0');
  assert.equal(c.sql("SELECT has_function_privilege('fleet_app','resource_enqueue_source(text,text,text)','EXECUTE')").trim(),'f');
  // A restricted source write can capture safely, and the trigger restores that caller's scope.
  assert.equal(c.sql("SET fleet.project_ids='A'; INSERT INTO execution(id,kind,state,project_id) VALUES('restricted-e','tool_run','succeeded','A'); SELECT current_setting('fleet.project_ids')").trim(),'A');
  assert.equal(c.sql("SELECT count(*) FROM resource_work WHERE execution_id='restricted-e'").trim(),'1');
 } finally {await c.stop();}
});

// Requirement: newest confirmed usage while running; risk: completion-only totals, double-counted cumulative usage, or invented process time.
test('native usage updates running totals atomically and replay does not double count',async()=>{
 const c=await startCoordinator({name:'resource-usage'});
 try{
  const post=async(path:string,body:object,secret=c.secret)=>{
   const r=await fetch(c.base+path,{method:'POST',headers:{authorization:'Bearer '+secret,'content-type':'application/json',...(secret===c.secret?{'x-fleet-person':'owner@trial.test'}:{})},body:JSON.stringify(body)});
   const b=await r.json();assert.equal(r.status,200,JSON.stringify(b));return b as any;
  };
  const host=await post('/api/ops/hosts_register',{id:'capture-host'});
  c.sql("INSERT INTO execution(id,kind,state,project_id,host_id,harness,started_at,data) VALUES('running-e','agent_run','running','A','capture-host','codex',now(),jsonb_build_object('id','running-e','hostId','capture-host'))");
  const events=[{id:'usage-one',seq:1,type:'usage.observation',timestamp:Date.now(),data:{fleetNormalized:{lifecycle:'usage.observation',nativeUsage:{last:{inputTokens:100,outputTokens:7,reasoningOutputTokens:3},total:{inputTokens:99999,outputTokens:9999}}}}}];
  await post('/api/hosts/events',{hostId:'capture-host',executionId:'running-e',events},host.secret);
  const read=()=>JSON.parse(c.sql("SELECT jsonb_build_object('state',e.state,'input',t.tokens_input,'output',t.tokens_output,'reasoning',t.tokens_reasoning,'wall',t.wall_ms,'cpu',t.cpu_ms,'api',t.api_ms,'owner',t.project_id) FROM execution e JOIN execution_totals t ON e.id=t.execution_id WHERE e.id='running-e'").trim());
  const expected={state:'running',input:100,output:7,reasoning:3,wall:null,cpu:null,api:null,owner:'A'};
  assert.deepEqual(read(),expected);
  const work=Number(c.sql("SELECT count(*) FROM resource_work WHERE execution_id='running-e'").trim());
  await post('/api/hosts/events',{hostId:'capture-host',executionId:'running-e',events},host.secret);
  assert.deepEqual(read(),expected);
  assert.equal(Number(c.sql("SELECT count(*) FROM resource_work WHERE execution_id='running-e'").trim()),work);
  c.sql("UPDATE execution SET project_id=NULL WHERE id='running-e'");
  await post('/api/hosts/events',{hostId:'capture-host',executionId:'running-e',events},host.secret);
  assert.equal(read().owner,null,'later usage cannot restore old project');
 }finally{await c.stop();}
});
