import {test} from 'node:test';
import assert from 'node:assert/strict';
import {randomUUID} from 'node:crypto';
import {startCoordinator} from '../harness.ts';

test('Explicit task asks bind projectless PM questions with grants, stable identity and audited idle replacement (requirement: fleet.md shared task questions; risk: real blockers stay invisible or cross authority)', async () => {
 const c=await startCoordinator({name:'task-question-binding'});
 const call=async(path:string,body:unknown,person='owner@trial.test',status=200)=>{
  const r=await fetch(c.base+path,{method:'POST',headers:{authorization:'Bearer '+c.secret,'x-fleet-person':person,'content-type':'application/json'},body:JSON.stringify(body)});
  const x=await r.json();assert.equal(r.status,status,JSON.stringify(x));return x;
 };
 try {
  const person='member@trial.test';
  const {sessionId}=await call('/api/ops/session_create',{},person);
  const eid=randomUUID(),task=randomUUID(),other=randomUUID(),denied=randomUUID(),idle=randomUUID(),ownerQuestion=randomUUID();
  c.sql(`INSERT INTO task(id,project_id,title,state,wait_reason) VALUES('${task}','desiign','blocked','waiting','question'),('${other}','desiign','other','in_progress',NULL),('${denied}','hello-world','denied','in_progress',NULL);
   INSERT INTO execution(id,kind,state,session_id,directed_by,data) VALUES('${eid}','agent_run','running','${sessionId}','${person}','{"id":"${eid}","sessionId":"${sessionId}"}');
   INSERT INTO question(id,kind,task_id,project_id,text,asked_by,data) VALUES('${idle}','operator_decision','${task}','desiign','generic failure','fleet','{"idle":true}'),('${ownerQuestion}','operator_decision','${task}','desiign','preserve owner question','owner','{}')`);
  const b={executionId:eid,taskId:task,question:'Actual missing environment blocker',requestId:'explicit-task'};
  const q=await call('/api/ops/ask',b,person);assert.equal(q.taskId,task);assert.equal(q.projectId,'desiign');
  const detail=await call('/api/ops/task_get',{taskId:task},person);
  assert.equal(detail.questions.find((x:any)=>x.id===idle).state,'withdrawn');assert.equal(detail.questions.find((x:any)=>x.id===ownerQuestion).state,'open');
  assert.equal(detail.openQuestions.find((x:any)=>x.id===q.id).text,b.question);assert.equal(detail.state,'waiting');assert.equal(detail.waitReason,'question');
  assert.equal(c.sql(`SELECT count(*) FROM change WHERE kind='question' AND id='${idle}' AND op='U'`).trim(),'1');
  const thread=await fetch(c.base+'/api/threads/session/'+sessionId+'/entries?limit=30',{headers:{authorization:'Bearer '+c.secret,'x-fleet-person':person}});assert.equal(thread.status,200);const text=await thread.text();assert.ok(text.includes(q.id)&&text.includes(b.question));
  assert.equal((await call('/api/ops/ask',b,person)).id,q.id);
  await call('/api/ops/ask',{...b,taskId:other},person,409);
  const {taskId:omitted,...withoutTask}=b;await call('/api/ops/ask',withoutTask,person,409);
  await call('/api/ops/ask',{...b,requestId:'denied',taskId:denied},person,404);
  assert.equal((await call('/api/ops/ask',{executionId:eid,resume:q.id},person)).taskId,task);
  await call('/api/ops/ask',{executionId:eid,resume:q.id,taskId:task},person,400);
  const bound=randomUUID();c.sql(`INSERT INTO execution(id,kind,state,session_id,project_id,task_id,directed_by,data) VALUES('${bound}','agent_run','running','${sessionId}','desiign','${task}','${person}','{"id":"${bound}","sessionId":"${sessionId}"}')`);
  const inferred=await call('/api/ops/ask',{executionId:bound,requestId:'inferred',question:'Bound worker question'},person);assert.equal(inferred.taskId,task);
  await call('/api/ops/ask',{executionId:bound,taskId:other,requestId:'conflict',question:'Wrong task'},person,409);
  await call('/api/ops/ask',{executionId:bound,taskId:denied,requestId:'project-conflict',question:'Wrong project'},'owner@trial.test',409);
  assert.equal(c.sql(`SELECT data->>'taskId' FROM document WHERE id='${sessionId}'`).trim(),'','PM session remains unattached');
 } finally {await c.stop();}
});

test('Registration refresh preserves runtime causes before a failed terminal receipt (requirement: fleet.md truthful failure reason; risk: valid host registration erases preparation/protocol/process cause)',async()=>{
 const c=await startCoordinator({name:'task-runtime-cause'});
 const call=async(path:string,body:unknown,auth=c.secret,person:string|null='owner@trial.test')=>{
  const r=await fetch(c.base+path,{method:'POST',headers:{authorization:'Bearer '+auth,...(person?{'x-fleet-person':person}:{}),'content-type':'application/json'},body:JSON.stringify(body)});const x=await r.json();assert.equal(r.status,200,JSON.stringify(x));return x;
 };
 try {
  const {secret}=await call('/api/ops/hosts_register',{id:'trial-host'});
  const task=await call('/api/ops/task_create',{projectId:'hello-world',title:'failure cause'});
  const {sessionId}=await call('/api/sessions',{});c.sql(`UPDATE document SET data=data||jsonb_build_object('taskId','${task.id}') WHERE id='${sessionId}'`);
  const e=await call(`/api/sessions/${sessionId}/messages`,{requestId:randomUUID(),text:'fixture'});
  const poll=()=>call('/api/hosts/poll',{platform:'darwin',capabilities:[],stores:[],problems:[]},secret,null);
  const cmd=(await poll()).commands.find((x:any)=>x.executionId===e.id);
  await call('/api/hosts/receipts',{executionId:e.id,commandId:cmd.id,status:'started'},secret,null);
  await call('/api/hosts/events',{executionId:e.id,events:[{id:randomUUID(),seq:1,type:'preparation.failed',timestamp:Date.now(),data:{reason:'coding-tool-unready: browser'}}]},secret,null);
  await poll();assert.equal(c.sql(`SELECT problem FROM execution WHERE id='${e.id}'`).trim(),'preparation failed: coding-tool-unready: browser');
  for(const cause of ['harness protocol failed: EOF','harness exited 1: unavailable']){c.sql(`UPDATE execution SET problem='${cause}' WHERE id='${e.id}'`);await poll();assert.equal(c.sql(`SELECT problem FROM execution WHERE id='${e.id}'`).trim(),cause);}
  c.sql(`UPDATE execution SET problem='unregistered host fixed' WHERE id='${e.id}'`);await poll();assert.equal(c.sql(`SELECT coalesce(problem,'cleared') FROM execution WHERE id='${e.id}'`).trim(),'cleared');
  c.sql(`UPDATE execution SET problem='preparation failed: coding-tool-unready: browser' WHERE id='${e.id}'`);
  await call('/api/hosts/receipts',{executionId:e.id,commandId:cmd.id,status:'failed'},secret,null);
  const t=await call('/api/ops/task_get',{taskId:task.id});assert.equal(t.executions.at(-1).problem,'preparation failed: coding-tool-unready: browser');
 } finally {await c.stop();}
});
