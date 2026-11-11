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
