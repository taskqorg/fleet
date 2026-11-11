// Outside tests of system management (docs/designs/fleet-system-management.md): host remove and purge.
//   FLEET_TRIAL_TESTS=tests/go/system.test.ts node tests/trial/run.ts
import {test} from 'node:test';
import assert from 'node:assert/strict';
import {randomUUID} from 'node:crypto';
import {startCoordinator, type Coordinator} from '../harness.ts';

const OWNER = 'owner@trial.test', MEMBER = 'member@trial.test';
async function call(c: Coordinator, path: string, body: unknown, o: {person?: string | null, bearer?: string} = {}) {
  const headers: Record<string, string> = {authorization: 'Bearer ' + (o.bearer ?? c.secret), 'content-type': 'application/json'};
  if (o.person !== null) headers['x-fleet-person'] = o.person ?? OWNER;
  const r = await fetch(c.base + path, {method: 'POST', headers, body: JSON.stringify(body)});
  const text = await r.text(); let out: any = text; try { out = JSON.parse(text); } catch {}
  return {status: r.status, body: out};
}

test('M1 a removed host is a tombstone: its polls get 410, lists hide it, history keeps it until purge finds nothing referencing it (requirement: fleet-system-management.md Hosts, Archive delete purge; risk: a removed host keeps taking work, or its history loses the host)', async () => {
  const c = await startCoordinator({name: 'sys1'});
  try {
    const id = 'h-' + randomUUID().slice(0, 8);
    const secret = (await call(c, '/api/ops/hosts_register', {id})).body.secret;
    const poll = () => call(c, '/api/hosts/poll', {platform: 'linux', capabilities: [], stores: [], problems: []}, {bearer: secret, person: null});
    assert.equal((await poll()).status, 200);
    assert.ok([403, 404].includes((await call(c, '/api/ops/hosts_remove', {id, preview: false}, {person: MEMBER})).status), 'only the holder or the owner');
    const preview = await call(c, '/api/ops/hosts_remove', {id, preview: true});
    assert.deepEqual(preview.body, {id, runningExecutions: 0, openSessions: 0, unreplicatedArtifacts: 0, removed: false});
    assert.equal((await poll()).status, 200, 'a preview changes nothing');
    const done = await call(c, '/api/ops/hosts_remove', {id, preview: false});
    assert.equal(done.body.removed, true, JSON.stringify(done.body));
    const after = await poll();
    assert.deepEqual([after.status, after.body.message], [410, 'host-removed']);
    assert.equal((await call(c, '/api/ops/hosts_remove', {id, preview: false})).status, 409);
    const list = (await call(c, '/api/ops/hosts_list', {})).body.hosts;
    assert.ok(!list.some((h: any) => h.id === id), 'hidden by default');
    const all = (await call(c, '/api/ops/hosts_list', {includeRemoved: true})).body.hosts;
    assert.ok(all.find((h: any) => h.id === id)?.removedAt > 0);
    const p = await call(c, '/api/ops/system_purge', {preview: true});
    assert.ok(p.body.removed[0].ids.includes(id), JSON.stringify(p.body));
    assert.equal((await call(c, '/api/ops/system_purge', {preview: false}, {person: MEMBER})).status, 403);
    const purged = await call(c, '/api/ops/system_purge', {preview: false});
    assert.equal(purged.body.purged, true);
    assert.ok(!(await call(c, '/api/ops/hosts_list', {includeRemoved: true})).body.hosts.some((h: any) => h.id === id), 'purged');
    assert.equal((await poll()).status, 401, 'after purge the secret is unknown');
  } finally { await c.stop(); }
});
