// Outside tests of connections, the pipeline runner and Lab (one file: they share the fake gateway). HTTP only, plus the two file seams the
// coordinator shares with fleet-gateway: <FLEET_GATEWAY_DIR>/snapshot.json and <FLEET_GATEWAY_DIR>/state/spool/*.jsonl,
// and the key files in FLEET_SECRET_DIR. This file plays the gateway on FLEET_GATEWAY_URL. Run:
//   d=$(mktemp -d); FLEET_SECRET_DIR=$d/secret FLEET_GATEWAY_DIR=$d/gateway FLEET_GATEWAY_URL=http://127.0.0.1:47811 \
//   FLEET_CONNECTION_TICK_MS=200 FLEET_PIPELINE_TICK_MS=200 FLEET_TRIAL_TESTS=tests/go/connections.test.ts \
//   [FLEET_TRIAL_SERVER=...] node tests/trial/run.ts
// Each test names its requirement (docs/designs/fleet-connections.md, fleet-lab.md) and the risk it guards.
import {after, before, test as nodeTest} from 'node:test';
import assert from 'node:assert/strict';
import {createServer, type Server} from 'node:http';
import {createHash, randomUUID} from 'node:crypto';
import {existsSync, mkdirSync, readFileSync, statSync, writeFileSync} from 'node:fs';
import {join} from 'node:path';

const BASE = process.env.FLEET_TRIAL_URL!, SECRET = process.env.FLEET_TRIAL_BROWSER_SECRET!;
const SECRET_DIR = process.env.FLEET_SECRET_DIR!, GATEWAY_DIR = process.env.FLEET_GATEWAY_DIR!, GATEWAY_URL = process.env.FLEET_GATEWAY_URL!;
if (!BASE || !SECRET || !SECRET_DIR || !GATEWAY_DIR || !GATEWAY_URL) throw Error('Run through tests/trial/run.ts with FLEET_SECRET_DIR, FLEET_GATEWAY_DIR and FLEET_GATEWAY_URL set');
const OWNER = 'owner@trial.test', OUTSIDER = 'outsider@trial.test';
const test = (name: string, fn: () => Promise<void>) => nodeTest(name, {timeout: 30_000}, fn);
const sha = (s: string) => createHash('sha256').update(s).digest('hex');

async function call(method: string, path: string, body?: unknown, person = OWNER) {
  const r = await fetch(BASE + path, {method, headers: {authorization: 'Bearer ' + SECRET, 'x-fleet-person': person, ...(body === undefined ? {} : {'content-type': 'application/json'})}, body: body === undefined ? undefined : JSON.stringify(body)});
  const text = await r.text(); let parsed: any = text; try { parsed = JSON.parse(text); } catch {}
  return {status: r.status, body: parsed};
}
const op = (name: string, body: unknown = {}, person = OWNER) => call('POST', '/api/ops/' + name, body, person);
const ok = (r: {status: number, body: any}) => { assert.equal(r.status, 200, JSON.stringify(r.body)); return r.body; };
const snapshot = () => JSON.parse(readFileSync(join(GATEWAY_DIR, 'snapshot.json'), 'utf8'));
async function until<T>(fn: () => Promise<T | undefined> | T | undefined, what: string): Promise<T> {
  for (let i = 0; i < 100; i++) { try { const v = await fn(); if (v) return v; } catch {} await new Promise(r => setTimeout(r, 100)); }
  throw Error('timed out: ' + what);
}

// The fake gateway: every call answers 502 provider-timeout (the charge is unknown), and records the bearer it saw.
const seen: string[] = [];
let gateway: Server;
before(async () => {
  gateway = createServer((req, res) => {
    seen.push(String(req.headers.authorization));
    res.writeHead(502, {'content-type': 'application/json', 'x-fleet-error': 'provider-timeout', 'x-fleet-call-id': randomUUID()}).end('{"error":"timeout"}');
  });
  await new Promise<void>(r => gateway.listen(Number(new URL(GATEWAY_URL).port), '127.0.0.1', r));
});
after(() => gateway.close());

