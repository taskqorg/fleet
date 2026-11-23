// Outside test of the completion gate on a completed receipt (go/coordinator/host_receipts.go, artifactGate).
//   FLEET_TRIAL_TESTS=tests/go/completion.test.ts node tests/trial/run.ts
import {test} from 'node:test';
import assert from 'node:assert/strict';
import {randomUUID} from 'node:crypto';
import {mkdtempSync, rmSync} from 'node:fs';
import {tmpdir} from 'node:os';
import {join} from 'node:path';
import {hostCommand, lit, startCoordinator} from '../harness.ts';
import {fakeS3} from '../s3-fake.ts';

test('G1 every declared artifact reaches the completion gate, also past 10,000 (requirement: a completed run waits for every declared output while it uploads, and task completion requirements for every matching one; risk: a silent cap drops a late required output and the execution finishes without it)', {timeout: 60_000}, async () => {
  const c = await startCoordinator({name: 'completion'});
  const call = async (path: string, body: unknown, auth = c.secret, person: string | null = 'owner@trial.test') => {
    const r = await fetch(c.base + path, {method: 'POST', headers: {authorization: 'Bearer ' + auth, ...(person ? {'x-fleet-person': person} : {}), 'content-type': 'application/json'}, body: JSON.stringify(body)});
    const text = await r.text(); let parsed: any = text; try { parsed = JSON.parse(text); } catch {}
    assert.equal(r.status, 200, `${path}: ${text}`);
    return parsed;
  };
  try {
    const {secret} = await call('/api/ops/hosts_register', {id: 'trial-host'});
    const task = await call('/api/ops/task_create', {projectId: 'hello-world', title: 'needs a txt', completionRequirements: {artifacts: ['*.txt']}});
    const {sessionId} = await call('/api/ops/session_create', {});
    const e = await call('/api/ops/message_send', {sessionId: sessionId, requestId: randomUUID(), text: 'hello'});
    c.sql(`UPDATE execution SET task_id='${task.id ?? task.taskId}' WHERE id='${e.id}'`);
    const command = await hostCommand(q => call('/api/hosts/poll' + q, {platform: 'darwin', capabilities: [], stores: [], problems: []}, secret, null), (x: any) => x.executionId === e.id);
    const artifacts = [...Array.from({length: 10000}, (_, i) => `out/${i}.bin`), 'late.txt'];
    await call('/api/hosts/receipts', {commandId: command.id, executionId: e.id, status: 'started'}, secret, null);
    await call('/api/hosts/receipts', {commandId: command.id, executionId: e.id, status: 'completed', artifacts}, secret, null);
    const row = JSON.parse(c.sql(`SELECT json_build_object('state',state::text,'pending',data->'pendingArtifacts') FROM execution WHERE id='${e.id}'`));
    // Every declared output still uploading is waited for (Lab script steps read output.json too early otherwise).
    assert.equal(row.state, 'waiting');
    assert.equal(row.pending.length, artifacts.length);
    assert.ok(row.pending.includes('late.txt'));
  } finally { await c.stop(); }
});

test('G2 a completed run whose declared outputs are not stored yet waits, unreserved or uploading (requirement: a step reads its outputs only after they are stored, Lab 2026-11-11; risk: a script step finishes with files [] or stays running)', async () => {
  const c = await startCoordinator({name: 'completion-outputs'});
  const call = async (path: string, body: unknown, auth = c.secret, person: string | null = 'owner@trial.test') => {
    const r = await fetch(c.base + path, {method: 'POST', headers: {authorization: 'Bearer ' + auth, ...(person ? {'x-fleet-person': person} : {}), 'content-type': 'application/json'}, body: JSON.stringify(body)});
    const text = await r.text(); let parsed: any = text; try { parsed = JSON.parse(text); } catch {}
    assert.equal(r.status, 200, `${path}: ${text}`);
    return parsed;
  };
  try {
    const {secret} = await call('/api/ops/hosts_register', {id: 'trial-host'});
    const {sessionId} = await call('/api/ops/session_create', {});
    const e = await call('/api/ops/message_send', {sessionId: sessionId, requestId: randomUUID(), text: 'hello'});
    const command = await hostCommand(q => call('/api/hosts/poll' + q, {platform: 'darwin', capabilities: [], stores: [], problems: []}, secret, null), (x: any) => x.executionId === e.id);
    await call('/api/hosts/receipts', {commandId: command.id, executionId: e.id, status: 'started'}, secret, null);
    await call('/api/hosts/receipts', {commandId: command.id, executionId: e.id, status: 'completed', artifacts: ['output.json']}, secret, null);
    const state = () => c.sql(`SELECT state::text FROM execution WHERE id='${e.id}'`).trim();
    assert.equal(state(), 'waiting', 'not done while output.json is not stored');
    // Reserved and still uploading: still waiting. (Settling once the upload ends is the existing settleWaiting path.)
    c.sql(`INSERT INTO artifact(id,scope,content_hash,size,media_type,name,execution_id) VALUES('${randomUUID()}','prod','x',2,'application/json','output.json','${e.id}')`);
    await call('/api/hosts/receipts', {commandId: command.id, executionId: e.id, status: 'completed', artifacts: ['output.json']}, secret, null);
    assert.equal(state(), 'waiting', 'a reserved output still uploading keeps it waiting');
  } finally { await c.stop(); }
});

test('G3 a task whose last execution succeeds asks for acceptance (requirement: data foundation §3, completed after an acceptance; risk: task 353a0d2d stayed in progress for days after its only run finished in 15 s)', async () => {
  const c = await startCoordinator({name: 'completion-accept'});
  const call = async (path: string, body: unknown, auth = c.secret, person: string | null = 'owner@trial.test') => {
    const r = await fetch(c.base + path, {method: 'POST', headers: {authorization: 'Bearer ' + auth, ...(person ? {'x-fleet-person': person} : {}), 'content-type': 'application/json'}, body: JSON.stringify(body)});
    const text = await r.text(); let parsed: any = text; try { parsed = JSON.parse(text); } catch {}
    assert.equal(r.status, 200, `${path}: ${text}`);
    return parsed;
  };
  try {
    const {secret} = await call('/api/ops/hosts_register', {id: 'trial-host'});
    const task = await call('/api/ops/task_create', {projectId: 'hello-world', title: 'answer only'});
    const taskId = task.id ?? task.taskId;
    const {sessionId} = await call('/api/ops/session_create', {});
    const e = await call('/api/ops/message_send', {sessionId: sessionId, requestId: randomUUID(), text: 'hello'});
    c.sql(`UPDATE execution SET task_id='${taskId}' WHERE id='${e.id}'; UPDATE task SET state='in_progress' WHERE id='${taskId}'`);
    const command = await hostCommand(q => call('/api/hosts/poll' + q, {platform: 'darwin', capabilities: [], stores: [], problems: []}, secret, null), (x: any) => x.executionId === e.id);
    await call('/api/hosts/receipts', {commandId: command.id, executionId: e.id, status: 'started'}, secret, null);
    await call('/api/hosts/receipts', {commandId: command.id, executionId: e.id, status: 'completed'}, secret, null);
    const before = await call('/api/ops/task_get', {taskId});
    assert.deepEqual(before.openQuestions, [], 'a process exit without a work report creates no auto-acceptance');
    assert.equal(before.completionReportMissing, true, 'task detail notes missing completion report');
    assert.equal((await call('/api/ops/worker_done', {executionId: e.id, outcome: 'succeeded'})).action, 'accepted');
    await call('/api/hosts/receipts', {commandId: command.id, executionId: e.id, status: 'completed'}, secret, null);
    const asked = () => c.sql(`SELECT count(*) FROM question WHERE task_id='${taskId}' AND kind='acceptance' AND state='open'`).trim();
    assert.equal(asked(), '1', 'one acceptance question, also after a repeated receipt');
    await call('/api/ops/task_request_acceptance', {taskId, text: 'Accept the answer'});
    assert.equal(asked(), '1', 'an explicit request reuses the open question');
    const t = await call('/api/ops/task_get', {taskId});
    const q = t.questions.find((x: any) => x.kind === 'acceptance');
    await call('/api/ops/questions_answer', {questionId: q.id, answer: 'accepted'});
    assert.equal((await call('/api/ops/task_get', {taskId})).state, 'completed');
  } finally { await c.stop(); }
});

