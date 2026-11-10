// Lab trial (docs/designs/fleet-lab.md "Entities"): a trial inserted by SQL fixture is a plain execution with lab artifacts and no task.
import {test} from 'node:test';
import assert from 'node:assert/strict';
import {dbApp} from './pg-test.ts';
import {attachStores,upload} from './artifact-helpers.ts';

test('lab trial: experiment row and pipeline_run with experiment_id, lab artifacts, not in Tasks',async()=>{
 const app:any=await dbApp({browserSecret:'b',machineSecret:'m',hostId:'mac',storeEndpointHosts:['127.0.0.1']});
 await app.ready();await attachStores(app,'m',['mac']);
 const s=app.store;
 await s.query("INSERT INTO experiment(id,project_id,kind,pipeline_id,variants) VALUES('x1','fleet-hello-world','ab','web-research','[{\"id\":\"a\"},{\"id\":\"b\"}]')");
 const trial={id:'t1',kind:'pipeline_run' as const,state:'succeeded' as const,projectId:'fleet-hello-world',hostId:'mac',experimentId:'x1',variant:'a',requestId:'x1:a:ex1:0'};
 assert.equal(await s.executions.insertIfAbsent(trial),true);
 assert.equal(await s.executions.insertIfAbsent({...trial,id:'t1-retry'}),false,'same request_id inserts nothing');
 const row=(await s.query('SELECT task_id,experiment_id,variant,request_id FROM execution WHERE id=$1',['t1']))[0];
 assert.deepEqual(row,{task_id:null,experiment_id:'x1',variant:'a',request_id:'x1:a:ex1:0'});
 await upload(app,'m',{hostId:'mac',executionId:'t1',path:'output.json',content:'{}',mediaType:'application/json'});
 assert.deepEqual((await s.query("SELECT scope FROM artifact WHERE execution_id='t1'")).map((r:any)=>r.scope),['lab']);
 const tasks=(await app.inject({method:'POST',url:'/api/ops/task_list',headers:{authorization:'Bearer b'},payload:{}})).json();
 assert.ok(!JSON.stringify(tasks).includes('t1'),'trial is not a task');
 // The change feed carries the new kinds.
 assert.ok((await s.query("SELECT 1 FROM change WHERE kind='experiment' AND id='x1'")).length);
 await s.query("INSERT INTO score(id,experiment_id,project_id,trial_ids,evaluator,evaluator_version,form,value) VALUES('sc1','x1','fleet-hello-world','{t1}','checks','1','pass_fail','true')");
 assert.equal((await s.query("SELECT id FROM score WHERE trial_ids @> '{t1}'"))[0].id,'sc1');
 await app.close();
});
