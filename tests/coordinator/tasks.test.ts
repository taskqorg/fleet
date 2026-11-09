import {test} from 'node:test';import assert from 'node:assert/strict';import {createStore} from '../../packages/coordinator/store.js';
const fresh=async()=>{const s=createStore('memory://');await s.migrate();return s};
const state=async(s:any,id:string)=>(await s.tasks.get(id)).state;
test('task transitions follow the design table only',async()=>{const s=await fresh();await s.tx(async()=>{
 const dep=await s.tasks.create({title:'dependency',projectId:'p'}),id=await s.tasks.create({title:'Build the site',projectId:'p',dependsOn:[dep]});
 assert.equal(await state(s,id),'queued');
 // queued -> in_progress needs a started execution and completed blocking dependencies.
 await assert.rejects(s.tasks.transition(id,'first_execution'),(e:any)=>e.statusCode===409);
 await s.executions.createExecution({id:'run',kind:'agent_run',taskId:id,projectId:'p',state:'running'});
 await assert.rejects(s.tasks.transition(id,'first_execution'),/dependency/);
 assert.equal(await s.tasks.settle(id),'waiting');assert.equal((await s.tasks.get(id)).waitReason,'dependency');
 await s.executions.createExecution({id:'deprun',kind:'agent_run',taskId:dep,state:'running'});await s.tasks.transition(dep,'first_execution');
 const acc=await s.tasks.ask({kind:'acceptance',taskId:dep,text:'Accept?',options:['accepted','rejected'],data:{artifactIds:['artifact:1']}});
 await s.tasks.answer(acc,{answer:'accepted',by:'operator'});const done=await s.tasks.get(dep);assert.deepEqual([done.state,done.acceptedBy,done.acceptedArtifactIds],['completed','operator',['artifact:1']]);
 assert.equal(await s.tasks.settle(id),'in_progress');
 // An agent question blocks; its answer unblocks.
 await s.executions.createExecution({id:'asker',kind:'agent_run',taskId:id,state:'running'});
 const q=await s.tasks.ask({kind:'agent_question',executionId:'asker',text:'Which colour?'});assert.equal(await state(s,id),'waiting');
 assert.equal((await s.tasks.questions({taskId:id}))[0].taskId,id);
 await s.tasks.answer(q,{answer:'blue',by:'operator'});assert.equal(await state(s,id),'in_progress');
 await assert.rejects(s.tasks.answer(q,{answer:'red',by:'operator'}),(e:any)=>e.statusCode===409);
 // Pause returns to the state before; rejection keeps the task in progress; cancel is final.
 await s.tasks.update({taskId:id,action:'pause'});assert.equal(await state(s,id),'paused');await s.tasks.update({taskId:id,action:'resume'});assert.equal(await state(s,id),'in_progress');
 const r=await s.tasks.ask({kind:'acceptance',taskId:id,text:'Accept?',options:['accepted','rejected']});await assert.rejects(s.tasks.answer(r,{answer:'maybe',by:'operator'}),(e:any)=>e.statusCode===400);
 await s.tasks.answer(r,{answer:'rejected',by:'operator'});assert.equal(await state(s,id),'in_progress');
 await s.tasks.update({taskId:id,action:'cancel'});assert.equal(await state(s,id),'cancelled');await assert.rejects(s.tasks.update({taskId:id,action:'resume'}),(e:any)=>e.statusCode===409);
 const t=await s.tasks.get(id);assert.equal(t.progress.executions,2);assert.deepEqual(t.dependencies,[{id:dep,kind:'blocks'}]);assert.equal(t.openQuestions.length,0);
 assert.deepEqual((await s.tasks.list({projectId:'p'})).map((x:any)=>[x.title,x.state]),[['dependency','completed'],['Build the site','cancelled']])});await s.close()});