test('G4 a task whose run fails asks the operator, a new run withdraws it, fail task fails it (requirement: no task stays in progress without an open execution or question, and a retry stays possible; risk: task 2273393d sat in progress with a failed run and nothing showed it)', async () => {
  const c = await startCoordinator({name: 'completion-fail'});
  const call = async (path: string, body: unknown, auth = c.secret, person: string | null = 'owner@trial.test') => {
    const r = await fetch(c.base + path, {method: 'POST', headers: {authorization: 'Bearer ' + auth, ...(person ? {'x-fleet-person': person} : {}), 'content-type': 'application/json'}, body: JSON.stringify(body)});
    const text = await r.text(); let parsed: any = text; try { parsed = JSON.parse(text); } catch {}
    assert.equal(r.status, 200, `${path}: ${text}`);
    return parsed;
  };
  try {
    const {secret} = await call('/api/ops/hosts_register', {id: 'trial-host'});
    const task = await call('/api/ops/task_create', {projectId: 'hello-world', title: 'answer only'});
    const taskId = task.id ?? task.taskId;
    const {sessionId} = await call('/api/ops/session_create', {});
    // A task-bound session: each new run carries the task from its creation, as a worker session does.
    c.sql(`UPDATE session SET task_id='${taskId}' WHERE id='${sessionId}'`);
    const run = async (status: string) => {
      const e = await call('/api/ops/message_send', {sessionId: sessionId, requestId: randomUUID(), text: 'hello'});
      c.sql(`UPDATE execution SET task_id='${taskId}' WHERE id='${e.id}'`);
      await call('/api/ops/task_get', {taskId});
      const command = await hostCommand(q => call('/api/hosts/poll' + q, {platform: 'darwin', capabilities: [], stores: [], problems: []}, secret, null), (x: any) => x.executionId === e.id);
      await call('/api/hosts/receipts', {commandId: command.id, executionId: e.id, status: 'started'}, secret, null);
      if (status === 'completed') assert.equal((await call('/api/ops/worker_done', {executionId: e.id, outcome: 'succeeded'})).action, 'accepted');
      await call('/api/hosts/receipts', {commandId: command.id, executionId: e.id, status}, secret, null);
    };
    const open = () => JSON.parse(c.sql(`SELECT coalesce(json_agg(kind::text),'[]') FROM question WHERE task_id='${taskId}' AND state='open'`));
    const state = () => c.sql(`SELECT state::text FROM task WHERE id='${taskId}'`).trim();
    await run('failed');
    assert.deepEqual(open(), ['operator_decision']);
    assert.equal(state(), 'waiting', 'the failed run shows as a decision');
    await run('completed');
    assert.deepEqual(open(), ['acceptance'], 'a new run withdraws the decision; its success asks acceptance');
    c.sql(`UPDATE question SET state='withdrawn' WHERE task_id='${taskId}'`);
    await run('failed');
    const t = await call('/api/ops/task_get', {taskId});
    const d = t.questions.find((x: any) => x.state === 'open');
    assert.equal(d.kind, 'operator_decision', 'the run that ended last decides, not an earlier success');
    assert.deepEqual(d.options, ['fail task', 'accept earlier result']);
    const bad = await fetch(c.base + '/api/ops/questions_answer', {method: 'POST', headers: {authorization: 'Bearer ' + c.secret, 'x-fleet-person': 'owner@trial.test', 'content-type': 'application/json'}, body: JSON.stringify({questionId: d.id, answer: 'keep open'})});
    assert.equal(bad.status, 400, 'an answer outside the options is refused, never read as keep open');
    await call('/api/ops/questions_answer', {questionId: d.id, answer: 'fail task'});
    assert.equal(state(), 'failed');
  } finally { await c.stop(); }
});

test('G5 a task left idle before the guard settles when the coordinator starts (requirement: no task in progress without an open execution or question; risk: tasks stuck before the fix stay in progress for days)', async () => {
  const c = await startCoordinator({name: 'completion-start'});
  try {
    const ids = {ok: randomUUID(), bad: randomUUID(), live: randomUUID()};
    for (const [k, id] of Object.entries(ids)) c.sql(`INSERT INTO task(id,project_id,title,state,delivery_method) VALUES('${id}','hello-world','stuck ${k}','in_progress','{"methodId":"legacy"}')`);
    c.sql(`INSERT INTO execution(id,kind,state,project_id,task_id,data) VALUES
      ('${randomUUID()}','agent_run','succeeded','hello-world','${ids.ok}','{"report":{"outcome":"succeeded","at":1}}'),
      ('${randomUUID()}','agent_run','failed','hello-world','${ids.bad}','{}'),
      ('${randomUUID()}','agent_run','running','hello-world','${ids.live}','{}')`);
    await c.restart();
    const open = (id: string) => c.sql(`SELECT coalesce(string_agg(kind::text, ','), '') FROM question WHERE task_id='${id}' AND state='open'`).trim();
    assert.equal(open(ids.ok), 'acceptance');
    assert.equal(open(ids.bad), 'operator_decision');
    assert.equal(open(ids.live), '', 'a task with a live run is left alone');
    assert.equal(c.sql(`SELECT state::text FROM task WHERE id='${ids.bad}'`).trim(), 'waiting');
    // A rejection waits for a new run: a second start asks nothing again.
    c.sql(`UPDATE question SET state='answered', answer='rejected', answered_at=now() WHERE task_id='${ids.ok}'`);
    await c.restart();
    assert.equal(open(ids.ok), '', 'a rejected result is not asked again on the next start');
    assert.equal(open(ids.bad), 'operator_decision', 'the decision is not asked twice');
    assert.equal(c.sql(`SELECT count(*) FROM question WHERE task_id='${ids.bad}'`).trim(), '1');
  } finally { await c.stop(); }
});

