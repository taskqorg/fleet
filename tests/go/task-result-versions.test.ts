import {test} from 'node:test';
import assert from 'node:assert/strict';
import {randomUUID} from 'node:crypto';
import {startCoordinator} from '../harness.ts';

test('Task result content versions resist replay and preserve explicit lineage and unknown identities (requirement: task result relevance; risk: replayed partial bytes replace a final report or an old required artifact hides real rework)',async()=>{
 const c=await startCoordinator({name:'task-result-versions'});
 const hash=(n:number)=>n.toString(16).padStart(64,'0');
 const task=()=>{const id=randomUUID();c.sql(`INSERT INTO task(id,project_id,title,state) VALUES('${id}','hello-world','result versions','in_progress')`);return id;};
 const execution=(tid:string)=>{const id=randomUUID();c.sql(`INSERT INTO execution(id,kind,state,project_id,task_id) VALUES('${id}','agent_run','succeeded','hello-world','${tid}')`);return id;};
 const artifact=(eid:string,sha:string,required=false,scope='prod',project:string|null='hello-world',previous:string|null=null,name='report.json')=>{
  const id=randomUUID();c.sql(`INSERT INTO artifact(id,project_id,scope,content_hash,size,media_type,name,execution_id,required,previous_artifact_id,state) VALUES('${id}',${project===null?'NULL':"'"+project+"'"},'${scope}','${sha}',1,'application/json','${name}','${eid}',${required},${previous===null?'NULL':"'"+previous+"'"},'stored')`);return id;
 };
 const legacy=(eid:string,sha?:string,scope?:string)=>{const id=randomUUID();c.sql(`INSERT INTO document(id,kind,project_id,data) VALUES('${id}','artifact','hello-world','${JSON.stringify({id,executionId:eid,path:'legacy.json',sha256:sha,scope})}')`);return id;};
 const get=async(tid:string,person='owner@trial.test')=>{const r=await fetch(c.base+'/api/ops/task_get',{method:'POST',headers:{authorization:'Bearer '+c.secret,'x-fleet-person':person,'content-type':'application/json'},body:JSON.stringify({taskId:tid})});const x=await r.json();assert.equal(r.status,200,JSON.stringify(x));return x;};
 try {
  const t=task(),old=execution(t),final=execution(t),replay=execution(t);
  artifact(old,hash(1));artifact(old,hash(2));
  const proof=artifact(final,hash(2),true),report=artifact(final,hash(3),true);
  artifact(final,hash(1));artifact(replay,hash(1));artifact(replay,hash(2));artifact(replay,hash(4),true,'prod','hello-world',null,'mcp-lifecycle-diagnostic.jsonl');
  c.sql(`UPDATE execution SET state='failed',problem='unknown:abandoned' WHERE id='${old}'`);
  const q=randomUUID();c.sql(`INSERT INTO question(id,kind,project_id,task_id,text,asked_by) VALUES('${q}','operator_decision','hello-world','${t}','Retain unresolved decision','fleet')`);
  const actual=await get(t);assert.equal(actual.latestResult.artifactId,report);assert.notEqual(actual.latestResult.artifactId,proof);assert.equal(actual.questions[0].state,'open');assert.equal(actual.executions[0].problem,'unknown:abandoned');

  // Equal bytes in another object scope or project are independent publications.
  for(const variation of ['scope','project']){
   const tid=task(),e1=execution(tid),e2=execution(tid),e3=execution(tid);artifact(e1,hash(10),true);artifact(e2,hash(11),true);
   const fresh=artifact(e3,hash(10),false,variation==='scope'?'lab':'prod',variation==='project'?'desiign':'hello-world');
   assert.equal((await get(tid)).latestResult.artifactId,fresh,variation);
  }
  const foreignTask=task(),foreign=artifact(execution(foreignTask),hash(20),true);
  const isolated=task();artifact(execution(isolated),hash(21),true);const own=artifact(execution(isolated),hash(20));assert.equal((await get(isolated)).latestResult.artifactId,own,'another task must not age these bytes');

  for(const unknown of ['hash','scope','project','invalid-hash']){
   const tid=task(),e1=execution(tid),e2=execution(tid),e3=execution(tid);
   const make=(e:string)=>unknown==='project'?artifact(e,hash(30),false,'prod',null):legacy(e,unknown==='hash'?undefined:unknown==='invalid-hash'?'unknown':hash(30),unknown==='scope'?undefined:'prod');
   make(e1);artifact(e2,hash(31),true);const fresh=make(e3);assert.equal((await get(tid)).latestResult.id,fresh,unknown);
  }

  for(const reservation of ['pending','failed']){
   const tid=task(),e1=execution(tid),e2=execution(tid),e3=execution(tid),unpublished=artifact(e1,hash(35));
   c.sql(`UPDATE artifact SET state='${reservation}' WHERE id='${unpublished}'`);artifact(e2,hash(36),true);const published=artifact(e3,hash(35));assert.equal((await get(tid)).latestResult.artifactId,published,'an unpublished reservation is not a retained version');
  }

  const lineage=task(),l1=execution(lineage),l2=execution(lineage),l3=execution(lineage),l4=execution(lineage);
  artifact(l1,hash(40),true);const middle=artifact(l2,hash(41),true);artifact(l2,hash(43),true);
  const explicit=artifact(l3,hash(40),true,'prod','hello-world',middle);
  artifact(l4,hash(40));artifact(l4,hash(41));artifact(l1,hash(40));
  assert.equal((await get(lineage)).latestResult.artifactId,explicit,'late old-execution aliases cannot collapse explicit lineage');
  c.sql(`UPDATE artifact SET state='deleted' WHERE id='${middle}'`);assert.equal((await get(lineage)).latestResult.artifactId,explicit,'predecessor tombstones still identify explicit lineage');
  c.sql(`UPDATE artifact SET state='deleted' WHERE id='${explicit}'`);const surviving=await get(lineage);assert.equal(surviving.latestResult.sha256,hash(40));assert.notEqual(surviving.latestResult.artifactId,explicit,'a deleted explicit publication anchors its surviving aliases but is not displayed');
  const l5=execution(lineage),newBytes=artifact(l5,hash(42));artifact(execution(lineage),hash(40));assert.equal((await get(lineage)).latestResult.artifactId,newBytes,'a genuinely new optional deliverable still wins');
  artifact(execution(lineage),hash(40),false,'prod','hello-world',foreign);
  assert.equal((await get(lineage)).latestResult.artifactId,newBytes,'foreign-task pointer is not explicit lineage');
  const l7=execution(lineage),republished=artifact(l7,hash(41),true,'prod','hello-world',newBytes);artifact(execution(lineage),hash(41));assert.equal((await get(lineage)).latestResult.artifactId,republished,'automatic aliases stay in the newest explicit version');

  const same=task(),sameRun=execution(same);artifact(sameRun,hash(50),true);const prior=artifact(sameRun,hash(51),true);const rollback=artifact(sameRun,hash(50),false,'prod','hello-world',prior);assert.equal((await get(same)).latestResult.artifactId,rollback,'explicit publication has a new position even within one execution');

  // Explicit acceptance outranks later publications without deleting history.
  const accepted=task(),acceptedRun=execution(accepted),chosen=artifact(acceptedRun,hash(60));
  const later=artifact(execution(accepted),hash(61),false,'prod','hello-world',null,'diagnostic.jsonl');
  const select=(ids:string[])=>c.sql(`UPDATE task SET state='completed',accepted_at=now(),accepted_by='owner@trial.test',accepted_artifact_ids=ARRAY[${ids.map(id=>"'"+id+"'").join(',')}]::text[] WHERE id='${accepted}'`);
  select([chosen]);assert.equal((await get(accepted)).latestResult.artifactId,chosen);
  const unrelated=artifact(execution(task()),hash(70));select([unrelated]);assert.equal((await get(accepted)).latestResult,null,'owner cannot select another task publication');
  const childRun=execution(accepted),childArtifact=artifact(childRun,hash(72)),childSession=randomUUID();
  c.sql(`INSERT INTO document(id,kind,project_id,data) VALUES('${childSession}','session','hello-world','${JSON.stringify({taskId:accepted})}');UPDATE execution SET task_id=NULL,session_id='${childSession}' WHERE id='${childRun}'`);
  select([childArtifact]);assert.equal((await get(accepted)).latestResult.artifactId,childArtifact,'attached child session results retain lineage without direct execution task binding');
  const historical=legacy(acceptedRun,hash(71),'prod');select([historical]);assert.equal((await get(accepted)).latestResult.id,historical,'accepted legacy document remains readable');
  const alias=randomUUID();c.sql(`UPDATE artifact SET legacy_id='${alias}' WHERE id='${chosen}'`);select([alias]);assert.equal((await get(accepted)).latestResult.artifactId,chosen,'canonical legacy alias resolves');

  select([chosen,later]);assert.equal((await get(accepted)).latestResult.artifactId,chosen,'selection order, not publication order');
  select([later,chosen]);assert.equal((await get(accepted)).latestResult.artifactId,later);
  c.sql(`UPDATE artifact SET state='deleted' WHERE id='${later}'`);
  assert.equal((await get(accepted)).latestResult.artifactId,chosen,'skip unavailable selection within accepted collection');
  select([randomUUID(),later]);assert.equal((await get(accepted)).latestResult,null,'missing/deleted refs never fall back to unaccepted history');
  select([chosen]);c.sql(`UPDATE artifact SET state='pending' WHERE id='${chosen}'`);
  assert.equal((await get(accepted)).latestResult,null,'pending is not retained');
  c.sql(`UPDATE artifact SET state='stored' WHERE id='${chosen}';UPDATE task SET project_id='desiign' WHERE id='${accepted}'`);
  assert.equal((await get(accepted,'member@trial.test')).latestResult,null,'RLS hides accepted artifact from another project');
  c.sql(`UPDATE task SET project_id='hello-world',state='in_progress' WHERE id='${accepted}'`);
  assert.equal((await get(accepted)).latestResult.id,historical,'unaccepted work retains existing publication fallback');
  assert.equal(c.sql(`SELECT count(*) FROM artifact WHERE id IN ('${chosen}','${later}')`).trim(),'2','history preserved');
 } finally {await c.stop();}
});