test('a delivery is a task: field and state mapping (design §2)',async()=>{const s=await fresh();
 await s.put('project-question',{id:'q1',projectId:'p',requestId:'delivery:r1',text:'Confirm the task',state:'waiting',createdAt:1,actor:'operator'});
 const r:any={id:'d1',projectId:'p',requestId:'r1',signature:'{}',task:'Make a page\nwith sources',methodId:'m',profileId:'baseline',account:'fleet-codex',invocation:'pipeline',questionId:'q1',previousDeliveryId:null,createdAt:1,state:'waiting',steps:[]};
 await s.put('delivery',r);assert.deepEqual(await s.get('d1','delivery'),r);
 let t=await s.tasks.get('d1');assert.deepEqual([t.title,t.expectedOutcome,t.state,t.waitReason,t.legacyId],['Make a page','Make a page\nwith sources','waiting','question','d1']);
 assert.deepEqual(t.completionRequirements,{requestId:'r1',signature:'{}'});assert.deepEqual(t.deliveryMethod,{methodId:'m',profileId:'baseline',account:'fleet-codex',invocation:'pipeline',branch:null});
 assert.deepEqual(t.questions.map((q:any)=>[q.id,q.kind,q.taskId]),[['q1','operator_decision','d1']]);
 // The operator answer goes through the one answer path; the legacy record still reads as before.
 const q=await s.get('q1','project-question');Object.assign(q,{state:'answered',answer:'yes',answeredAt:2,answeredBy:'operator'});await s.put('project-question',q);
 assert.deepEqual(await s.get('q1','project-question'),q);assert.equal((await s.tasks.questions({taskId:'d1'}))[0].state,'answered');
 await s.put('execution',{id:'g',executionId:'g',sessionId:'sx',hostId:'mac',state:'started',startedAt:5,deliveryId:'d1'});
 await s.put('delivery',{...r,state:'generating',steps:[{kind:'generate',executionId:'g',state:'started'}]});t=await s.tasks.get('d1');assert.equal(t.state,'in_progress');assert.deepEqual(t.executions.map((e:any)=>[e.id,e.taskId]),[['g','d1']]);
 await s.put('artifact',{id:'artifact:g:index.html',executionId:'g',path:'index.html',contentBase64:'eA==',size:1,sha256:'0'});
 await s.put('delivery',{...r,state:'merged',steps:[{kind:'generate',executionId:'g',state:'completed'}]});t=await s.tasks.get('d1');
 const acc=t.questions.find((q:any)=>q.kind==='acceptance')!;assert.equal(t.state,'in_progress');assert.deepEqual(acc.artifactIds,['artifact:g:index.html']);assert.equal(t.latestResult.path,'index.html');
 await s.put('delivery',{...r,state:'merged',steps:[{kind:'generate',executionId:'g',state:'completed'}]});assert.equal((await s.tasks.questions({taskId:'d1',kind:'acceptance'})).length,1);
 await s.tasks.answer(acc.id,{answer:'accepted',by:'operator'});assert.equal(await state(s,'d1'),'completed');
 // A revision is recorded as revision_of and does not wait for the revised task.
 await s.put('delivery',{...r,id:'d2',requestId:'r2',previousDeliveryId:'d1',questionId:undefined,state:'researching'});t=await s.tasks.get('d2');assert.deepEqual(t.dependencies,[{id:'d1',kind:'revision_of'}]);
 await s.put('delivery',{...r,id:'d3',requestId:'r3',questionId:undefined,state:'failed'});await s.put('pipeline-run',{id:'run3',projectId:'p',state:'failed',steps:[],requestedBy:{kind:'coordinator',deliveryId:'d3'}});
 await s.put('delivery',{...r,id:'d3',requestId:'r3',questionId:undefined,state:'failed'});assert.equal(await state(s,'d3'),'failed');
 assert.deepEqual((await s.all('delivery',{projectId:'p'})).map((x:any)=>x.id),['d1','d2','d3']);await s.close()});