test('Failed work report cannot solicit acceptance despite successful process; restart retires stale acceptance (requirement: fleet.md task completion truth; risk: exit zero offers incomplete work for acceptance)', async () => {
  const c = await startCoordinator({name: 'completion-report'});
  try {
    const task = randomUUID(), execution = randomUUID(), question = randomUUID();
    c.sql(`INSERT INTO task(id,project_id,title,state) VALUES('${task}','hello-world','incomplete work','in_progress');
      INSERT INTO execution(id,kind,state,project_id,task_id,data) VALUES('${execution}','agent_run','succeeded','hello-world','${task}','{"report":{"outcome":"failed","summary":"not delivered","at":1}}');
      INSERT INTO question(id,kind,task_id,text,asked_by) VALUES('${question}','acceptance','${task}','obsolete acceptance','fleet')`);
    await c.restart();
    assert.equal(c.sql(`SELECT state FROM question WHERE id='${question}'`).trim(), 'withdrawn');
    assert.equal(c.sql(`SELECT state FROM task WHERE id='${task}'`).trim(), 'waiting');
    assert.equal(c.sql(`SELECT kind FROM question WHERE task_id='${task}' AND state='open'`).trim(), 'operator_decision');
    assert.equal(c.sql(`SELECT state FROM execution WHERE id='${execution}'`).trim(), 'succeeded');
  } finally { await c.stop(); }
});

test('Ask rejects mixed modes and persists idempotent fresh questions (requirement: agent question contract; risk: continuation prose is interpreted as an unknown question ID and loses the blocker)', async () => {
  const c = await startCoordinator({name: 'completion-ask'});
  const call = async (op: string, body: unknown, status = 200) => {
    const r = await fetch(c.base + '/api/ops/' + op, {method: 'POST', headers: {authorization: 'Bearer ' + c.secret, 'x-fleet-person': 'owner@trial.test', 'content-type': 'application/json'}, body: JSON.stringify(body)});
    const result = await r.json(); assert.equal(r.status, status, JSON.stringify(result)); return result;
  };
  try {
    const {sessionId} = await (await fetch(c.base + '/api/ops/session_create', {method:'POST', headers:{authorization:'Bearer '+c.secret,'x-fleet-person':'owner@trial.test','content-type':'application/json'},body:'{}'})).json();
    assert.ok(sessionId);
    const executionId = randomUUID(), requestId = randomUUID();
    c.sql(`INSERT INTO execution(id,kind,state,session_id,project_id,data) VALUES('${executionId}','agent_run','running','${sessionId}','hello-world','{"id":"${executionId}","sessionId":"${sessionId}"}')`);
    await call('ask', {executionId, requestId, question:'Which result is required?', resume:'continue after answer'}, 400);
    const q = await call('ask', {executionId, requestId, question:'Which result is required?'});
    assert.equal((await call('ask', {executionId, requestId, question:'Which result is required?'})).id, q.id);
    assert.equal((await call('ask', {executionId, resume:q.id})).id, q.id);
    await call('ask', {executionId, requestId, question:'A different question'}, 409);
  } finally { await c.stop(); }
});

test('Acceptance shared paths reject failed current work and retain deliberate earlier/external decisions (requirement: task outcome truth; risk: manual API bypass accepts obsolete or explicitly failed work)', async () => {
  const c = await startCoordinator({name: 'completion-acceptance-guards'});
  const call = async (op: string, body: unknown, status = 200) => {
    const r = await fetch(c.base + '/api/ops/' + op, {method:'POST', headers:{authorization:'Bearer '+c.secret,'x-fleet-person':'owner@trial.test','content-type':'application/json'},body:JSON.stringify(body)});
    const result = await r.json(); assert.equal(r.status,status,JSON.stringify(result)); return result;
  };
  try {
    const task = randomUUID(), failed = randomUUID(), stale = randomUUID();
    c.sql(`INSERT INTO task(id,project_id,title,state) VALUES('${task}','hello-world','failed result','in_progress');
      INSERT INTO execution(id,kind,state,project_id,task_id,data) VALUES('${failed}','agent_run','succeeded','hello-world','${task}','{"report":{"outcome":"failed","summary":"incomplete","at":1}}');
      INSERT INTO question(id,kind,task_id,text,asked_by) VALUES('${stale}','acceptance','${task}','stale','fleet')`);
    await call('task_request_acceptance',{taskId:task},409);
    await call('questions_answer',{questionId:stale,answer:'accepted'},409);
    await c.restart();
    assert.equal(c.sql(`SELECT count(*) FROM change WHERE kind='question' AND id='${stale}' AND op='U'`).trim(),'1','withdrawal emits the normal audited question change');
    const external = await call('task_create',{projectId:'hello-world',title:'external result'});
    const externalID = external.id ?? external.taskId;
    c.sql(`UPDATE task SET state='in_progress' WHERE id='${externalID}'`);
    const externalQuestion = await call('task_request_acceptance',{taskId:externalID});
    await call('questions_answer',{questionId:externalQuestion.id,answer:'accepted'});
    await call('questions_answer',{questionId:externalQuestion.id,answer:'accepted'});
    const earlier = randomUUID(), success = randomUUID();
    c.sql(`INSERT INTO task(id,project_id,title,state) VALUES('${earlier}','hello-world','earlier result','in_progress');
      INSERT INTO execution(id,kind,state,project_id,task_id,data) VALUES('${success}','agent_run','succeeded','hello-world','${earlier}','{"report":{"outcome":"succeeded","at":1}}');
      INSERT INTO execution(id,kind,state,project_id,task_id) VALUES('${randomUUID()}','agent_run','failed','hello-world','${earlier}')`);
    await c.restart();
    const decision = (await call('task_get',{taskId:earlier})).questions.find((q:any)=>q.state==='open');
    await call('questions_answer',{questionId:decision.id,answer:'accept earlier result'});
    const acceptance = (await call('task_get',{taskId:earlier})).questions.find((q:any)=>q.state==='open');
    assert.equal(acceptance.executionId,success);
    await call('questions_answer',{questionId:acceptance.id,answer:'accepted'});
    assert.equal((await call('task_get',{taskId:earlier})).state,'completed');
    // Explicit task_request_acceptance works for a confirmed succeeded run without worker_done, even with a required mcp-lifecycle log.
    const manual = randomUUID(), manualRun = randomUUID(), manualOut = randomUUID();
    c.sql(`INSERT INTO task(id,project_id,title,state) VALUES('${manual}','hello-world','explicit without report','in_progress');
      INSERT INTO execution(id,kind,state,observation,project_id,task_id) VALUES('${manualRun}','agent_run','succeeded','confirmed','hello-world','${manual}');
      INSERT INTO artifact(id,scope,content_hash,size,media_type,name,execution_id,task_id,required,state) VALUES
        ('${manualOut}','prod','okhash',5,'text/plain','deliverable.txt','${manualRun}','${manual}',false,'stored'),
        ('${randomUUID()}','prod','loghash',5,'application/x-ndjson','mcp-lifecycle-trace.jsonl','${manualRun}','${manual}',true,'stored')`);
    await c.restart();
    assert.deepEqual((await call('task_get',{taskId:manual})).openQuestions,[],'no auto-acceptance without report');
    const manualOffer = await call('task_request_acceptance',{taskId:manual});
    assert.deepEqual([manualOffer.executionId,manualOffer.artifactIds],[manualRun,[manualOut]]);
    await c.restart();
    assert.equal((await call('task_get',{taskId:manual})).openQuestions.length,1,'explicit acceptance stays open across restart');
    await call('questions_answer',{questionId:manualOffer.id,answer:'accepted'});
    assert.equal((await call('task_get',{taskId:manual})).state,'completed');
  } finally { await c.stop(); }
});

