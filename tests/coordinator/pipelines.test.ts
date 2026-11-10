// Pipeline format v2 runner (docs/designs/fleet-lab.md "Connections: one accounting path").
import {test} from 'node:test';
import assert from 'node:assert/strict';
import {mkdtempSync,mkdirSync,writeFileSync,chmodSync,readFileSync,cpSync} from 'node:fs';
import {fileURLToPath} from 'node:url';
import {createApp} from './test-app.ts';
import {resolveRefs,problemOf,validatePipeline,shellQuote} from '../../packages/coordinator/pipelines.ts';
import {attachStores,upload} from './artifact-helpers.ts';

const fake=fileURLToPath(new URL('./fixtures/fake_connection.py',import.meta.url));
// A config directory with the given pipelines, the repository's profiles, and one connection record per answer.
function setup(pipelines:Record<string,any>,answers:Record<string,string>){
 const dir=mkdtempSync('/tmp/fleet-pipelines-');mkdirSync(dir+'/pipelines');mkdirSync(dir+'/projects');cpSync('config/agents',dir+'/agents',{recursive:true});
 for(const [id,p] of Object.entries(pipelines))writeFileSync(`${dir}/pipelines/${id}.json`,JSON.stringify(p));
 for(const [conn,answer] of Object.entries(answers)){mkdirSync(`${dir}/connections/${conn}`,{recursive:true});writeFileSync(`${dir}/connections/${conn}/connection.json`,JSON.stringify({answer,log:dir+'/calls.log',jobState:dir+'/job.state'}));chmodSync(`${dir}/connections/${conn}/connection.json`,0o600)}
 writeFileSync(dir+'/calls.log','');writeFileSync(dir+'/job.state','running');
 const config={database:dir+'/db',browserSecret:'b',machineSecret:'m',hostId:'host',projectConfigDirectory:dir,profilesDirectory:dir+'/agents',connectionsDir:dir+'/connections',connectionClient:['python3',fake],storeEndpointHosts:['127.0.0.1'],pipelineTickMs:100};
 const calls=()=>readFileSync(dir+'/calls.log','utf8').split('\n').filter(Boolean).map(l=>JSON.parse(l));
 return {dir,config,calls}}
const boot=async(config:any,fakeStore?:any)=>{const app:any=createApp(config);await app.ready();const f=await attachStores(app,'m',['host'],fakeStore);return {app,f}};
const start=async(app:any,id:string,body:any)=>{const r=await app.inject({method:'POST',url:`/api/pipelines/${id}/runs`,headers:{authorization:'Bearer b'},payload:body});assert.equal(r.statusCode,200,r.body);return r.json()};
const until=async(app:any,id:string,ok:(r:any)=>boolean)=>{for(let i=0;i<200;i++){const r=await app.store.get(id,'pipeline-run');if(ok(r))return r;await new Promise(x=>setTimeout(x,50))}throw Error('timeout waiting for run '+id)};
const api=(conn='pipe-read',extra={})=>({id:'search',kind:'api',connection:conn,tool:'web_search',arguments:{query:'${example.query}'},...extra});

test('references resolve whole values and inline text; a missing one names its path',()=>{
 const ctx={example:{query:'q',inputs:[]},outputs:{search:{results:[{url:'https://a'}]}}};
 assert.deepEqual(resolveRefs({u:'${steps.search.output.results}',t:'see ${steps.search.output.results.0.url} for ${example.query}'},ctx),{u:[{url:'https://a'}],t:'see https://a for q'});
 assert.throws(()=>resolveRefs('${example.missing}',ctx),/ref:example.missing/);
 assert.throws(()=>resolveRefs('${steps.later.output.x}',ctx),/ref:steps.later.output.x/);
 assert.equal(resolveRefs('echo ${example.query}',{example:{query:"x'; rm -rf /"},outputs:{}},shellQuote),"echo 'x'\\''; rm -rf /'");
 assert.throws(()=>validatePipeline({version:1,id:'x',steps:[]}),/version must be 2/);
 assert.throws(()=>validatePipeline({version:2,id:'x',steps:[api(),api()]}),/step id/);
});

