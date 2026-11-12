// Requirement: owner review P7/P8/P9 (docs/design-system/owner-rules.md OR-35..OR-37): task_page sorts inside a group by
// recently changed first (or created, either direction), takes key:value terms in its search, and gives every row its
// live facts (state since, executions, last agent, host). Risk: the list keeps creation order, filters stay a no-op button,
// or row facts need a second request per row.
import {test} from 'node:test';
import assert from 'node:assert/strict';
import {startCoordinator} from '../harness.ts';

test('TL1 task_page sorts recently changed first, reads key:value search terms and returns row facts (requirement: owner-rules OR-35, OR-36, OR-37; risk: the list keeps creation order, filters need a Filter button, or row facts need a request per row)', {timeout: 30_000}, async t => {
  const c = await startCoordinator({name: 'tasklist'}); t.after(() => c.stop());
  const op = async (name: string, body: object, status = 200) => {
    const r = await fetch(c.base + '/api/ops/' + name, {method: 'POST', headers: {authorization: 'Bearer ' + c.secret, 'x-fleet-person': 'owner@trial.test', 'content-type': 'application/json'}, body: JSON.stringify(body)});
    const d: any = await r.json(); assert.equal(r.status, status, JSON.stringify(d)); return d;
  };
  const a = (await op('task_create', {title: 'Sort alpha'})).id, b = (await op('task_create', {title: 'Sort beta'})).id;
  await op('task_update', {taskId: a, title: 'Sort alpha'}); // a is now the most recently changed
  c.sql(`INSERT INTO execution(id,kind,state,task_id,harness,host_id,started_at) VALUES ('tl-1','agent_run','failed','${a}','codex','mac-mini',now()), ('tl-2','agent_run','running','${a}','claude','mac-mini',now());`);
  const ids = async (body: object) => (await op('task_page', {group: 'none', filter: {search: 'Sort'}, ...body})).tasks.map((x: any) => x.id);
  assert.deepEqual(await ids({}), [a, b], 'default: recently changed first');
  assert.deepEqual(await ids({sort: 'changed'}), [b, a]);
  assert.deepEqual(await ids({sort: '-created'}), [b, a]);
  await op('task_page', {sort: 'title'}, 400);
  assert.deepEqual(await ids({filter: {search: 'agent:claude host:mini Sort'}}), [a]);
  assert.deepEqual(await ids({filter: {search: 'agent:codex'}}), [], 'agent: is the running execution\'s harness');
  assert.deepEqual(await ids({filter: {search: 'beta state:queued'}}), [b]);
  const row = (await op('task_page', {group: 'none', filter: {search: 'alpha'}})).tasks[0];
  assert.equal(row.executions, 2); assert.equal(row.lastAgent, 'claude'); assert.equal(row.host, 'mac-mini'); assert.ok(row.stateAt);
  const page = await op('task_page', {group: 'none', limit: 1, filter: {search: 'Sort'}});
  assert.deepEqual((await op('task_page', {group: 'none', limit: 1, filter: {search: 'Sort'}, after: page.nextAfter})).tasks.map((x: any) => x.id), [b], 'cursor follows the sort');
  const m = await op('list_membership', {ids: [a, b], lists: [{name: 'tasks', group: 'none', sort: 'changed', filter: {}}]});
  assert.deepEqual(m.lists[0].rows.map((x: any) => x.id).sort(), [a, b].sort());
});