test('Latest result selects newer deliverables before older required outputs and lifecycle logs (requirement: task result relevance; risk: rework shows stale evidence)', async () => {
  const c = await startCoordinator({name:'completion-result-order'});
  try {
    const task=randomUUID(), old=randomUUID(), current=randomUUID();
    c.sql(`INSERT INTO task(id,project_id,title,state) VALUES('${task}','hello-world','rework','in_progress');
      INSERT INTO execution(id,kind,state,project_id,task_id) VALUES('${old}','agent_run','succeeded','hello-world','${task}'),('${current}','agent_run','succeeded','hello-world','${task}');
      INSERT INTO artifact(id,scope,content_hash,size,media_type,name,execution_id,required,state) VALUES
      ('${randomUUID()}','prod','old',1,'text/plain','old.txt','${old}',true,'stored'),
      ('${randomUUID()}','prod','new',1,'text/plain','new.txt','${current}',false,'stored'),
      ('${randomUUID()}','prod','log',1,'application/x-ndjson','mcp-lifecycle-diagnostic.jsonl','${current}',true,'stored')`);
    const response=await fetch(c.base+'/api/ops/task_get',{method:'POST',headers:{authorization:'Bearer '+c.secret,'x-fleet-person':'owner@trial.test','content-type':'application/json'},body:JSON.stringify({taskId:task})});
    assert.equal(response.status,200);assert.equal((await response.json()).latestResult.path,'new.txt');
  } finally { await c.stop(); }
});

test('Late failed work report reconciles the canonical task bound through its session (requirement: process completion differs from work outcome; risk: optional record taskId skips withdrawal after exit zero)', async () => {
  const c = await startCoordinator({name:'completion-late-report'});
  const call = async (path: string, body: unknown, auth=c.secret, person: string|null='owner@trial.test') => {
    const r=await fetch(c.base+path,{method:'POST',headers:{authorization:'Bearer '+auth,...(person?{'x-fleet-person':person}:{}),'content-type':'application/json'},body:JSON.stringify(body)});
    const x=await r.json(); assert.equal(r.status,200,JSON.stringify(x)); return x;
  };
  try {
    const {secret}=await call('/api/ops/hosts_register',{id:'trial-host'});
    const task=await call('/api/ops/task_create',{projectId:'hello-world',title:'missing deliverable'});
    const taskId=task.id??task.taskId;
    const {sessionId}=await call('/api/ops/session_create',{});
    c.sql(`UPDATE session SET task_id='${taskId}' WHERE id='${sessionId}'`);
    const e=await call('/api/ops/message_send', {sessionId: sessionId, requestId:randomUUID(),text:'finish process without required deliverable'});
    assert.equal(c.sql(`SELECT task_id FROM execution WHERE id='${e.id}'`).trim(),taskId);
    assert.equal(c.sql(`SELECT data ? 'taskId' FROM execution WHERE id='${e.id}'`).trim(),'f','the canonical session binding does not require a JSON taskId alias');
    const command = await hostCommand(q => call('/api/hosts/poll' + q, {platform:'darwin',capabilities:[],stores:[],problems:[]}, secret, null), (x:any)=>x.executionId===e.id);
    await call('/api/hosts/receipts',{commandId:command.id,executionId:e.id,status:'started'},secret,null);
    await call('/api/hosts/receipts',{commandId:command.id,executionId:e.id,status:'completed'},secret,null);
    const before=await call('/api/ops/task_get',{taskId});
    assert.deepEqual(before.openQuestions,[]);
    const report=await call('/api/ops/worker_done',{executionId:e.id,outcome:'failed',summary:'Required deliverable absent'}); assert.equal(report.action,'accepted');
    const after=await call('/api/ops/task_get',{taskId});
    assert.equal(after.state,'waiting'); assert.equal(after.waitReason,'question');
    assert.deepEqual(after.openQuestions.map((q:any)=>q.kind),['operator_decision']);
    assert.equal(after.executions[0].state,'succeeded'); assert.equal(after.executions[0].report.outcome,'failed');
  } finally { await c.stop(); }
});

// Requirement: a retained task-linked continuation blocks idle acceptance across restart; risk: accepting unfinished self-delivery or losing its task authority.
test('self-delivery queued continuation retains the canonical task and suppresses intermediate acceptance across restart', async () => {
  const c = await startCoordinator({name: 'completion-delivery-queue'});
  const call = async (path: string, body: unknown, auth = c.secret, person: string | null = 'owner@trial.test') => {
    const r = await fetch(c.base + path, {method:'POST',headers:{authorization:'Bearer '+auth,...(person?{'x-fleet-person':person}:{}),'content-type':'application/json'},body:JSON.stringify(body)});
    const text=await r.text(); assert.equal(r.status,200,`${path}: ${text}`);return JSON.parse(text);
  };
  try {
    const {secret}=await call('/api/ops/hosts_register',{id:'trial-host'});
    const task=await call('/api/ops/task_create',{projectId:'hello-world',title:'self-delivery checkpoint'});
    const tid=task.id??task.taskId;
    const {sessionId}=await call('/api/ops/session_create',{});
    await call('/api/ops/session_task_attach',{sessionId,taskId:tid});
    const e=await call('/api/ops/message_send', {sessionId: sessionId, requestId:randomUUID(),text:'deliver'});
    c.sql(`UPDATE task SET state='in_progress' WHERE id='${tid}'`);
    const poll=(q='')=>call('/api/hosts/poll'+q,{platform:'darwin',capabilities:[],stores:[],problems:[]},secret,null);
    const command=await hostCommand(poll,(x:any)=>x.executionId===e.id);
    await call('/api/hosts/receipts',{commandId:command.id,executionId:e.id,status:'started'},secret,null);
    const stale = randomUUID();
    c.sql(`INSERT INTO question(id,kind,task_id,execution_id,project_id,text,asked_by) VALUES
      ('${stale}','acceptance','${tid}','${e.id}','hello-world','Intermediate offer','fleet')`);
    const request={requestId:randomUUID(),text:'Checkpoint stored; verify landing after executor replacement'};
    const queue=await call('/api/ops/message_send',{sessionId:sessionId,...request});
    assert.equal(queue.queued,true);
    assert.equal(c.sql(`SELECT state FROM question WHERE id='${stale}'`).trim(),'withdrawn','continuation supersedes the offer before promotion');
    const queuedTask=c.sql(`SELECT task_id FROM queued_message WHERE request_id='${request.requestId}'`).trim();assert.equal(queuedTask,tid);
    await c.restart();
    assert.equal((await call('/api/ops/message_send',{sessionId:sessionId,...request})).queued,true,'idempotent retained queue');
    await call('/api/hosts/receipts',{commandId:command.id,executionId:e.id,status:'completed'},secret,null);
    const acceptance=()=>c.sql(`SELECT count(*) FROM question WHERE task_id='${tid}' AND kind='acceptance' AND state='open'`).trim();
    assert.equal(acceptance(),'0','no intermediate acceptance before or after promotion');
    const next=JSON.parse(c.sql(`SELECT json_build_object('id',id,'task',task_id,'state',state) FROM execution WHERE session_id='${sessionId}' AND id<>'${e.id}' ORDER BY seq DESC LIMIT 1`));
    assert.equal(next.task,tid);assert.equal(next.state,'preparing');
    const promoted=await hostCommand(poll,(x:any)=>x.executionId===next.id);assert.ok(promoted,'queued continuation eventually dispatched');
    assert.equal((await call('/api/ops/worker_done',{executionId:next.id,outcome:'succeeded'})).action,'accepted');
    await call('/api/hosts/receipts',{commandId:promoted.id,executionId:next.id,status:'completed'},secret,null);
    assert.equal(acceptance(),'1','acceptance only after the final continuation');
  } finally {await c.stop();}
});