test('row-level security: forced on the owner, scoped by fleet.project_ids',async()=>{const s=await fresh();
 await s.put('session',{id:'sp',createdAt:1,projectId:'p'});await s.put('session',{id:'sq',createdAt:1,projectId:'q'});await s.put('session',{id:'sn',createdAt:1});
 await s.tx(async()=>{await s.tasks.create({id:'tp',title:'p task',projectId:'p'});await s.tasks.create({id:'tq',title:'q task',projectId:'q'})});
 await s.query("DO $$ BEGIN IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname='fleet_app_test') THEN CREATE ROLE fleet_app_test NOLOGIN NOBYPASSRLS; END IF; END $$");
 await s.query('GRANT SELECT,INSERT,UPDATE,DELETE ON ALL TABLES IN SCHEMA public TO fleet_app_test');await s.query('GRANT USAGE,SELECT ON ALL SEQUENCES IN SCHEMA public TO fleet_app_test');
 const seen=async(scope:string|null)=>s.tx(async()=>{await s.query('SET LOCAL ROLE fleet_app_test');if(scope===null)await s.query("RESET fleet.project_ids");else await s.query("SELECT set_config('fleet.project_ids',$1,true)",[scope]);
  return {docs:(await s.query("SELECT id FROM document WHERE kind='session' ORDER BY id")).map((r:any)=>r.id),tasks:(await s.query('SELECT id FROM task ORDER BY id')).map((r:any)=>r.id)}});
 assert.deepEqual(await seen('p'),{docs:['sp'],tasks:['tp']});assert.deepEqual(await seen('p,'),{docs:['sn','sp'],tasks:['tp']});assert.deepEqual(await seen('*'),{docs:['sn','sp','sq'],tasks:['tp','tq']});
 assert.deepEqual(await seen(null),{docs:[],tasks:[]});
 await assert.rejects(s.tx(async()=>{await s.query('SET LOCAL ROLE fleet_app_test');await s.query("SELECT set_config('fleet.project_ids','p',true)");await s.query("INSERT INTO task(id,title,project_id) VALUES('x','x','q')")}),/row-level security/);
 await s.close()});

test('a task moves to in_progress when its first execution starts, without any delivery',async()=>{const s=await fresh();
 await s.tx(async()=>{await s.tasks.create({id:'t',title:'model',projectId:'p'});await s.put('session',{id:'ss',createdAt:1,projectId:'p',deliveryId:'t'});await s.put('execution',{id:'x',executionId:'x',sessionId:'ss',hostId:'h',state:'queued'})});
 assert.equal(await state(s,'t'),'queued');await s.put('execution',{id:'x',executionId:'x',sessionId:'ss',hostId:'h',state:'started',startedAt:1});assert.equal(await state(s,'t'),'in_progress');await s.close()});

