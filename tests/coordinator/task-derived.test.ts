// Derived task values (docs/designs/fleet-view-urls-and-list-path.md §3, migration 10): triggers keep task_derived current on
// every write path, emit task-kind change rows, follow project moves, and the drift check agrees.
import {test} from 'node:test';import assert from 'node:assert/strict';
import {freshDb} from './pg-test.ts';
// PGlite by default; with FLEET_TEST_PG the same tests run on PostgreSQL with the PDS18 roles (CI job postgres-roles).
const fresh=async()=>(await freshDb()).s;
const derived=async(s:any,id:string)=>{const [r]=await s.query('SELECT project_id,agent,host,started_at,open_questions FROM task_derived WHERE task_id=$1',[id]);
 return r?{projectId:r.project_id,agent:r.agent,host:r.host,started:r.started_at!==null,open:Number(r.open_questions)}:null};
const noDrift=async(s:any)=>assert.deepEqual((await s.read(()=>s.tasks.derivedDrift())).drifted,[]);

test('store write paths keep task_derived current and the drift check empty',async()=>{const s=await fresh();
 let t='';await s.tx(async()=>{t=await s.tasks.create({title:'T',projectId:'p'})});
 assert.deepEqual(await derived(s,t),{projectId:'p',agent:null,host:null,started:false,open:0});await noDrift(s);
 await s.tx(async()=>{await s.executions.createExecution({id:'e1',kind:'agent_run',taskId:t,projectId:'p',hostId:'mac',harness:'codex',state:'running'});await s.tasks.transition(t,'first_execution')});
 assert.deepEqual(await derived(s,t),{projectId:'p',agent:'codex',host:'mac',started:false,open:0});await noDrift(s); // createExecution leaves started_at unset
 await s.tx(()=>s.query("UPDATE execution SET started_at=now() WHERE id='e1'"));assert.equal((await derived(s,t))!.started,true);
 await s.tx(()=>s.executions.transition('e1','succeeded'));
 assert.deepEqual(await derived(s,t),{projectId:'p',agent:null,host:'mac',started:true,open:0});
 await s.tx(()=>s.executions.createExecution({id:'e2',kind:'agent_run',taskId:t,projectId:'p',hostId:'pds18',harness:'claude',state:'preparing'}));
 assert.deepEqual(await derived(s,t),{projectId:'p',agent:'claude',host:'pds18',started:true,open:0});
 let q:any;await s.tx(async()=>{q=await s.tasks.ask({kind:'acceptance',taskId:t,text:'Accept?',options:['accepted','rejected']})});
 assert.equal((await derived(s,t))!.open,1);await noDrift(s);
 await s.tx(()=>s.tasks.answer(q.id??q,{answer:'accepted',by:'operator'}));
 assert.equal((await derived(s,t))!.open,0);await noDrift(s);
 await s.tx(()=>s.tasks.update({taskId:t,action:'cancel'})).catch(()=>{});await noDrift(s);
 await s.close()});

test('raw moves and deletes recompute both tasks; derived changes emit task rows without moving task.updated_at',async()=>{const s=await fresh();
 let a='',b='';await s.tx(async()=>{a=await s.tasks.create({title:'A'});b=await s.tasks.create({title:'B'});await s.executions.createExecution({id:'e',kind:'agent_run',taskId:a,hostId:'mac',harness:'codex',state:'running'})});
 const [{updated_at:before}]=await s.query('SELECT updated_at FROM task WHERE id=$1',[a]);
 const c0=(await s.read(()=>s.changes.since(null))).cursor;
 await s.tx(()=>s.query('UPDATE execution SET task_id=$2 WHERE id=$1',['e',b]));
 assert.equal((await derived(s,a))!.agent,null);assert.equal((await derived(s,b))!.agent,'codex');
 const ch=(await s.read(()=>s.changes.since(c0))).changes.filter((c:any)=>c.kind==='task').map((c:any)=>c.id).sort();
 assert.deepEqual(ch,[a,b].sort());
 const [{updated_at:after}]=await s.query('SELECT updated_at FROM task WHERE id=$1',[a]);assert.equal(+after,+before);
 const c1=(await s.read(()=>s.changes.since(null))).cursor;
 await s.tx(()=>s.query("UPDATE execution SET last_seen_at=now() WHERE id='e'")); // not a trigger column: no recompute, no row
 assert.deepEqual((await s.read(()=>s.changes.since(c1))).changes.filter((c:any)=>c.kind==='task'),[]);
 await s.tx(()=>s.query("DELETE FROM execution WHERE id='e'"));
 assert.deepEqual(await derived(s,b),{projectId:null,agent:null,host:null,started:false,open:0});await noDrift(s);
 await s.close()});