// The fixture commits storage state directly, reproducing a lost callback
// between verification and execution settlement without timing races.
test('coordinator restart recovers artifact waits (requirement: the artifact completion gate; risk: a linked pipeline stays running after its output is stored)', {timeout: 90_000}, async t => {
  const dir = mkdtempSync(join(tmpdir(), 'fleet-completion-recovery-'));
  t.after(() => rmSync(dir, {recursive: true, force: true}));
  const store = await fakeS3();
  t.after(() => { store.close(); });
  const c = await startCoordinator({name: 'completion-recovery', env: {
    FLEET_PIPELINE_TICK_MS: '0',
    FLEET_STORE_SECRETS: join(dir, 'stores'),
    FLEET_STORE_ENDPOINT_HOSTS: '127.0.0.1',
    FLEET_DEFAULT_ARTIFACT_LOCATIONS: 'trial-host',
  }});
  const call = async (path: string, body: unknown, hostSecret?: string) => {
    const r = await fetch(c.base + path, {method: 'POST', headers: {
      authorization: 'Bearer ' + (hostSecret ?? c.secret),
      ...(hostSecret ? {} : {'x-fleet-person': 'owner@trial.test'}),
      'content-type': 'application/json',
    }, body: JSON.stringify(body)});
    const text = await r.text();
    assert.equal(r.status, 200, `${path}: ${text}`);
    return JSON.parse(text);
  };
  const until = async <T>(read: () => T | undefined | Promise<T | undefined>): Promise<T> => {
    const deadline = Date.now() + 10_000;
    while (Date.now() < deadline) {
      const result = await read();
      if (result !== undefined) return result;
      await new Promise(resolve => setTimeout(resolve, 50));
    }
    throw new Error('Fixture did not settle');
  };
  try {
    const {secret} = await call('/api/ops/hosts_register', {id: 'trial-host'});
    const poll = () => call('/api/hosts/poll', {
      platform: 'darwin', capabilities: [], stores: [{id: 'fixture-store', reachable: true}], problems: [],
    }, secret);
    await poll();
    await call('/api/hosts/stores', {store: {id: 'fixture-store', bucket: 'fleet',
      endpoint: `http://127.0.0.1:${store.port}`, accessKeyId: 'fixture-key', secretAccessKey: 'fixture-secret',
    }}, secret);
    const pipelineId = 'completion-recovery';
    await call('/api/ops/definition_create', {kind: 'pipeline', id: pipelineId, message: 'Completion recovery fixture', content: {pipeline: {
      id: pipelineId, version: 2, steps: [{id: 'work', kind: 'agent', profile: 'baseline', prompt: 'Fixture only'}],
    }}});
    const {runId} = await call('/api/ops/pipeline_run_create', {pipelineId, requestId: randomUUID(), projectId: 'hello-world'});
    const executionId = await until(() => c.sql(`SELECT id FROM execution WHERE kind='agent_run' AND data->>'pipelineRunId'='${runId}'`).trim() || undefined);
    const command = await until(async () => (await poll()).commands.find((x: any) => x.executionId === executionId));
    const receipt = (status: string, artifacts?: string[]) => call('/api/hosts/receipts', {commandId: command.id, executionId, status, artifacts}, secret);
    await receipt('started');
    await receipt('completed', ['output.json']);
    const state = (id: string) => c.sql(`SELECT state::text FROM execution WHERE id='${id}'`).trim();
    assert.equal(state(executionId), 'waiting');
    assert.equal(state(runId), 'running');
    c.sql(`
      INSERT INTO execution(id, kind, state, ended_at, data) VALUES
        ('elapsed-stored','agent_run','waiting',now(),'{"waitReason":"artifacts","waitingSince":1,"declaredArtifacts":["output.json"],"pendingArtifacts":["output.json"]}'),
        ('elapsed-pending','agent_run','waiting',now(),'{"waitReason":"artifacts","waitingSince":1,"declaredArtifacts":["output.json"],"pendingArtifacts":["output.json"]}'),
        ('still-uploading','agent_run','waiting',now(),jsonb_build_object('waitReason','artifacts','waitingSince',${Date.now()},'declaredArtifacts',jsonb_build_array('output.json'),'pendingArtifacts',jsonb_build_array('output.json'))),
        ('failed-required','agent_run','waiting',now(),jsonb_build_object('waitReason','artifacts','waitingSince',${Date.now()},'declaredArtifacts',jsonb_build_array('output.json','proof.txt'),'pendingArtifacts',jsonb_build_array('output.json','proof.txt'))),
        ('shared-name','agent_run','waiting',now(),jsonb_build_object('waitReason','artifacts','waitingSince',${Date.now()},'pendingArtifacts',jsonb_build_array('proof.txt')));
      UPDATE execution SET data=data || jsonb_build_object('id',id,'executionId',id,'finishedAt',${Date.now()})
        WHERE id IN ('elapsed-stored','elapsed-pending','still-uploading','failed-required','shared-name');
      INSERT INTO artifact(id, scope, content_hash, size, media_type, name, execution_id, state, required) VALUES
        ('${randomUUID()}','lab','proof',2,'application/json','output.json','${executionId}','stored',false),
        ('${randomUUID()}','lab','proof',2,'application/json','output.json','elapsed-stored','stored',false),
        ('${randomUUID()}','lab','proof',2,'application/json','output.json','elapsed-pending','pending',false),
        ('${randomUUID()}','lab','proof',2,'application/json','output.json','still-uploading','pending',false),
        ('${randomUUID()}','lab','proof',2,'application/json','output.json','failed-required','pending',false),
        ('${randomUUID()}','lab','proof',2,'text/plain','proof.txt','failed-required','failed',true),
        ('${randomUUID()}','lab','proof',2,'text/plain','proof.txt','shared-name','stored',true),
        ('${randomUUID()}','lab','proof',2,'text/plain','proof.txt','shared-name','pending',false);
    `);
    await c.restart();
    await until(() => state(runId) === 'running' ? undefined : true);
    assert.equal(state(runId), 'succeeded');
    assert.equal(state(executionId), 'succeeded');
    const expected = {
      'elapsed-stored': 'succeeded', 'elapsed-pending': 'failed',
      'still-uploading': 'waiting', 'failed-required': 'failed', 'shared-name': 'succeeded',
    };
    const states = () => JSON.parse(c.sql(`SELECT json_object_agg(id, state::text) FROM execution
      WHERE id IN ('elapsed-stored','elapsed-pending','still-uploading','failed-required','shared-name')`));
    await until(() => states()['shared-name'] === 'waiting' ? undefined : true);
    assert.deepEqual(states(), expected);
  } finally { await c.stop(); }
});