test('infra codes are classified; unknown stays unknown',()=>{
 assert.equal(problemOf('error','queue-full'),'infra:queue-full');assert.equal(problemOf('error','http-429'),'infra:http-429');
 assert.equal(problemOf('error','no-usable-key'),'infra:no-usable-key');assert.equal(problemOf('unknown','provider-timeout'),'unknown:provider-timeout');
 assert.equal(problemOf('error','bad-request'),'step:bad-request');
});

test('api steps record tool_run rows with credits; a failed step stops later steps; a missing reference fails with ref:',async()=>{
 const s=setup({two:{version:2,id:'two',steps:[api(),{id:'read',kind:'api',connection:'pipe-read',tool:'web_extract',arguments:{urls:'${steps.search.output.results.0.url}'}}]},
  bad:{version:2,id:'bad',steps:[api('full'),{id:'after',kind:'script',command:'true'}]},ref:{version:2,id:'ref',steps:[api(),{id:'after',kind:'script',command:'echo ${steps.search.output.nothing}'}]},agent:{version:2,id:'agent',steps:[{id:'a',kind:'agent',profile:'codex-researcher',prompt:'x'}]}},{'pipe-read':'ok',full:'queue-full'});
 const {app,f}=await boot(s.config);
 try{
  const run=await until(app,(await start(app,'two',{requestId:'r1',example:{query:'hello'}})).id,r=>r.state!=='running');
  assert.equal(run.state,'completed');assert.deepEqual(run.usage,{credits:4});
  assert.equal(s.calls()[1].arguments.urls,'https://example.com/a');assert.equal(s.calls()[0].label,run.id+':step:0');
  const tools=await app.store.query("SELECT e.parent_execution_id AS parent,t.connection_cost,t.connection_cost_basis AS basis FROM execution e JOIN execution_totals t ON t.execution_id=e.id WHERE e.kind='tool_run' ORDER BY e.id");
  assert.deepEqual(tools.map((t:any)=>[t.parent,t.connection_cost.credits,t.basis]),[[run.id+':step:0',2,'measured'],[run.id+':step:1',2,'measured']]);
  assert.ok(run.steps.every((x:any)=>x.outputArtifactId));
  const out=await app.store.query("SELECT name FROM artifact WHERE execution_id=$1",[run.id+':step:1']);assert.deepEqual(out.map((x:any)=>x.name),['output.json']);
  // Same requestId: the same run, no new call.
  assert.equal((await start(app,'two',{requestId:'r1',example:{query:'hello'}})).id,run.id);assert.equal(s.calls().length,2);
  assert.equal((await app.inject({method:'POST',url:'/api/pipelines/two/runs',headers:{authorization:'Bearer b'},payload:{requestId:'r1',example:{query:'other'}}})).statusCode,409);
  const nostart=await until(app,(await start(app,'agent',{requestId:'r4',example:{query:'x'},overrides:{skills:['absent-skill']}})).id,r=>r.state!=='running');
  assert.equal(nostart.steps[0].problem,'step:start');

  const bad=await until(app,(await start(app,'bad',{requestId:'r2',example:{query:'x'}})).id,r=>r.state!=='running');
  assert.equal(bad.state,'failed');assert.equal(bad.steps[0].problem,'infra:queue-full');assert.equal(bad.steps[1].state,'pending');
  assert.equal((await app.store.query('SELECT problem FROM execution WHERE id=$1',[bad.id]))[0].problem,'infra:queue-full');
  assert.equal((await app.store.query("SELECT count(*)::int AS n FROM command WHERE kind='script'").catch(()=>[{n:0}]))[0]?.n??0,0);

  const ref=await until(app,(await start(app,'ref',{requestId:'r3',example:{query:'x'}})).id,r=>r.state!=='running');
  assert.equal(ref.state,'failed');assert.equal(ref.steps[1].problem,'ref:steps.search.output.nothing');
 }finally{await app.close();await f.close()}
});

