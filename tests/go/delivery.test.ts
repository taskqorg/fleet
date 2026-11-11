// Outside test of worktree cleanup on delivery (docs/designs/fleet-system-management.md, Worktree on delivery).
//   FLEET_TRIAL_TESTS=tests/go/delivery.test.ts node tests/trial/run.ts
import {test} from 'node:test';
import assert from 'node:assert/strict';
import {randomUUID} from 'node:crypto';
import {startCoordinator} from '../harness.ts';

test('W1 accepting a task sends one teardown with deleteBranch for its finished session, and the project default keeps unpushed worktrees (requirement: Worktree on delivery; risk: delivered worktrees pile up on hosts)', {timeout: 60_000}, async () => {
  const c = await startCoordinator({name: 'delivery'});
  const call = async (path: string, body: unknown, auth = c.secret, person: string | null = 'owner@trial.test') => {
    const r = await fetch(c.base + path, {method: 'POST', headers: {authorization: 'Bearer ' + auth, ...(person ? {'x-fleet-person': person} : {}), 'content-type': 'application/json'}, body: JSON.stringify(body)});
    const text = await r.text(); let parsed: any = text; try { parsed = JSON.parse(text); } catch {}
    assert.equal(r.status, 200, `${path}: ${text}`);
    return parsed;
  };
  try {
    const {secret} = await call('/api/ops/hosts_register', {id: 'trial-host'});
    const task = await call('/api/ops/task_create', {projectId: 'hello-world', title: 'delivered'});
    const taskId = task.id ?? task.taskId;
    const {sessionId} = await call('/api/sessions', {});
    c.sql(`UPDATE document SET data=data||'{"projectId":"hello-world"}' WHERE kind='session' AND id='${sessionId}'`);
    const e = await call(`/api/sessions/${sessionId}/messages`, {requestId: randomUUID(), text: 'hello'});
    c.sql(`UPDATE execution SET task_id='${taskId}' WHERE id='${e.id}'`);
    // R3; Save after enqueue cannot rewrite launch context or cleanup of the original workspace.
    const before = await call('/api/ops/project_context', {projectId:'hello-world'});
    const changed = structuredClone(before.content);changed.settings.cleanup={keepWhen:[]};
    changed.contextFiles[0].text='LATER PROJECT CANARY';
    await call('/api/ops/project_update',{projectId:'hello-world',requestId:randomUUID(),baseRevision:before.revision,content:changed});
    const poll = () => call('/api/hosts/poll', {platform: 'darwin', capabilities: [], stores: [], problems: []}, secret, null);
    const command = (await poll()).commands.find((x: any) => x.executionId === e.id);
    assert.deepEqual(command.payload.project.cleanup.keepWhen, ['uncommitted','unpushed','teardown-failed']);
    assert.ok(!command.payload.instructions.includes('LATER PROJECT CANARY'));
    await call('/api/hosts/receipts', {commandId: command.id, executionId: e.id, status: 'started'}, secret, null);
    await call('/api/hosts/receipts', {commandId: command.id, executionId: e.id, status: 'completed'}, secret, null);
    c.sql(`UPDATE task SET state='in_progress' WHERE id='${taskId}'`);
    const question = await call('/api/ops/task_request_acceptance', {taskId});
    await call('/api/ops/question_answer', {questionId: question.id, answer: 'accepted'});
    const teardowns = (await poll()).commands.filter((x: any) => x.kind === 'teardown' && x.sessionId === sessionId);
    assert.equal(teardowns.length, 1);
    assert.equal(teardowns[0].payload.deleteBranch, true);
    assert.deepEqual(teardowns[0].payload.project.cleanup.keepWhen, ['uncommitted', 'unpushed', 'teardown-failed']);
  } finally { await c.stop(); }
});
