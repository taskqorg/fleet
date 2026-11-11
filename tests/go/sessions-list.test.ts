// Requirement: Conversations lists and membership stay scoped, complete and responsive under retained history.
// Risk: RLS prevents a JSON sessionId index condition, so correlated history scans multiply by every session.
import {test} from 'node:test';
import assert from 'node:assert/strict';
import {startCoordinator} from '../harness.ts';

// Fixture watchdog owns cleanup; this is not a product latency threshold or a coordinator timeout change.
test('sessions_list and membership with production-shaped RLS history retain title, activity, counts and actor secrecy', {timeout: 30_000}, async t => {
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
    SELECT 'session','list-'||i,'fleet-hello-world',jsonb_build_object('createdAt',i,'agentConfig',jsonb_build_object('harness','codex')) || CASE WHEN i=1599 THEN '{"archivedAt":1}'::jsonb ELSE '{}'::jsonb END FROM generate_series(0,1599) i;
    INSERT INTO document(kind,id,project_id,data) VALUES
      ('session','list-foreign','hidden-project','{}'),
      ('message','list-foreign-message','hidden-project','{"sessionId":"list-0","text":"Foreign title canary"}'),
      ('message','list-first','fleet-hello-world','{"sessionId":"list-0","text":"Needle first title"}'),
      ('message','list-second','fleet-hello-world','{"sessionId":"list-0","text":"Later message is not the title"}');
    INSERT INTO document(kind,id,project_id,data)
    SELECT 'event','list-event-'||i,'fleet-hello-world',jsonb_build_object('sessionId','list-'||(i%1600),'payload',repeat(md5(i::text),32)) FROM generate_series(1,9500) i;
    INSERT INTO document(kind,id,project_id,data) VALUES ('event','list-last','fleet-hello-world','{"sessionId":"list-0"}');
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
});
