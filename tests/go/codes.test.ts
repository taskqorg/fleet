// Outside test of human entity codes (D17; docs/designs/fleet-data-foundation.md §2, Human codes) against the real binary
// and Postgres: issue per project, lookup by code, rename with aliases, and no code beyond the caller's scope.
//   FLEET_TRIAL_TESTS=tests/go/codes.test.ts node tests/trial/run.ts   (or tests/go/run.sh, which sets the store env)
import {test} from 'node:test';
import assert from 'node:assert/strict';
import {startCoordinator} from '../harness.ts';

test('K1 tasks and questions carry T-/Q- codes per project; codes resolve, a renamed project keeps old codes as aliases, other scopes see none (requirement: D17 human codes; risk: UUIDs shown to people, a rename breaks links in messengers, a code leaks a forbidden row)', {timeout: 60_000}, async () => {
  const c = await startCoordinator({name: 'codes'});
  const raw = async (name: string, body: unknown, person = 'owner@trial.test') => {
    const r = await fetch(c.base + '/api/ops/' + name, {method: 'POST', headers: {authorization: 'Bearer ' + c.secret, 'x-fleet-person': person, 'content-type': 'application/json'}, body: JSON.stringify(body)});
    const text = await r.text(); let parsed: any = text; try { parsed = JSON.parse(text); } catch {}
    return {status: r.status, body: parsed};
  };
  const op = async (name: string, body: unknown) => { const r = await raw(name, body); assert.equal(r.status, 200, `${name}: ${JSON.stringify(r.body)}`); return r.body; };
  try {
    const a = await op('task_create', {projectId: 'hello-world', title: 'first'});
    const b = await op('task_create', {projectId: 'hello-world', title: 'second'});
    const other = await op('task_create', {projectId: 'fleet-hello-world', title: 'other project'});
    const none = await op('task_create', {title: 'no project'});
    assert.match(a.code, /^T-[A-Z][A-Z0-9]{1,4}-1$/);
    const scope = a.code.split('-')[1];
    assert.equal(b.code, `T-${scope}-2`);
    assert.match(other.code, /^T-[A-Z][A-Z0-9]{1,4}-1$/);
    assert.notEqual(other.code.split('-')[1], scope, 'each project its own code');
    assert.equal(none.code, 'T-ALL-1');
    const page = await op('task_page', {filter: {projectId: ['hello-world']}});
    assert.deepEqual(page.tasks.map((t: any) => t.code).sort(), [a.code, b.code].sort());
    assert.equal((await op('task_page', {filter: {search: a.code.toLowerCase()}})).tasks[0]?.id, a.id, 'search finds a task by its code');

    c.sql(`INSERT INTO question(id,kind,project_id,task_id,text) VALUES('q-codes','operator_decision','hello-world','${a.id}','which colour?')`);
    assert.equal((await op('task_get', {taskId: a.id})).questions[0].code, `Q-${scope}-1`);

    assert.deepEqual(await op('entity_resolve', {code: a.code.toLowerCase()}), {code: a.code, id: a.id, type: 'T', currentCode: a.code, superseded: false});
    assert.equal((await raw('entity_resolve', {code: `T-${scope}-99`})).status, 404);
    assert.equal((await raw('entity_resolve', {code: a.id})).status, 400);

    assert.equal((await raw('project_code_set', {projectId: 'hello-world', code: 'ABC'})).status, 400, 'B is not allowed');
    assert.equal((await raw('project_code_set', {projectId: 'hello-world', code: other.code.split('-')[1]})).status, 409, 'another project holds it');
    assert.deepEqual(await op('project_code_set', {projectId: 'hello-world', code: 'hwx'}), {projectId: 'hello-world', code: 'HWX'});
    const renamed = await op('task_get', {taskId: a.id});
    assert.match(renamed.code, /^T-HWX-[12]$/);
    assert.deepEqual(await op('entity_resolve', {code: a.code}), {code: a.code, id: a.id, type: 'T', currentCode: renamed.code, superseded: true});
    assert.equal((await op('task_create', {projectId: 'hello-world', title: 'third'})).code, 'T-HWX-3');
    assert.equal((await raw('project_code_set', {projectId: 'hello-world', code: scope})).status, 409, 'an old code is never reused');

    const many = await op('entity_resolve_many', {text: `see ${a.code}, ${renamed.code} and ${other.code}; not T-QQQ-7 or #12`});
    assert.deepEqual(many.codes.map((x: any) => x.id), [a.id, a.id, other.id]);

    const outsider = await raw('entity_resolve', {code: other.code}, 'outsider@trial.test');
    assert.notEqual(outsider.status, 200, JSON.stringify(outsider.body));
  } finally { await c.stop(); }
});