// task_page after migration 10 (docs/designs/fleet-view-urls-and-list-path.md §3): same rows, order, counts and groups as the
// query it replaced. The old query is kept here as the reference.
test('task_page on task_derived matches the old per-task subquery query for every group and filter',async()=>{const s=await fresh();
 await s.tx(async()=>{for(let i=0;i<60;i++){const id=await s.tasks.create({id:'t'+String(i).padStart(2,'0'),title:['Render','Fix CI','Hero copy'][i%3]+' '+i,projectId:[null,'dashq','game'][i%3],priority:['urgent','normal','background'][i%3]});
  if(i%4===0)await s.executions.createExecution({id:'e'+i,kind:'agent_run',taskId:id,projectId:[null,'dashq','game'][i%3],hostId:['mac','pds18'][i%2],harness:['codex','claude'][i%2],state:i%8?'running':'succeeded'});
  if(i%5===0)await s.tasks.ask({kind:'operator_decision',taskId:id,text:'q'+i,options:['a']})}});
 const G:Record<string,string>={none:"left(x.id,0)",priority:"(CASE x.priority WHEN 'urgent' THEN '1' WHEN 'normal' THEN '2' ELSE '3' END)",project:"coalesce(x.project_id,'~')",agent:"coalesce(x.agent,'~')",host:"coalesce(x.host,'~')",
  state:"(CASE x.state WHEN 'waiting' THEN '1' WHEN 'failed' THEN '2' WHEN 'in_progress' THEN '3' WHEN 'queued' THEN '4' WHEN 'paused' THEN '5' WHEN 'completed' THEN '6' ELSE '7' END)"};
 const base=`WITH x AS (SELECT t.id,t.project_id,t.state,t.priority,t.seq,
  (SELECT e.harness FROM execution e WHERE e.task_id=t.id AND e.state IN ('preparing','running') ORDER BY e.seq DESC LIMIT 1) AS agent,
  (SELECT e.host_id FROM execution e WHERE e.task_id=t.id ORDER BY e.seq DESC LIMIT 1) AS host,
  (SELECT count(*) FROM question q WHERE q.task_id=t.id AND q.state='open') AS open_questions FROM task t)`;
 const old=async(group:string,f:{state?:string[],search?:string,projectId?:string|null})=>{const c:string[]=[],cs:string[]=[],p:any[]=[];
  if(f.projectId!==undefined){if(f.projectId===null)c.push('x.project_id IS NULL');else{p.push(f.projectId);c.push(`x.project_id=$${p.length}`)}}
  if(f.search){p.push('%'+f.search.toLowerCase()+'%');c.push(`lower((SELECT title FROM task WHERE id=x.id)) LIKE $${p.length}`)}
  cs.push(...c);if(f.state?.length){p.push(f.state);c.push(`x.state::text=ANY($${p.length})`)}
  const W=(w:string[])=>w.length?' WHERE '+w.join(' AND '):'';
  const rows=(await s.query(`${base} SELECT x.id,x.agent,x.host,x.open_questions FROM x${W(c)} ORDER BY ${G[group]},-x.seq,x.id`,p)).map((r:any)=>[r.id,r.agent,r.host,Number(r.open_questions)]);
  const counts=Object.fromEntries((await s.query(`${base} SELECT x.state::text AS s,count(*) AS n FROM x${W(cs)} GROUP BY 1`,p.slice(0,p.length-(f.state?.length?1:0)))).map((r:any)=>[r.s,Number(r.n)]));
  const groups=group==='none'?[]:(await s.query(`${base} SELECT ${G[group]} AS g,count(*) AS n FROM x${W(c)} GROUP BY 1 ORDER BY 1`,p)).map((r:any)=>[r.g,Number(r.n)]);
  return {rows,counts,groups}};
 for(const group of Object.keys(G))for(const f of [{},{state:['in_progress']},{search:'hero'},{projectId:'game'},{projectId:null},{state:['queued','in_progress'],projectId:'dashq'}]){
  const n=await s.read(()=>s.tasks.page({filter:f,group,limit:500}));const o=await old(group,f);
  assert.deepEqual(n.tasks.map((t:any)=>[t.id,t.agent,t.host,t.openQuestions]),o.rows,`rows ${group} ${JSON.stringify(f)}`);
  assert.deepEqual(Object.fromEntries(Object.entries(n.counts).filter(([,v])=>v)),o.counts,`counts ${group} ${JSON.stringify(f)}`);
  assert.deepEqual(n.groups.map((g:any)=>[g.key,g.count]),o.groups,`groups ${group} ${JSON.stringify(f)}`)}
 await s.close()});

test('task_page takes the view codec page params: project lists with "no project"',async()=>{const s=await fresh();
 const {parseView,toPageParams}=await import('../../packages/view-url/view-url.js');
 await s.tx(async()=>{await s.tasks.create({id:'a',title:'a',projectId:'p'});await s.tasks.create({id:'b',title:'b',projectId:'q'});await s.tasks.create({id:'c',title:'c'})});
 const ids=async(url:string)=>(await s.read(()=>s.tasks.page({...toPageParams(parseView(url)!),limit:50}))).tasks.map((t:any)=>t.id).sort();
 assert.deepEqual(await ids('/tasks?project=p'),['a']);assert.deepEqual(await ids('/tasks?project=-,p'),['a','c']);
 assert.deepEqual(await ids('/tasks?project=p,q'),['a','b']);assert.deepEqual(await ids('/tasks'),['a','b','c']);
 await s.close()});