test('project moves follow the task; an execution inserted before its task gets the project at commit',async()=>{const s=await fresh();
 let t='';await s.tx(async()=>{t=await s.tasks.create({title:'T',projectId:'p1'})});
 await s.tx(()=>s.query("UPDATE task SET project_id='q' WHERE id=$1",[t]));assert.equal((await derived(s,t))!.projectId,'q');
 await s.tx(async()=>{await s.executions.createExecution({id:'early',kind:'agent_run',taskId:'later',projectId:'p',hostId:'mac',harness:'codex',state:'running'});
  await s.tasks.create({id:'later',title:'Later',projectId:'p'})});
 assert.deepEqual(await derived(s,'later'),{projectId:'p',agent:'codex',host:'mac',started:false,open:0});await noDrift(s);
 await s.close()});

test('row-level security: a narrowly scoped writer still stores the full count; readers see task_derived by project',async()=>{const {s,scoped}=await freshDb();
 let t='';await s.tx(async()=>{t=await s.tasks.create({id:'tp',title:'p task',projectId:'p'});await s.tasks.ask({kind:'operator_decision',taskId:t,text:'other project q',options:['a']})});
 await s.query("UPDATE question SET project_id='q' WHERE task_id='tp'");
 await scoped('p',st=>st.query("INSERT INTO question(id,kind,project_id,task_id,text) VALUES('q2','agent_question','p','tp','in p')"));
 assert.equal((await derived(s,t))!.open,2); // the question in project q counts although the writer sees only p
 assert.equal((await scoped('p',st=>st.query("SELECT task_id FROM task_derived"))).length,1);
 assert.equal((await scoped('q',st=>st.query("SELECT task_id FROM task_derived"))).length,0);
 await s.close()});

test('backfill: tasks written before migration 10 get their derived row',async()=>{const {s,migrate}=await freshDb(9);
 await s.tx(async()=>{await s.query("INSERT INTO task(id,title,project_id) VALUES('old','Old','p')");
  await s.query("INSERT INTO execution(id,kind,state,task_id,host_id,harness,started_at) VALUES('oe','agent_run','running','old','mac','codex',now())")});
 await migrate();
 assert.deepEqual(await derived(s,'old'),{projectId:'p',agent:'codex',host:'mac',started:true,open:0});await noDrift(s);
 await s.close()});

// Review of PR #30.
test('review 1: rewriting execution.seq recomputes "latest" host and agent',async()=>{const s=await fresh();
 let t='';await s.tx(async()=>{t=await s.tasks.create({title:'T'});await s.executions.createExecution({id:'x1',kind:'agent_run',taskId:t,hostId:'mac',harness:'codex',state:'running'});
  await s.executions.createExecution({id:'x2',kind:'agent_run',taskId:t,hostId:'pds18',harness:'claude',state:'running'})});
 assert.equal((await derived(s,t))!.host,'pds18');
 await s.tx(()=>s.query("UPDATE execution SET seq=(SELECT max(seq)+1 FROM execution) WHERE id='x1'"));
 assert.deepEqual([(await derived(s,t))!.host,(await derived(s,t))!.agent],['mac','codex']);await noDrift(s);
 await s.close()});

test('review 2: deleting a task and then its executions in one transaction commits',async()=>{const s=await fresh();
 let t='';await s.tx(async()=>{t=await s.tasks.create({title:'T'});await s.executions.createExecution({id:'d1',kind:'agent_run',taskId:t,hostId:'mac',harness:'codex',state:'running'})});
 await s.tx(async()=>{await s.query('DELETE FROM task WHERE id=$1',[t]);await s.query("DELETE FROM execution WHERE id='d1'")});
 assert.equal(await derived(s,t),null);await noDrift(s);
 await s.close()});

