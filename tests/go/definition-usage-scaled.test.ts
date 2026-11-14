import {test} from 'node:test';
import assert from 'node:assert/strict';
import {mkdtempSync,writeFileSync,rmSync} from 'node:fs';
import {tmpdir} from 'node:os';
import {join} from 'node:path';
import {startCoordinator} from '../harness.ts';

test('scaled definition usage preserves all edge kinds, cycles, duplicate paths, saved revisions and scoped counts (requirement: definition usage complete leaf accounting; risk: recursive optimization omits descendants, double counts or leaks grants)', {timeout:60_000}, async()=>{
 const dir=mkdtempSync(join(tmpdir(),'fleet-definition-usage-'));
 const people=join(dir,'people.json');
 writeFileSync(people,JSON.stringify([{email:'owner@trial.test',owner:true,roles:['operator','architect']},{email:'scoped@trial.test',roles:['operator','architect'],grants:[{kind:'project',id:'desiign'}]}]));
 const c=await startCoordinator({name:'definition-scaled',people});
 try{
  c.sql(`INSERT INTO definition(kind,id,path,head) VALUES ('pipeline','scaled-usage','pipelines/scaled-usage.json',2);
   INSERT INTO definition_revision(kind,id,revision,content,content_sha,source,actor_kind,by_email,message,at)
   SELECT 'pipeline','scaled-usage',i,'{"name":"scaled-usage","steps":[]}'::jsonb,'sha-'||i,'editor','person','owner@trial.test','fixture',now() FROM generate_series(1,2)i;
   INSERT INTO execution(id,kind,state,project_id,definitions,started_at)
   SELECT 'scale-root-'||i,'pipeline_run','succeeded','desiign',jsonb_build_array(jsonb_build_object('kind','pipeline','id','scaled-usage','revision',1+i%2)),now() FROM generate_series(1,400)i;
   INSERT INTO document(id,kind,project_id,data)
   SELECT 'scale-session-'||i,'session','desiign',jsonb_build_object('parentExecutionId','scale-root-'||i) FROM generate_series(1,400)i;
   INSERT INTO execution(id,kind,state,project_id,parent_execution_id,delegation,session_id,data,started_at)
   SELECT 'scale-leaf-'||i||'-'||m,CASE WHEN m=4 THEN 'tool_run'::execution_kind ELSE 'agent_run'::execution_kind END,'succeeded','desiign',
    CASE WHEN m=1 THEN 'scale-root-'||i WHEN m=5 THEN 'scale-leaf-'||i||'-1' END,
    CASE WHEN m IN (1,2) THEN jsonb_build_object('parentExecutionId','scale-root-'||i) END,
    CASE WHEN m IN (1,3) THEN 'scale-session-'||i END,
    CASE WHEN m=4 THEN jsonb_build_object('pipelineRunId','scale-root-'||i) ELSE '{}'::jsonb END,now()
   FROM generate_series(1,400)i CROSS JOIN generate_series(1,5)m;
   UPDATE execution SET parent_execution_id='scale-leaf-'||substring(id from 12)||'-5' WHERE id LIKE 'scale-root-%';
   INSERT INTO execution(id,kind,state,project_id,definitions,started_at) VALUES
    ('scale-hidden-root','pipeline_run','succeeded','secret', '[{"kind":"pipeline","id":"scaled-usage","revision":1}]',now()),
    ('scale-draft-root','pipeline_run','succeeded','desiign','[{"kind":"pipeline","id":"scaled-usage","revision":1,"draft":true}]',now());
   INSERT INTO execution(id,kind,state,project_id,parent_execution_id,started_at) VALUES
    ('scale-hidden-leaf','agent_run','succeeded','secret','scale-hidden-root',now()),
    ('scale-draft-leaf','agent_run','succeeded','desiign','scale-draft-root',now());
   INSERT INTO execution(id,kind,state,project_id,started_at) SELECT 'scale-unrelated-'||i,'agent_run','succeeded','desiign',now() FROM generate_series(1,20000)i;
   INSERT INTO execution_totals(execution_id,project_id,billing_basis,tokens_input)
   SELECT id,project_id,'metered',CASE WHEN id='scale-hidden-leaf' THEN 9999 ELSE 1 END FROM execution WHERE id LIKE 'scale-leaf-%' OR id IN ('scale-hidden-leaf','scale-draft-leaf');`);
  async function op(name:string,body:object,person:string){
   const r=await fetch(c.base+'/api/ops/'+name,{method:'POST',headers:{authorization:'Bearer '+c.secret,'x-fleet-person':person,'content-type':'application/json'},body:JSON.stringify(body)});
   const d=await r.json();assert.equal(r.status,200,JSON.stringify(d));return d;
  }
  for(const [person,runs,leaves,tokens] of [['owner@trial.test',401,2001,11999],['scoped@trial.test',400,2000,2000]] as const){
   const usage=await op('definition_usage',{kind:'pipeline',id:'scaled-usage',days:30},person);
   assert.equal(usage.total.runs,runs);assert.equal(usage.total.resources.executions,leaves);assert.equal(usage.total.resources.tokens.input,tokens);
   assert.equal(usage.byRevision.length,2);
   assert.equal(usage.byRevision.reduce((n:number,x:any)=>n+x.window.runs,0),runs);
   const list=await op('definition_list',{kind:'pipeline'},person);const row=list.items.find((x:any)=>x.id==='scaled-usage');
   assert.equal(row.last30.runs,runs);assert.deepEqual(row.last30.resources,usage.total.resources);
  }
 }finally{await c.stop();rmSync(dir,{recursive:true,force:true});}
});
