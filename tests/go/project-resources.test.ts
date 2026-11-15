import {test} from 'node:test';
import assert from 'node:assert/strict';
import {spawn} from 'node:child_process';
import {join} from 'node:path';
import {homedir} from 'node:os';
import {startCoordinator,root} from '../harness.ts';

// Requirement: real PostgreSQL processor protocol and off-by-default rollout; risk: capture stops,
// restart loses backlog, unqualified prepared reads activate, or tests only exercise a mock reducer.
test('prepared resource processor qualifies against isolated PostgreSQL; off restart retains work',async()=>{
 const env={FLEET_RESOURCE_PROCESSOR:'off',FLEET_LOST_AFTER_MS:'3600000'};
 const c=await startCoordinator({name:'resource-processor',env});
 try{
  c.sql("INSERT INTO execution(id,kind,state,project_id) VALUES('off-restart','tool_run','succeeded','OFF')");
  const id=c.sql("SELECT work_id FROM resource_work WHERE execution_id='off-restart'").trim();
  await c.restart();
  assert.equal(c.sql("SELECT work_id FROM resource_work WHERE execution_id='off-restart'").trim(),id);
  assert.equal(c.sql("SELECT count(*) FROM resource_contribution").trim(),'0');
  // Fixture-only fault injection at the actual page-seeding SQL boundary (never production).
  c.sql(`CREATE TABLE resource_fixture_failure(kind text PRIMARY KEY); INSERT INTO resource_fixture_failure VALUES('build'),('metadata');
   CREATE FUNCTION resource_fixture_fail_page() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
    IF NEW.execution_id LIKE 'crash-%' AND EXISTS(SELECT 1 FROM resource_fixture_failure WHERE kind=NEW.origin) THEN RAISE EXCEPTION 'fixture page crash'; END IF;
    RETURN NEW; END $$;
   CREATE TRIGGER resource_fixture_fail BEFORE INSERT ON resource_work FOR EACH ROW EXECUTE FUNCTION resource_fixture_fail_page();`);
  const db=c.sql('SELECT current_database()').trim();
  const pg=process.env.FLEET_TRIAL_PGDATA||join(homedir(),'.cache/fleet-trial/pg');
  const url=`postgresql://fleet_app@/${db}?host=${encodeURIComponent(pg)}&port=${process.env.FLEET_TRIAL_PGPORT||'54329'}`;
  const code=await new Promise<number|null>((resolve,reject)=>{
   const child=spawn('go',['test','./coordinator','-run','^TestResourcePostgres$','-count=1','-v'],{cwd:join(root,'go'),env:{...process.env,FLEET_RESOURCE_TEST_URL:url},stdio:'inherit'});
   child.once('error',reject);child.once('exit',resolve);
  });
  assert.equal(code,0,'real PostgreSQL processor proof');
  const res=await fetch(c.base+'/api/ops/project_resources',{method:'POST',headers:{authorization:'Bearer '+c.secret,'x-fleet-person':'owner@trial.test','content-type':'application/json'},body:JSON.stringify({projectId:'fleet'})});
  assert.equal(res.status,503,'prepared public reads remain disabled until rollout');
  // An actually accepted native observation must traverse normalization AND the opted-in processor.
  env.FLEET_RESOURCE_PROCESSOR='on';await c.restart();
  const post=async(path:string,body:object,secret=c.secret)=>{
   const r=await fetch(c.base+path,{method:'POST',headers:{authorization:'Bearer '+secret,'content-type':'application/json',...(secret===c.secret?{'x-fleet-person':'owner@trial.test'}:{})},body:JSON.stringify(body)});
   const b=await r.json();assert.equal(r.status,200,JSON.stringify(b));return b as any;
  };
  const host=await post('/api/ops/hosts_register',{id:'processor-host'});
  c.sql("INSERT INTO execution(id,kind,state,project_id,host_id,harness,started_at,data) VALUES('inrun-processor','agent_run','running','INRUN','processor-host','codex',now(),'{\"hostId\":\"processor-host\"}')");
  const events=[{id:'processor-usage',seq:1,type:'usage.observation',timestamp:Date.now(),data:{fleetNormalized:{lifecycle:'usage.observation',nativeUsage:{last:{inputTokens:100,outputTokens:7,reasoningOutputTokens:3},total:{inputTokens:99999,outputTokens:9999}}}}}];
  await post('/api/hosts/events',{hostId:'processor-host',executionId:'inrun-processor',events},host.secret);
  const until=Date.now()+10000;
  while(c.sql("SELECT coalesce(header#>>'{tokens,input}','') FROM resource_project_summary WHERE project_id='INRUN'").trim()!=='100'){
   assert.ok(Date.now()<until,'accepted running usage did not reach prepared state');await new Promise(r=>setTimeout(r,20));
  }
  const observed=JSON.parse(c.sql("SELECT jsonb_build_object('state',e.state,'reasoning',s.header#>'{tokens,reasoning}','cpu',t.cpu_ms,'api',t.api_ms,'wall',t.wall_ms) FROM execution e JOIN execution_totals t ON t.execution_id=e.id JOIN resource_project_summary s ON s.project_id=e.project_id WHERE e.id='inrun-processor'").trim());
  assert.deepEqual(observed,{state:'running',reasoning:3,cpu:null,api:null,wall:null});

 }finally{await c.stop();}
});

