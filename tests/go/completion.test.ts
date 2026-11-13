// Outside test of the completion gate on a completed receipt (go/coordinator/host_receipts.go, artifactGate).
//   FLEET_TRIAL_TESTS=tests/go/completion.test.ts node tests/trial/run.ts
import {test} from 'node:test';
import assert from 'node:assert/strict';
import {randomUUID} from 'node:crypto';
import {startCoordinator} from '../harness.ts';

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
    const {sessionId} = await call('/api/sessions', {});
    const e = await call(`/api/sessions/${sessionId}/messages`, {requestId: randomUUID(), text: 'hello'});
    c.sql(`UPDATE execution SET task_id='${task.id ?? task.taskId}' WHERE id='${e.id}'`);
    const {commands} = await call('/api/hosts/poll', {platform: 'darwin', capabilities: [], stores: [], problems: []}, secret, null);
    const command = commands.find((x: any) => x.executionId === e.id);
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
    const {sessionId} = await call('/api/sessions', {});
    const e = await call(`/api/sessions/${sessionId}/messages`, {requestId: randomUUID(), text: 'hello'});
    const {commands} = await call('/api/hosts/poll', {platform: 'darwin', capabilities: [], stores: [], problems: []}, secret, null);
    const command = commands.find((x: any) => x.executionId === e.id);
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
    const {sessionId} = await call('/api/sessions', {});
    const e = await call(`/api/sessions/${sessionId}/messages`, {requestId: randomUUID(), text: 'hello'});
    c.sql(`UPDATE execution SET task_id='${taskId}' WHERE id='${e.id}'; UPDATE task SET state='in_progress' WHERE id='${taskId}'`);
    const {commands} = await call('/api/hosts/poll', {platform: 'darwin', capabilities: [], stores: [], problems: []}, secret, null);
    const command = commands.find((x: any) => x.executionId === e.id);
    await call('/api/hosts/receipts', {commandId: command.id, executionId: e.id, status: 'started'}, secret, null);
    await call('/api/hosts/receipts', {commandId: command.id, executionId: e.id, status: 'completed'}, secret, null);
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
    const {sessionId} = await call('/api/sessions', {});
    // A task-bound session: each new run carries the task from its creation, as a worker session does.
    c.sql(`UPDATE document SET data=data||jsonb_build_object('taskId','${taskId}') WHERE id='${sessionId}'`);
    const run = async (status: string) => {
      const e = await call(`/api/sessions/${sessionId}/messages`, {requestId: randomUUID(), text: 'hello'});
      c.sql(`UPDATE execution SET task_id='${taskId}' WHERE id='${e.id}'`);
      await call('/api/ops/task_get', {taskId});
      const {commands} = await call('/api/hosts/poll', {platform: 'darwin', capabilities: [], stores: [], problems: []}, secret, null);
      const command = commands.find((x: any) => x.executionId === e.id);
      await call('/api/hosts/receipts', {commandId: command.id, executionId: e.id, status: 'started'}, secret, null);
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
    c.sql(`INSERT INTO execution(id,kind,state,project_id,task_id) VALUES('${randomUUID()}','agent_run','succeeded','hello-world','${ids.ok}'),('${randomUUID()}','agent_run','failed','hello-world','${ids.bad}'),('${randomUUID()}','agent_run','running','hello-world','${ids.live}')`);
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
    const {sessionId} = await (await fetch(c.base + '/api/sessions', {method:'POST', headers:{authorization:'Bearer '+c.secret,'x-fleet-person':'owner@trial.test','content-type':'application/json'},body:'{}'})).json();
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
      INSERT INTO execution(id,kind,state,project_id,task_id) VALUES('${success}','agent_run','succeeded','hello-world','${earlier}');
      INSERT INTO execution(id,kind,state,project_id,task_id) VALUES('${randomUUID()}','agent_run','failed','hello-world','${earlier}')`);
    await c.restart();
    const decision = (await call('task_get',{taskId:earlier})).questions.find((q:any)=>q.state==='open');
    await call('questions_answer',{questionId:decision.id,answer:'accept earlier result'});
    const acceptance = (await call('task_get',{taskId:earlier})).questions.find((q:any)=>q.state==='open');
    assert.equal(acceptance.executionId,success);
    await call('questions_answer',{questionId:acceptance.id,answer:'accepted'});
    assert.equal((await call('task_get',{taskId:earlier})).state,'completed');
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
    assert.equal(c.sql(`SELECT latest_result->>'path' FROM task_view WHERE task_id='${task}'`).trim(),'new.txt');
  } finally { await c.stop(); }
});