test('unknown outcome is not retried; a sync call interrupted by a restart becomes unknown',async()=>{
 const s=setup({slow:{version:2,id:'slow',steps:[api('lost')]}},{lost:'provider-timeout'});
 const {app,f}=await boot(s.config);
 try{const run=await until(app,(await start(app,'slow',{requestId:'u1',example:{query:'x'}})).id,r=>r.state!=='running');
  assert.equal(run.state,'unknown');assert.equal(run.steps[0].problem,'unknown:provider-timeout');
  await new Promise(x=>setTimeout(x,400));assert.equal(s.calls().length,1);
  // A run left with its sync api step running (coordinator died mid-call) is unknown after start, never replayed.
  const stuck={...run,id:'stuck',requestId:'u2',state:'running',steps:[{...run.steps[0],state:'running',problem:undefined,error:undefined}]};await app.store.put('pipeline-run',stuck);
  await app.close();const again=await boot(s.config,f);
  try{const r=await until(again.app,'stuck',x=>x.state!=='running');assert.equal(r.steps[0].problem,'unknown:interrupted');assert.equal(s.calls().length,1)}finally{await again.app.close()}
 }finally{await f.close()}
});

test('a job step keeps its jobId and resumes polling after a restart',async()=>{
 const s=setup({job:{version:2,id:'job',steps:[{...api('meshy',{mode:'job'}),id:'model',tool:'image_to_3d',arguments:{image:'${example.image}'}}]}},{meshy:'job'});
 const first=await boot(s.config);
 const id=(await start(first.app,'job',{requestId:'j1',example:{image:'a.png'}})).id;
 const submitted=await until(first.app,id,r=>r.steps[0].jobId);assert.equal(submitted.steps[0].jobId,'job-1');
 assert.equal((await first.app.store.query('SELECT data FROM execution WHERE id=$1',[id+':step:0']))[0].data.jobId,'job-1');
 await first.app.close();
 writeFileSync(s.dir+'/job.state','succeeded');
 const second=await boot(s.config,first.f);
 try{const done=await until(second.app,id,r=>r.state!=='running');assert.equal(done.state,'completed');assert.deepEqual(done.steps[0].output,{model:'m.glb'});
  assert.equal(s.calls().filter(c=>c.tool==='image_to_3d').length,1);assert.ok(s.calls().some(c=>c.tool==='job_status'));
  const cost=await second.app.store.query("SELECT t.connection_cost FROM execution e JOIN execution_totals t ON t.execution_id=e.id WHERE e.kind='tool_run' AND e.parent_execution_id=$1",[id+':step:0']);
  assert.ok(cost.some((c:any)=>c.connection_cost?.credits===7));
 }finally{await second.app.close();await first.f.close()}
});

