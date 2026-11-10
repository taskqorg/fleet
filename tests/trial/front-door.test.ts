// Outside tests of the Go front door, slice 1 (docs/designs/fleet-stack.md#front-door-fd). They pin today's TypeScript
// behaviour of the routes Go serves where the trial tests (slice.test.ts) do not reach, and that every other request reaches
// the TypeScript coordinator unchanged. They run against the TypeScript coordinator alone and against the mixed setup
// (`node tests/trial/run.ts`, `FLEET_TRIAL_ARM=mixed node tests/trial/run.ts`). Each test names its requirement and risk.
import {test as nodeTest} from 'node:test';
import assert from 'node:assert/strict';
import {randomUUID} from 'node:crypto';

const BASE = process.env.FLEET_TRIAL_URL!, SECRET = process.env.FLEET_TRIAL_BROWSER_SECRET!;
if (!BASE || !SECRET) throw Error('FLEET_TRIAL_URL and FLEET_TRIAL_BROWSER_SECRET are required (node tests/trial/run.ts sets them)');
const OWNER = 'owner@trial.test', OUTSIDER = 'outsider@trial.test';
const test = (name: string, fn: () => Promise<void>) => nodeTest(name, {timeout: 30_000}, fn);
const as = (person: string, extra: Record<string, string> = {}) => ({authorization: 'Bearer ' + SECRET, 'x-fleet-person': person, ...extra});
async function call(method: string, path: string, init: {headers?: Record<string, string>, body?: string} = {}) {
  const r = await fetch(BASE + path, {method, redirect: 'manual', headers: init.headers, body: init.body});
  const text = await r.text(); let body: any = text; try { body = JSON.parse(text); } catch {}
  return {status: r.status, body, headers: r.headers};
}
const json = (person: string, value: unknown) => ({headers: as(person, {'content-type': 'application/json'}), body: JSON.stringify(value)});
const session = async () => (await call('POST', '/api/sessions', json(OWNER, {}))).body.sessionId as string;
// The first SSE frame of a stream, then the stream is closed.
async function firstFrame(path: string) {
  const ac = new AbortController();
  const r = await fetch(BASE + path, {headers: as(OWNER), signal: ac.signal});
  if (r.status !== 200) { const body = await r.json(); ac.abort(); return {status: r.status, body}; }
  let buf = ''; const dec = new TextDecoder();
  for await (const chunk of r.body as any) { buf += dec.decode(chunk, {stream: true}); if (buf.includes('\n\n')) break; }
  ac.abort();
  const block = buf.slice(0, buf.indexOf('\n\n'));
  return {status: 200, event: block.match(/^event: (.*)$/m)?.[1], data: JSON.parse(block.match(/^data: (.*)$/m)![1])};
}

test('FD1 every route the front door does not own reaches the TypeScript coordinator unchanged (risk: the proxy adds a credential check, drops a redirect or rewrites a body)', async () => {
  const root = await call('GET', '/');
  assert.equal(root.status, 302, 'public app route without credentials'); assert.equal(root.headers.get('location'), '/overview');
  const profiles = await call('GET', '/api/profiles', {headers: as(OWNER)});
  assert.equal(profiles.status, 200); assert.ok(Array.isArray(profiles.body.profiles));
  assert.equal((await call('GET', '/api/profiles')).status, 401);
  const missing = await call('GET', '/api/no-such-route', {headers: as(OWNER)});
  assert.equal(missing.status, 404); assert.equal(missing.body.message, 'Route GET:/api/no-such-route not found');
  assert.equal((await call('GET', '/api/sessions/' + 'x'.repeat(101), {headers: as(OWNER)})).status, 414, 'a parameter over 100 characters: Fastify answers 414, Go passes it on');
});

test('FD2 browser requests with an Origin other than the server origin are refused (risk: the front door drops the cross-origin check)', async () => {
  const id = await session();
  assert.equal((await call('GET', `/api/sessions/${id}`, {headers: as(OWNER, {origin: 'https://evil.test'})})).status, 403);
  assert.equal((await call('POST', '/api/sessions', {...json(OWNER, {}), headers: {...json(OWNER, {}).headers, origin: 'https://evil.test'}})).status, 403);
  assert.equal((await call('GET', `/api/sessions/${id}`, {headers: as(OWNER, {origin: BASE})})).status, 200, 'the server origin passes');
  const s = await fetch(BASE + '/api/stream', {headers: as(OWNER, {origin: 'https://evil.test'})});
  assert.equal(s.status, 403); await s.text();
});