test('lost task executions reconcile questions and acceptance (requirement: truthful task waits and exact completion; risk: falsely accepting work or waiting for a withdrawn question)', async () => {
  const c = await startCoordinator({name: 'completion-lost', env: {FLEET_LOST_AFTER_MS: '3600000'}});
  const call = async (op: string, body: unknown, status = 200) => {
    const response = await fetch(c.base + '/api/ops/' + op, {method: 'POST', headers: {
      authorization: 'Bearer ' + c.secret, 'x-fleet-person': 'owner@trial.test', 'content-type': 'application/json',
    }, body: JSON.stringify(body)});
    const result = await response.json();
    assert.equal(response.status, status, JSON.stringify(result));
    return result;
  };
  try {
    const seed = async (title: string) => {
      const taskId = randomUUID(), executionId = randomUUID();
      const {sessionId} = await call('session_create', {});
      c.sql(`INSERT INTO task(id,project_id,title,state) VALUES('${taskId}','hello-world','${title}','in_progress');
        UPDATE session SET task_id='${taskId}' WHERE id='${sessionId}';
        INSERT INTO execution(id,kind,state,observation,project_id,task_id,session_id,data)
        VALUES('${executionId}','agent_run','running','unconfirmed','hello-world','${taskId}','${sessionId}',
          '{"id":"${executionId}","sessionId":"${sessionId}","projectId":"hello-world","taskId":"${taskId}"}')`);
      return {taskId, executionId};
    };
    const lost = await seed('Lost after asking');
    const abandonedQuestion = randomUUID();
    const owner = await seed('Independent owner decision');
    const ownerQuestion = randomUUID();
    c.sql(`UPDATE task SET state='waiting',wait_reason='question' WHERE id IN ('${lost.taskId}','${owner.taskId}');
      INSERT INTO question(id,task_id,execution_id,project_id,kind,text,options) VALUES
      ('${abandonedQuestion}','${lost.taskId}','${lost.executionId}','hello-world','agent_question','Which target?', NULL),
      ('${ownerQuestion}','${owner.taskId}','${owner.executionId}','hello-world','operator_decision','Choose the release scope', '["keep","change"]')`);
    const read = (id: string) => call('task_get', {taskId: id});
    const open = (task: any) => task.questions.filter((q: any) => q.state === 'open');
    assert.equal((await read(lost.taskId)).waitReason, 'question');
    await call('executions_abandon', {ids: [lost.executionId, owner.executionId], reason: 'Fixture lost host acknowledgement'});
    const after = await read(lost.taskId);
    assert.equal(after.executions[0].problem, 'unknown:abandoned');
    assert.equal(after.executions[0].observation, 'unconfirmed');
    assert.equal(after.questions.find((q: any) => q.id === abandonedQuestion).state, 'withdrawn');
    assert.deepEqual([after.state, after.waitReason, open(after).map((q: any) => q.kind)], ['waiting', 'question', ['operator_decision']]);
    assert.match(open(after)[0].text, /outcome is unknown/);
    assert.deepEqual(open(await read(owner.taskId)).map((q: any) => q.id), [ownerQuestion]);

    const unconfirmed = await seed('Awaiting host observation');
    await call('task_request_acceptance', {taskId: unconfirmed.taskId}, 409);
    const legacy = await seed('Legacy empty question wait');
    c.sql(`UPDATE task SET state='waiting',wait_reason='question' WHERE id='${legacy.taskId}';
      UPDATE execution SET state='failed',problem='unknown:abandoned',ended_at=now() WHERE id='${legacy.executionId}'`);
    assert.deepEqual(open(await read(legacy.taskId)), []);
    const stale = randomUUID();
    c.sql(`INSERT INTO question(id,task_id,execution_id,project_id,kind,text) VALUES
      ('${stale}','${unconfirmed.taskId}','${unconfirmed.executionId}','hello-world','acceptance','Premature prompt')`);
    await call('questions_answer', {questionId: stale, answer: 'accepted'}, 409);
    await c.restart();
    const pending = await read(unconfirmed.taskId);
    assert.equal(pending.state, 'in_progress');
    assert.equal(pending.waitReason ?? null, null);
    assert.equal(pending.executions[0].observation, 'unconfirmed');
    assert.deepEqual(open(pending), []);
    assert.deepEqual(open(await read(legacy.taskId)).map((q: any) => q.kind), ['operator_decision']);
  } finally { await c.stop(); }
});