test('agent and script steps get earlier outputs as inputs; the agent call file becomes tool_run rows',async()=>{
 const s=setup({chain:{version:2,id:'chain',steps:[api(),{id:'summary',kind:'agent',profile:'codex-researcher',prompt:'Summarize ${example.query}'},{id:'check',kind:'script',command:'test -s "$FLEET_INPUT_DIR/summary/summary.md"'}]}},{'pipe-read':'ok'});
 const {app,f}=await boot(s.config);
 const host=async(route:string,b:any)=>{const r=await app.inject({method:'POST',url:'/api/hosts/'+route,headers:{authorization:'Bearer m'},payload:b});assert.equal(r.statusCode,200,r.body);return r.json()};
 const finish=async(c:any,files:Record<string,string>)=>{const body={hostId:'host',commandId:c.id,executionId:c.executionId};await host('receipts',{...body,status:'started'});
  for(const [path,content] of Object.entries(files))await upload(app,'m',{hostId:'host',executionId:c.executionId,path,content,mediaType:'application/octet-stream'});
  await host('receipts',{...body,status:'completed',artifacts:Object.keys(files)})};
 try{const run0=await start(app,'chain',{requestId:'c1',example:{query:'fleet',inputs:[{path:'notes.txt',text:'n'}],references:[{path:'x',text:'secret'}]}});
  await until(app,run0.id,r=>r.steps[1].executionId);
  const [agent]=(await host('poll',{hostId:'host'})).commands;assert.equal(agent.kind,'start');
  assert.equal(agent.payload.text,'Summarize fleet');assert.equal(agent.payload.connectionLabel,run0.id+':step:1');assert.equal(agent.payload.webRoute,'connections');
  assert.deepEqual(agent.payload.inputs.map((i:any)=>i.path).sort(),['example/notes.txt','search/output.json']);
  // The host may read exactly the named inputs.
  const got=await host('artifacts/get',{hostId:'host',executionId:agent.executionId,artifactId:agent.payload.inputs[0].artifactId});assert.ok(got.url);
  // An upload still pending after the completion receipt holds the step until it settles.
  await app.store.query("INSERT INTO artifact(id,scope,content_hash,size,media_type,name,execution_id,state) VALUES('late','prod',$1,1,'text/plain','late.txt',$2,'pending')",['0'.repeat(64),agent.executionId]);
  await finish(agent,{'summary.md':'# Sources\nhttps://example.com/a','.connection-calls.jsonl':JSON.stringify({tool:'web_search',request_id:'q',label:run0.id+':step:1',outcome:'ok',error_code:null,usage:{credits:3}})+'\n'});
  await new Promise(x=>setTimeout(x,500));assert.equal((await app.store.get(run0.id,'pipeline-run')).steps[1].state,'running');
  await app.store.query("UPDATE artifact SET state='failed' WHERE id='late'");
  await until(app,run0.id,r=>r.steps[2].executionId);
  const [script]=(await host('poll',{hostId:'host'})).commands;assert.equal(script.kind,'script');
  assert.ok(script.payload.inputs.some((i:any)=>i.path==='summary/summary.md'));assert.ok(!script.payload.inputs.some((i:any)=>i.path.includes('.connection-calls')));
  await finish(script,{'script.stdout':''});
  const run=await until(app,run0.id,r=>r.state!=='running');assert.equal(run.state,'completed');assert.deepEqual(run.usage,{credits:5});
  const agentCalls=await app.store.query("SELECT t.connection_cost FROM execution e JOIN execution_totals t ON t.execution_id=e.id WHERE e.kind='tool_run' AND e.parent_execution_id=$1",[run.id+':step:1']);
  assert.deepEqual(agentCalls.map((c:any)=>c.connection_cost.credits),[3]);
  assert.ok(!JSON.stringify(agent.payload).includes('secret'));
 }finally{await app.close();await f.close()}
});

test('an agent step with overrides.host runs only on that host; an unknown host fails the step and says why',async()=>{
 const s=setup({agent:{version:2,id:'agent',steps:[{id:'a',kind:'agent',profile:'codex-researcher',prompt:'x'}]}},{});
 const {app}=await boot({...s.config,nativeHostIds:['w']});
 try{
  const run=await start(app,'agent',{requestId:'h1',example:{query:'x'},overrides:{host:'w'}});
  const r=await until(app,run.id,x=>!!x.steps[0].executionId);
  assert.equal((await app.store.get(r.steps[0].executionId,'execution')).hostId,'w');
  const bad=await until(app,(await start(app,'agent',{requestId:'h2',example:{query:'x'},overrides:{host:'nope'}})).id,x=>x.state!=='running');
  assert.equal(bad.steps[0].problem,'step:start');assert.match(JSON.stringify(bad.steps[0]),/Unknown host nope/);
 }finally{await app.close()}
});
