// Outside tests of the browser request checks the Go port had dropped (packages/coordinator/app.ts onRequest, people.ts):
// the Origin check behind Access proxy auth and the people file re-read. Each test starts its own coordinator
// (tests/harness.ts) with its own configuration.
//   FLEET_TRIAL_TESTS=tests/go/origin.test.ts node tests/trial/run.ts
// Each test names its requirement and the risk it guards.
import {test} from 'node:test';
import assert from 'node:assert/strict';
import {spawnSync} from 'node:child_process';
import {randomUUID} from 'node:crypto';
import {mkdtempSync, readFileSync, utimesSync, writeFileSync} from 'node:fs';
import {tmpdir} from 'node:os';
import {join} from 'node:path';
import {root, startCoordinator, type Coordinator} from '../harness.ts';

const OWNER = 'owner@trial.test', ORIGIN = 'https://fleet.trial.test';
async function call(c: Coordinator, method: string, path: string, o: {origin?: string, person?: string | null, bearer?: string, body?: unknown} = {}) {
  const headers: Record<string, string> = {authorization: 'Bearer ' + (o.bearer ?? c.secret)};
  if (o.person !== null) headers['x-fleet-person'] = o.person ?? OWNER;
  if (o.origin) headers.origin = o.origin;
  if (method !== 'GET') headers['content-type'] = 'application/json';
  const r = await fetch(c.base + path, {method, headers, body: method === 'GET' ? undefined : JSON.stringify(o.body ?? {})});
  const text = await r.text(); let body: any = text; try { body = JSON.parse(text); } catch {}
  return {status: r.status, body};
}

test('O1 behind Access proxy auth a browser write must come from the configured origin; reads and host requests are unaffected (requirement: fleet-operator-app proxy mode; risk: a cross-site page posts to the API with the owner\'s Access cookie)', async () => {
  const c = await startCoordinator({name: 'origin', env: {FLEET_BROWSER_PROXY_AUTH: 'true', FLEET_BROWSER_ORIGIN: ORIGIN}});
  try {
    const none = await call(c, 'POST', '/api/ops/questions_list');
    assert.deepEqual([none.status, none.body.message], [403, 'Origin required']);
    const cross = await call(c, 'POST', '/api/ops/questions_list', {origin: 'https://evil.test'});
    assert.deepEqual([cross.status, cross.body.message], [403, 'Origin required']);
    assert.equal((await call(c, 'POST', '/api/ops/questions_list', {origin: ORIGIN})).status, 200);
    assert.equal((await call(c, 'POST', '/api/sessions', {origin: 'https://evil.test'})).status, 403, 'the trial route table is checked too');
    // Reads need no Origin; a read naming another origin is refused.
    assert.equal((await call(c, 'GET', '/api/profiles')).status, 200);
    const read = await call(c, 'GET', '/api/profiles', {origin: 'https://evil.test'});
    assert.deepEqual([read.status, read.body.message], [403, 'Origin rejected']);
    // A host carries its own secret and no person: no Origin rule applies.
    const secret = (await call(c, 'POST', '/api/ops/hosts_register', {origin: ORIGIN, body: {id: 'h-' + randomUUID().slice(0, 8)}})).body.secret;
    const poll = await call(c, 'POST', '/api/hosts/poll', {bearer: secret, person: null, origin: 'https://evil.test', body: {platform: 'linux', capabilities: [], stores: [], problems: []}});
    assert.equal(poll.status, 200, JSON.stringify(poll.body));
  } finally { await c.stop(); }
});

test('O2 without a configured origin a browser request naming another origin is refused, its own origin accepted (requirement: app.ts Origin rejected; risk: cross-site writes on a local coordinator)', async () => {
  const c = await startCoordinator({name: 'origin2'});
  try {
    const cross = await call(c, 'POST', '/api/ops/questions_list', {origin: 'https://evil.test'});
    assert.deepEqual([cross.status, cross.body.message], [403, 'Origin rejected']);
    assert.equal((await call(c, 'POST', '/api/ops/questions_list', {origin: c.base})).status, 200);
    assert.equal((await call(c, 'POST', '/api/ops/questions_list')).status, 200, 'a request without Origin (a program) is accepted');
  } finally { await c.stop(); }
});

test('O3 proxy auth without a configured origin fails startup (requirement: app.ts configuration check; risk: proxy mode running with no origin check)', () => {
  const r = spawnSync(process.env.FLEET_TRIAL_SERVER || 'go/bin/fleet-coordinator', {cwd: root, shell: true, encoding: 'utf8', timeout: 10_000,
    env: {...process.env, FLEET_BROWSER_PROXY_AUTH: 'true', FLEET_BROWSER_ORIGIN: '', FLEET_PEOPLE_FILE: '/nonexistent', FLEET_DATABASE_URL: 'postgresql://nobody@/none'}});
  assert.notEqual(r.status, 0);
  assert.match(r.stderr, /Proxy browser auth requires configured browser origin/);
});

