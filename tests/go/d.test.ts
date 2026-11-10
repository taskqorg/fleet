// Outside tests of Go rewrite area D (artifacts, stores, results, the built app). HTTP only, against FLEET_TRIAL_URL; an
// in-process fake S3 plays the store. Run:
//   FLEET_STORE_ENDPOINT_HOSTS=127.0.0.1 FLEET_DEFAULT_ARTIFACT_LOCATIONS=trial-host FLEET_STORE_SECRETS=$(mktemp -d) \
//   FLEET_TRIAL_TESTS=tests/go/d.test.ts FLEET_TRIAL_SERVER=go/bin/fleet-coordinator node tests/trial/run.ts
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

async function host(id: string) {
  const {secret} = ok(await call('POST', '/api/ops/hosts_register', {id}, owner));
  return (path: string, body: unknown) => call('POST', path, body, {authorization: 'Bearer ' + secret});
}

test('Artifact write protocol and result bytes (artifacts.ts reserve/parts/commit/verified, GET /api/results Range). Risk: an executor upload never becomes a readable result, or a Range read returns the wrong bytes.', {timeout: 30_000}, async () => {
  const h = await host('trial-host');
  ok(await h('/api/hosts/stores', {store: {id: 'trial-store', bucket: 'fleet', endpoint: `http://127.0.0.1:${s3port}`, accessKeyId: 'k', secretAccessKey: 's'}}));
  const {sessionId} = ok(await call('POST', '/api/sessions', {}, owner));
  const execution = ok(await call('POST', `/api/sessions/${sessionId}/messages`, {requestId: randomUUID(), text: 'hello'}, owner));
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
  const all = await call('GET', `/api/results/${r.artifactId}`, undefined, owner);
  assert.equal(all.status, 200); assert.equal(all.body, bytes.toString());
  assert.match(all.headers.get('content-security-policy') ?? '', /^sandbox/);
  const part = await call('GET', `/api/results/${r.artifactId}`, undefined, {...owner, range: 'bytes=6-13'});
  assert.equal(part.status, 206); assert.equal(part.body, 'artifact'); assert.equal(part.headers.get('content-range'), `bytes 6-13/${bytes.length}`);
  const listed = ok(await call('POST', '/api/ops/artifact_list', {executionId: execution.id, limit: 10}, owner));
  assert.deepEqual(listed.artifacts.map((a: any) => a.state), ['stored']);
});

test('Store registry: only an allowed endpoint host, and the free-space warning marks a store full (stores.ts). Risk: a host points the coordinator and its signatures at any URL, or a full store keeps receiving uploads.', async () => {
  const h = await host('second-host');
  const bad = await h('/api/hosts/stores', {store: {id: 'evil', bucket: 'b', endpoint: 'https://evil.example', accessKeyId: 'k', secretAccessKey: 's'}});
  assert.equal(bad.status, 403);
  ok(await h('/api/hosts/stores', {store: {id: 'second-store', bucket: 'b', endpoint: `http://127.0.0.1:${s3port}`, accessKeyId: 'k', secretAccessKey: 's', capacityBytes: 100}}));
  assert.equal(ok(await call('POST', '/api/ops/store_set_free_space_warning', {storeId: 'second-store', bytes: 1000}, owner)).state, 'full');
  const {stores, problems} = ok(await call('POST', '/api/ops/stores_list', {}, owner));
  assert.ok(stores.some((s: any) => s.id === 'second-store' && s.state === 'full'));
  assert.ok(problems.some((p: any) => p.kind === 'store-full' && p.storeId === 'second-store'));
});

test('The built app is public, the API is not (app.ts PUBLIC_ROUTES). Risk: the browser cannot load the app shell, or result bytes are served without credentials.', async () => {
  const root = await call('GET', '/');
  assert.equal(root.status, 302); assert.equal(root.headers.get('location'), '/overview');
  assert.equal((await call('GET', '/api/results/anything')).status, 401);
});

test('review 7: every grouping the view-url codec writes is accepted by task_page (packages/view-url TASK_GROUPS). Risk: a list URL the app produces answers 400.', async () => {
  for (const group of TASK_GROUPS) ok(await call('POST', '/api/ops/task_page', {group, limit: 1}, owner));
});
