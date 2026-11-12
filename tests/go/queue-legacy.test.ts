// Executions queued before a coordinator change still start on the newest coordinator; one bad queued execution never
// blocks a host's poll. Own coordinator, seeded through sql().
import {test} from 'node:test';
import assert from 'node:assert/strict';
import {randomUUID} from 'node:crypto';
import {startCoordinator, lit} from '../harness.ts';

test('Q1 a project execution queued before project snapshots starts with the project as it is now, and the poll stays 200 (requirement: host poll serves every host, 2026-11-12 PDS18 outage; risk: one legacy queued execution fails every poll of the host it ranks first)', {timeout: 30_000}, async () => {
  const c = await startCoordinator({name: 'queue-legacy'});
  const owner = {authorization: 'Bearer ' + c.secret, 'x-fleet-person': 'owner@trial.test', 'content-type': 'application/json'};
  const post = async (path: string, body: object, headers: Record<string, string>) => {
    const r = await fetch(c.base + path, {method: 'POST', headers, body: JSON.stringify(body)});
    return {status: r.status, body: await r.json()};
  };
  const ok = (r: {status: number, body: any}) => { assert.equal(r.status, 200, JSON.stringify(r.body)); return r.body; };
  try {
    const secret = ok(await post('/api/ops/hosts_register', {id: 'trial-host'}, owner)).secret;
    const poll = () => post('/api/hosts/poll', {platform: 'linux', capabilities: [], stores: [], problems: []}, {authorization: 'Bearer ' + secret, 'content-type': 'application/json'});
    ok(await poll());
    const s = ok(await post('/api/ops/session_start', {requestId: randomUUID(), text: 'hi', projectId: 'hello-world', profileId: 'pm', hostId: 'trial-host'}, owner));
    c.sql(`UPDATE execution SET data = data - 'projectSnapshot' WHERE id=${lit(s.executionId)};`);
    const start = ok(await poll()).commands.find((x: any) => x.executionId === s.executionId && x.kind === 'start');
    assert.ok(start, 'the legacy execution starts');
    assert.equal(start.payload.projectId, 'hello-world');
  } finally {
    await c.stop();
  }
});
