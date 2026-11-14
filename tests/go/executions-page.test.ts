import {test} from 'node:test';
import assert from 'node:assert/strict';
import {startCoordinator} from '../harness.ts';

test('executions_list opt-in pages preserve complete stable filtered RLS enumeration and legacy shape (requirement: bounded execution enumeration; risk: omitted or duplicate runs, leaked cursors, silent legacy truncation)', async () => {
  const c = await startCoordinator({name:'execution-pages'});
  try {
    for (const id of ['desiign','hidden']) {
      const r=await fetch(c.base+'/api/ops/project_create',{method:'POST',headers:{authorization:'Bearer '+c.secret,'x-fleet-person':'owner@trial.test','content-type':'application/json'},body:JSON.stringify({id,requestId:'page-'+id,content:{settings:{id,name:id,contextFiles:['rules.md'],roles:{pm:'pm'}},contextFiles:[{path:'rules.md',text:'Paging fixture'}]}})});
      assert.ok([200,409].includes(r.status),await r.text());
    }
    c.sql(`INSERT INTO task(id,project_id,title) VALUES ('page-task','desiign','Paging task');
      INSERT INTO execution(id,kind,state,project_id,session_id,parent_execution_id,problem) VALUES
      ('page-a','agent_run','failed','desiign','page-session','page-parent','test'),
      ('page-b','agent_run','running','desiign','page-session','page-parent',NULL),
      ('page-c','tool_run','failed','desiign','page-session','page-parent','test'),
      ('page-foreign','agent_run','failed','hidden','other',NULL,'test');
      UPDATE execution SET task_id='page-task' WHERE id IN ('page-a','page-b');
      UPDATE execution SET seq=900000 WHERE id IN ('page-a','page-b','page-c');`);
    async function op(body: object, person='owner@trial.test', status=200) {
      const r=await fetch(c.base+'/api/ops/executions_list',{method:'POST',headers:{authorization:'Bearer '+c.secret,'x-fleet-person':person,'content-type':'application/json'},body:JSON.stringify(body)});
      const data=await r.json();assert.equal(r.status,status,JSON.stringify(data));return data;
    }
    const filters=[{}, {projectId:'desiign'}, {projectIds:['desiign']}, {projectIds:['-']}, {sessionId:'page-session'}, {parentExecutionId:'page-parent'}, {problem:true}, {needsYou:true}, {taskId:'absent'}, {taskId:'page-task'}];
    for(const person of ['owner@trial.test','member@trial.test','outsider@trial.test']) for(const filter of filters.filter(f => person==='owner@trial.test' || !('projectId' in f) && !('projectIds' in f))) {
      const legacy=await op(filter,person);assert.deepEqual(Object.keys(legacy),['executions']);
      let after: string|undefined;const rows:any[]=[];
      do {
        const p=await op({...filter,limit:1,...(after?{after}:{})},person);
        assert.ok(p.executions.length<=1);assert.equal(typeof p.hasMore,'boolean');
        rows.push(...p.executions);after=p.nextAfter;
        assert.equal(p.hasMore,typeof after==='string');
      } while(after);
      assert.deepEqual(rows,legacy.executions);
      assert.equal(new Set(rows.map(x=>x.id)).size,rows.length);
    }
    assert.deepEqual((await op({projectId:'desiign'})).executions.map((x:any)=>x.id),['page-a','page-b','page-c']);
    for(const body of [{limit:0},{limit:-1},{limit:1001},{limit:1.5},{after:'page-a'},{limit:1,after:''},{limit:1,after:'absent'},{limit:1,projectId:'hidden',after:'page-a'}]) await op(body,'owner@trial.test',400);
    await op({limit:1,after:'page-foreign'},'member@trial.test',400);
    assert.equal((await op({limit:1000})).hasMore,false);
  } finally {await c.stop();}
});
