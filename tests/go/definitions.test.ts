// Outside tests of definitions in the store (docs/designs/fleet-definition-editors.md). HTTP only, against a coordinator on a
// fresh database. Run: FLEET_TRIAL_TESTS=tests/go/definitions.test.ts node tests/trial/run.ts
// Each test names its requirement and the risk it guards.
import {test} from 'node:test';
import assert from 'node:assert/strict';
import {readFileSync} from 'node:fs';

const BASE = process.env.FLEET_TRIAL_URL!, SECRET = process.env.FLEET_TRIAL_BROWSER_SECRET!;
if (!BASE || !SECRET) throw Error('Run through tests/trial/run.ts');
const OWNER = 'owner@trial.test', MEMBER = 'member@trial.test';

async function op(name: string, body: unknown, person = OWNER) {
  const r = await fetch(BASE + '/api/ops/' + name, {method: 'POST', headers: {authorization: 'Bearer ' + SECRET, 'x-fleet-person': person, 'content-type': 'application/json'}, body: JSON.stringify(body)});
  return {status: r.status, body: await r.json()};
}
const ok = (r: {status: number, body: any}) => { assert.equal(r.status, 200, JSON.stringify(r.body)); return r.body; };

test('D1 a fresh store imports every shipped definition as revision 1 from the repository (requirement: Where definitions live, Reconcile rule 1; risk: a fresh database starts without the seed, or a head differs from the file)', async () => {
  const agents = ok(await op('definition_list', {kind: 'agent'})).items;
  const pm = agents.find((x: any) => x.id === 'pm');
  assert.ok(pm, JSON.stringify(agents.map((x: any) => x.id)));
  assert.deepEqual([pm.revision, pm.state, pm.path], [1, 'synced', 'config/agents/pm.json']);
  assert.ok(pm.agent.model, 'agent facts');
  const g = ok(await op('definition_get', {kind: 'agent', id: 'pm'}));
  assert.equal(g.text, readFileSync('config/agents/pm.json', 'utf8'), 'the stored text is the shipped canonical file');
  assert.deepEqual([g.meta.source, g.meta.actor.kind, g.head], ['repo', 'system', 1]);
  assert.equal(g.content.agent.id, 'pm');
  const pipes = ok(await op('definition_list', {kind: 'pipeline'})).items;
  assert.ok(pipes.some((x: any) => x.id === 'hello-search'));
  assert.equal(ok(await op('definition_get', {kind: 'catalogue', id: 'catalogue'})).content.catalogue.capability.length > 0, true);
});

test('D2 definition reads are the architect\'s; unknown ones are 404 (requirement: Rights; risk: an operator reads or a typo reads another definition)', async () => {
  assert.equal((await op('definition_list', {kind: 'agent'}, MEMBER)).status, 403);
  assert.equal((await op('definition_get', {kind: 'agent', id: 'no-such'})).status, 404);
  assert.equal((await op('definition_get', {kind: 'agent', id: 'pm', revision: 9})).status, 404);
  assert.equal((await op('definition_list', {kind: 'skill'})).status, 400);
});
