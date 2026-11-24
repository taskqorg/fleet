// Outside tests of artifacts, stores, results and the public routes of the built app. HTTP only, against FLEET_TRIAL_URL; an
// in-process fake S3 plays the store. Run:
//   FLEET_STORE_ENDPOINT_HOSTS=127.0.0.1 FLEET_DEFAULT_ARTIFACT_LOCATIONS=trial-host FLEET_STORE_SECRETS=$(mktemp -d) \
//   FLEET_TRIAL_TESTS=tests/go/artifacts.test.ts FLEET_TRIAL_SERVER=go/bin/fleet-coordinator node tests/trial/run.ts
// Each test names its requirement and the risk it guards. SigV4 itself is checked against the AWS vector in go test.
import {after, test} from 'node:test';
import assert from 'node:assert/strict';
import {createHash, randomUUID} from 'node:crypto';
import {fakeS3} from '../s3-fake.ts';
import {TASK_GROUPS} from '../../packages/view-url/view-url.ts';

const BASE = process.env.FLEET_TRIAL_URL!, SECRET = process.env.FLEET_TRIAL_BROWSER_SECRET!;
const OWNER = 'owner@trial.test';
type Reply = {status: number, body: any, headers: Headers};
async function call(method: string, path: string, body?: unknown, headers: Record<string, string> = {}): Promise<Reply> {
  const r = await fetch(BASE + path, {method, redirect: 'manual', headers: {...(body === undefined ? {} : {'content-type': 'application/json'}), ...headers}, body: body === undefined ? undefined : JSON.stringify(body)});
  const text = await r.text(); let parsed: any = text; try { parsed = JSON.parse(text); } catch {}
  return {status: r.status, body: parsed, headers: r.headers};
}
const owner = {authorization: 'Bearer ' + SECRET, 'x-fleet-person': OWNER};
const ok = (r: Reply) => { assert.equal(r.status, 200, JSON.stringify(r.body)); return r.body; };

const {port: s3port, close: closeS3} = await fakeS3();
after(closeS3);

const secrets = new Map<string, string>(); // a host registers once; later tests reuse its secret
async function host(id: string) {
  if (!secrets.has(id)) secrets.set(id, ok(await call('POST', '/api/ops/hosts_register', {id}, owner)).secret);
  const secret = secrets.get(id)!;
  return (path: string, body: unknown) => call('POST', path, body, {authorization: 'Bearer ' + secret});
}

test('Artifact write protocol and result bytes (artifacts.ts reserve/parts/commit/verified, GET /api/results Range). Requirement: docs/designs/fleet-data-foundation.md results. Risk: an executor upload never becomes a readable result, or a Range read returns the wrong bytes.', {timeout: 30_000}, async () => {
  const h = await host('trial-host');
  ok(await h('/api/hosts/stores', {store: {id: 'trial-store', bucket: 'fleet', endpoint: `http://127.0.0.1:${s3port}`, accessKeyId: 'k', secretAccessKey: 's'}}));
  const {sessionId} = ok(await call('POST', '/api/ops/session_create', {}, owner));
  const execution = ok(await call('POST', '/api/ops/message_send', {sessionId: sessionId, requestId: randomUUID(), text: 'hello'}, owner));
  ok(await h('/api/hosts/poll', {platform: 'darwin', capabilities: [], stores: [], problems: []}));
  const bytes = Buffer.from('hello artifact bytes\n'), sha256 = createHash('sha256').update(bytes).digest('hex');
  const r = ok(await h('/api/hosts/artifacts/reserve', {executionId: execution.id, name: 'out/hello.txt', mediaType: 'text/plain', size: bytes.length, sha256}));
  assert.equal(r.state, 'pending');
  const {parts} = ok(await h('/api/hosts/artifacts/parts', {artifactId: r.artifactId, partNumbers: [1]}));
  const put = await fetch(parts[0].url, {method: 'PUT', body: bytes});
  assert.equal(put.status, 200);
  const c = ok(await h('/api/hosts/artifacts/commit', {artifactId: r.artifactId, parts: [{partNumber: 1, etag: put.headers.get('etag')}]}));
  assert.equal(c.state, 'verifying', 'the store host is the writer: it verifies now');
  const staged = Buffer.from(await (await fetch(c.verify.url)).arrayBuffer());
  const v = ok(await h('/api/hosts/artifacts/verified', {artifactId: r.artifactId, storeId: c.verify.storeId, sha256: createHash('sha256').update(staged).digest('hex')}));
  assert.equal(v.state, 'stored');
  const view = ok(await call('POST', '/api/ops/artifact_get', {artifactId: r.artifactId}, owner));
  assert.equal(view.url, `/api/results/${r.artifactId}`, 'people and tools read via Fleet, not a separate store login');
  assert.equal((await call('GET', view.url)).status, 401, 'the URL preserves Fleet authentication');
  const all = await call('GET', view.url, undefined, owner);
  assert.equal(all.status, 200); assert.equal(all.body, bytes.toString());
  assert.match(all.headers.get('content-security-policy') ?? '', /^sandbox/);
  const part = await call('GET', `/api/results/${r.artifactId}`, undefined, {...owner, range: 'bytes=6-13'});
  assert.equal(part.status, 206); assert.equal(part.body, 'artifact'); assert.equal(part.headers.get('content-range'), `bytes 6-13/${bytes.length}`);
  const listed = ok(await call('POST', '/api/ops/artifact_list', {executionId: execution.id, limit: 10}, owner));
  assert.deepEqual(listed.artifacts.map((a: any) => a.state), ['stored']);
});

