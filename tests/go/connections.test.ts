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
import {existsSync, mkdirSync, readFileSync, rmSync, statSync, writeFileSync} from 'node:fs';
import {join} from 'node:path';
import {fakeS3} from '../s3-fake.ts';
import {examplesFromExport} from '../../scripts/lab-export.ts';

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
    if (req.url === '/v1/call/typesafe/system_one') {
      // Plays TypeSafe's reply (shape seen live: answers.<id>.noul, model, usage) and spools the call line as fleet-gateway does.
      const callId = randomUUID(), token = String(req.headers.authorization).replace(/^Bearer /, '');
      const dir = join(GATEWAY_DIR, 'state', 'spool'); mkdirSync(dir, {recursive: true});
      writeFileSync(join(dir, new Date().toISOString().slice(0, 10) + '.jsonl'), JSON.stringify({callId, tokenSha256: sha(token), executionId: 'gateway-side', connection: 'typesafe', tool: 'system_one',
        status: 200, outcome: 'ok', keyFingerprint: 'aaaaaaaaaaaa', usage: {unit: 'input_tokens', amount: 286}, startedAt: Date.now(), ms: 5}) + '\n', {flag: 'a'});
      res.writeHead(200, {'content-type': 'application/json', 'x-fleet-call-id': callId, 'x-fleet-outcome': 'ok'})
        .end(JSON.stringify({model: 'jev-1.14.0', answers: {q: {type: 'noul', noul: 0.93}}, usage: {input_tokens: 286, output_tokens: 20}}));
      return;
    }
    res.writeHead(502, {'content-type': 'application/json', 'x-fleet-error': 'provider-timeout', 'x-fleet-call-id': randomUUID()}).end('{"error":"timeout"}');
  });
  await new Promise<void>(r => gateway.listen(Number(new URL(GATEWAY_URL).port), '127.0.0.1', r));
});
after(() => gateway.close());

const name = (p: string) => p + '-' + randomUUID().slice(0, 8);
// One fake S3 for the file: artifacts written in one test are read in later ones.
const s3 = fakeS3();
after(async () => (await s3).close());

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