test('acceptance binds to the execution and its retained artifacts (requirement: exact eligible delivery; risk: accepting an older artifact after a new run)', async () => {
  const c = await startCoordinator({name: 'completion-version'});
  const call = async (op: string, body: unknown, status = 200) => {
    const response = await fetch(c.base + '/api/ops/' + op, {method: 'POST', headers: {
      authorization: 'Bearer ' + c.secret, 'x-fleet-person': 'owner@trial.test', 'content-type': 'application/json',
    }, body: JSON.stringify(body)});
    const result = await response.json();
    assert.equal(response.status, status, JSON.stringify(result));
    return result;
  };
  try {
    const taskId = randomUUID(), old = randomUUID(), current = randomUUID(), oldOutput = randomUUID(), newOutput = randomUUID();
    const report = '{"report":{"outcome":"succeeded","at":1}}';
    c.sql(`INSERT INTO task(id,project_id,title,state,completion_requirements)
      VALUES('${taskId}','hello-world','Versioned report','in_progress','{"artifacts":["report.txt"]}');
      INSERT INTO execution(id,kind,state,project_id,task_id,data) VALUES('${old}','agent_run','succeeded','hello-world','${taskId}','${report}');
      INSERT INTO artifact(id,scope,content_hash,size,media_type,name,execution_id,task_id,state)
      VALUES('${oldOutput}','prod','older',1,'text/plain','report.txt','${old}','${taskId}','stored')`);
    const older = await call('task_request_acceptance', {taskId});
    assert.deepEqual([older.executionId, older.artifactIds], [old, [oldOutput]]);
    c.sql(`INSERT INTO execution(id,kind,state,project_id,task_id,data) VALUES('${current}','agent_run','succeeded','hello-world','${taskId}','${report}')`);
    await call('task_request_acceptance', {taskId}, 409);
    await call('questions_answer', {questionId: older.id, answer: 'accepted'}, 409);
    c.sql(`INSERT INTO artifact(id,scope,content_hash,size,media_type,name,execution_id,task_id,state)
      VALUES('${newOutput}','prod','newer',1,'text/plain','report.txt','${current}','${taskId}','stored')`);
    await call('questions_answer', {questionId: older.id, answer: 'accepted'}, 409);
    const rejected = await call('questions_answer', {questionId: older.id, answer: 'rejected'});
    assert.equal(rejected.answer, 'rejected', 'rejecting an obsolete result cannot reject the current one');
    const legacy = randomUUID();
    c.sql(`INSERT INTO question(id,kind,task_id,project_id,text,asked_by) VALUES
      ('${legacy}','acceptance','${taskId}','hello-world','Unbound legacy offer','fleet')`);
    await call('questions_answer', {questionId: legacy, answer: 'accepted'}, 409);
    await c.restart();
    const task = await call('task_get', {taskId});
    const offer = task.questions.find((q: any) => q.state === 'open');
    assert.equal(task.questions.find((q: any) => q.id === legacy).state, 'withdrawn');
    assert.deepEqual([offer.kind, offer.executionId, offer.artifactIds], ['acceptance', current, [newOutput]]);
    await call('questions_answer', {questionId: offer.id, answer: 'accepted', artifactIds: [oldOutput]}, 409);
    await call('questions_answer', {questionId: offer.id, answer: 'accepted'});
    assert.deepEqual((await call('task_get', {taskId})).acceptedArtifactIds, [newOutput]);
  } finally { await c.stop(); }
});

test('terminal observation confirmation advances acceptance without restart (requirement: evidence-driven reconciliation; risk: a late host event leaves an eligible result behind an obsolete decision)', async () => {
  const c = await startCoordinator({name: 'completion-observation'});
  const call = async (path: string, body: unknown, hostSecret?: string) => {
    const r = await fetch(c.base + path, {method: 'POST', headers: {
      authorization: 'Bearer ' + (hostSecret ?? c.secret),
      ...(hostSecret ? {} : {'x-fleet-person': 'owner@trial.test'}), 'content-type': 'application/json',
    }, body: JSON.stringify(body)});
    const result = await r.json();
    assert.equal(r.status, 200, JSON.stringify(result));
    return result;
  };
  try {
    const {secret} = await call('/api/ops/hosts_register', {id: 'trial-host'});
    const {sessionId} = await call('/api/ops/session_create', {});
    const taskId = randomUUID(), run = randomUUID(), stale = randomUUID();
    c.sql(`INSERT INTO task(id,project_id,title,state,wait_reason) VALUES('${taskId}','hello-world','Awaiting proof','waiting','question');
      INSERT INTO execution(id,kind,state,observation,project_id,task_id,session_id,host_id,data)
      VALUES('${run}','agent_run','succeeded','unconfirmed','hello-world','${taskId}','${sessionId}','trial-host',
        '{"id":"${run}","sessionId":"${sessionId}","hostId":"trial-host","report":{"outcome":"succeeded","at":1}}');
      INSERT INTO question(id,kind,task_id,execution_id,project_id,text,asked_by,data) VALUES
        ('${stale}','operator_decision','${taskId}','${run}','hello-world','Obsolete generated decision','fleet','{"idle":true}')`);
    const event = {executionId: run, events: [{id: randomUUID(), seq: 0, type: 'resource.observation', timestamp: 1, data: {}}]};
    await call('/api/hosts/events', event, secret);
    await call('/api/hosts/events', event, secret);
    const task = await call('/api/ops/task_get', {taskId});
    assert.equal(task.executions[0].observation, 'confirmed');
    assert.equal(task.questions.find((q: any) => q.id === stale).state, 'withdrawn');
    assert.deepEqual(task.openQuestions.map((q: any) => q.kind), ['acceptance']);
    assert.equal(task.questions.find((q: any) => q.state === 'open').executionId, run);
  } finally { await c.stop(); }
});

