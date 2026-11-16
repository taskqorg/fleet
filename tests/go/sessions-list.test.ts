// Requirement: R2 shared core and R3 reliability/authority: Conversations lists and membership remain scoped and complete under retained history.
// Risk: RLS prevents a JSON sessionId index condition, so correlated history scans multiply by every session.
import {test, type TestContext} from 'node:test';
import assert from 'node:assert/strict';
import {startCoordinator} from '../harness.ts';

// R2, R3; risk: RLS turns per-session JSON history lookups into repeated scans and hides/leaks list membership.
// Fixture watchdog owns cleanup; this is not a product latency threshold or a coordinator timeout change.
test('sessions_list and membership with production-shaped RLS history retain title, activity, counts and actor secrecy', {timeout: 30_000}, async (t: TestContext) => {
  const c = await startCoordinator({name: 'sesslist'}); t.after(() => c.stop());
  const owner = 'owner@trial.test', member = 'member@trial.test', outsider = 'outsider@trial.test';
  const op = async (name: string, body: object, who = member) => {
    const r = await fetch(c.base + '/api/ops/' + name, {method: 'POST', headers: {authorization: 'Bearer ' + c.secret, 'x-fleet-person': who, 'content-type': 'application/json'}, body: JSON.stringify(body)});
    const d: any = await r.json(); assert.equal(r.status, 200, JSON.stringify(d)); return d;
  };
  const mine = (await op('session_create', {})).sessionId;
  const theirs = (await op('session_create', {}, outsider)).sessionId;
  // Similar fanout to the incident (1658 sessions, 9495 events); no native work is launched.
  c.sql(`INSERT INTO document(kind,id,project_id,data)
    SELECT 'session','list-'||i,'desiign',jsonb_build_object('createdAt',i,'agentConfig',jsonb_build_object('harness','codex')) || CASE WHEN i=1599 THEN '{"archivedAt":1}'::jsonb ELSE '{}'::jsonb END FROM generate_series(0,1599) i;
    INSERT INTO document(kind,id,project_id,data) VALUES
      ('session','list-foreign','hidden-project','{}'),
      ('message','list-foreign-message','hidden-project','{"sessionId":"list-0","text":"Foreign title canary"}'),
      ('message','list-first','desiign','{"sessionId":"list-0","text":"Needle first title"}'),
      ('message','list-second','desiign','{"sessionId":"list-0","text":"Later message is not the title"}');
    INSERT INTO document(kind,id,project_id,data)
    SELECT 'event','list-event-'||i,'desiign',jsonb_build_object('sessionId','list-'||(i%1600),'payload',repeat(md5(i::text),32)) FROM generate_series(1,9500) i;
    INSERT INTO document(kind,id,project_id,data) VALUES ('event','list-last','desiign','{"sessionId":"list-0"}');
    ANALYZE document;`);
  const title = await op('sessions_list', {filter: {search: 'Needle'}});
  assert.equal(title.total, 1); assert.equal(title.sessions[0].id, 'list-0'); assert.equal(title.sessions[0].title, 'Needle first title');
  const page = await op('sessions_list', {limit: 3});
  assert.equal(page.total, 1600); assert.equal(page.counts.idle, 1600); assert.equal(page.sessions[0].id, 'list-0'); assert.ok(page.nextAfter);
  const next = await op('sessions_list', {limit: 3, after: page.nextAfter});
  assert.ok(next.sessions.every((s: any) => !page.sessions.some((p: any) => p.id === s.id)));
  assert.equal((await op('sessions_list', {filter: {archived: true}})).total, 1);
  const membership = await op('list_membership', {ids: ['list-0', mine, theirs, 'list-foreign', 'list-1599'], lists: [{name: 'sessions', filter: {}}]});
  assert.deepEqual(membership.lists[0].rows.map((s: any) => s.id).sort(), ['list-0', mine].sort());
  assert.equal(membership.lists[0].counts.idle, 1600);
  assert.equal((await op('sessions_list', {filter: {search: 'Foreign title canary'}})).total, 0);
  const foreign = await op('sessions_list', {}, outsider); assert.deepEqual(foreign.sessions.map((s: any) => s.id), [theirs]);
  const all = await op('sessions_list', {}, owner); assert.equal(all.total, 1602);
  // Incident 2026-11-12: an ignored projectId filter listed every session and a cleanup archived them all.
  const raw = async (name: string, body: object, who = owner) => (await fetch(c.base + '/api/ops/' + name, {method: 'POST', headers: {authorization: 'Bearer ' + c.secret, 'x-fleet-person': who, 'content-type': 'application/json'}, body: JSON.stringify(body)})).status;
  assert.equal((await op('sessions_list', {filter: {projectId: ['hidden-project']}}, owner)).total, 1);
  assert.deepEqual((await op('sessions_list', {filter: {projectId: [null]}}, owner)).sessions.map((s: any) => s.id).sort(), [mine, theirs].sort());
  assert.equal(await raw('sessions_list', {filter: {project: ['hidden-project']}}), 400);
  assert.equal(await raw('task_page', {filter: {archived: true}}), 400);
  assert.equal(await raw('sessions_list', {filter: {projectId: []}}), 400);
  assert.equal(await raw('sessions_list', {filter: {state: 'running'}}), 400);
  assert.equal(await raw('session_unarchive', {sessionId: 'list-1599', reason: 'test'}, member), 403);
  assert.equal(await raw('session_unarchive', {sessionId: 'list-1599'}), 400);
  await op('session_unarchive', {sessionId: 'list-1599', reason: 'restore a wrong archive'}, owner);
  assert.equal((await op('sessions_list', {filter: {archived: true}}, owner)).total, 0);
  assert.equal(await raw('session_unarchive', {sessionId: 'list-1599', reason: 'again'}), 409);
});