test('FD3 a person names only projects they are granted, by body or query (risk: a scoped person reaches project content outside row-level security)', async () => {
  const id = await session();
  const r = await call('GET', `/api/sessions/${id}?projectId=secret`, {headers: as(OUTSIDER)});
  assert.equal(r.status, 404); assert.equal(r.body.message, 'Unknown project');
  assert.equal((await call('POST', '/api/sessions', json(OUTSIDER, {projectId: 'secret'}))).status, 404);
  assert.equal((await call('GET', `/api/executions/${randomUUID()}/events?projectId=secret`, {headers: as(OUTSIDER)})).body.message, 'Unknown project');
  // Today a person without grants or the architect role cannot write a null-project session: row-level security, 403.
  const own = await call('POST', '/api/sessions', json(OUTSIDER, {}));
  assert.equal(own.status, 403, 'no project named: the grant check passes, row-level security refuses'); assert.equal(own.body.message, 'Not in your scope');
  assert.equal((await call('GET', `/api/sessions/${id}?projectId=secret`, {headers: as(OWNER)})).status, 200, 'the owner sees every project');
});

test('FD4 a session reads back with its whole view, an unknown one with Fastify\'s error shape (risk: the Go read drops fields the operator app reads)', async () => {
  const id = await session();
  const s = (await call('GET', `/api/sessions/${id}`, {headers: as(OWNER)})).body;
  for (const k of ['queued', 'questions', 'handoffs', 'transitions', 'forks', 'delegations', 'messages', 'executions']) assert.deepEqual(s[k], [], k);
  assert.equal(s.id, id); assert.equal(s.sessionId, id); assert.equal(typeof s.createdAt, 'number');
  const created = (await call('POST', '/api/sessions', json(OWNER, {}))).body;
  assert.deepEqual(Object.keys(created).sort(), ['createdAt', 'directedBy', 'id', 'sessionId']);
  const missing = await call('GET', `/api/sessions/${randomUUID()}`, {headers: as(OWNER)});
  assert.deepEqual(missing.body, {statusCode: 404, error: 'Not Found', message: 'Not found'});
  assert.equal((await call('GET', `/api/executions/${randomUUID()}/events`, {headers: as(OWNER)})).status, 404);
});

test('FD5 the stream cursor is read as Number() reads it; an invalid one is 400 with {error} (risk: a client resume URL that works today is refused)', async () => {
  for (const bad of ['abc', '1.5', '-1', '1e300', '9007199254740992'])
    assert.deepEqual(await firstFrame('/api/stream?cursor=' + bad), {status: 400, body: {error: 'Invalid cursor'}}, bad);
  assert.deepEqual(await firstFrame('/api/stream?cursor=%205%20'), {status: 200, event: 'ready', data: {cursor: 5}});
  assert.deepEqual(await firstFrame('/api/stream?cursor=5e0'), {status: 200, event: 'ready', data: {cursor: 5}});
  assert.deepEqual(await firstFrame('/api/stream?cursor=0x5'), {status: 200, event: 'ready', data: {cursor: 5}});
  const head = await firstFrame('/api/stream?cursor=');
  assert.equal(head.event, 'ready'); assert.ok(Number.isSafeInteger(head.data.cursor), 'an empty cursor starts at the head');
});

test('FD6 request bodies are parsed as Fastify parses them (risk: the front door accepts or refuses a body differently)', async () => {
  const h = as(OWNER);
  assert.equal((await call('POST', '/api/sessions', {headers: {...h, 'content-type': 'application/xml'}, body: '<x/>'})).status, 415);
  assert.equal((await call('POST', '/api/sessions', {headers: {...h, 'content-type': 'application/json'}, body: ''})).status, 400);
  assert.equal((await call('POST', '/api/sessions', {headers: {...h, 'content-type': 'application/json'}, body: '{bad'})).status, 400);
  assert.equal((await call('POST', '/api/sessions', {headers: {...h, 'content-type': 'text/plain'}, body: 'hello'})).status, 200);
  assert.equal((await call('POST', '/api/sessions', {headers: h})).status, 200, 'no body');
});