// Requirement: opt-in service builds and resumes pages while capture remains on; risk: a durable
// page/checkpoint is skipped after restart or rows inserted behind its key disappear from coverage.
test('processor startup resumes durable initial-build pages and drains off-period facts',async()=>{
 const env={FLEET_RESOURCE_PROCESSOR:'off',FLEET_LOST_AFTER_MS:'3600000'};
 const c=await startCoordinator({name:'resource-start',env});
 try{
  c.sql(`INSERT INTO definition(kind,id,path,head,project_id) VALUES('project','build-fixture','fixture',0,'build-fixture');
   INSERT INTO execution(id,kind,state,project_id,data) SELECT 'build-'||lpad(i::text,4,'0'),'tool_run','succeeded','build-fixture','{"tool":"fixture"}'::jsonb FROM generate_series(1,205) i;
   DELETE FROM resource_work WHERE execution_id LIKE 'build-%';`);
  // Simulate a committed first build page, then a stopped process. Work and checkpoint are atomic.
  c.sql(`BEGIN; INSERT INTO resource_build(project_id,after_execution_id) VALUES('build-fixture','build-0100');
   INSERT INTO resource_project_summary(project_id,build_generation) SELECT project_id,generation FROM resource_build WHERE project_id='build-fixture';
   WITH seeded AS (INSERT INTO resource_work(execution_id,origin,build_generation) SELECT id,'build',b.generation FROM execution e JOIN resource_build b ON b.project_id=e.project_id WHERE e.project_id='build-fixture' AND e.id<='build-0100' RETURNING work_id)
   INSERT INTO resource_work_scope SELECT work_id,'build-fixture' FROM seeded; COMMIT;`);
  await c.restart();
  assert.equal(c.sql("SELECT after_execution_id FROM resource_build WHERE project_id='build-fixture'").trim(),'build-0100');
  assert.equal(c.sql("SELECT count(*) FROM resource_work WHERE origin='build'").trim(),'100');
  c.sql("INSERT INTO execution(id,kind,state,project_id,data) VALUES('build-0000','tool_run','succeeded','build-fixture','{\"tool\":\"fixture\"}')");
  env.FLEET_RESOURCE_PROCESSOR='on';await c.restart();
  const deadline=Date.now()+20000;
  while(c.sql("SELECT ready FROM resource_project_summary WHERE project_id='build-fixture'").trim()!=='t'){
   assert.ok(Date.now()<deadline,'processor did not publish ready build');
   await new Promise(r=>setTimeout(r,25));
  }
  const h=JSON.parse(c.sql("SELECT header FROM resource_project_summary WHERE project_id='build-fixture'").trim());
  assert.equal(h.executions,206);assert.equal(h.tools.totalCalls,206);assert.equal(h.itemCounts.tools,1);
  assert.equal(c.sql("SELECT count(*) FROM resource_work_scope WHERE project_id='build-fixture'").trim(),'0');
  const version=c.sql("SELECT epoch::text||':'||revision::text FROM resource_project_summary WHERE project_id='build-fixture'").trim();
  env.FLEET_RESOURCE_PROCESSOR='off';await c.restart();
  c.sql("DELETE FROM execution WHERE id='build-0000'");await c.restart();
  assert.equal(c.sql("SELECT epoch::text||':'||revision::text FROM resource_project_summary WHERE project_id='build-fixture'").trim(),version);
  assert.equal(c.sql("SELECT count(*) FROM resource_work WHERE execution_id='build-0000'").trim(),'1');
  env.FLEET_RESOURCE_PROCESSOR='on';await c.restart();
  const until=Date.now()+10000;
  while(c.sql("SELECT header->>'executions' FROM resource_project_summary WHERE project_id='build-fixture'").trim()!=='205'){
   assert.ok(Date.now()<until,'off backlog did not drain');await new Promise(r=>setTimeout(r,25));
  }
 }finally{await c.stop();}
});