test('restart keeps withdrawn checkpoints out of Needs you (requirement: genuine human actions only; risk: rollout replaces stale acceptance with new blocking decisions)', async () => {
  const c = await startCoordinator({name: 'flt25-burst-restart'});
  const post = async (name: string, body: unknown, status = 200) => {
    const response = await fetch(c.base + '/api/ops/' + name, {method: 'POST', headers: {
      authorization: 'Bearer ' + c.secret, 'x-fleet-person': 'owner@trial.test', 'content-type': 'application/json',
    }, body: JSON.stringify(body)});
    const data = await response.json();
    assert.equal(response.status, status, JSON.stringify(data));
    return data;
  };
  try {
    const fixtures = Array.from({length: 10}, (_, i) => ({
      task: randomUUID(), run: randomUUID(), tool: i < 5 ? randomUUID() : null,
      question: randomUUID(), artifact: randomUUID(),
    }));
    const eligible = {task: randomUUID(), run: randomUUID()};
    c.sql(fixtures.map(({task,run,tool,question,artifact}, i) => `
      INSERT INTO task(id,project_id,title,state) VALUES(${lit(task)},'hello-world',${lit('Intermediate checkpoint '+(i+1))},'in_progress');
      INSERT INTO execution(id,kind,state,observation,project_id,task_id,ended_at)
        VALUES(${lit(run)},'agent_run','succeeded','confirmed','hello-world',${lit(task)},now()-interval '2 seconds');
      ${tool ? `INSERT INTO execution(id,kind,state,observation,project_id,task_id,parent_execution_id,ended_at)
        VALUES(${lit(tool)},'tool_run','succeeded','confirmed','hello-world',${lit(task)},${lit(run)},now()-interval '1 second');` : ''}
      INSERT INTO artifact(id,scope,content_hash,size,media_type,name,execution_id,task_id,state)
        VALUES(${lit(artifact)},'prod',${lit('invented-fixture-hash-'+i)},1,'text/plain','checkpoint.txt',${lit(run)},${lit(task)},'stored');
      INSERT INTO question(id,kind,project_id,task_id,execution_id,text,asked_by,data)
        VALUES(${lit(question)},'acceptance','hello-world',${lit(task)},${lit(tool??run)},'Accept checkpoint?','fleet',
          ${lit({artifactIds:tool?[]:[artifact]})});
    `).join('\n')+`
      INSERT INTO task(id,project_id,title,state) VALUES(${lit(eligible.task)},'hello-world','Eligible completed work','in_progress');
      INSERT INTO execution(id,kind,state,project_id,task_id,ended_at,data)
        VALUES(${lit(eligible.run)},'agent_run','succeeded','hello-world',${lit(eligible.task)},now(),
          '{"report":{"outcome":"succeeded","at":1}}');
    `);
    const tasks = fixtures.map(f=>lit(f.task)).join(',');
    const questions = fixtures.map(f=>lit(f.question)).join(',');
    const artifacts = fixtures.map(f=>lit(f.artifact)).join(',');
    const counts = () => JSON.parse(c.sql(`
      SELECT json_build_object(
        'withdrawnOriginals',(SELECT count(*) FROM question WHERE state='withdrawn' AND id IN (${questions})),
        'acceptances',(SELECT count(*) FROM question WHERE kind='acceptance' AND task_id IN (${tasks})),
        'openAcceptances',(SELECT count(*) FROM question WHERE state='open' AND kind='acceptance' AND task_id IN (${tasks})),
        'openDecisions',(SELECT count(*) FROM question WHERE state='open' AND kind='operator_decision' AND task_id IN (${tasks})),
        'storedArtifacts',(SELECT count(*) FROM artifact WHERE state='stored' AND id IN (${artifacts})),
        'completedTasks',(SELECT count(*) FROM task WHERE state='completed' AND id IN (${tasks})));
    `));
    const withdrawn = await post('questions_withdraw',{ids:fixtures.map(x=>x.question),reason:'Fixture withdrawal of intermediate checkpoint offers'});
    assert.equal(withdrawn.withdrawn.length,10);
    const initial = {withdrawnOriginals:10,acceptances:10,openAcceptances:0,openDecisions:0,storedArtifacts:10,completedTasks:0};
    assert.deepEqual(counts(),initial);
    const generated = randomUUID();
    let eligibleOffer: string | undefined;
    for (let restart=1; restart<=2; restart++) {
      if (restart === 2) {
        const {task, run} = fixtures[0];
        c.sql(`UPDATE task SET state='waiting',wait_reason='question' WHERE id=${lit(task)};
          INSERT INTO question(id,kind,project_id,task_id,execution_id,text,asked_by,data)
          VALUES(${lit(generated)},'operator_decision','hello-world',${lit(task)},${lit(run)},
            'Legacy generated decision','fleet','{"idle":true}')`);
      }
      await c.restart();
      const observed = counts();
      assert.deepEqual(observed,initial);
      const task = await post('task_get',{taskId:eligible.task});
      const open = task.questions.filter((q: {state:string})=>q.state==='open');
      assert.equal(open.length,1);
      assert.equal(open[0].kind,'acceptance');
      assert.equal(open[0].executionId,eligible.run);
      if (eligibleOffer) assert.equal(open[0].id, eligibleOffer);
      eligibleOffer = open[0].id;
      assert.equal(task.state,'in_progress');
    }
    assert.equal(c.sql(`SELECT state FROM question WHERE id=${lit(generated)}`).trim(), 'withdrawn');
    assert.equal((await post('task_get', {taskId: fixtures[0].task})).state, 'in_progress');
  } finally {
    await c.stop();
  }
});

test('task detail explains a missing completion report without asking for action (requirement: informational completion evidence; risk: a successful process is mistaken for either completed task work or failed work)', async () => {
  const c = await startCoordinator({name: 'completion-report-note'});
  try {
    const cases = [
      {name: 'missing', state: 'succeeded', observation: 'confirmed', outcome: null, note: true},
      {name: 'eligible', state: 'succeeded', observation: 'confirmed', outcome: 'succeeded'},
      {name: 'failed', state: 'failed', observation: 'confirmed', outcome: null},
      {name: 'reported incomplete', state: 'succeeded', observation: 'confirmed', outcome: 'failed'},
      {name: 'unconfirmed', state: 'succeeded', observation: 'unconfirmed', outcome: null},
      {name: 'active', state: 'succeeded', observation: 'confirmed', outcome: null},
      {name: 'queued', state: 'succeeded', observation: 'confirmed', outcome: null},
      {name: 'valid earlier offer', state: 'succeeded', observation: 'confirmed', outcome: null},
      {name: 'stale earlier offer', state: 'succeeded', observation: 'confirmed', outcome: null, note: true},
    ];
    for (const x of cases) {
      const taskId = randomUUID(), executionId = randomUUID(), earlierId = randomUUID(), sessionId = randomUUID();
      const earlier = x.name.endsWith('earlier offer');
      c.sql(`INSERT INTO task(id,project_id,title,state) VALUES(${lit(taskId)},'hello-world',${lit(x.name)},'in_progress');
        ${earlier ? `INSERT INTO execution(id,kind,state,project_id,task_id,ended_at,data)
          VALUES(${lit(earlierId)},'agent_run','succeeded','hello-world',${lit(taskId)},now(),
            '{"report":{"outcome":"succeeded","at":1}}');
          INSERT INTO question(id,kind,task_id,execution_id,project_id,text,data)
          VALUES(${lit(randomUUID())},'acceptance',${lit(taskId)},${lit(x.name==='valid earlier offer'?earlierId:executionId)},
            'hello-world','Earlier result','{"earlierResult":true,"artifactIds":[]}');` : ''}
        ${x.name === 'active' ? `INSERT INTO execution(id,kind,state,project_id,task_id)
          VALUES(${lit(randomUUID())},'agent_run','running','hello-world',${lit(taskId)});` : ''}
        INSERT INTO execution(id,kind,state,observation,project_id,task_id,ended_at,data)
        VALUES(${lit(executionId)},'agent_run',${lit(x.state)},${lit(x.observation)},'hello-world',${lit(taskId)},now(),
          ${lit(x.outcome ? {report:{outcome:x.outcome,at:1}} : {})});
        ${x.name === 'queued' ? `INSERT INTO session(id,project_id,task_id) VALUES(${lit(sessionId)},'hello-world',${lit(taskId)});
          INSERT INTO execution(id,kind,state,project_id,session_id)
          VALUES(${lit(randomUUID())},'agent_run','running','hello-world',${lit(sessionId)});
          INSERT INTO queued_message(id,session_id,task_id,project_id,state)
          VALUES(${lit(randomUUID())},${lit(sessionId)},${lit(taskId)},'hello-world','queued');` : ''}`);
      const response = await fetch(c.base + '/api/ops/task_get', {method:'POST', headers:{
        authorization:'Bearer ' + c.secret, 'x-fleet-person':'owner@trial.test', 'content-type':'application/json',
      }, body:JSON.stringify({taskId})});
      const task = await response.json();
      assert.equal(response.status, 200, JSON.stringify(task));
      assert.equal(task.completionReportMissing === true, x.note === true, x.name);
      assert.equal(task.state, 'in_progress', x.name);
      assert.equal(task.questions.length, earlier ? 1 : 0, x.name);
      if (x.name === 'queued') assert.deepEqual(task.executions.map((e: any) => e.state), ['succeeded']);
    }
  } finally { await c.stop(); }
});
