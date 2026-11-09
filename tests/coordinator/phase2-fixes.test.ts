import {test} from 'node:test';import assert from 'node:assert/strict';import {mkdtempSync,mkdirSync,writeFileSync} from 'node:fs';import {tmpdir} from 'node:os';
import {createApp} from '../../packages/coordinator/app.js';import {createStore} from '../../packages/coordinator/store.js';
// Defects found on the first live Phase 2 run (PR #20): acceptance answer 404, missing parent links, problem on a succeeded step.
test('question_answer answers an acceptance question and completes the task',async()=>{const app=createApp({database:'memory://',browserSecret:'b',machineSecret:'m',hostId:'mac'});await app.ready();const s=(app as any).store;
 await s.tx(async()=>{await s.put('execution',{id:'g',executionId:'g',sessionId:'sx',hostId:'mac',state:'completed',startedAt:1,finishedAt:2,deliveryId:'d'});
  await s.put('delivery',{id:'d',projectId:'p',requestId:'r',task:'Page',state:'merged',steps:[{kind:'generate',executionId:'g',state:'completed'}]})});
 const acc=(await s.tasks.questions({taskId:'d',kind:'acceptance'}))[0];
 for(const url of ['/api/project-operations/question_answer','/api/ops/question_answer']){const r=await app.inject({method:'POST',url,headers:{authorization:'Bearer b'},payload:{questionId:acc.id,answer:'accepted'}});assert.equal(r.statusCode,200,url+' '+r.body);assert.equal(r.json().state,'answered')}
 const t=await s.tasks.get('d');assert.deepEqual([t.state,t.acceptedBy],['completed','operator']);
 assert.equal((await app.inject({method:'POST',url:'/api/ops/question_answer',headers:{authorization:'Bearer b'},payload:{questionId:'missing',answer:'x'}})).statusCode,404);await app.close()});
test('sessions started by a host agent link their executions to the calling execution',async()=>{
 const dir=mkdtempSync(tmpdir()+'/fleet-parent-');mkdirSync(dir+'/projects/p1',{recursive:true});mkdirSync(dir+'/agents');
 writeFileSync(dir+'/agents/w.json',JSON.stringify({id:'w',model:'gpt-6.1-sol',effort:'low',instructions:'x',mcpServers:[]}));
 writeFileSync(dir+'/projects/p1/project.json',JSON.stringify({id:'p1',name:'P1',contextFiles:[],roles:{pm:'w',worker:'w',reviewer:'w'},repos:[{id:'main',remote:'git@example.com:a/b.git'}],environments:[{hostId:'mac',checkoutPath:'/x'}]}));
 const app=createApp({database:'memory://',browserSecret:'b',machineSecret:'m',hostId:'mac',projectConfigDirectory:dir,profilesDirectory:dir+'/agents'});const s=(app as any).store;
 const call=async(url:string,payload?:any,machine=false)=>(await app.inject({method:payload?'POST':'GET',url,headers:{authorization:'Bearer '+(machine?'m':'b')},payload}));
 const tool=async(c:any,name:string,args:any)=>JSON.parse((await call('/api/hosts/agent-tools',{hostId:'mac',commandId:c.id,executionId:c.executionId,request:{jsonrpc:'2.0',id:1,method:'tools/call',params:{name,arguments:args}}},true)).json().result.content[0].text);
 const pm=(await call('/api/ops/session_start',{projectId:'p1',role:'pm',requestId:'pm1',text:'Plan'})).json();
 const [pc]=(await call('/api/hosts/poll',{hostId:'mac'},true)).json().commands;
 const worker=await tool(pc,'session_start',{role:'worker',requestId:'w1',text:'Do it'}),reviewer=await tool(pc,'session_start',{role:'reviewer',requestId:'r1',text:'Review'});
 for(const child of [worker,reviewer]){const e=await s.executions.get(child.executionId);assert.equal(e.parentExecutionId,pc.executionId);assert.equal(e.kind,'agent_run')}
 assert.equal((await s.executions.get(pc.executionId)).parentExecutionId,null);
 assert.deepEqual((await s.executions.list({parentExecutionId:pc.executionId})).map((e:any)=>e.id).sort(),[worker.executionId,reviewer.executionId].sort());
 assert.equal(pm.sessionId!==worker.sessionId,true);await app.close()});
test('problems belong to open executions: succeeded clears, failed keeps, terminal rows are not marked',async()=>{const s=createStore('memory://');await s.migrate();
 await s.tx(async()=>{for(const id of ['ok','bad','late'])await s.executions.createExecution({id,kind:'pipeline_step',hostId:'nowhere'})});
 assert.equal(await s.executions.detectProblems({hosts:[],environments:{}}),3);
 await s.executions.transition('ok','succeeded');await s.executions.transition('bad','failed');
 assert.equal((await s.executions.get('ok')).problem,null);assert.equal((await s.executions.get('bad')).problem,'unregistered host nowhere');
 await s.executions.detectProblems({hosts:[],environments:{}});assert.equal((await s.executions.get('ok')).problem,null);
 // Legacy writes take the same rule.
 await s.put('execution',{id:'leg',executionId:'leg',hostId:'nowhere',state:'queued'});await s.executions.detectProblems({hosts:[],environments:{}});assert.ok((await s.executions.get('leg')).problem);
 await s.put('execution',{id:'leg',executionId:'leg',hostId:'nowhere',state:'completed'});assert.equal((await s.executions.get('leg')).problem,null);
 await s.put('pipeline-run',{id:'run',state:'running',steps:[{id:'x',state:'running'}]});await s.query("UPDATE execution SET problem='stale' WHERE id='run:step:0'");
 await s.put('pipeline-run',{id:'run',state:'completed',steps:[{id:'x',state:'completed'}]});assert.equal((await s.executions.get('run:step:0')).problem,null);await s.close()});
test('migration 7 backfills parent links and clears problems of succeeded executions, idempotently',async()=>{const s=createStore('memory://');await s.migrate(6);
 await s.put('session',{id:'pm',createdAt:1,projectId:'p'});await s.put('execution',{id:'pm-1',executionId:'pm-1',sessionId:'pm',state:'completed',createdAt:1});await s.put('execution',{id:'pm-2',executionId:'pm-2',sessionId:'pm',state:'started',createdAt:5});
 await s.put('session',{id:'w',createdAt:6,projectId:'p',startRequest:'p:w1',createdBySessionId:'pm'});await s.put('execution',{id:'w-1',executionId:'w-1',sessionId:'w',state:'completed',createdAt:7});
 await s.put('execution',{id:'later',executionId:'later',sessionId:'pm',state:'queued',createdAt:9});
 await s.query("UPDATE execution SET parent_execution_id=NULL,problem='unregistered host pds18' WHERE id IN ('w-1','pm-1')");
 await s.migrate();
 assert.equal((await s.executions.get('w-1')).parentExecutionId,'pm-2');assert.equal((await s.executions.get('w-1')).problem,null);assert.equal((await s.executions.get('pm-1')).problem,null);
 assert.equal((await s.executions.get('pm-2')).parentExecutionId,null);
 assert.deepEqual(await s.get('w-1','execution'),{id:'w-1',executionId:'w-1',sessionId:'w',state:'completed',createdAt:7});await s.close()});