test('Agent artifact listing pages (artifact_routes.go hostArtifactList). Requirement: one artifact_list call never returns an unbounded reply; the agent pages on with nextCursor. Risk: a project of many artifacts returns one ~220k-character reply, over the harness tool-output limit (Lab trial 2026-11-17).', {timeout: 60_000}, async () => {
  const h = await host('trial-host');
  const {sessionId} = ok(await call('POST', '/api/ops/session_create', {}, owner));
  const execution = ok(await call('POST', '/api/ops/message_send', {sessionId: sessionId, requestId: randomUUID(), text: 'many'}, owner));
  ok(await h('/api/hosts/poll', {platform: 'darwin', capabilities: [], stores: [], problems: []}));
  const ids: string[] = [];
  for (let i = 0; i < 51; i++) {
    const bytes = Buffer.from(`page ${i}\n`);
    ids.push(ok(await h('/api/hosts/artifacts/reserve', {executionId: execution.id, name: `out/${i}.txt`, mediaType: 'text/plain', size: bytes.length, sha256: createHash('sha256').update(bytes).digest('hex')})).artifactId);
  }
  const first = ok(await h('/api/hosts/artifacts/list', {executionId: execution.id}));
  assert.equal(first.artifacts.length, 50, 'one page');
  assert.equal(first.artifacts[0].id, ids[50], 'newest first');
  assert.equal(typeof first.nextCursor, 'string');
  const rest = ok(await h('/api/hosts/artifacts/list', {executionId: execution.id, filter: {cursor: first.nextCursor}}));
  assert.deepEqual(rest.artifacts.map((a: any) => a.id), [ids[0]]);
  assert.equal(rest.nextCursor, undefined, 'the last page has no cursor');
  assert.equal((await h('/api/hosts/artifacts/list', {executionId: execution.id, filter: {cursor: 'x'}})).status, 400);
});

test('Store registry: only an allowed endpoint host, and the free-space warning marks a store full (stores.ts). Requirement: docs/designs/fleet-data-foundation.md stores. Risk: a host points the coordinator and its signatures at any URL, or a full store keeps receiving uploads.', async () => {
  const h = await host('second-host');
  const bad = await h('/api/hosts/stores', {store: {id: 'evil', bucket: 'b', endpoint: 'https://evil.example', accessKeyId: 'k', secretAccessKey: 's'}});
  assert.equal(bad.status, 403);
  ok(await h('/api/hosts/stores', {store: {id: 'second-store', bucket: 'b', endpoint: `http://127.0.0.1:${s3port}`, accessKeyId: 'k', secretAccessKey: 's', capacityBytes: 100}}));
  assert.equal(ok(await call('POST', '/api/ops/store_set_free_space_warning', {storeId: 'second-store', bytes: 1000}, owner)).state, 'full');
  const {stores, problems} = ok(await call('POST', '/api/ops/stores_list', {}, owner));
  assert.ok(stores.some((s: any) => s.id === 'second-store' && s.state === 'full'));
  assert.ok(problems.some((p: any) => p.kind === 'store-full' && p.storeId === 'second-store'));
});

test('The built app is public, the API is not (app.ts PUBLIC_ROUTES). Requirement: docs/designs/fleet-people-access.md public routes. Risk: the browser cannot load the app shell, or result bytes are served without credentials.', async () => {
  const root = await call('GET', '/');
  assert.equal(root.status, 302); assert.equal(root.headers.get('location'), '/home');
  assert.equal((await call('GET', '/api/results/anything')).status, 401);
});

test('GET /api/health is public and /api/stream pings an idle stream (TypeScript stream: a ping every 25 s). Requirement: docs/designs/fleet-operator-app.md /api/stream. Risk: deploys cannot see the running commit or the database, or proxies and Access drop an idle stream.', {timeout: 10_000}, async () => {
  const h = await call('GET', '/api/health');
  assert.equal(h.status, 200); assert.equal(h.body.database, 'ok'); assert.ok('commit' in h.body && 'built' in h.body);
  assert.ok(!JSON.stringify(h.body).includes(SECRET));
  const ac = new AbortController(), r = await fetch(BASE + '/api/stream', {headers: owner, signal: ac.signal});
  const reader = r.body!.getReader(), dec = new TextDecoder(); let text = '';
  const end = Date.now() + 5000;
  while (!text.includes('\n: ping\n') && Date.now() < end) { const {value, done} = await reader.read(); if (done) break; text += dec.decode(value); }
  ac.abort();
  assert.match(text, /^event: ready/); assert.ok(text.includes('\n: ping\n'), 'no ping within 5 s (FLEET_STREAM_PING_MS=300)');
});

test('review 7: every grouping the view-url codec writes is accepted by task_page (packages/view-url TASK_GROUPS). Requirement: docs/designs/fleet-view-urls-and-list-path.md. Risk: a list URL the app produces answers 400.', async () => {
  for (const group of TASK_GROUPS) ok(await call('POST', '/api/ops/task_page', {group, limit: 1}, owner));
});