test('P1 people.json is re-read when it changes; a broken file keeps the last valid list and asks the owner once (requirement: people.ts; risk: a people edit needs a restart, or a typo locks everyone out)', async () => {
  const file = join(mkdtempSync(join(tmpdir(), 'fleet-people-')), 'people.json');
  const list = JSON.parse(readFileSync(join(root, 'tests/trial/people.json'), 'utf8'));
  writeFileSync(file, JSON.stringify(list));
  const c = await startCoordinator({name: 'people', env: {FLEET_PEOPLE_FILE: file}});
  const NEW = 'new@trial.test';
  const touch = (text: string, t: number) => { writeFileSync(file, text); utimesSync(file, t, t); };
  const asked = async () => (await call(c, 'POST', '/api/ops/questions_list', {body: {state: 'open'}})).body.questions.filter((x: any) => /people\.json is not valid/.test(x.text)).length;
  try {
    assert.equal((await call(c, 'POST', '/api/ops/questions_list', {person: NEW})).status, 403);
    touch(JSON.stringify([...list, {email: NEW, roles: ['operator']}]), Date.now() / 1000 + 10);
    assert.equal((await call(c, 'POST', '/api/ops/questions_list', {person: NEW})).status, 200, 'an added person works without a restart');
    touch('[{"email": broken', Date.now() / 1000 + 20);
    assert.equal((await call(c, 'POST', '/api/ops/questions_list', {person: NEW})).status, 200, 'a broken file keeps the last valid list');
    assert.equal(await asked(), 1);
    touch('[]', Date.now() / 1000 + 30);
    assert.equal((await call(c, 'POST', '/api/ops/questions_list', {person: NEW})).status, 200);
    assert.equal(await asked(), 1, 'one open question, not one per broken edit');
  } finally { await c.stop(); }
});

test('P2 a people.json entry as malformed as the old parsePeople refused (e-mail, roles, owner type, grants shape) is left out and named once; everyone else keeps working (requirement: fleet-people-access people file; risk: a loose entry grants a role, or one bad row locks out site-agents)', async () => {
  const file = join(mkdtempSync(join(tmpdir(), 'fleet-people-')), 'people.json');
  const list = JSON.parse(readFileSync(join(root, 'tests/trial/people.json'), 'utf8'));
  const NEW = 'new@trial.test';
  const bad = [{email: 'not-an-email', roles: ['operator']}, {email: 'r@trial.test', roles: ['admin']}, {email: 'o@trial.test', roles: ['operator'], owner: 'yes'},
    {email: 'g@trial.test', roles: ['operator'], grants: [{kind: 'project'}]}, {email: 'k@trial.test', roles: ['operator'], extra: 1}];
  const AGENTS = {email: 'site-agents', name: 'Site agents', roles: ['operator', 'architect'], accessCommonName: 'agents-client', grants: [{kind: '*', id: '*'}]};
  writeFileSync(file, JSON.stringify([...list, {email: NEW, roles: ['operator']}, AGENTS, ...bad]));
  const c = await startCoordinator({name: 'people-rows', env: {FLEET_PEOPLE_FILE: file}});
  try {
    assert.equal((await call(c, 'POST', '/api/ops/questions_list', {person: NEW})).status, 200, 'a good entry next to bad ones works');
    assert.equal((await call(c, 'POST', '/api/ops/questions_list', {person: 'agents-client'})).status, 200, 'site-agents works next to bad rows');
    for (const p of ['not-an-email', 'r@trial.test', 'o@trial.test', 'g@trial.test', 'k@trial.test'])
      assert.equal((await call(c, 'POST', '/api/ops/questions_list', {person: p})).status, 403, p);
    const qs = (await call(c, 'POST', '/api/ops/questions_list', {body: {state: 'open'}})).body.questions.filter((x: any) => /left out/.test(x.text));
    assert.equal(qs.length, 1); assert.equal(qs[0].text.match(/entry \d+:/g).length, bad.length, qs[0].text);
    const set = await call(c, 'POST', '/api/ops/people_set', {body: {people: [...list, bad[1]]}});
    assert.equal(set.status, 400, 'people_set refuses any malformed entry');
  } finally { await c.stop(); }
});

test('G1 the person gate covers every route and operation, not only those registered through it (requirement: fleet-people-access §2 architect role and project grants; risk: a plain operator reads subscriptions or transport records, or names an ungranted project)', async () => {
  const c = await startCoordinator({name: 'gate'});
  const OUT = 'outsider@trial.test', MEMBER = 'member@trial.test';
  try {
    for (const [method, path] of [['GET', '/api/accounts'], ['POST', '/api/ops/transport_inspect'], ['POST', '/api/ops/artifact_rule_preview'], ['POST', '/api/recovery-tools']]) {
      const r = await call(c, method, path, {person: OUT});
      assert.deepEqual([r.status, r.body.message], [403, 'Architect role required'], method + ' ' + path);
    }
    const r = await call(c, 'POST', '/api/ops/budgets_list', {person: MEMBER, body: {projectId: 'not-granted'}});
    assert.deepEqual([r.status, r.body.message], [404, 'Unknown project']);
  } finally { await c.stop(); }
});