// R2 shared core (OR-35, OR-37); risk: the Conversations list groups or pages differently from what the client merges, so rows
// repeat or vanish between pages, and an unknown group or sort silently falls back to another order.
test('sessions_list groups, sorts and pages by last activity; membership answers with the same keys and groups', {timeout: 30_000}, async (t: TestContext) => {
  const c = await startCoordinator({name: 'sessorder'}); t.after(() => c.stop());
  const send = (name: string, body: object) => fetch(c.base + '/api/ops/' + name, {method: 'POST', headers: {authorization: 'Bearer ' + c.secret, 'x-fleet-person': 'owner@trial.test', 'content-type': 'application/json'}, body: JSON.stringify(body)});
  const op = async (name: string, body: object) => { const r = await send(name, body); const d: any = await r.json(); assert.equal(r.status, 200, JSON.stringify(d)); return d; };
  // Activity is the latest of the session document, its messages and events, and the start or end of its executions;
  // not an execution's updated_at, which host receipts move.
  c.sql(`INSERT INTO document(kind,id,project_id,data,updated_at) VALUES
      ('session','g-a','alpha','{"agentConfig":{"harness":"codex"}}','2026-11-01T10:00:00Z'),
      ('session','g-b','beta','{}','2026-11-01T11:00:00Z'),
      ('session','g-c','alpha','{}','2026-11-01T09:00:00Z'),
      ('session','g-d',NULL,'{}','2026-11-01T08:00:00Z'),
      ('session','g-e','beta','{}','2026-11-01T07:00:00Z');
    INSERT INTO document(kind,id,project_id,data,updated_at) VALUES ('message','g-e-m','beta','{"sessionId":"g-e","text":"latest"}','2026-11-01T13:00:00Z');
    INSERT INTO execution(id,kind,state,project_id,session_id,harness,started_at,updated_at) VALUES ('g-c-x','agent_run','waiting','alpha','g-c','claude','2026-11-01T12:00:00Z','2026-11-01T14:00:00Z');`);
  const ids = (r: any) => r.sessions.map((s: any) => s.id);
  const all = async (body: object) => {
    const out: string[] = []; let after: unknown = undefined;
    do { const r = await op('sessions_list', {...body, limit: 2, ...(after ? {after} : {})}); out.push(...ids(r)); after = r.nextAfter; } while (after);
    return out;
  };
  const def = await op('sessions_list', {});
  assert.deepEqual(ids(def), ['g-c', 'g-a', 'g-e', 'g-b', 'g-d']);
  assert.deepEqual(def.groups, [{key: 'alpha', label: 'alpha', count: 2}, {key: 'beta', label: 'beta', count: 2}, {key: '~', label: null, count: 1}]);
  const gc = def.sessions[0];
  assert.equal(gc.executions, 1); assert.equal(Date.parse(gc.activityAt), Date.parse('2026-11-01T12:00:00Z')); assert.equal(gc.sortKey.length, 3); assert.equal(gc.sortKey[0], 'alpha');
  assert.equal(Date.parse(def.sessions[2].activityAt), Date.parse('2026-11-01T13:00:00Z'));
  assert.deepEqual(await all({}), ids(def));
  assert.deepEqual(ids(await op('sessions_list', {group: 'project', sort: '-activity'})), ids(def));
  const byState = await op('sessions_list', {group: 'state'});
  assert.deepEqual(ids(byState), ['g-c', 'g-e', 'g-b', 'g-a', 'g-d']);
  assert.deepEqual(byState.groups.map((g: any) => [g.label, g.count]), [['waiting', 1], ['idle', 4]]);
  const byAgent = await op('sessions_list', {group: 'agent'});
  assert.deepEqual(ids(byAgent), ['g-c', 'g-a', 'g-e', 'g-b', 'g-d']);
  assert.deepEqual(byAgent.groups.map((g: any) => g.label), ['claude', 'codex', null]);
  assert.deepEqual(await all({group: 'none', sort: 'created'}), ['g-a', 'g-b', 'g-c', 'g-d', 'g-e']);
  assert.deepEqual(await all({group: 'none', sort: '-created'}), ['g-e', 'g-d', 'g-c', 'g-b', 'g-a']);
  assert.deepEqual(await all({group: 'state', sort: 'activity'}), ['g-c', 'g-d', 'g-a', 'g-b', 'g-e']);
  assert.deepEqual((await op('sessions_list', {group: 'none'})).groups, []);
  // The state filter narrows the groups; the state counts stay those of the whole list (the quick filter).
  const waiting = await op('sessions_list', {group: 'project', filter: {state: ['waiting']}});
  assert.deepEqual(ids(waiting), ['g-c']); assert.deepEqual(waiting.groups, [{key: 'alpha', label: 'alpha', count: 1}]); assert.equal(waiting.counts.idle, 4);
  for (const body of [{group: 'priority'}, {group: 3}, {sort: 'changed'}, {sort: '-title'}, {after: [1, 'g-a']}, {after: 'x'}]) assert.equal((await send('sessions_list', body)).status, 400, JSON.stringify(body));
  const m = await op('list_membership', {ids: ['g-a', 'g-c', 'g-e'], lists: [{name: 'sessions', filter: {}, group: 'state', sort: 'created'}, {name: 'sessions', filter: {state: ['idle']}}]});
  assert.deepEqual(m.lists[0].rows.map((s: any) => s.id), ['g-c', 'g-a', 'g-e']);
  assert.deepEqual(m.lists[0].rows.map((s: any) => s.sortKey[0]), ['1', '5', '5']);
  assert.deepEqual(m.lists[0].groups.map((g: any) => g.key), ['1', '5']);
  assert.deepEqual(m.lists[1].rows.map((s: any) => s.id), ['g-a', 'g-e']); assert.equal(m.lists[1].rows[0].sortKey[0], 'alpha');
  assert.equal((await send('list_membership', {ids: ['g-a'], lists: [{name: 'sessions', group: 'host'}]})).status, 400);
});