const name = (p: string) => p + '-' + randomUUID().slice(0, 8);

test('C1 key value stays in the secret file (requirement: docs/designs/fleet-connections.md; risk: a key value in a row, a reply or the gateway snapshot)', async () => {
  const c = name('tavily');
  ok(await op('connections_create', {name: c, provider: 'tavily'}));
  const value = 'tvly-' + randomUUID();
  const k = ok(await op('connections_key_add', {connection: c, value}));
  assert.equal(k.fingerprint, sha(value).slice(0, 12));
  assert.ok(!JSON.stringify(k).includes(value));
  const file = join(SECRET_DIR, 'connections', c, k.fingerprint);
  assert.equal(readFileSync(file, 'utf8').trim(), value);
  assert.equal(statSync(file).mode & 0o777, 0o600);
  assert.ok(!JSON.stringify(ok(await op('connections_list'))).includes(value));
  const s = await until(() => snapshot().connections[c]?.keys?.length ? snapshot() : undefined, 'snapshot key');
  assert.deepEqual(s.connections[c].keys.map((x: any) => x.id), [k.fingerprint]);
  assert.ok(!readFileSync(join(GATEWAY_DIR, 'snapshot.json'), 'utf8').includes(value));
  // Retire: the value file goes, the row stays.
  ok(await op('connections_key_state', {keyId: k.fingerprint, state: 'retired'}));
  assert.equal(existsSync(file), false);
  await until(() => snapshot().connections[c].keys.length === 0 || undefined, 'retired key leaves the snapshot');
});

test('C5 connection writes reach the change feed (requirement: C14, the Keys screen follows api-connection and connection-key changes; risk: the screen never updates after an action)', async () => {
  const {cursor} = ok(await op('changes_since', {}));
  const c = name('tavily');
  ok(await op('connections_create', {name: c, provider: 'tavily'}));
  ok(await op('connections_key_add', {connection: c, value: 'tvly-' + randomUUID()}));
  const kinds = await until(async () => { const r = ok(await op('changes_since', {cursor})); const k = new Set(r.changes.map((x: any) => x.kind)); return k.has('api-connection') && k.has('connection-key') ? k : undefined; }, 'connection changes');
  assert.ok(kinds.has('api-connection') && kinds.has('connection-key'));
});

test('C2 only the owner handles key values (requirement: docs/designs/fleet-connections.md; risk: a non-owner reading or adding a key)', async () => {
  const c = name('tavily');
  ok(await op('connections_create', {name: c, provider: 'tavily', holder: OUTSIDER}));
  assert.equal((await op('connections_key_add', {connection: c, value: 'x'.repeat(20)}, OUTSIDER)).status, 403);
  assert.equal((await op('connections_token_create', {name: name('t'), connections: [c]}, OUTSIDER)).status, 403);
});

test('C3 owner token: hash only in the snapshot, revoke removes it (requirement: docs/designs/fleet-connections.md; risk: a revoked bearer still reaching providers)', async () => {
  const c = name('tavily');
  ok(await op('connections_create', {name: c, provider: 'tavily'}));
  ok(await op('connections_set_access', {connection: c, access: {projects: ['*']}}));
  const t = name('t');
  const m = ok(await op('connections_token_create', {name: t, connections: [c, 'no-such']}));
  assert.equal(typeof m.token, 'string'); assert.deepEqual(m.refused, ['no-such']);
  assert.equal(m.connections[0].tools.some((x: any) => x.name === 'web_search'), true);
  await until(() => snapshot().tokens.some((x: any) => x.sha256 === sha(m.token)) || undefined, 'token in snapshot');
  assert.ok(!readFileSync(join(GATEWAY_DIR, 'snapshot.json'), 'utf8').includes(m.token));
  ok(await op('connections_token_revoke', {name: t}));
  await until(() => !snapshot().tokens.some((x: any) => x.sha256 === sha(m.token)) || undefined, 'token leaves snapshot');
  assert.equal((await op('connections_token_create', {name: t, connections: [c]})).status, 200, 'a revoked name can be reused');
});

