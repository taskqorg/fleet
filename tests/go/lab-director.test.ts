// Requirement: fleet-people-access §5, Lab creator inheritance. Risk: background/recovered Lab work borrows owner rights.
// Own real PostgreSQL fixture; SQL is used only for historical/recovery seeds and transaction-state assertions.
import {test} from 'node:test';
import assert from 'node:assert/strict';
import {mkdtempSync,readFileSync,writeFileSync} from 'node:fs';
import {tmpdir} from 'node:os';
import {hostCommand, startCoordinator} from '../harness.ts';
import {fakeS3} from '../s3-fake.ts';
import {selectDefaultSwarmTrial} from '../../app/src/views/lab-swarm.ts';

// Requirement: fleet-people-access §5 Lab creator inheritance; risk: background runs escalate to owner authority.
test('Lab retains authenticated requester through background enqueue, revocation, deletion and recovery without owner escalation', {timeout:60_000}, async t=>{
 const owner='owner@trial.test',creator='architect@trial.test',project='desiign';
 const dir=mkdtempSync(tmpdir()+'/fleet-lab-director-');
 const people=JSON.parse(readFileSync('tests/trial/people.json','utf8'));people.push({email:creator,name:'Architect',roles:['operator','architect'],grants:[{kind:'project',id:project}]});writeFileSync(dir+'/people.json',JSON.stringify(people));
 const store=await fakeS3();
 const c=await startCoordinator({name:'lab_director',people:dir+'/people.json',env:{FLEET_PIPELINE_TICK_MS:'100',FLEET_RULES_TICK_MS:'100',FLEET_SECRET_DIR:dir+'/secret',FLEET_STORE_SECRETS:dir+'/stores',FLEET_STORE_ENDPOINT_HOSTS:'127.0.0.1',FLEET_DEFAULT_ARTIFACT_LOCATIONS:'trial-host',FLEET_GATEWAY_DIR:dir+'/gateway',FLEET_GATEWAY_URL:'http://127.0.0.1:1'}});
 const request=async(path:string,body:any,person=owner,token=c.secret)=>{const r=await fetch(c.base+path,{method:body===undefined?'GET':'POST',headers:{authorization:'Bearer '+token,...(token===c.secret?{'x-fleet-person':person}:{}),'content-type':'application/json'},body:body===undefined?undefined:JSON.stringify(body)});return{status:r.status,body:await r.json() as any}};
 const ok=(r:{status:number,body:any})=>{assert.equal(r.status,200,JSON.stringify(r.body));return r.body};
 const op=async(name:string,body:any,person=owner)=>ok(await request('/api/ops/'+name,body,person));
 const until=async(fn:()=>Promise<any>)=>{for(let i=0;i<200;i++){const x=await fn();if(x)return x;await new Promise(r=>setTimeout(r,25))}throw Error('fixture state did not settle')};
 const agentRows=(id:string)=>JSON.parse(c.sql(`SELECT coalesce(json_agg(data),'[]') FROM execution WHERE kind='agent_run' AND data->>'experimentId'='${id}'`));
 try{
  const host=await op('hosts_register',{id:'trial-host',holder:creator});
  const poll={platform:'darwin',capabilities:[],stores:[{id:'fixture-store',reachable:true}],problems:[]};
  const hostCall=async(path:string,body:any)=>ok(await request(path,body,owner,host.secret));
  await hostCall('/api/hosts/poll',poll);
  await hostCall('/api/hosts/stores',{store:{id:'fixture-store',bucket:'fleet',endpoint:`http://127.0.0.1:${store.port}`,accessKeyId:'fixture-key',secretAccessKey:'fixture-secret'}});
  await op('definition_create',{kind:'pipeline',id:'lab-director-fixture',content:{pipeline:{id:'lab-director-fixture',version:2,steps:[{id:'read',kind:'agent',profile:'baseline',prompt:'Fixture: never execute a model.'}]}},message:'fixture'});
  const example={id:'lab-director-fixture',version:1,origin:'synthetic',use_case_id:'fleet:UC5',fixture:null,prompt:'fixture',inputs:[],references:[],checks:[],split:'tune'};
  const ex=(await op('lab_examples_add',{examples:[example],approved:true})).examples[0];
  const body={kind:'single',projectId:project,pipelineId:'lab-director-fixture',examples:[ex.artifactId],variants:[{label:'fixture'}],repeats:1,evaluators:[],goal:'identity fixture',hypothesis:'creator retained'};
  const create=async(person=creator)=>op('lab_experiment_create',body,person);
  const get=async(id:string)=>op('lab_experiment_get',{experimentId:id});
  const launch=async(id:string)=>op('lab_experiment_action',{experimentId:id,action:'launch'});
  t.beforeEach(async()=>{await op('people_set',{people})});
  await t.test('authenticated creation ignores no caller identity and rejects forged provenance',async()=>{
   assert.equal((await request('/api/ops/lab_experiment_create',{...body,requestedBy:owner},creator)).status,400);
   const e=await create();assert.equal(e.requestedBy,creator);await launch(e.id);
   const row=await until(async()=>agentRows(e.id)[0]);assert.equal(row.directedBy,creator);
   const session=await op('session_get',{sessionId:row.sessionId});assert.equal(session.directedBy,creator);
   const run=await op('pipeline_get',{runId:row.pipelineRunId});assert.equal(run.requestedBy.email,creator);
   const command=await hostCommand(q=>hostCall('/api/hosts/poll'+q,poll),(x:any)=>x.executionId===row.id);assert.ok(command,'fixture host claims native command without executing it');
   const lineage=JSON.parse(c.sql(`SELECT json_agg(json_build_object('kind',kind,'experiment',experiment_id,'variant',variant)) FROM execution WHERE id IN ('${row.pipelineRunId}','${row.pipelineStepId}','${row.id}')`));
   assert.equal(lineage.length,3);
   for(const item of lineage) assert.deepEqual([item.experiment,item.variant],[e.id,'fixture'],'trial identity survives native enqueue and host assignment');
   const mine=await op('task_create',{projectId:project,title:'mine fixture'});
   const foreign=await op('task_create',{projectId:'hello-world',title:'foreign fixture'});
   const response=await request('/api/hosts/agent-tools',{executionId:row.id,commandId:command.id,request:{jsonrpc:'2.0',id:1,method:'tools/call',params:{name:'task_get',arguments:{taskId:foreign.id}}}},owner,host.secret);
   assert.equal(response.status,404,'actual bound tool uses creator current project scope, not owner');
   await op('people_set',{people:people.map((p:any)=>p.email===creator?{...p,grants:[]}:p)});
   const revoked=await request('/api/hosts/agent-tools',{executionId:row.id,commandId:command.id,request:{jsonrpc:'2.0',id:2,method:'tools/call',params:{name:'task_get',arguments:{taskId:mine.id}}}},owner,host.secret);
   assert.equal(revoked.status,404,'native bound request refreshes revoked authority');
   await op('people_set',{people});
  });
  // Requirement: a selected trial step waiting for placement shows its durable reason through dispatch and settlement.
  // Risk: Fleet reports two stuck executions for one undispatched child, or stops reconciling the original run.
  await t.test('a coding trial waits on forge capability, then dispatches and reconciles the original child', async () => {
   const id = 'lab-coding-placement';
   const codingHost = 'lab-coding-host';
   const codingHostRegistration = await op('hosts_register',{id:codingHost,holder:owner});
   const codingHostCall = async(path:string,body:any) => ok(await request(path,body,owner,codingHostRegistration.secret));
   const settings = {
    id, name: 'Coding placement fixture', contextFiles: [],
    repos: [{id:'main',remote:'https://github.com/invented/project.git',forge:'github',defaultBranch:'main'}],
    environments: [{hostId:codingHost,checkoutPath:'/invented/project'}],
    execution: {profiles:['coding-worker'],tools:['node','pnpm','browser'],forge:true},
   };
   await op('project_create',{id,requestId:id,content:{settings,contextFiles:[]}});
   await op('people_set',{people:people.map((p:any)=>p.email===creator?{...p,grants:[...p.grants,{kind:'project',id}]}:p)});
   // The shipped coding profile declares this connection. Grant it only in the fixture, without a provider key.
   const connections = (await op('connections_list',{})).connections;
   if (!connections.some((c:any)=>c.name==='youtrack')) await op('connections_create',{name:'youtrack',provider:'youtrack'});
   await op('connections_set_access',{connection:'youtrack',access:{projects:[id]}});
   const tools = ['tool:node','tool:pnpm','tool:browser'];
   const codingPoll = {...poll,capabilities:tools,stores:[]};
   await codingHostCall('/api/hosts/poll',codingPoll);
   const experiment = await op('lab_experiment_create',{
    ...body,projectId:id,pipelineId:'coding-task',variants:[{label:'placement',overrides:{host:codingHost}}],
   },owner);
   assert.equal(experiment.requestedBy,owner);
   await launch(experiment.id);
   const child = await until(async()=>{
    const row = agentRows(experiment.id)[0];
    if (row) return row;
    const trial = (await get(experiment.id)).trials[0];
    assert.ok(!trial || !['failed','cancelled'].includes(trial.state),JSON.stringify({state:trial?.state,problem:trial?.problem}));
   });
   assert.equal(child.directedBy,owner);
   const read = () => op('pipeline_get',{runId:child.pipelineRunId},creator);
   const wait = 'project forge connection unavailable on '+codingHost;
   const decision = await until(async()=> {
    const route = await op('route_decisions',{executionId:child.id});
    return route.decisions.at(-1);
   });
   assert.equal(decision.winner,null);
   assert.deepEqual(decision.candidates.map((candidate:any)=>candidate.host),[codingHost]);
   assert.equal(decision.candidates[0].skipped,wait);
   c.sql(`UPDATE execution SET started_at=now()-interval '1 hour' WHERE id IN ('${child.pipelineRunId}','${child.pipelineStepId}');
    INSERT INTO execution(id,kind,state,project_id,started_at) VALUES ('placement-stuck-control','agent_run','running','${id}',now()-interval '1 hour');`);
   const findings = await until(async()=> {
    const reply = await op('telemetry_findings',{projectId:id});
    return reply.findings.some((f:any)=>f.executionId==='placement-stuck-control')&&reply.findings;
   });
   const pending = await read();
   assert.equal(pending.state,'running','pipeline clients must keep polling during a placement wait');
   assert.equal(pending.waitDetail,wait);
   assert.deepEqual([pending.plan.entries[0].execution.state,pending.plan.entries[0].execution.waitDetail],['waiting',wait]);
   const lab = await get(experiment.id);
   const trial = lab.trials.find((t:any)=>t.id===child.pipelineRunId);
   assert.deepEqual([trial.state,trial.waitDetail],['waiting',wait]);
   assert.equal(trial.spend.tokens,undefined,'missing usage stays unknown');
   // The default Lab Swarm board follows a live trial even when a completed attempt has a higher number.
   const finishedAttempt = {...trial,id:'finished-swarm-attempt',state:'succeeded',cell:{...trial.cell,attempt:(trial.cell.attempt ?? 0)+1}};
   assert.equal(selectDefaultSwarmTrial({...lab,trials:[trial,finishedAttempt]})?.id,trial.id,'a placement-waiting trial outranks a later finished attempt');
   assert.ok(!findings.some((f:any)=>[child.pipelineRunId,child.pipelineStepId].includes(f.executionId)));
   assert.deepEqual((await op('execution_events',{executionId:child.id})).events,[]);

   const capable = {...codingPoll,capabilities:[...tools,'forge:invented/project']};
   const command = await hostCommand(q=>codingHostCall('/api/hosts/poll'+q,capable),(x:any)=>x.executionId===child.id);
   assert.ok(command,'the original child dispatches once the host is eligible');
   const routed = await op('route_decisions',{executionId:child.id});
   assert.equal(routed.decisions.at(-1).winner?.host,codingHost);
   await codingHostCall('/api/hosts/receipts',{commandId:command.id,executionId:child.id,status:'started'});
   const active = await read();
   assert.equal(active.state,'running');
   assert.equal(active.plan.entries[0].dispatch.phase,'handed_off');
   assert.equal(active.plan.entries[0].execution.waitDetail,undefined);
   // A completed dispatch counts as progress even when the run and step events predate the wait.
   c.sql(`UPDATE execution SET started_at=now()-interval '1 hour' WHERE id IN ('${child.pipelineRunId}','${child.pipelineStepId}');
    UPDATE event SET updated_at=now()-interval '1 hour' WHERE execution_id IN ('${child.pipelineRunId}','${child.pipelineStepId}');
    INSERT INTO execution(id,kind,state,project_id,started_at) VALUES ('placement-handed-off-control','agent_run','running','${id}',now()-interval '1 hour');`);
   const afterHandoff = await until(async()=>{
    const reply = await op('telemetry_findings',{projectId:id});
    return reply.findings.some((f:any)=>f.executionId==='placement-handed-off-control')&&reply.findings;
   });
   assert.ok(!afterHandoff.some((f:any)=>[child.pipelineRunId,child.pipelineStepId].includes(f.executionId)), 'a recent handoff resets the idle clock after a long placement wait');
   await codingHostCall('/api/hosts/receipts',{commandId:command.id,executionId:child.id,status:'failed',error:'fixture failure'});
   const ended = await until(async()=>{const run=await read();return run.state==='failed'&&run;});
   assert.equal(ended.plan.entries[0].execution.state,'failed');
   assert.equal(ended.plan.entries[0].execution.nativeExecutionId,child.id);
   assert.equal(agentRows(experiment.id).length,1);
   await until(async()=>(await get(experiment.id)).state==='done');
  });
  // Requirement: Fleet Lab variant lineage; risk: script jobs cannot be attributed to their trial.
  await t.test('script steps retain the trial variant independently of an agent profile',async()=>{
   await hostCall('/api/hosts/poll',{...poll,capabilities:['script']});
   await op('definition_create',{kind:'pipeline',id:'lab-script-lineage',content:{pipeline:{id:'lab-script-lineage',version:2,steps:[{id:'script',kind:'script',host:'trial-host',command:'true'}]}},message:'fixture'});
   const e=await op('lab_experiment_create',{...body,pipelineId:'lab-script-lineage',variants:[{label:'reviewed'}]});await launch(e.id);
   const job=await until(async()=>JSON.parse(c.sql(`SELECT coalesce(json_agg(json_build_object('id',id,'variant',variant,'experiment',experiment_id)),'[]') FROM execution WHERE kind='host_job' AND experiment_id='${e.id}'`))[0]);
   assert.equal(job.variant,'reviewed');assert.equal(job.experiment,e.id);
  });
  // R1, R2, R3; risk: a background pipeline drops or refreshes frozen source pins after project Save.
  await t.test('background Lab enqueue retains the pipeline frozen project source descriptor',async()=>{
   const id='lab-pinned-context';
   const source={repo:'specq',revision:'1'.repeat(40),core:[{path:'engineering/core.md',sha256:'a'.repeat(64)}]};
   const content={settings:{id,name:'Pinned context fixture',contextFiles:['rules.md'],roles:{worker:'baseline'},repos:[{id:'main',remote:'https://github.com/taskqorg/fleet.git'},{id:'specq',remote:'https://github.com/taskqorg/specq.git'}],contextSources:[source]},contextFiles:[{path:'rules.md',text:'Fixture only; never run a model.'}]};
   await op('project_create',{id,requestId:'lab-pinned-create',content});
   const e=await op('lab_experiment_create',{...body,projectId:id},owner);await launch(e.id);
   const row=await until(async()=>agentRows(e.id)[0]);assert.deepEqual(row.projectSnapshot.settings.contextSources,[source]);
   const next=structuredClone(content);next.settings.contextSources[0].revision='2'.repeat(40);
   await op('project_update',{projectId:id,requestId:'lab-pinned-update',baseRevision:1,content:next});
   assert.deepEqual(agentRows(e.id)[0].projectSnapshot.settings.contextSources,[source]);
   const run=await op('pipeline_get',{runId:row.pipelineRunId});assert.deepEqual(run.projectSnapshot.settings.contextSources,[source]);
  });
  await t.test('project or architect-role revocation pauses; regrant permits explicit resume as the same person',async()=>{
   for(const field of ['grants','roles']){
    const e=await create();await op('people_set',{people:people.map((p:any)=>p.email===creator?{...p,[field]:field==='roles'?['operator']:[]}:p)});
    const paused=await launch(e.id);assert.equal(paused.state,'paused');assert.equal(paused.stopReason,'requester:access-revoked');assert.equal(agentRows(e.id).length,0);
    await op('people_set',{people});await op('lab_experiment_action',{experimentId:e.id,action:'resume'});
    assert.equal((await until(async()=>agentRows(e.id)[0])).directedBy,creator);assert.equal((await get(e.id)).stopReason,null);
   }
  });
  await t.test('revocation after a script started fences the actual next agent enqueue',async()=>{
   const pipelineId='lab-director-late';
   const lateHost=await op('hosts_register',{id:'late-host',holder:owner});
   const lateCall=async(path:string,body:any)=>ok(await request(path,body,owner,lateHost.secret));
   await lateCall('/api/hosts/poll',poll);
   await op('definition_create',{kind:'pipeline',id:pipelineId,content:{pipeline:{id:pipelineId,version:2,steps:[{id:'prepare',kind:'script',host:'late-host',command:'echo fixture'},{id:'read',kind:'agent',profile:'baseline',prompt:'Never execute a model.'}]}},message:'late enqueue fixture'});
   const e=await op('lab_experiment_create',{...body,pipelineId},creator);await launch(e.id);
   const row=await until(async()=>JSON.parse(c.sql(`SELECT coalesce(json_agg(data),'[]') FROM execution WHERE data->>'actor'='program-worker' AND data->>'experimentId'='${e.id}'`))[0]);
   const command=await until(async()=>(await lateCall('/api/hosts/poll',poll)).commands.find((x:any)=>x.executionId===row.id));
   await lateCall('/api/hosts/receipts',{commandId:command.id,executionId:row.id,status:'started'});
   await op('people_set',{people:people.map((p:any)=>p.email===creator?{...p,grants:[]}:p)});
   await lateCall('/api/hosts/receipts',{commandId:command.id,executionId:row.id,status:'completed',artifacts:[]});
   await until(async()=>{const x=await get(e.id);return x.state==='paused'||x.state==='completed'||x.state==='failed'});
   assert.equal(agentRows(e.id).length,0,'actual continuation must not borrow owner authority');
   await op('people_set',{people});
  });
  await t.test('deletion tombstone survives same-email re-add and historical NULL is never backfilled',async()=>{
   const e=await create();await op('people_set',{people:people.filter((p:any)=>p.email!==creator)});await op('people_set',{people});
   const stopped=await launch(e.id);assert.equal(stopped.state,'stopped');assert.equal(stopped.stopReason,'requester:removed');assert.equal(stopped.requestedBy,creator);assert.ok(stopped.requesterRemovedAt);assert.equal(agentRows(e.id).length,0);
   const legacy=await create();c.sql(`UPDATE experiment SET requested_by=NULL WHERE id='${legacy.id}'`);
   const old=await launch(legacy.id);assert.equal(old.state,'stopped');assert.equal(old.stopReason,'requester:unknown');assert.equal(old.requestedBy,null);assert.equal(agentRows(legacy.id).length,0);assert.equal((await get(legacy.id)).id,legacy.id,'history remains readable');
  });
  await t.test('periodic recovery uses stored requester and never invents ownership for unknown rows',async()=>{
   const e=await create();c.sql(`UPDATE experiment SET state='running' WHERE id='${e.id}'`);
   assert.equal((await until(async()=>agentRows(e.id)[0])).directedBy,creator);
   const legacy=await create();c.sql(`UPDATE experiment SET requested_by=NULL,state='running' WHERE id='${legacy.id}'`);
   await until(async()=>{const x=await get(legacy.id);return x.state==='stopped'&&x.stopReason==='requester:unknown'});assert.equal(agentRows(legacy.id).length,0);
   const own=await create(owner);await launch(own.id);assert.equal((await until(async()=>agentRows(own.id)[0])).directedBy,owner,'authenticated owner remains intentional owner');
  });
  await t.test('another scoped architect can act without hiding the stored requester or borrowing its authority',async()=>{
   const e=await create(owner);
   await op('lab_experiment_action',{experimentId:e.id,action:'launch'},creator);
   assert.equal((await until(async()=>agentRows(e.id)[0])).directedBy,owner);
   const foreign=await op('lab_experiment_create',{...body,projectId:'hello-world'});
   assert.equal((await request('/api/ops/lab_experiment_action',{experimentId:foreign.id,action:'launch'},creator)).status,404,'temporary internal authority lookup does not expose foreign experiment');
  });
  await t.test('create/remove race serializes creator validation and deletion tombstone',async()=>{
   const [made,removed]=await Promise.all([request('/api/ops/lab_experiment_create',body,creator),request('/api/ops/people_set',{people:people.filter((p:any)=>p.email!==creator)})]);ok(removed);
   if(made.status===200){const x=await get(made.body.id);assert.ok(x.requesterRemovedAt);await op('people_set',{people});const stopped=await launch(x.id);assert.equal(stopped.stopReason,'requester:removed');assert.equal(agentRows(x.id).length,0)}
   else {assert.ok([401,403].includes(made.status),JSON.stringify(made));await op('people_set',{people})}
  });
 }finally{await c.stop();store.close()}
});