test('review 3: the drift check run by a scoped caller sees all of each task and reports no false drift',async()=>{const {s,scoped}=await freshDb();
 await s.tx(async()=>{await s.tasks.create({id:'tp',title:'p task',projectId:'p'});await s.tasks.ask({kind:'operator_decision',taskId:'tp',text:'q',options:['a']})});
 await s.query("UPDATE question SET project_id='q' WHERE task_id='tp'");
 const r=await scoped('p',st=>st.tasks.derivedDrift());
 assert.deepEqual(r.drifted,[]);
 await s.close()});

test('review 4: a task missing its derived row still lists (empty values) and the drift check reports it',async()=>{const s=await fresh();
 await s.tx(async()=>{await s.tasks.create({id:'gap',title:'Gap'})});await s.query("DELETE FROM task_derived WHERE task_id='gap'");
 for(const group of ['priority','agent']){const p=await s.read(()=>s.tasks.page({group}));assert.deepEqual(p.tasks.map((t:any)=>[t.id,t.agent,t.openQuestions]),[['gap',null,0]],group);assert.equal(p.total,1)}
 assert.equal((await s.read(()=>s.tasks.membership({},'agent',['gap']))).length,1);
 assert.deepEqual((await s.read(()=>s.tasks.derivedDrift())).drifted.map((d:any)=>[d.taskId,d.stored]),[['gap',null]]);
 await s.close()});

test('review 10: group names are checked as own keys ("constructor" is a 400)',async()=>{const s=await fresh();
 for(const group of ['constructor','toString','__proto__'])await assert.rejects(s.read(()=>s.tasks.page({group})),(e:any)=>e.statusCode===400,group);
 await s.close()});

// Review of PR #31.
test('review #31-2/3: a scoped caller sees drift only for its own projects, and checked counts the same tasks',async()=>{const {s,scoped}=await freshDb();
 await s.tx(async()=>{await s.tasks.create({id:'tp',title:'p',projectId:'p'});await s.tasks.create({id:'tq',title:'q',projectId:'q'})});
 await s.query("DELETE FROM task_derived");
 const r=await scoped('p',st=>st.tasks.derivedDrift());
 assert.deepEqual([r.checked,r.drifted.map((d:any)=>d.taskId)],[1,['tp']]);
 const all=await s.read(()=>s.tasks.derivedDrift());assert.deepEqual([all.checked,all.drifted.map((d:any)=>d.taskId)],[2,['tp','tq']]);
 // task_derived_values sees every project: not callable by the app role.
 await assert.rejects(scoped('p',st=>st.query("SELECT * FROM task_derived_values('tq')")),/permission denied/);
 await s.close()});

test('review #31-4: the backfill writes no change row per existing task',async()=>{const {s,migrate}=await freshDb(9);
 await s.tx(async()=>{for(let i=0;i<5;i++)await s.query("INSERT INTO task(id,title) VALUES($1,'x')",['b'+i])});
 const [{n:before}]=await s.query('SELECT count(*)::int AS n FROM change');await migrate();
 const [{n:after}]=await s.query('SELECT count(*)::int AS n FROM change');assert.equal(after,before);
 assert.equal((await s.query('SELECT count(*)::int AS n FROM task_derived'))[0].n,5);
 await s.close()});

// Deploy of 426d1e2: the recompute switches fleet.project_ids to '*' with set_config(..., true) and must set the caller's
// value back, so a scoped writer's later statements in the same transaction stay scoped.
test('the recompute and the drift check leave the caller\'s fleet.project_ids as it was',async()=>{const {s,scoped}=await freshDb();
 await s.tx(async()=>{await s.tasks.create({id:'tp',title:'p',projectId:'p'})});
 const after=await scoped('p',st=>st.tx(async()=>{await st.query("INSERT INTO question(id,kind,project_id,task_id,text) VALUES('qx','agent_question','p','tp','x')");await st.tasks.derivedDrift();
  const [{v}]=await st.query("SELECT current_setting('fleet.project_ids',true) AS v");return v}));
 assert.equal(after,'p');
 assert.equal((await s.query("SELECT open_questions FROM task_derived WHERE task_id='tp'"))[0].open_questions,1);
 await s.close()});
