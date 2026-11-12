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
let trialHostSecret = '';
let typedReplyBarrier: {reached: () => void; release: Promise<void>} | undefined;
let assetJob: {connection: string; mode: 'exact'|'missing'|'ambiguous'|'wrong-tool'|'ungranted'; jobId: string; kind?: 'rigging'; args: any; calls: string[]} | undefined;
before(async () => {
  gateway = createServer(async (req, res) => {
    seen.push(String(req.headers.authorization));
    if (assetJob && req.url?.startsWith('/v1/call/' + assetJob.connection + '/')) {
      const fixture = assetJob, tool = req.url.split('/').at(-1)!;
      fixture.calls.push(tool);
      const reply = (body: unknown) => res.writeHead(200, {'content-type':'application/json','x-fleet-call-id':randomUUID(),'x-fleet-outcome':'ok'}).end(JSON.stringify(body));
      if (tool === (fixture.kind || 'image_to_image')) {
        let body = ''; req.on('data', chunk => { body += chunk; }); req.on('end', () => {
          assert.deepEqual(JSON.parse(body), fixture.args);
          res.writeHead(502, {'content-type':'application/json','x-fleet-call-id':randomUUID(),'x-fleet-outcome':'unknown','x-fleet-error':'provider-timeout'}).end('{}');
        });
      } else if (tool === (fixture.kind ? 'rigging_recent_tasks' : 'image_recent_tasks')) {
        const candidate = {...fixture.args, id:fixture.jobId, created_at:Math.floor(Date.now()/1000)};
        if (fixture.mode === 'missing') delete candidate[fixture.kind ? 'input_task_id' : 'reference_image_urls'];
        reply({result:fixture.mode === 'ambiguous' ? [candidate,{...candidate,id:randomUUID()}] : [candidate]});
      } else if (tool === 'rigging_job_status') reply({status:'SUCCEEDED',result:{rigged_character_glb_url:'https://invented.invalid/rig.glb'},consumed_credits:7});
      else if (tool === 'image_job_status') reply({status:'SUCCEEDED',image_urls:['https://invented.invalid/front.png'],consumed_credits:6});
      else res.writeHead(500).end('unexpected tool');
      return;
    }

    if (req.url === '/v1/call/typesafe/system_one') {
      const barrier = typedReplyBarrier;
      if (barrier) { barrier.reached(); await barrier.release; }
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
let artifactReadBarrier: {hash: string, reached: () => void, release: Promise<void>} | undefined;
const s3 = (async () => {
  const backend = await fakeS3();
  const proxy = createServer(async (req,res) => {
    const chunks: Buffer[]=[]; for await (const c of req) chunks.push(c as Buffer);
    const body=Buffer.concat(chunks);
    const response=await fetch(`http://127.0.0.1:${backend.port}${req.url}`, {method:req.method,headers:req.headers as Record<string,string>,body:['GET','HEAD'].includes(req.method!)?undefined:body});
    const bytes=Buffer.from(await response.arrayBuffer());
    const barrier=artifactReadBarrier;
    if (barrier && req.method==='GET' && req.url!.includes(barrier.hash)) { artifactReadBarrier=undefined; barrier.reached(); await barrier.release; }
    res.writeHead(response.status,Object.fromEntries(response.headers));res.end(bytes);
  });
  const port=await new Promise<number>(resolve=>proxy.listen(0,'127.0.0.1',()=>resolve((proxy.address() as any).port)));
  return {...backend,port,close:()=>{proxy.close();backend.close();}};
})();
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
  // A disabled key comes back only through the owner; its question then withdraws itself (live stand, 2026-11-12).
  ok(await op('connections_key_state', {keyId: k.fingerprint, state: 'active'}));
  assert.ok(!ok(await op('questions_list', {state: 'open'})).questions.some((x: any) => x.cause === cause), 'key question withdrawn after re-enable');
});

test('C4b questions_withdraw: owner only, withdraws open questions with one audit row each and skips the rest (requirement: needs-you shows only what still needs the owner; risk: stale questions bury a needed one)', async () => {
  const c = name('tavily');
  ok(await op('connections_create', {name: c, provider: 'tavily'}));
  ok(await op('connections_set_access', {connection: c, access: {projects: ['*']}}));
  const k = ok(await op('connections_key_add', {connection: c, value: 'tvly-' + randomUUID()}));
  const m = ok(await op('connections_token_create', {name: name('t'), connections: [c]}));
  const day = new Date().toISOString().slice(0, 10), dir = join(GATEWAY_DIR, 'state', 'spool');
  mkdirSync(dir, {recursive: true});
  writeFileSync(join(dir, day + '.jsonl'), JSON.stringify({callId: randomUUID(), tokenSha256: sha(m.token), executionId: 'token:x', tool: 'web_search', connection: c, outcome: 'error', errorCode: 'http-401', status: 401, keyFingerprint: k.fingerprint, startedAt: Date.now(), ms: 5, disabled: {key: k.fingerprint, status: 401}}) + '\n', {flag: 'a'});
  const cause = `key:${k.fingerprint}:disabled`;
  const qu = await until(async () => ok(await op('questions_list', {state: 'open'})).questions.find((x: any) => x.cause === cause), 'key question');
  assert.equal((await op('questions_withdraw', {ids: [qu.id], reason: 'stale'}, OUTSIDER)).status, 403);
  assert.equal((await op('questions_withdraw', {ids: [qu.id], reason: ' '})).status, 400);
  const missing = randomUUID();
  const r = ok(await op('questions_withdraw', {ids: [qu.id, missing], reason: 'test: no longer needs the owner'}));
  assert.deepEqual(r.withdrawn, [qu.id]);
  assert.deepEqual(r.skipped, [{id: missing, state: 'unknown'}]);
  assert.deepEqual(ok(await op('questions_withdraw', {ids: [qu.id], reason: 'again'})).skipped, [{id: qu.id, state: 'withdrawn'}]);
  assert.ok(!ok(await op('questions_list', {state: 'open'})).questions.some((x: any) => x.id === qu.id));
  const audit = ok(await op('setting_changes', {target: 'question:' + qu.id})).changes;
  assert.equal(audit.length, 1);
  assert.equal(audit[0].value, 'withdrawn'); assert.equal(audit[0].reason, 'test: no longer needs the owner');
  ok(await op('connections_key_state', {keyId: k.fingerprint, state: 'retired'}));
});

test('C7 a finished Meshy job counts its credits once however often it is polled (requirement: docs/designs/fleet-asset-pipelines.md §1, a csgo Meshy charge lands as connection_cost; risk: each poll of one job charged again)', async () => {
  const c = name('meshy');
  ok(await op('connections_create', {name: c, provider: 'meshy'}));
  ok(await op('connections_set_access', {connection: c, access: {projects: ['*']}}));
  ok(await op('connections_key_add', {connection: c, value: 'msy-' + randomUUID()}));
  const m = ok(await op('connections_token_create', {name: name('t'), connections: [c]}));
  ok(await op('budget_set', {scope: 'connection', scopeId: c, unit: 'credits', limit: 45, action: 'stop'}));
  const day = new Date().toISOString().slice(0, 10), dir = join(GATEWAY_DIR, 'state', 'spool');
  mkdirSync(dir, {recursive: true});
  const poll = (job: string) => JSON.stringify({callId: randomUUID(), tokenSha256: sha(m.token), executionId: 'token:x', tool: 'job_status', connection: c, outcome: 'ok', status: 200, startedAt: Date.now(), ms: 5, usage: {unit: 'credits', amount: 30}, usageKey: c + ':' + job});
  const blocked = () => (snapshot().connections[c]?.blocked ?? []).includes('connection');
  writeFileSync(join(dir, day + '.jsonl'), poll('job-a') + '\n' + poll('job-a') + '\n' + poll('job-a') + '\n', {flag: 'a'});
  await new Promise(r => setTimeout(r, 1500)); // ingest and budget ticks
  assert.equal(blocked(), false, 'three polls of one job counted more than 30 credits');
  writeFileSync(join(dir, day + '.jsonl'), poll('job-b') + '\n', {flag: 'a'});
  await until(() => blocked() || undefined, 'a second job brings the total to 60 over the 45 stop budget');
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
  trialHostSecret = secret;
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
  let reached = false, release!: () => void;
  const gate = new Promise<void>(resolve => { release = resolve; });
  // Keep the trial in flight until the owner stop commits, regardless of runner speed.
  typedReplyBarrier = {reached: () => { reached = true; }, release: gate};
  try {
    ok(await call('POST', `/api/lab/experiments/${e.id}/launch`, {}));
    const amended = ok(await op('lab_experiment_note', {experimentId: e.id, kind: 'amendment', field: 'hypothesis', value: 'Jev answers with p >= 0.8', text: 'tighter after the first trial'}));
    assert.equal(amended.kind, 'amendment');
    assert.equal((await op('lab_experiment_note', {experimentId: e.id, kind: 'amendment', field: 'variants', value: 'x', text: 'no'})).status, 400);
    await until(() => reached || undefined, 'A2 gateway reply held');
    ok(await call('POST', `/api/lab/experiments/${e.id}/stop`, {}));
  } finally { typedReplyBarrier = undefined; release(); }
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

// One domain fixture extends the counted gateway/S3 above. It speaks the real host protocol; no DB repairs or provider calls.
async function suffixFixture(projectId = 'cs', differentOuter = false) {
  const secret = trialHostSecret;
  assert.ok(secret, 'shared store host registered');
  const host = async (path: string, body: object, auth = secret) => {
    const r = await fetch(BASE + path, {method: 'POST', headers: {authorization: 'Bearer ' + auth, 'content-type': 'application/json'}, body: JSON.stringify(body)});
    return {status: r.status, body: await r.json()};
  };
  const pipelineId = name('suffix');
  const g = ok(await op('definition_get', {kind: 'pipeline', id: 'typed-decision'}));
  const content = structuredClone(g.content); content.pipeline.id = pipelineId;
  content.pipeline.steps.push({id: 'combine', kind: 'script', command: 'cat "$FLEET_INPUT_DIR/decide/output.json" > "$FLEET_OUTPUT_DIR/output.json"'});
  ok(await op('definition_create', {kind: 'pipeline', id: pipelineId, content, message: 'local suffix fixture'}));
  const example = JSON.parse(readFileSync('config/lab/examples/fleet-typed-decision-smoke.json', 'utf8'));
  example.version = 4000000 + Math.floor(Math.random() * 1e8);
  example.inputs = [{path: 'seed.txt', text: 'frozen local input\n'}];
  const {examples: [pin]} = ok(await op('lab_examples_add', {projectId, examples: [example], approved: true}));
  const exp = ok(await op('lab_experiment_create', {kind: 'single', projectId, pipelineId: differentOuter ? 'typed-decision' : pipelineId, examples: [pin.artifactId], variants: [{label: 'v', ...(differentOuter ? {pipelineId} : {})}],
    evaluators: [{kind: 'script', ref: 'example.checks', form: 'pass_fail'}], goal: 'Recover only local suffix', hypothesis: 'The confirmed result is reused without another call'}));
  ok(await op('lab_experiment_action', {experimentId: exp.id, action: 'launch'}));
  const view = () => op('lab_experiment_get', {experimentId: exp.id}).then(ok);
  const trial = await until(async () => { const v = await view(); return v.trials[0]?.state === 'running' ? v.trials[0] : undefined; }, 'source trial');
  const run = await until(async () => { const r = ok(await call('GET', `/api/pipeline-runs/${trial.id}`)); return r.steps[1]?.executionId ? r : undefined; }, 'suffix command');
  const poll = () => host('/api/hosts/poll', {platform: 'darwin', capabilities: [], stores: [], problems: []}).then(ok);
  const command = (await poll()).commands.find((c: any) => c.executionId === run.steps[1].executionId);
  assert.ok(command);
  const receipt = (cmd: any, status: string, extra = {}) => host('/api/hosts/receipts', {commandId: cmd.id, executionId: cmd.executionId, status, ...extra}).then(ok);
  await receipt(command, 'started');
  const proof = {id: command.id + '-1', seq: 1, type: 'process.reconciled', timestamp: Date.now(), data: {ownedProcesses: 1, resolved: true, launchWindowUnresolved: false, survived: false}};
  const prove = () => host('/api/hosts/events', {executionId: command.executionId, events: [proof]}).then(ok);
  const request = {experimentId: exp.id, cell: `${trial.cell.example}|${trial.cell.variant}|${trial.cell.repeat}`, action: 'retry_suffix', sourceRunId: trial.id, requestId: randomUUID()};
  // The view's cell key is authoritative; it is not the trial execution ID.
  const upload = async (cmd: any, text: string, outputName = 'output.json') => {
    const r = ok(await host('/api/hosts/artifacts/reserve', {executionId: cmd.executionId, name: outputName, mediaType: 'application/json', size: Buffer.byteLength(text), sha256: sha(text)}));
    if (r.state === 'stored') return r.artifactId;
    const {parts} = ok(await host('/api/hosts/artifacts/parts', {artifactId: r.artifactId, partNumbers: [1]}));
    const put = await fetch(parts[0].url, {method: 'PUT', body: text}); assert.equal(put.status, 200);
    const c = ok(await host('/api/hosts/artifacts/commit', {artifactId: r.artifactId, parts: [{partNumber: 1, etag: put.headers.get('etag')}]}));
    const bytes = Buffer.from(await (await fetch(c.verify.url)).arrayBuffer());
    ok(await host('/api/hosts/artifacts/verified', {artifactId: r.artifactId, storeId: c.verify.storeId, sha256: createHash('sha256').update(bytes).digest('hex')}));
    return r.artifactId;
  };
  const complete = async (runId: string, scoring = true) => {
    const r = await until(async()=>{const v=ok(await call('GET', `/api/pipeline-runs/${runId}`));return v.steps[1]?.executionId?v:undefined;},'script launch');
    const cmd = await until(async()=> (await poll()).commands.find((c: any) => c.executionId === r.steps[1].executionId),'script intake');
    await receipt(cmd, 'started');
    const {mkdtempSync} = await import('node:fs'); const {tmpdir} = await import('node:os'); const {execFileSync} = await import('node:child_process');
    const dir = mkdtempSync(join(tmpdir(), 'fleet-suffix-'));
    try {
      mkdirSync(join(dir, 'in/decide'), {recursive: true}); mkdirSync(join(dir, 'out'));
      for (const input of cmd.payload.inputs) {
        const response = await fetch(BASE + '/api/results/' + input.artifactId, {headers: {authorization: 'Bearer ' + SECRET, 'x-fleet-person': OWNER}});
        assert.equal(response.status, 200); const path = join(dir, 'in', input.path); mkdirSync(join(path, '..'), {recursive: true}); writeFileSync(path, Buffer.from(await response.arrayBuffer()));
      }
      execFileSync('/bin/sh', ['-c', cmd.payload.command], {env: {...process.env, FLEET_INPUT_DIR: join(dir, 'in'), FLEET_OUTPUT_DIR: join(dir, 'out')}});
      const output = readFileSync(join(dir, 'out/output.json'), 'utf8'); assert.equal(JSON.parse(output).p, 0.93);
      await upload(cmd, output); await receipt(cmd, 'completed', {artifacts: ['output.json']});
    } finally { rmSync(dir, {recursive: true, force: true}); }
    if (!scoring) return until(async () => { const r = ok(await call('GET', `/api/pipeline-runs/${runId}`)); return r.state === 'succeeded' ? r : undefined; }, 'ordinary rerun completes');
    return until(async () => { const v = await view(); return v.scores.some((s: any) => s.trialIds.includes(runId)) ? v : undefined; }, 'normal recovered score');
  };
  return {exp,trial,run,command,request,host,view,prove,receipt,poll,complete,upload,content,pipelineId};
}

test('L2 local-suffix retry: real stored bytes, normal scoring and atomic replay without another provider call (requirement: fleet-lab local-suffix retry; risk: double charge, lost cell lineage or replay dispatch)', async () => {
  const people = ok(await op('people_list')).people;
  ok(await op('people_set', {people: [...people, {email: 'architect@trial.test', name: 'Scoped architect', roles: ['operator', 'architect'], grants: [{kind: 'project', id: 'cs'}]}]}));
  const f = await suffixFixture();
  assert.equal((await op('lab_cell_action', f.request)).status, 409, 'no command-bound proof');
  await f.prove();
  assert.equal((await op('lab_cell_action', f.request)).status, 409, 'no closing unknown receipt');
  await f.receipt(f.command, 'unknown');
  const before = seen.length;
  const replies = await Promise.all([op('lab_cell_action', f.request, 'architect@trial.test'), op('lab_cell_action', f.request)]);
  const [a,b] = replies.map(ok); assert.deepEqual(a.recovery,b.recovery); assert.equal(a.recovery.sourceRunId,f.trial.id);
  const rid = a.recovery.runId; assert.notEqual(rid,f.trial.id);
  const recovered = a.trials.find((t: any) => t.id === rid);
  assert.deepEqual(recovered.recovery,{sourceRunId:f.trial.id,reusedStepIds:['decide']});
  assert.equal(recovered.cell.attempt,f.trial.cell.attempt+1);
  const r = ok(await call('GET',`/api/pipeline-runs/${rid}`));
  assert.equal(r.steps[0].outputArtifactId,f.run.steps[0].outputArtifactId); assert.equal((r.steps[0].calls ?? []).length,0);
  const done = await f.complete(rid); assert.ok(done.trials.find((t: any) => t.id===rid).comparable);
  assert.equal(done.spend.calls.value,1); assert.equal(seen.length,before);
  const source = ok(await call('GET',`/api/pipeline-runs/${f.trial.id}`));
  assert.deepEqual(source.steps[0].calls,f.run.steps[0].calls);
  assert.deepEqual(source.steps[0].usage,f.run.steps[0].usage);
  assert.deepEqual(done.trials.find((t:any)=>t.id===f.trial.id).cost,a.trials.find((t:any)=>t.id===f.trial.id).cost);
  assert.deepEqual(ok(await op('lab_cell_action',f.request)).recovery,a.recovery,'stable replay after scoring');
  const bad = await op('lab_cell_action',{...f.request,sourceRunId:rid}); assert.equal(bad.status,409);
  const outsider = await op('lab_cell_action',f.request,OUTSIDER); assert.equal(outsider.status,403); assert.ok(!JSON.stringify(outsider.body).includes(rid));
  const newer = await op('lab_cell_action',{experimentId:f.exp.id,cell:f.request.cell,action:'retry'}); ok(newer);
  assert.deepEqual(ok(await op('lab_cell_action',f.request)).recovery,a.recovery,'stable replay after latest changed');
});

test('L3 guarded races and privacy (requirement: fleet-lab local-suffix retry; risk: competing requests allocate two attempts or a foreign request leaks cached lineage)', async () => {
  const f = await suffixFixture(); await f.prove(); await f.receipt(f.command,'unknown');
  const before = seen.length;
  const rs = await Promise.all([op('lab_cell_action',f.request),op('lab_cell_action',{...f.request,requestId:randomUUID()})]);
  assert.deepEqual(rs.map(r=>r.status).sort(),[200,409]);
  const v = await f.view(); assert.equal(v.trials.length,2); assert.equal(seen.length,before);
  const winner = ok(rs.find(r=>r.status===200)!); const rid=winner.recovery.runId;
  const foreign = await suffixFixture('hello-world'); await foreign.prove(); await foreign.receipt(foreign.command,'unknown');
  const privateReply = await op('lab_cell_action',{...foreign.request,requestId:f.request.requestId},'architect@trial.test');
  assert.equal(privateReply.status,404); assert.ok(!JSON.stringify(privateReply.body).includes(foreign.trial.id));
  const different = await op('lab_cell_action',{...f.request,sourceRunId:foreign.trial.id});
  assert.equal(different.status,409); assert.ok(!JSON.stringify(different.body).includes(rid));
  // A source without runtime proof never passes because an arbitrary caller supplies proof/output-looking fields.
  assert.equal((await op('lab_cell_action',{...foreign.request,sourceRunId:'missing',proof:'approved',outputArtifactId:f.run.steps[0].outputArtifactId})).status,409);
  assert.equal((await f.view()).trials.length,2);
});

test('L4 content checks fail before mutation (requirement: fleet-lab frozen inputs and actual bytes; risk: metadata-only availability or definition drift cancels the source)', async () => {
  const f=await suffixFixture(); await f.prove(); await f.receipt(f.command,'unknown'); const before=seen.length;
  const store=await s3;
  const original=JSON.stringify(f.run.steps[0].output);
  const entry=[...store.objects.entries()].find(([key,bytes])=>key.includes('/cs/lab/') && bytes.toString()===original); assert.ok(entry);
  const [key,bytes]=entry;
  try {
    store.objects.set(key,Buffer.alloc(bytes.length,120));
    assert.equal((await op('lab_cell_action',f.request)).status,409,'corrupt actual bytes with unchanged metadata');
    store.objects.delete(key);
    assert.equal((await op('lab_cell_action',f.request)).status,409,'unavailable actual bytes with unchanged metadata');
  } finally {store.objects.set(key,bytes);}
  assert.equal((await f.view()).trials.length,1); assert.equal(seen.length,before);
  const g=ok(await op('definition_get',{kind:'pipeline',id:f.pipelineId})); const changed=structuredClone(g.content); changed.pipeline.steps[1].command+='; true';
  ok(await op('definition_save',{kind:'pipeline',id:f.pipelineId,baseRevision:g.head,content:changed,message:'drift fixture'}));
  assert.equal((await op('lab_cell_action',f.request)).status,409);
  assert.equal((await f.view()).trials.length,1); assert.equal(seen.length,before);
  const {secret:wrongSecret}=ok(await op('hosts_register',{id:name('other-host')}));
  assert.equal((await f.host('/api/hosts/events',{executionId:f.command.executionId,events:[{id:'wrong',seq:7,type:'process.reconciled',timestamp:Date.now(),data:{}}]},wrongSecret)).status,403);
});

// R3; risk: local reuse accepts an old project context after a saved effective project change.
test('Project revision drift rejects suffix reuse before cancellation or new provider work', async () => {
  const f=await suffixFixture(); await f.prove(); await f.receipt(f.command,'unknown'); const before=seen.length;
  const project=ok(await op('project_context',{projectId:'cs'}));
  const content=structuredClone(project.content);content.contextFiles[0].text+='\nNew context requires a fresh run.';
  ok(await op('project_update',{projectId:'cs',requestId:randomUUID(),baseRevision:project.revision,content}));
  const result=await op('lab_cell_action',f.request);assert.equal(result.status,409);assert.match(result.body.message,/frozen project revision/);
  assert.equal((await f.view()).trials.length,1);assert.equal(seen.length,before);
});

test('L5 ordinary failed rerun preserves immutable API output through transitive reuse (requirement: fleet-lab shared pipelineOutputs; risk: reused API bytes disappear or another provider call is made)', async () => {
  const f=await suffixFixture(); await f.prove(); await f.receipt(f.command,'unknown');
  ok(await op('lab_cell_action',{experimentId:f.exp.id,cell:f.request.cell,action:'cancel'}));
  const before=seen.length;
  const firstReply=ok(await op('pipeline_rerun',{runId:f.trial.id,mode:'failed',requestId:randomUUID(),outputArtifactId:'caller-cannot-override'}));
  const first=ok(await call('GET',`/api/pipeline-runs/${firstReply.runId}`));
  assert.equal(first.steps[0].outputArtifactId,f.run.steps[0].outputArtifactId);
  const started=await until(async()=>{const r=ok(await call('GET',`/api/pipeline-runs/${first.id}`)); return r.steps[1]?.executionId?r:undefined;},'ordinary rerun starts');
  const cmd=(await f.poll()).commands.find((c:any)=>c.executionId===started.steps[1].executionId); assert.ok(cmd);
  await f.receipt(cmd,'started'); await f.receipt(cmd,'failed',{error:'local fixture exit'});
  await until(async()=>{const r=ok(await call('GET',`/api/pipeline-runs/${first.id}`));return r.state==='failed'?r:undefined;},'local suffix fails');
  const secondReply=ok(await op('pipeline_rerun',{runId:first.id,mode:'failed',requestId:randomUUID()}));
  const second=ok(await call('GET',`/api/pipeline-runs/${secondReply.runId}`));
  assert.equal(second.steps[0].outputArtifactId,f.run.steps[0].outputArtifactId); assert.equal(second.steps[0].reusedFrom,first.id);
  const finished = await f.complete(second.id,false); assert.equal(seen.length,before);
  // Two child paths normalize to the same name; last stored child output wins over the coordinator fallback.
  const late = await f.upload({executionId:finished.steps[1].executionId},JSON.stringify({p:0.42,marker:'last child path wins'}),'output/output.json');
  const head = ok(await op('definition_get',{kind:'pipeline',id:f.pipelineId})); const expanded=structuredClone(head.content);
  expanded.pipeline.steps.push({id:'tail',kind:'script',command:'cat "$FLEET_INPUT_DIR/combine/output.json" > "$FLEET_OUTPUT_DIR/output.json"'});
  ok(await op('definition_save',{kind:'pipeline',id:f.pipelineId,baseRevision:head.head,content:expanded,message:'append local namespace fixture'}));
  const tail = ok(await op('pipeline_rerun',{runId:second.id,mode:'failed',requestId:randomUUID()}));
  const tailRun = await until(async()=>{const r=ok(await call('GET',`/api/pipeline-runs/${tail.runId}`));return r.steps[2]?.executionId?r:undefined;},'tail command');
  const intake = (await f.poll()).commands.find((c:any)=>c.executionId===tailRun.steps[2].executionId);assert.ok(intake);
  const combined = intake.payload.inputs.filter((x:any)=>x.path==='combine/output.json');assert.equal(combined.length,1);assert.equal(combined[0].artifactId,late);
  assert.equal(intake.payload.inputs.find((x:any)=>x.path==='decide/output.json').artifactId,f.run.steps[0].outputArtifactId);
  // The command was accepted by this poll; finish that exact command without requesting a second delivery.
  await f.receipt(intake,'started');
  const data = await fetch(BASE+'/api/results/'+late,{headers:{authorization:'Bearer '+SECRET,'x-fleet-person':OWNER}});assert.equal(data.status,200);
  const actual = await data.text();assert.equal(JSON.parse(actual).p,0.42);
  await f.upload(intake,actual);await f.receipt(intake,'completed',{artifacts:['output.json']});
  await until(async()=>{const r=ok(await call('GET',`/api/pipeline-runs/${tail.runId}`));return r.state==='succeeded'?r:undefined;},'tail completes');
  assert.equal(seen.length,before);
});

test('L7 legacy retry race (requirement: fleet-lab atomic identity and latest recheck; risk: full retry races a suffix into two attempts)', async () => {
  const f=await suffixFixture();await f.prove();await f.receipt(f.command,'unknown');
  ok(await op('lab_cell_action',{experimentId:f.exp.id,cell:f.request.cell,action:'cancel'}));
  const rs=await Promise.all([op('lab_cell_action',{...f.request,requestId:randomUUID()}),op('lab_cell_action',{experimentId:f.exp.id,cell:f.request.cell,action:'retry'})]);
  assert.deepEqual(rs.map(r=>r.status).sort(),[200,409]);
  assert.equal((await f.view()).trials.length,2);
});

test('L8 transitive Lab suffix retains immediate source and original artifact (requirement: fleet-lab exact provenance; risk: an intermediate attempt masquerades as the provider origin)', async () => {
  const f=await suffixFixture();await f.prove();await f.receipt(f.command,'unknown');
  const before=seen.length;
  const first=ok(await op('lab_cell_action',f.request)).recovery.runId;
  const run=await until(async()=>{const r=ok(await call('GET',`/api/pipeline-runs/${first}`));return r.steps[1]?.executionId?r:undefined;},'first recovery script');
  const cmd=await until(async()=>(await f.poll()).commands.find((c:any)=>c.executionId===run.steps[1].executionId),'first recovery intake');
  await f.receipt(cmd,'started');
  ok(await f.host('/api/hosts/events',{executionId:cmd.executionId,events:[{id:cmd.id+'-2',seq:2,type:'process.reconciled',timestamp:Date.now(),data:{ownedProcesses:1,resolved:true,survived:false,launchWindowUnresolved:false}}]}));
  await f.receipt(cmd,'unknown');
  const second=ok(await op('lab_cell_action',{...f.request,sourceRunId:first,requestId:randomUUID()})).recovery.runId;
  const derived=ok(await call('GET',`/api/pipeline-runs/${second}`));
  assert.equal(derived.steps[0].reusedFrom,first);assert.equal(derived.steps[0].outputArtifactId,f.run.steps[0].outputArtifactId);
  const done=await f.complete(second);assert.equal(done.trials.find((t:any)=>t.id===second).recovery.sourceRunId,first);
  assert.equal(done.spend.calls.value,1);assert.equal(seen.length,before);
});

test('Lab suffix recovery uses variant pipeline when outer pipeline differs (requirement: Compare lifecycle and local-suffix provenance; risk: resolver bypasses pinned source definition or repeats provider work)', async () => {
  const f = await suffixFixture('cs', true);
  await f.prove(); await f.receipt(f.command, 'unknown');
  const before = seen.length;
  const recovered = ok(await op('lab_cell_action', f.request)).recovery.runId;
  const done = await f.complete(recovered);
  assert.equal(seen.length, before);
  assert.ok(done.trials.find((t: any) => t.id === recovered).comparable);
  const run = ok(await call('GET', `/api/pipeline-runs/${recovered}`));
  assert.equal(run.pipelineId, f.pipelineId);
  assert.equal(run.steps[0].outputArtifactId, f.run.steps[0].outputArtifactId);
});

test('L6 preflight never holds the write lock and revalidates lifecycle changes (requirement: fleet-lab preflight/commit boundary; risk: a slow store blocks writes or deleted content passes a stale snapshot)', async () => {
  const f=await suffixFixture();await f.prove();await f.receipt(f.command,'unknown');
  const hash=sha(JSON.stringify(f.run.steps[0].output));
  let reached!:()=>void, release!:()=>void;
  const hit=new Promise<void>(r=>reached=r), gate=new Promise<void>(r=>release=r);
  artifactReadBarrier={hash,reached,release:gate};
  const pending=op('lab_cell_action',f.request);
  try {
    await Promise.race([hit,new Promise((_,reject)=>setTimeout(()=>reject(Error('store read was not reached')),5000))]);
    const write=op('lab_experiment_note',{experimentId:f.exp.id,kind:'note',text:'unrelated write while store read is blocked'});
    ok(await Promise.race([write,new Promise<never>((_,reject)=>setTimeout(()=>reject(Error('write lock held during byte read')),5000))]));
    ok(await op('artifact_purge',{contentHash:hash,projectId:'cs',scope:'lab',reason:'isolated preflight lifecycle race'}));
  } finally {artifactReadBarrier=undefined;release();}
  assert.equal((await pending).status,409);
  const v=await f.view();assert.equal(v.trials.length,1);assert.notEqual(v.trials[0].state,'cancelled');
});

test('A3 Promote saves the method as a catalogue revision (source lab) and sets the recommendation (requirement: fleet-definition-editors.md, Promote; risk: a promote that only returns a patch, so the winner never reaches the catalogue)', async () => {
  const example = JSON.parse(readFileSync('config/lab/examples/fleet-typed-decision-smoke.json', 'utf8'));
  example.version = 2000 + Math.floor(Math.random() * 1e6);
  const {examples: [imported]} = ok(await call('POST', '/api/lab/examples', {examples: [example], approved: true}));
  const e = ok(await call('POST', '/api/lab/experiments', {kind: 'single', projectId: 'hello-world', pipelineId: 'typed-decision', examples: [imported.artifactId], variants: [{label: 'promoted'}],
    evaluators: [{kind: 'script', ref: 'example.checks', form: 'pass_fail'}]}));
  const before = ok(await op('definition_get', {kind: 'catalogue', id: 'catalogue'})).head;
  const p = ok(await op('lab_promote', {experimentId: e.id, variant: 'promoted'}));
  assert.deepEqual([p.cataloguePatch, p.catalogue.revision, p.recommendation !== null], [null, before + 1, true]);
  const g = ok(await op('definition_get', {kind: 'catalogue', id: 'catalogue'}));
  assert.equal(g.meta.source, 'lab');
  assert.ok(g.content.catalogue.methods.some((m: any) => m.id === p.method.id));
  const again = ok(await op('lab_promote', {experimentId: e.id, variant: 'promoted'}));
  assert.equal(again.catalogue ?? null, null, 'a second promote finds the method and saves nothing');
});

test('L1 loop consumes scores created by the final evaluator in the same reconcile (requirement: UC9 no_gain; risk: stale pre-evaluation scores halt a successful iteration as unavailable)', async () => {
  const example = JSON.parse(readFileSync('config/lab/examples/fleet-typed-decision-smoke.json', 'utf8'));
  example.version = 6000000 + Math.floor(Math.random() * 1e8);
  const {examples: [pin]} = ok(await op('lab_examples_add', {examples: [example], approved: true}));
  const exp = ok(await op('lab_experiment_create', {kind: 'loop', projectId: 'hello-world', pipelineId: 'typed-decision', examples: [pin.artifactId], variants: [{label: 'baseline'}], repeats: 1, goal: 'Protect loop primary evidence', hypothesis: 'Only complete current scores can advance a loop',
    evaluators: [{kind: 'typed', ref: 'labels', form: 'typed'}], primaryEvaluator: 'typed:labels:typed',
    strategy: {generator: {kind: 'list', params: {variants: []}}, allocator: {kind: 'full'}, no_gain: {k: 1}}}));
  ok(await op('lab_experiment_action', {experimentId: exp.id, action: 'launch'}));
  const done: any = await until(async () => { const x = ok(await op('lab_experiment_get', {experimentId: exp.id})); return ['done', 'stopped'].includes(x.state) ? x : undefined; }, 'loop terminal state');
  assert.equal(done.stopReason, 'no_gain', JSON.stringify(done));
  assert.equal(done.scores.filter((s: any) => s.evaluator === 'typed:labels:typed').length, 1);
  assert.equal(done.strategies.length, 0, 'no strategy call after no_gain');
});

test('L2 missing primary evidence stops a loop without optional no_gain (requirement: UC9 explicit unavailable evidence; risk: failed judges launch more strategy work and paid trials)', async () => {
  const example = JSON.parse(readFileSync('config/lab/examples/fleet-typed-decision-smoke.json', 'utf8'));
  example.version = 7000000 + Math.floor(Math.random() * 1e8);
  const {examples: [pin]} = ok(await op('lab_examples_add', {examples: [example], approved: true}));
  // hello-search expects example.query, which a judge's input intentionally does not contain: it fails before any provider call.
  const exp = ok(await op('lab_experiment_create', {kind: 'loop', projectId: 'hello-world', pipelineId: 'typed-decision', examples: [pin.artifactId], variants: [{label: 'baseline'}], repeats: 1, goal: 'Protect loop primary evidence', hypothesis: 'Only complete current scores can advance a loop',
    evaluators: [{kind: 'judge', ref: 'hello-search', form: 'pass_fail'}], primaryEvaluator: 'judge:hello-search:pass_fail',
    strategy: {generator: {kind: 'list', params: {variants: [{label: 'extra', overrides: {}}]}}, allocator: {kind: 'full'}}}));
  ok(await op('lab_experiment_action', {experimentId: exp.id, action: 'launch'}));
  const done: any = await until(async () => { const x = ok(await op('lab_experiment_get', {experimentId: exp.id})); return ['done', 'stopped'].includes(x.state) ? x : undefined; }, 'loop missing primary');
  assert.equal(done.state, 'stopped');
  assert.equal(done.stopReason, 'primary-score-unavailable');
  assert.equal(done.trials.length, 1);
  assert.equal(done.strategies.length, 0, 'unavailable is not permission to continue');
});

test('L3 withdrawing a weak rating records refusal without a quality label (requirement: owner can decline Lab comparison; risk: refusal recorded as a pick/tie, overwritten answer or lingering needs-you)', async () => {
  const example = JSON.parse(readFileSync('config/lab/examples/fleet-typed-decision-smoke.json', 'utf8'));
  example.version = 8000000 + Math.floor(Math.random() * 1e8);
  const {examples: [pin]} = ok(await op('lab_examples_add', {examples: [example], approved: true}));
  const exp = ok(await op('lab_experiment_create', {kind: 'single', projectId: 'hello-world', pipelineId: 'typed-decision', examples: [pin.artifactId], variants: [{label: 'baseline'}],
    goal: 'Withdrawal fixture', hypothesis: 'Withdrawal creates no human score'}));
  ok(await op('lab_experiment_action', {experimentId: exp.id, action: 'launch'}));
  const done: any = await until(async () => { const x = ok(await op('lab_experiment_get', {experimentId: exp.id})); return x.state === 'done' ? x : undefined; }, 'withdrawal trial');
  const rating = ok(await op('lab_rating_create', {experimentId: exp.id, form: 'pass_fail', trialIds: [done.trials[0].id]}));
  assert.equal((await op('lab_rating_withdraw', {questionId: rating.id, reason: 'not meaningful'}, OUTSIDER)).status, 403);
  const withdrawn = ok(await op('lab_rating_withdraw', {questionId: rating.id, reason: 'not meaningful'}));
  assert.equal(withdrawn.state, 'withdrawn'); assert.equal(withdrawn.answer, null);
  assert.equal(withdrawn.withdrawal.reason, 'not meaningful'); assert.equal(withdrawn.withdrawal.actor, OWNER);
  assert.equal(ok(await op('lab_rating_withdraw', {questionId: rating.id, reason: 'replay'})).withdrawal.reason, 'not meaningful');
  assert.equal((await op('questions_answer', {questionId: rating.id, answer: 'true'})).status, 409);
  assert.equal(ok(await op('lab_experiment_get', {experimentId: exp.id})).scores.length, 0);
});

test('Catalogue Compare snapshots selected variant pipelines without dispatch (requirement: D20 Compare methods; risk: another pipeline or catalogue ref masquerades as the executed method)', async () => {
  const pipe = name('compare');
  const original = ok(await op('definition_get', {kind: 'pipeline', id: 'typed-decision'}));
  const content = structuredClone(original.content); content.pipeline.id = pipe;
  ok(await op('definition_create', {kind: 'pipeline', id: pipe, content, message: 'Compare fixture'}));
  const ex = JSON.parse(readFileSync('config/lab/examples/fleet-typed-decision-smoke.json', 'utf8'));
  ex.use_case_id = 'fleet:UC1'; ex.version = 5000000 + Math.floor(Math.random() * 1e6);
  ok(await op('lab_examples_add', {projectId: 'hello-world', examples: [ex], approved: true}));
  const head = ok(await op('definition_get', {kind: 'catalogue', id: 'catalogue'}));
  const cat = structuredClone(head.content);
  cat.catalogue.methods.push({id: 'compare-a', name: 'Compare A', scope: 'hello-world', pipeline: 'typed-decision.json'},
    {id: 'compare-b', name: 'Compare B', scope: 'hello-world', pipeline: pipe + '.json', configRef: 'explicit-catalogue-identity'});
  ok(await op('definition_save', {kind: 'catalogue', id: 'catalogue', baseRevision: head.head, content: cat, message: 'Compare methods fixture'}));
  const body = {projectId: 'hello-world', capability: cat.catalogue.capability, methods: ['compare-a', 'compare-b'], baseline: 'compare-b', useCase: 'fleet:UC1'};
  const before = seen.length;
  const created = ok(await op('lab_compare_methods', body));
  const view = ok(await op('lab_experiment_get', {experimentId: created.experimentId}));
  assert.equal(view.state, 'draft'); assert.equal(view.trials.length, 0); assert.equal(seen.length, before);
  assert.deepEqual(view.variants.map((v: any) => [v.label, v.pipelineId]), [['compare-b', pipe], ['compare-a', 'typed-decision']]);
  assert.equal(view.variants[0].catalogueConfigRef, 'explicit-catalogue-identity');
  assert.notEqual(view.variants[0].config_ref, view.variants[0].catalogueConfigRef);
  assert.equal(view.variants[0].catalogueRef.revision, head.head + 1);
  assert.equal((await op('lab_compare_methods', {...body, baseline: 'search-basic'})).status, 400);
  assert.equal((await op('lab_compare_methods', {...body, methods: ['compare-a', 'compare-a']})).status, 400);
  assert.equal((await op('lab_compare_methods', {...body, useCase: undefined})).status, 400);
  assert.equal((await op('lab_compare_methods', body, OUTSIDER)).status, 403);
  for (const field of ['goal', 'hypothesis']) ok(await op('lab_experiment_note', {experimentId: created.experimentId, kind: 'amendment', field, value: 'Controlled Compare execution fixture', text: `Set ${field} before explicit launch`}));
  ok(await op('lab_experiment_action', {experimentId: created.experimentId, action: 'launch'}));
  const completed = await until(async () => {const v = ok(await op('lab_experiment_get', {experimentId: created.experimentId})); return v.state === 'done' ? v : undefined;}, 'two variant pipelines complete');
  for (const t of completed.trials) {
    const r = ok(await call('GET', `/api/pipeline-runs/${t.id}`));
    const expected = t.cell.variant === 'compare-b' ? pipe : 'typed-decision';
    assert.equal(r.pipelineId, expected);
    assert.ok(r.definitions.some((d: any) => d.kind === 'pipeline' && d.id === expected));
  }

  const promoted = ok(await op('lab_promote', {experimentId: created.experimentId, variant: 'compare-b'}));
  assert.equal(promoted.method.pipeline, pipe + '.json');
  assert.equal(ok(await op('lab_promote', {experimentId: created.experimentId, variant: 'compare-b'})).catalogue ?? null, null);
});

test('Catalogue reports a compatible observed partial mean and keeps banded source evidence separate (requirement: D20 method assessments; risk: unscored cells become zero, or catalogue identity hides a different executable configuration)', async () => {
  const ex = JSON.parse(readFileSync('config/lab/examples/fleet-typed-decision-smoke.json', 'utf8'));
  ex.version = 6000000 + Math.floor(Math.random() * 1e6);
  const missing = structuredClone(ex); missing.id += '-unscored'; missing.references = missing.references.filter((r: any) => r.path !== 'expected.json');
  const imported = ok(await op('lab_examples_add', {projectId: 'hello-world', examples: [ex, missing], approved: true})).examples;
  const create = (label: string, overrides: object) => op('lab_experiment_create', {kind: 'single', projectId: 'hello-world', pipelineId: 'typed-decision', examples: imported.map((e: any) => e.artifactId), variants: [{label, overrides}], evaluators: [{kind: 'typed', ref: 'labels', form: 'typed'}], primaryEvaluator: 'typed:labels:typed', goal: 'Measure eligible partial population', hypothesis: 'A missing label stays unscored'}).then(ok);
  const active = async (label: string) => {
    const v = ok(await op('lab_experiment_create', {kind: 'single', projectId: 'hello-world', pipelineId: 'typed-decision', examples: imported.map((e: any) => e.artifactId), variants: [{label, overrides: {prompt: 'Catalogue evidence fixture'}}], evaluators: [{kind:'human',ref:'owner',form:'pass_fail'}], primaryEvaluator:'human:owner:pass_fail', budgets:[{unit:'trials',limit:1,action:'pause'}], goal:'Keep active evidence visible', hypothesis:'Unrated human evaluator stays incomplete'}));
    ok(await op('lab_experiment_action', {experimentId:v.id,action:'launch'}));
    await until(async () => {const got = ok(await op('lab_experiment_get', {experimentId:v.id})); return got.state === 'paused' ? got : undefined;}, 'budget pauses active fixture');
    return v;
  };
  const older = await active('older-paused');
  const e = await create('partial-evidence', {prompt: 'Catalogue evidence fixture'});
  ok(await op('lab_experiment_action', {experimentId: e.id, action: 'launch'}));
  await until(async () => {const v = ok(await op('lab_experiment_get', {experimentId: e.id})); return v.state === 'done' && v.scores.length === 2 ? v : undefined;}, 'partial evidence scores');
  const promoted = ok(await op('lab_promote', {experimentId: e.id, variant: 'partial-evidence'}));
  const methods = ok(await op('catalogue', {projectId: 'hello-world'})).methods;
  const a = methods.find((m: any) => m.id === promoted.method.id).assessment;
  assert.deepEqual([a.basis, a.score, a.scoreForm, a.eligibleCells, a.expectedCells, a.partial], ['lab', 1, 'typed', 1, 2, true]);
  assert.notEqual(a.ongoingExperimentId, older.id, 'older paused work is not a newer follow-up');
  assert.equal(a.variant, 'partial-evidence'); assert.ok(a.evaluatorVersion); assert.equal(a.experimentId, e.id);
  assert.ok(methods.find((m: any) => m.id === promoted.method.id).recommendedFor.includes('hello-world'));
  const foreign = ok(await op('catalogue', {projectId: 'cs'}));
  assert.ok(!JSON.stringify(foreign).includes(e.id), 'same system catalogue must not leak project experiment links');
  const newer = await active('newer-paused');
  const refreshed = ok(await op('catalogue', {projectId:'hello-world'})).methods.find((m: any) => m.id === promoted.method.id).assessment;
  assert.equal(refreshed.ongoingExperimentId, newer.id); assert.equal(refreshed.experimentId, e.id);
  const banded = await create('banded-evidence', {prompt: 'Catalogue banded fixture', bands: {yes: 0.95, no: 0.05}});
  const collisionHead = ok(await op('definition_get', {kind: 'catalogue', id: 'catalogue'}));
  const collisionContent = structuredClone(collisionHead.content);
  collisionContent.catalogue.methods.push({id: 'banded-ref-collision', name: 'Other executable', scope: 'hello-world', pipeline: 'hello-search.json', configRef: banded.variants[0].config_ref});
  ok(await op('definition_save', {kind: 'catalogue', id: 'catalogue', baseRevision: collisionHead.head, content: collisionContent, message: 'Explicit ref collision fixture'}));
  const b = ok(await op('lab_promote', {experimentId: banded.id, variant: 'banded-evidence'}));
  assert.equal(b.method.overrides.bands, undefined);
  assert.notEqual(b.method.id, 'banded-ref-collision');
  assert.equal(ok(await op('lab_promote', {experimentId: banded.id, variant: 'banded-evidence'})).catalogue ?? null, null);
  const assessment = ok(await op('catalogue', {projectId: 'hello-world'})).methods.find((m: any) => m.id === b.method.id).assessment;
  // A draft source is visible only as provenance, never a completed assessment.
  assert.equal(assessment.score, null);
  assert.ok(!assessment.experimentId);
  assert.equal(assessment.sourceExperimentId, banded.id);
  assert.match(assessment.reason, /different executable configuration/);
});



test('P4 S7 preparation adopts exact identity only and retains image receipts (requirement: fleet-asset-pipelines S7; risk: unrelated jobs adopted, a second charged POST or lost prepared views)', async () => {
  const args = {reference_image_urls:['data:image/png;base64,a'],prompt:'invented own character',ai_model:'nano-banana-2',generate_multi_view:true};
  try {
    for (const mode of ['exact','missing','ambiguous','wrong-tool','ungranted'] as const) {
      const connection = name('meshy-prep'), pipelineId = name('prep');
      ok(await op('connections_create',{name:connection,provider:'meshy'}));
      ok(await op('connections_set_access',{connection,access:{projects:['*']}}));
      ok(await op('connections_key_add',{connection,value:'msy-invented-'+randomUUID()}));
      const legacy=['image_to_3d','job_status','recent_tasks'];
      assert.equal((await op('connections_set_tools',{connection,tools:['image_job_status']},OUTSIDER)).status,403);
      assert.equal((await op('connections_set_tools',{connection,tools:['invented-unknown']})).status,400);
      assert.equal((await op('connections_set_tools',{connection,tools:['job_status','job_status']})).status,400);
      ok(await op('connections_set_tools',{connection,tools:legacy}));
      if (mode!=='ungranted') {
        const granted=ok(await op('connections_set_tools',{connection,tools:[...legacy,'image_to_image','image_job_status','image_recent_tasks']}));
        assert.deepEqual(granted.access.projects,['*'],'tool update preserves project grants');
        await until(()=>snapshot().connections[connection]?.tools.includes('image_job_status')||undefined,'explicit tools reach gateway snapshot');
        const changes=ok(await op('setting_changes',{target:'connection:'+connection})).changes;
        assert.ok(changes.some((c:any)=>c.field==='tools'&&c.previous.includes('job_status')&&c.value.includes('image_job_status')),'tool grant keeps previous and next settings');
      }

      assetJob = {connection,mode,jobId:randomUUID(),args,calls:[]};
      const step = {id:'prep',kind:'api',connection,tool:'image_to_image',mode:'job',status_tool:mode==='wrong-tool'?'image_to_image':'image_job_status',recent_tool:'image_recent_tasks',arguments:args};
      ok(await op('definition_create',{kind:'pipeline',id:pipelineId,content:{pipeline:{id:pipelineId,version:2,steps:[step]}},message:'Invented uncharged S7 job fixture'}));
      const started = ok(await op('pipeline_run_create',{pipelineId,projectId:'hello-world',requestId:randomUUID(),example:{}}));
      const run = await until(async()=>{const r=ok(await op('pipeline_get',{runId:started.runId}));return r.state!=='running'?r:undefined;},'preparation job ends');
      if (mode==='wrong-tool'||mode==='ungranted') {
        assert.equal(run.steps[0].problem,'step:job-tools'); assert.deepEqual(assetJob.calls,[],'a POST cannot be configured as polling before any paid submit'); continue;
      }
      assert.equal(assetJob.calls.filter(t=>t==='image_to_image').length,1,'uncertain POST never sent twice');
      assert.equal(assetJob.calls.filter(t=>t==='image_recent_tasks').length,1);
      if (mode==='exact') {
        assert.equal(run.state,'succeeded');
        assert.deepEqual(run.steps[0].output.image_urls,['https://invented.invalid/front.png']);
        assert.equal(run.steps[0].output.receipt.consumed_credits,6);
        assert.equal(run.steps[0].output.jobId,assetJob.jobId);
      } else {
        assert.equal(run.steps[0].problem,'unknown:submit-unconfirmed');
        assert.equal(run.observation,'unconfirmed');
        assert.equal(assetJob.calls.includes('image_job_status'),false,'unknown job never polled or resubmitted');
      }
    }
  } finally {assetJob=undefined;}
});


test('P5 source rig job preserves exact request and confirmed result (requirement: fleet-asset-pipelines S7; risk: wrong source rig adopted or uncertain charged POST repeated)', async () => {
  try {
    for (const mode of ['exact','missing'] as const) {
      const connection=name('meshy-rig'),pipelineId=name('rig');
      ok(await op('connections_create',{name:connection,provider:'meshy'}));
      ok(await op('connections_set_access',{connection,access:{projects:['*']}}));
      ok(await op('connections_key_add',{connection,value:'msy-invented-'+randomUUID()}));
      ok(await op('connections_set_tools',{connection,tools:['rigging','rigging_job_status','rigging_recent_tasks']}));
      const args={input_task_id:randomUUID(),height_meters:2.05};
      assetJob={connection,mode,kind:'rigging',jobId:randomUUID(),args,calls:[]};
      const step={id:'rig',kind:'api',connection,tool:'rigging',mode:'job',status_tool:'rigging_job_status',recent_tool:'rigging_recent_tasks',arguments:args};
      ok(await op('definition_create',{kind:'pipeline',id:pipelineId,content:{pipeline:{id:pipelineId,version:2,steps:[step]}},message:'Invented uncharged source rig fixture'}));
      const started=ok(await op('pipeline_run_create',{pipelineId,projectId:'hello-world',requestId:randomUUID(),example:{}}));
      const run=await until(async()=>{const r=ok(await op('pipeline_get',{runId:started.runId}));return r.state!=='running'?r:undefined;},'source rig job ends');
      assert.equal(assetJob.calls.filter(t=>t==='rigging').length,1);
      if (mode==='exact') {
        assert.equal(run.state,'succeeded');
        assert.deepEqual(run.steps[0].output.request,args);
        assert.deepEqual(run.steps[0].output.result,{rigged_character_glb_url:'https://invented.invalid/rig.glb'});
        assert.equal(run.steps[0].output.receipt.consumed_credits,7);
      } else {
        assert.equal(run.steps[0].problem,'unknown:submit-unconfirmed');
        assert.equal(assetJob.calls.includes('rigging_job_status'),false);
      }
    }
  } finally {assetJob=undefined;}
});

test('C8 connections_delete: owner only, refused while a key is in use, then the connection, its key rows and its value directory go (requirement: docs/designs/fleet-connections.md §1 delete; risk: test connections pile up, or a delete drops a live key)', async () => {
  const c = name('tavily');
  ok(await op('connections_create', {name: c, provider: 'tavily', holder: OUTSIDER}));
  const k = ok(await op('connections_key_add', {connection: c, value: 'tvly-' + randomUUID()}));
  assert.equal((await op('connections_delete', {connection: c}, OUTSIDER)).status, 403);
  assert.equal((await op('connections_delete', {connection: c})).status, 409, 'an active key blocks the delete');
  ok(await op('connections_set_access', {connection: c, access: {projects: ['*']}}));
  const t = ok(await op('connections_token_create', {name: name('t'), connections: [c]}));
  ok(await op('connections_key_state', {keyId: k.fingerprint, state: 'retired'}));
  assert.deepEqual(ok(await op('connections_delete', {connection: c})), {name: c, deleted: true});
  assert.ok(!ok(await op('connections_list')).connections.some((x: any) => x.name === c));
  await until(() => !snapshot().tokens.some((x: any) => x.sha256 === sha(t.token) && x.connections?.includes(c)) || undefined, 'tokens drop the deleted name');
  assert.equal(existsSync(join(SECRET_DIR, 'connections', c)), false);
  assert.equal((await op('connections_delete', {connection: c})).status, 404);
  ok(await op('connections_create', {name: c, provider: 'tavily'}));
});
