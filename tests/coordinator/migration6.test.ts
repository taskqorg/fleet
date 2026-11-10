import {test} from 'node:test';import assert from 'node:assert/strict';import {readFileSync} from 'node:fs';import {createStore,TYPED_KINDS} from '../../packages/coordinator/store.ts';
// Documents written by the coordinator at main 93c2cc0 (pre-Phase 2) while running its own test suite: 8 databases that together
// hold every kind migration 6 moves (execution, delegation, pipeline-run, delivery, project-question, question). Large artifact
// bodies are shortened.
const fixture:{source:number,kind:string,id:string,data:any,seq:number}[]=JSON.parse(readFileSync(new URL('./fixtures/documents-main-93c2cc0.json',import.meta.url),'utf8'));
test('migration 6 moves legacy documents into typed tables without changing any record',async()=>{
 const sources=[...new Set(fixture.map(r=>r.source))];let moved=0;
 for(const source of sources){const rows=fixture.filter(r=>r.source===source),s=createStore('memory://');await s.migrate(5);
  await s.tx(async()=>{for(const r of rows)await s.query('INSERT INTO document(kind,id,data,project_id,seq) VALUES($1,$2,$3,$4,$5)',[r.kind,r.id,JSON.stringify(r.data),typeof r.data?.projectId==='string'?r.data.projectId:null,r.seq])});
  await s.migrate(10);
  // Every record reads back as it was, through get and through all() in the old insertion order.
  for(const r of rows)assert.deepEqual(await s.get(r.id,r.kind),r.data,`${source} ${r.kind} ${r.id}`);
  for(const kind of new Set(rows.map(r=>r.kind)))assert.deepEqual((await s.all(kind)).map((x:any)=>x.id),rows.filter(r=>r.kind===kind).map(r=>r.id),`${source} order of ${kind}`);
  assert.equal((await s.query('SELECT count(*)::int AS n FROM document WHERE kind=ANY($1)',[TYPED_KINDS]))[0].n,0);
  // Typed rows: one execution per legacy execution and pipeline run (plus steps), one task per delivery, one question per question.
  const n=(k:string)=>rows.filter(r=>r.kind===k).length,steps=rows.filter(r=>r.kind==='pipeline-run').reduce((a,r)=>a+(r.data.steps?.length??0),0);
  const count=async(sql:string)=>(await s.query(sql))[0].n;
  assert.equal(await count("SELECT count(*)::int AS n FROM execution WHERE legacy_kind='execution'"),n('execution'));
  assert.equal(await count("SELECT count(*)::int AS n FROM execution WHERE legacy_kind IN ('pipeline-run','pipeline-step')"),n('pipeline-run')+steps);
  assert.equal(await count("SELECT count(*)::int AS n FROM task WHERE legacy_kind='delivery'"),n('delivery'));
  assert.equal(await count('SELECT count(*)::int AS n FROM question'),n('question')+n('project-question')+await count("SELECT count(*)::int AS n FROM question WHERE kind='acceptance'"));
  assert.equal(await count('SELECT count(*)::int AS n FROM execution WHERE delegation IS NOT NULL'),n('delegation'));
  for(const d of rows.filter(r=>r.kind==='delivery')){const t=await s.tasks.get(d.id);assert.equal(t.legacyId,d.id);
   for(const step of d.data.steps??[])if(step.executionId)assert.equal((await s.executions.get(step.executionId)).taskId,d.id);
   // A delivery whose step executions have started is in progress or further, not queued (steps move after the delivery).
   if((d.data.steps??[]).some((x:any)=>x.executionId&&rows.some(r=>r.kind==='execution'&&r.id===x.executionId&&r.data.state!=='queued')))assert.notEqual(t.state,'queued',d.id);
   if(d.data.questionId)assert.equal(t.questions.find((q:any)=>q.id===d.data.questionId)?.kind,'operator_decision')}
  for(const e of rows.filter(r=>r.kind==='execution'&&r.data.experimentId))assert.equal((await s.executions.get(e.id)).experimentId,e.data.experimentId);
  // Migration 11 deletes the old lab's experiment documents and unlinks their executions.
  await s.migrate();assert.equal((await s.query("SELECT count(*)::int AS n FROM document WHERE kind='experiment'"))[0].n,0);assert.equal((await s.query('SELECT count(*)::int AS n FROM execution WHERE experiment_id IS NOT NULL'))[0].n,0);
  moved+=rows.filter(r=>TYPED_KINDS.includes(r.kind)).length;await s.close()}
 console.log(`${fixture.length} documents from ${sources.length} databases, ${moved} moved to typed tables`)});