test('C5 connection writes reach the change feed of the owner and the holder (requirement: C14, the Keys screen follows api-connection and connection-key changes; risk: the screen never updates after an action)', async () => {
  const kindsAfter = async (cursor: number, person: string) => { // every page after cursor
    const k = new Set<string>();
    for (let c = cursor; ;) { const r = ok(await op('changes_since', {cursor: c, limit: 1000}, person)); r.changes.forEach((x: any) => k.add(x.kind)); if (r.changes.length < 1000) return k; c = r.cursor; }
  };
  const start = ok(await op('changes_since', {})).cursor, startOutsider = ok(await op('changes_since', {}, OUTSIDER)).cursor;
  const c = name('tavily');
  ok(await op('connections_create', {name: c, provider: 'tavily', holder: OUTSIDER}));
  ok(await op('connections_key_add', {connection: c, value: 'tvly-' + randomUUID()}));
  await until(async () => { const k = await kindsAfter(start, OWNER); return k.has('api-connection') && k.has('connection-key'); }, 'owner sees connection changes');
  await until(async () => (await kindsAfter(startOutsider, OUTSIDER)).has('api-connection'), 'holder sees the change of their connection');
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

test('C6 a spool line that cannot be imported keeps its file, and lines after a very long line are still imported (requirement: docs/designs/fleet-connections.md spool ingest; risk: retained accounting silently lost when a past day is retired)', async () => {
  const c = name('tavily');
  ok(await op('connections_create', {name: c, provider: 'tavily'}));
  ok(await op('connections_set_access', {connection: c, access: {projects: ['*']}}));
  const k = ok(await op('connections_key_add', {connection: c, value: 'tvly-' + randomUUID()}));
  const m = ok(await op('connections_token_create', {name: name('t'), connections: [c]}));
  const dir = join(GATEWAY_DIR, 'state', 'spool'), file = join(dir, (1900 + Math.floor(Math.random() * 90)) + '-01-01.jsonl');
  mkdirSync(dir, {recursive: true});
  const line = JSON.stringify({callId: randomUUID(), tokenSha256: sha(m.token), executionId: 'token:x', tool: 'web_search', connection: c, outcome: 'error', errorCode: 'http-401', status: 401, keyFingerprint: k.fingerprint, startedAt: Date.now(), ms: 5, disabled: {key: k.fingerprint, status: 401}});
  // A 17 MiB line (past the former 16 MiB scanner cap) before a valid line of a past day.
  writeFileSync(join(file), 'x'.repeat(17 << 20) + '\n' + line + '\n');
  await until(async () => ok(await op('connections_list')).connections.find((x: any) => x.name === c)?.keys.find((x: any) => x.state === 'disabled'), 'line after the long one imported');
  await new Promise(r => setTimeout(r, 600)); // more ingest passes
  assert.ok(existsSync(file), 'a past-day file with an unimported line was deleted');
  rmSync(file);
});

test('P1 pipeline run is idempotent by requestId and refuses a changed body (requirement: docs/designs/fleet-asset-pipelines.md; risk: a retried request running twice)', async () => {
  const requestId = randomUUID();
  const a = ok(await call('POST', '/api/pipelines/hello-search/runs', {requestId, example: {}}));
  const b = ok(await call('POST', '/api/pipelines/hello-search/runs', {requestId, example: {}}));
  assert.equal(a.id, b.id);
  // A run is read only in its own project's scope (risk: pipeline_get without projectId returned any run).
  assert.equal((await op('pipeline_get', {runId: a.id}, OUTSIDER)).status, 404);
  assert.equal(ok(await op('pipeline_get', {runId: a.id})).id, a.id);
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

test('A1 analytics stage 1: an api step on the typed-decision connection gives the trial output {answer, p, answered_by, tokens} and a tool_run with cost and basis (requirement: docs/designs/fleet-lab.md analytics staging 1; risk: a provider call recorded without its cost basis, or a typed answer the checks cannot read)', async () => {
  // The step's output.json is an artifact: a store on the default location host.
  const {port} = await s3;
  const {secret} = ok(await op('hosts_register', {id: 'trial-host'}));
  const stores = await fetch(BASE + '/api/hosts/stores', {method: 'POST', headers: {authorization: 'Bearer ' + secret, 'content-type': 'application/json'},
    body: JSON.stringify({store: {id: 'trial-store', bucket: 'fleet', endpoint: `http://127.0.0.1:${port}`, accessKeyId: 'k', secretAccessKey: 's'}})});
  assert.equal(stores.status, 200);
  await until(async () => {
    const list = ok(await op('connections_list'));
    if (!list.connections.some((x: any) => x.name === 'typesafe')) ok(await op('connections_create', {name: 'typesafe', provider: 'typesafe'}));
    return ok(await op('connections_set_access', {connection: 'typesafe', access: {projects: ['*']}}));
  }, 'typesafe connection');
  const example = JSON.parse(readFileSync('config/lab/examples/fleet-typed-decision-smoke.json', 'utf8'));
  const requestId = randomUUID();
  const r = await until(async () => { const x = ok(await call('POST', '/api/pipelines/typed-decision/runs', {requestId, example: {prompt: example.prompt}})); return x.state !== 'running' && x.state !== 'preparing' ? x : undefined; }, 'run ends');
  assert.equal(r.state, 'succeeded', JSON.stringify(r.steps));
  assert.deepEqual(r.steps[0].output, {answer: {type: 'noul', noul: 0.93}, p: 0.93, answered_by: 'jev-1.14.0', tokens: {input_tokens: 286, output_tokens: 20}});
  assert.deepEqual(r.steps[0].calls.map((c: any) => [c.tool, c.basis, c.cost?.unit, c.cost?.amount]), [['system_one', 'measured', 'input_tokens', 286]]);
});

test('S4 binary example files: import stores each file from the seed directory by its sha256 and refuses a mismatch (requirement: docs/designs/fleet-asset-pipelines.md §4, Binary example files; risk: an example whose image or GLB differs from the seed, or a run staging bytes nobody checked)', async () => {
  const example = JSON.parse(readFileSync('config/lab/examples/fleet-uc2-crate.json', 'utf8'));
  example.version = 3000000 + Math.floor(Math.random() * 1e6);
  const {examples: [imported]} = ok(await call('POST', '/api/lab/examples', {examples: [example], approved: true}));
  assert.ok(imported.artifactId);
  const tampered = {...example, version: example.version + 1, inputs: [{...example.inputs[0], sha256: '0'.repeat(64)}]};
  const refused = await call('POST', '/api/lab/examples', {examples: [tampered], approved: true});
  assert.equal(refused.status, 400); assert.match(refused.body.message ?? refused.body.error, /does not match its sha256/);
});

test('A2 analytics stage 2: the experiment record is required at launch, amended visibly, and an automatic or owner stop leaves the decision pending (requirement: docs/designs/fleet-lab.md, Experiment record; risk: an experiment runs without a stated goal and hypothesis, a changed hypothesis hides, or a stop reads as decided)', async () => {
  const example = JSON.parse(readFileSync('config/lab/examples/fleet-typed-decision-smoke.json', 'utf8'));
  example.version = 1000 + Math.floor(Math.random() * 1e6);
  const {examples: [imported]} = ok(await call('POST', '/api/lab/examples', {examples: [example], approved: true}));
  const create = (record: object) => call('POST', '/api/lab/experiments', {kind: 'single', projectId: 'hello-world', pipelineId: 'typed-decision', examples: [imported.artifactId], variants: [{label: 'jev'}],
    evaluators: [{kind: 'script', ref: 'example.checks', form: 'pass_fail'}], ...record});
  const bare = ok(await create({}));
  const refused = await call('POST', `/api/lab/experiments/${bare.id}/launch`, {});
  assert.equal(refused.status, 400); assert.match(refused.body.message ?? refused.body.error, /goal and hypothesis/);
  const e = ok(await create({goal: 'Decide yes/no questions cheaply', hypothesis: 'Jev answers the smoke question with p >= 0.5', decisionRule: 'adopt when the check passes'}));
  ok(await call('POST', `/api/lab/experiments/${e.id}/launch`, {}));
  const amended = ok(await op('lab_experiment_note', {experimentId: e.id, kind: 'amendment', field: 'hypothesis', value: 'Jev answers with p >= 0.8', text: 'tighter after the first trial'}));
  assert.equal(amended.kind, 'amendment');
  assert.equal((await op('lab_experiment_note', {experimentId: e.id, kind: 'amendment', field: 'variants', value: 'x', text: 'no'})).status, 400);
  ok(await call('POST', `/api/lab/experiments/${e.id}/stop`, {}));
  const got = ok(await call('GET', `/api/lab/experiments/${e.id}`));
  assert.deepEqual([got.state, got.decision, got.hypothesis, got.notes.map((n: any) => [n.kind, n.field, n.old, n.new])],
    ['stopped', 'pending', 'Jev answers with p >= 0.8', [['amendment', 'hypothesis', 'Jev answers the smoke question with p >= 0.5', 'Jev answers with p >= 0.8']]]);
  ok(await op('lab_experiment_decide', {experimentId: e.id, decision: 'inconclusive'}));
  assert.equal((await op('lab_experiment_decide', {experimentId: e.id, decision: 'adopt jev'})).status, 400, 'an override needs a reason');
  const decided = ok(await op('lab_experiment_decide', {experimentId: e.id, decision: 'adopt jev', reason: 'check passed on rerun'}));
  assert.deepEqual([decided.decision, decided.decidedBy, decided.notes.at(-1).field], ['adopt jev', OWNER, 'decision']);
});

test('A3 analytics stage 3: a trial cost is usage times the price entry in force, with basis and price id, and unavailable without one (requirement: docs/designs/fleet-analytics.md, Price table; risk: a cost shown as exact or as 0 when it is estimated or unknown)', async () => {
  const example = JSON.parse(readFileSync('config/lab/examples/fleet-typed-decision-smoke.json', 'utf8'));
  example.version = 2000000 + Math.floor(Math.random() * 1e6);
  const {examples: [imported]} = ok(await call('POST', '/api/lab/examples', {examples: [example], approved: true}));
  const e = ok(await call('POST', '/api/lab/experiments', {kind: 'single', projectId: 'hello-world', pipelineId: 'typed-decision', examples: [imported.artifactId], variants: [{label: 'jev'}],
    goal: 'Price the smoke', hypothesis: 'One call costs well under a cent'}));
  ok(await call('POST', `/api/lab/experiments/${e.id}/launch`, {}));
  const trial = async () => (await until(async () => { const x = ok(await call('GET', `/api/lab/experiments/${e.id}`)); return x.trials?.find((t: any) => t.state === 'succeeded' && t.cost.length) ?? undefined; }, 'a priced trial'));
  // A subject no entry prices yet: the cost is unavailable, never 0.
  const subject = 'typesafe/system_one';
  if (!ok(await op('prices_list', {subject})).prices.length) assert.deepEqual((await trial()).cost.map((c: any) => [c.value, c.basis]), [[null, 'unavailable']]);
  assert.equal((await op('price_add', {subject, unit: 'output_tokens', price: 1, currency: 'USD', source: 'owner'})).status, 400, 'the tool is metered in input_tokens');
  const price = ok(await op('price_add', {subject, unit: 'input_tokens', price: 0.000002, currency: 'USD', source: 'owner', effectiveFrom: 0}));
  const [cost] = (await trial()).cost;
  assert.deepEqual([cost.basis, cost.currency, cost.prices.map((p: any) => p.id).includes(price.id)], ['estimated', 'USD', true]);
  assert.ok(Math.abs(cost.value - 286 * 0.000002) < 1e-12, String(cost.value));
});

test('A4 analytics stage 4: a typed evaluator scores each trial through its variant bands against the label, per split and language (requirement: docs/designs/fleet-lab.md, Statistics, typed answers; risk: a confident-looking answer counted as correct without a label, a middle-band answer counted as confident, or tune and held_out mixed)', async () => {
  // A small synthetic labelled set in the export format (version 1); the fake gateway answers p 0.93 for every row.
  const rows = [{rowId: 'el1', input: 'Name: Λεμεσός. Is it Greek?', label: 'yes', tag: 'el', split: 'held_out'},
    {rowId: 'ru1', input: 'Name: Лимасол. Is it Russian?', label: 'yes', tag: 'ru', split: 'held_out'},
    {rowId: 'en1', input: 'Name: Limassol. Is it Greek?', label: 'no', tag: 'en', split: 'tune'}];
  const version = 3000000 + Math.floor(Math.random() * 1e6);
  const examples: any[] = examplesFromExport({schema: 1, project: 'cs', stepId: 'lang', exportedAt: '2026-11-11T00:00:00Z', rows}, 'cs:translate_taxonomy-language', version);
  // One example without a label: its trials are unscored with the reason, never counted.
  examples.push({...examples[0], id: 'cs-lang-nolabel', references: examples[0].references.filter((f: any) => f.path !== 'expected.json'), checks: []});
  const {examples: imported} = ok(await call('POST', '/api/lab/examples', {examples, approved: true}));
  const create = (variants: object[]) => call('POST', '/api/lab/experiments', {kind: 'ab', projectId: 'hello-world', pipelineId: 'typed-decision', examples: imported.map((x: any) => x.artifactId),
    variants, evaluators: [{kind: 'typed', ref: 'labels', form: 'typed'}], primaryEvaluator: 'typed:labels:typed', goal: 'Bands for the language step', hypothesis: 'Strict bands escalate more'});
  const inverted = await create([{label: 'a', overrides: {bands: {yes: 0.2, no: 0.8}}}, {label: 'b'}]);
  assert.equal(inverted.status, 400); assert.match(inverted.body.message, /0 <= no <= yes <= 1/);
  assert.equal((await call('POST', '/api/pipelines/typed-decision/runs', {requestId: randomUUID(), example: {prompt: 'x'}, overrides: {bands: {yes: 0.9, no: 0.1}}})).status, 400, 'a direct run takes no bands');
  const e = ok(await create([{label: 'wide', overrides: {bands: {yes: 0.8, no: 0.2}}}, {label: 'strict', overrides: {bands: {yes: 0.95, no: 0.05}}}]));
  ok(await call('POST', `/api/lab/experiments/${e.id}/launch`, {}));
  const got = await until(async () => { const x = ok(await call('GET', `/api/lab/experiments/${e.id}`)); return x.scores?.filter((s: any) => s.form === 'typed').length === 8 ? x : undefined; }, 'typed scores');
  const unscored = got.scores.filter((s: any) => s.value.answer === 'unscored');
  assert.deepEqual(unscored.map((s: any) => s.value.reason), Array(2).fill('the example has no yes/no label in references/expected.json'));
  const stats = Object.fromEntries(got.typed.map((v: any) => [`${v.variant}/${v.split}`, Object.fromEntries(v.stats.map((s: any) => [s.aggregate ? 'all' : s.tag, [s.n, s.coverage, s.selectiveAccuracy]]))]));
  // wide: p 0.93 is a confident yes: right on el and ru (held_out), wrong on en (tune). strict: everything abstains.
  assert.deepEqual(stats, {'wide/tune': {en: [1, 1, 0], all: [1, 1, 0]}, 'wide/held_out': {el: [1, 1, 1], ru: [1, 1, 1], all: [2, 1, 1]},
    'strict/tune': {en: [1, 0, null], all: [1, 0, null]}, 'strict/held_out': {el: [1, 0, null], ru: [1, 0, null], all: [2, 0, null]}});
  const [c] = got.comparison;
  assert.deepEqual([c.variant, c.baseline, c.split, c.result.n, c.result.mean], ['strict', 'wide', 'held_out', 2, -1]);
});
