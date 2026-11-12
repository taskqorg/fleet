// Requirement: R2 shared core and R7 agentic first: one search operation serves the Spotlight palette and agents.
// Risk: a field term is dropped and the palette lists everything, or a person finds tasks of a project outside their grants.
import {test} from 'node:test';
import assert from 'node:assert/strict';
import {startCoordinator} from '../harness.ts';

// R2, R7; risk: a field term is dropped and every task matches, or grants leak a hidden project's task.
test('search finds tasks by title and id prefix, applies field terms, keeps grants', {timeout: 30_000}, async t => {
  const c = await startCoordinator({name: 'search'}); t.after(() => c.stop());
  const op = (body: object, who?: string) => op0('search', body, who);
  const op0 = async (name: string, body: object, who = 'owner@trial.test') => {
    const r = await fetch(c.base + '/api/ops/' + name, {method: 'POST', headers: {authorization: 'Bearer ' + c.secret, 'x-fleet-person': who, 'content-type': 'application/json'}, body: JSON.stringify(body)});
    const d: any = await r.json(); return name === 'search' ? {status: r.status, d} : (assert.equal(r.status, 200, JSON.stringify(d)), {...d, status: r.status});
  };
  c.sql(`INSERT INTO task(id,project_id,title,updated_at) VALUES
      ('abc12345-flaky','hello-world','Fix flaky deploy', now() - interval '1 hour'),
      ('ffff0000-other','hello-world','Write abc12345 notes', now()),
      ('hidden-0001','hidden-project','Fix flaky hidden', now());
    UPDATE task_derived SET agent='claude',host='mac-mini' WHERE task_id='abc12345-flaky';`);
  const people = (await op0('people_list', {})).people;
  assert.equal((await op0('people_set', {people: [...people.filter((p: any) => p.email !== 'scoped@trial.test'), {email: 'scoped@trial.test', name: 'Scoped', roles: ['operator'], grants: [{kind: 'project', id: 'hello-world'}]}]})).status, 200);
  const ids = (r: any) => r.d.hits.filter((h: any) => h.kind === 'task').map((h: any) => h.id);
  const flaky = await op({query: 'flaky'});
  assert.equal(flaky.status, 200, JSON.stringify(flaky.d));
  assert.deepEqual(ids(flaky).sort(), ['abc12345-flaky', 'hidden-0001']);
  // An id that starts with the text comes before a newer title match (jump by code).
  assert.deepEqual(ids(await op({query: 'abc12345'})), ['abc12345-flaky', 'ffff0000-other']);
  assert.deepEqual(ids(await op({query: 'agent:claude host:mac'})), ['abc12345-flaky']);
  assert.deepEqual((await op({query: 'agent:codex flaky'})).d.hits, []);
  assert.deepEqual((await op({query: ''})).d.hits, []);
  assert.deepEqual(ids(await op({query: 'flaky'}, 'scoped@trial.test')), ['abc12345-flaky']);
  assert.ok((await op({query: 'hello'})).d.hits.some((h: any) => h.kind === 'project' && h.id === 'hello-world'));
  assert.ok((await op({query: 'hello agent:claude'})).d.hits.every((h: any) => h.kind === 'task'));
  assert.equal((await op({query: 'x'.repeat(501)})).status, 400);
  assert.equal((await op({query: 'flaky', limit: 0})).status, 400);
  assert.deepEqual(ids(await op({query: 'state:queued abc12345'})), ['abc12345-flaky', 'ffff0000-other']);
  assert.deepEqual(ids(await op({query: 'a_c'})), []);
  assert.deepEqual(ids(await op({query: 'deploy fix'})), ['abc12345-flaky']);
});