test('C4 spool line naming a disabled key disables its row and asks the owner once (requirement: docs/designs/fleet-connections.md; risk: a dead key kept in rotation)', async () => {
  const c = name('tavily');
  ok(await op('connections_create', {name: c, provider: 'tavily'}));
  ok(await op('connections_set_access', {connection: c, access: {projects: ['*']}}));
  const k = ok(await op('connections_key_add', {connection: c, value: 'tvly-' + randomUUID()}));
  const m = ok(await op('connections_token_create', {name: name('t'), connections: [c]}));
  const day = new Date().toISOString().slice(0, 10), dir = join(GATEWAY_DIR, 'state', 'spool');
  mkdirSync(dir, {recursive: true});
  const line = (callId: string) => JSON.stringify({callId, tokenSha256: sha(m.token), executionId: 'token:x', tool: 'web_search', connection: c, outcome: 'error', errorCode: 'http-401', status: 401, keyFingerprint: k.fingerprint, startedAt: Date.now(), ms: 5, disabled: {key: k.fingerprint, status: 401}});
  writeFileSync(join(dir, day + '.jsonl'), line(randomUUID()) + '\n' + line(randomUUID()) + '\n', {flag: 'a'});
  const key = await until(async () => ok(await op('connections_list')).connections.find((x: any) => x.name === c)?.keys.find((x: any) => x.state === 'disabled'), 'key disabled');
  assert.match(key.disabledReason, /401/);
  // A dispatch pass (a host poll) withdraws only its own stall questions, never the key question (live stand, 2026-11-10).
  const cause = `key:${k.fingerprint}:disabled`;
  await until(async () => ok(await op('questions_list', {state: 'open'})).questions.find((x: any) => x.cause === cause), 'key question');
  const secret = ok(await op('hosts_register', {id: name('h')})).secret;
  assert.equal((await fetch(BASE + '/api/hosts/poll', {method: 'POST', headers: {'content-type': 'application/json', authorization: 'Bearer ' + secret}, body: JSON.stringify({platform: 'linux', capabilities: [], stores: [], problems: []})})).status, 200);
  assert.ok(ok(await op('questions_list', {state: 'open'})).questions.some((x: any) => x.cause === cause), 'key question still open after dispatch');
  // A disabled key comes back only through the owner.
  ok(await op('connections_key_state', {keyId: k.fingerprint, state: 'active'}));
});

test('P1 pipeline run is idempotent by requestId and refuses a changed body (requirement: docs/designs/fleet-asset-pipelines.md; risk: a retried request running twice)', async () => {
  const requestId = randomUUID();
  const a = ok(await call('POST', '/api/pipelines/hello-search/runs', {requestId, example: {}}));
  const b = ok(await call('POST', '/api/pipelines/hello-search/runs', {requestId, example: {}}));
  assert.equal(a.id, b.id);
  assert.equal((await call('POST', '/api/pipelines/hello-search/runs', {requestId, example: {query: 'x'}})).status, 409);
  assert.equal((await call('POST', '/api/pipelines/hello-search/runs', {requestId: randomUUID(), bogus: 1})).status, 400);
  // A missing reference fails the step before any call: problem ref:<path>.
  const done = await until(async () => { const r = ok(await call('POST', '/api/pipelines/hello-search/runs', {requestId, example: {}})); return r.state !== 'running' ? r : undefined; }, 'run ends');
  assert.equal(done.state, 'failed'); assert.equal(done.steps[0].problem, 'ref:example.query');
});

