// Requirement: Fleet analytics native observations and Lab lineage.
// Risk: profile IDs merge variants, cumulative usage doubles counts, or absent measurements become zero.
import {test} from 'node:test';
import assert from 'node:assert/strict';
import {readFileSync} from 'node:fs';
import {startCoordinator,lit} from '../harness.ts';

// Requirement: fleet-analytics.md native observations and Lab attribution; risk: wrong variant labels or fabricated zero measurements.
test('retained Lab descendants and native observations remain correctly attributed after upgrade',async()=>{
 const c=await startCoordinator({name:'harness-comparison'});
 try{
  c.sql(`INSERT INTO execution(id,kind,state,project_id,experiment_id,variant,parent_execution_id,profile_id,started_at,ended_at) VALUES
   ('trial-a','pipeline_run','succeeded','desiign','comparison-test','ordinary',NULL,NULL,now()-interval '1 minute',now()),
   ('trial-b','pipeline_run','succeeded','desiign','comparison-test','reviewed',NULL,NULL,now()-interval '1 minute',now()),
   ('step-a','pipeline_step','succeeded','desiign',NULL,NULL,'trial-a',NULL,now()-interval '1 minute',now()),
   ('step-b','pipeline_step','succeeded','desiign',NULL,NULL,'trial-b',NULL,now()-interval '1 minute',now()),
   ('native-zero','agent_run','succeeded','desiign','comparison-test','baseline','step-a','baseline',now()-interval '1 minute',now()),
   ('native-calls','agent_run','succeeded','desiign','comparison-test','baseline','step-b','baseline',now()-interval '1 minute',now()),
   ('native-partial','agent_run','succeeded','desiign','comparison-test','baseline','step-b','baseline',now()-interval '1 minute',now()),
   ('native-api','agent_run','succeeded','desiign',NULL,NULL,'step-b','baseline',now()-interval '1 minute',now()),
   ('native-script','host_job','succeeded','desiign',NULL,NULL,'step-b',NULL,now()-interval '1 minute',now()),
   ('foreign-native','agent_run','succeeded','hello-world',NULL,NULL,'step-b',NULL,now()-interval '1 minute',now());
   INSERT INTO execution_totals(execution_id,project_id) SELECT id,project_id FROM execution WHERE kind IN ('agent_run','host_job');`);
  const ev=(id:string,seq:number,lifecycle:string,nativeUsage:any,native:any={})=>({id:id+'-'+seq,executionId:id,seq,type:lifecycle,data:{fleetNormalized:{lifecycle,nativeUsage,native}}});
  const events=[ev('native-zero',1,'usage.observation',{last:{reasoningOutputTokens:0},total:{reasoningOutputTokens:999}}),
   ev('native-calls',1,'usage.observation',{last:{reasoningOutputTokens:3},total:{reasoningOutputTokens:1000}}),
   ev('native-calls',2,'usage.observation',{last:{reasoningOutputTokens:7},total:{reasoningOutputTokens:1007}}),
   ev('native-calls',3,'result',{reasoningOutputTokens:9999}),
   ev('native-partial',1,'usage.observation',{last:{reasoningOutputTokens:3}}),
   ev('native-partial',2,'usage.observation',{last:{inputTokens:20}}),
   ev('native-api',1,'result',{}, {duration_api_ms:125,duration_ms:999})];
  for(const e of events)c.sql(`INSERT INTO document(kind,id,project_id,data) VALUES('event',${lit(e.id)},'desiign',${lit(e)}::jsonb)`);
  // Reapply the exact upgrade projection, twice, to populated history rather than an empty schema.
  const migration=readFileSync('go/coordinator/schema.sql','utf8').split('-- migration 54:')[1];
  const projection=migration.slice(migration.indexOf('CREATE OR REPLACE FUNCTION'));
  c.sql('BEGIN;'+projection+'COMMIT;');c.sql('BEGIN;'+projection+'COMMIT;');
  const rows=JSON.parse(c.sql(`SELECT json_agg(json_build_object('id',e.id,'reasoning',tokens_reasoning,'api',api_ms)) FROM execution e LEFT JOIN execution_totals t ON t.execution_id=e.id WHERE e.id LIKE 'native-%'`));
  const byId=new Map<string,any>(rows.map((r:any)=>[r.id,r]));
  assert.equal(byId.get('native-zero').reasoning,0);
  assert.equal(byId.get('native-calls').reasoning,10,'last call reports win over cumulative totals and final result');
  assert.equal(byId.get('native-partial').reasoning,null,'missing call data is not a measured partial sum');
  assert.equal(byId.get('native-api').api,125,'API duration is not duration_ms');
  assert.equal(c.sql("SELECT coalesce(experiment_id,'none') FROM execution WHERE id='foreign-native'").trim(),'none');
  assert.equal(c.sql("SELECT bool_and(relforcerowsecurity) FROM pg_class WHERE relname IN ('execution','execution_totals','document')").trim(),'t');
  const r=await fetch(c.base+'/api/ops/analytics_query',{method:'POST',headers:{authorization:'Bearer '+c.secret,'x-fleet-person':'owner@trial.test','content-type':'application/json'},body:JSON.stringify({window:{from:new Date(Date.now()-120000).toISOString(),to:new Date(Date.now()+120000).toISOString()},dimensions:['variant','kind'],filters:{experiment:['comparison-test']},measures:['executions','tokens_reasoning','api_ms']})});
  const q:any=await r.json();assert.equal(r.status,200,JSON.stringify(q));
  const zero=q.rows.find((r:any)=>r.dimensions.variant==='ordinary');
  assert.deepEqual(zero.measures.tokens_reasoning,{value:0,basis:'measured'});
  assert.equal(zero.measures.api_ms.value,null);assert.match(zero.measures.api_ms.reason,/harness did not report/);
  const reviewed=q.rows.find((r:any)=>r.dimensions.variant==='reviewed'&&r.dimensions.kind==='agent_run');
  assert.equal(reviewed.measures.tokens_reasoning.value,10);assert.equal(reviewed.measures.tokens_reasoning.basis,'unavailable');assert.equal(reviewed.measures.tokens_reasoning.partial,'1 of 3 measured');
  assert.equal(reviewed.measures.api_ms.value,125);assert.equal(reviewed.measures.api_ms.partial,'1 of 3 measured');
  assert.equal(q.rows.find((r:any)=>r.dimensions.kind==='host_job').dimensions.variant,'reviewed');
 }finally{await c.stop();}
});