test('P2 api step without a usable connection fails as infra, and an unanswered call is unknown, never replayed (requirement: docs/designs/fleet-asset-pipelines.md; risk: a charged call sent twice)', async () => {
  // No connection named tavily-less: refused at mint, nothing is sent.
  const seenBefore = seen.length;
  const none = await until(async () => { const r = ok(await call('POST', '/api/pipelines/hello-search/runs', {requestId: 'p2a', example: {query: 'q'}})); return r.state !== 'running' ? r : undefined; }, 'run ends');
  // tavily may exist from an import; then the call reaches the gateway and is unknown. Either way never "completed".
  if (seen.length === seenBefore) { assert.equal(none.state, 'failed'); assert.equal(none.steps[0].problem, 'infra:connection-unavailable'); }
  // With a usable connection the gateway sees one call with a run token; 502 provider-timeout leaves the outcome unknown.
  const c = await until(async () => {
    const list = ok(await op('connections_list'));
    if (!list.connections.some((x: any) => x.name === 'tavily')) ok(await op('connections_create', {name: 'tavily', provider: 'tavily'}));
    return ok(await op('connections_set_access', {connection: 'tavily', access: {projects: ['*']}}));
  }, 'tavily connection');
  assert.equal(c.name, 'tavily');
  const n = seen.length;
  const r = await until(async () => { const x = ok(await call('POST', '/api/pipelines/hello-search/runs', {requestId: 'p2b', example: {query: 'q'}})); return x.state !== 'running' ? x : undefined; }, 'run ends');
  assert.deepEqual([r.state, r.observation], ['failed', 'unconfirmed']); assert.equal(r.steps[0].problem, 'unknown:provider-timeout');
  assert.equal(seen.length - n, 1, 'one call, not replayed');
  assert.match(seen.at(-1)!, /^Bearer \S+$/);
  // The step's token is revoked after the call.
  await until(() => snapshot().tokens.every((t: any) => !String(t.executionId).startsWith(r.id)) || undefined, 'step token revoked');
});

test('P3 pipeline_run waits for the run of the project method and is idempotent (requirement: docs/designs/fleet-asset-pipelines.md; risk: the project search path left unported)', async () => {
  const requestId = randomUUID();
  const r = ok(await op('pipeline_run', {projectId: 'hello-world', requestId, query: 'fleet'}));
  assert.notEqual(r.state, 'running'); assert.equal(r.projectId, 'hello-world'); assert.equal(typeof r.selectedMethod, 'string');
  assert.equal(ok(await op('pipeline_run', {projectId: 'hello-world', requestId, query: 'fleet'})).id, r.id);
  assert.equal((await op('pipeline_run', {projectId: 'hello-world', requestId, query: 'other'})).status, 409);
  assert.equal(ok(await call('GET', '/api/pipeline-runs/' + r.id)).id, r.id);
});

test('L1 Lab registry and experiment validation (requirement: docs/designs/fleet-lab.md; risk: an experiment created over an unknown example or pipeline)', async () => {
  const reg = ok(await call('GET', '/api/lab/registry'));
  assert.ok(reg.useCases.length > 0 && reg.useCases.every((u: any) => typeof u.id === 'string'));
  assert.equal((await call('POST', '/api/lab/experiments', {kind: 'single', projectId: 'hello-world', pipelineId: 'hello-search', examples: ['nope'], variants: [{label: 'a'}]})).status, 400);
  assert.equal((await call('POST', '/api/lab/experiments', {kind: 'loop'})).status, 400);
  assert.equal((await call('POST', '/api/lab/experiments', {kind: 'single', projectId: 'hello-world', pipelineId: 'no-such-pipeline', examples: ['x'], variants: [{label: 'a'}]})).status >= 400, true);
  assert.equal((await call('GET', '/api/lab/experiments/no-such')).status, 404);
  assert.deepEqual(ok(await call('GET', '/api/lab/ratings?experimentId=no-such')).ratings, []);
});
