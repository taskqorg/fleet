// Outside tests of the trial slice (docs/designs/fleet-stack-trial.md#trial-slice-l1). They reach the coordinator only over HTTP
// and SSE at FLEET_TRIAL_URL and know nothing of its language, process or database, so they run unchanged against the
// TypeScript coordinator and against the Go and Rust ports. Run them with `node tests/trial/run.ts`.
// Each test names its requirement (SL1-SL11 in the specification) and the risk it guards.
import {after, test as nodeTest} from 'node:test';
import assert from 'node:assert/strict';
import {randomUUID} from 'node:crypto';

const BASE = process.env.FLEET_TRIAL_URL!, SECRET = process.env.FLEET_TRIAL_BROWSER_SECRET!;
if (!BASE || !SECRET) throw Error('FLEET_TRIAL_URL and FLEET_TRIAL_BROWSER_SECRET are required (node tests/trial/run.ts sets them)');
const OWNER = 'owner@trial.test', OUTSIDER = 'outsider@trial.test';
type Reply = {status: number, body: any};
// A failed assertion must not leave a stream open: the test process would never exit.
const openStreams = new Set<AbortController>();
after(() => { for (const ac of openStreams) ac.abort(); });
const test = (name: string, fn: () => Promise<void>) => nodeTest(name, {timeout: 30_000}, fn);

async function call(method: string, path: string, body?: unknown, headers: Record<string, string> = {}): Promise<Reply> {
  const r = await fetch(BASE + path, {method, redirect: 'manual', headers: {...(body === undefined ? {} : {'content-type': 'application/json'}), ...headers}, body: body === undefined ? undefined : JSON.stringify(body)});
  const text = await r.text();
  let parsed: any = text; try { parsed = JSON.parse(text); } catch {}
  return {status: r.status, body: parsed};
}
const as = (person: string) => ({authorization: 'Bearer ' + SECRET, 'x-fleet-person': person});
const browser = (method: string, path: string, body?: unknown, person = OWNER) => call(method, path, body, as(person));
const ok = (r: Reply) => { assert.equal(r.status, 200, JSON.stringify(r.body)); return r.body; };

// One registered host per test file run, plus a second host for cross-host checks.
const hosts = new Map<string, string>();
async function host(id: string) {
  if (!hosts.has(id)) hosts.set(id, ok(await browser('POST', '/api/ops/hosts_register', {id})).secret);
  const secret = hosts.get(id)!;
  return (path: string, body: unknown, headers: Record<string, string> = {}) => call('POST', path, body, {authorization: 'Bearer ' + secret, ...headers});
}
// The coordinator's default host is trial-host (FLEET_HOST_ID in run.ts): its poll claims a queued execution.
const session = async () => ok(await browser('POST', '/api/sessions', {})).sessionId as string;
async function started() {
  const h = await host('trial-host');
  // Receipt/event transport fixtures use explicit placement; asynchronous dispatch may choose either registered host.
  const started = ok(await browser('POST', '/api/ops/session_start', {requestId: randomUUID(), text: 'hello', projectId: 'hello-world', profileId: 'pm', hostId: 'trial-host'}));
  const sessionId = started.sessionId;
  const execution = {id: started.executionId};
  const {commands} = ok(await h('/api/hosts/poll', {platform: 'darwin', capabilities: [], stores: [], problems: []}));
  const command = commands.find((c: any) => c.executionId === execution.id);
  assert.ok(command, 'the poll delivers the start command');
  return {h, sessionId, executionId: execution.id as string, commandId: command.id as string};
}

// SSE: an open stream, its parsed events, and a way to wait for one.
async function stream(person: string, cursor?: number) {
  const ac = new AbortController(); openStreams.add(ac);
  const r = await fetch(BASE + '/api/stream' + (cursor === undefined ? '' : '?cursor=' + cursor), {headers: as(person), signal: ac.signal});
  const events: {id?: string, event?: string, data: any}[] = [];
  let buf = '', waiters: (() => void)[] = [];
  (async () => { const dec = new TextDecoder(); try { for await (const chunk of r.body as any) { buf += dec.decode(chunk, {stream: true});
    for (let i; (i = buf.indexOf('\n\n')) >= 0;) { const block = buf.slice(0, i); buf = buf.slice(i + 2); const e: any = {};
      for (const line of block.split('\n')) { const m = line.match(/^(id|event|data): ?(.*)$/); if (m) e[m[1]] = m[1] === 'data' ? JSON.parse(m[2]) : m[2]; }
      if ('data' in e) { events.push(e); for (const w of waiters.splice(0)) w(); } } } } catch {} })();
  const until = async (pred: (e: typeof events) => boolean, ms = 5000) => { const end = Date.now() + ms;
    while (!pred(events)) { if (Date.now() > end) return false; await new Promise<void>(res => { waiters.push(res); setTimeout(res, 100); }); } return true; };
  return {response: r, events, until, close: () => ac.abort()};
}

test('SL1 browser requests need the Bearer browser secret and a known person (risk: authorization header parsed loosely)', async () => {
  assert.equal((await call('GET', '/api/sessions/x')).status, 401, 'no credentials');
  assert.equal((await call('GET', '/api/sessions/x', undefined, {authorization: 'Basic ' + SECRET, 'x-fleet-person': OWNER})).status, 401, 'another scheme');
  assert.equal((await call('GET', '/api/sessions/x', undefined, {authorization: 'Bearer ' + SECRET.slice(0, -1), 'x-fleet-person': OWNER})).status, 401, 'a prefix of the secret');
  assert.equal((await call('GET', '/api/sessions/x', undefined, {authorization: 'Bearer ' + SECRET})).status, 401, 'no person');
  assert.equal((await call('GET', '/api/sessions/x', undefined, as('nobody@trial.test'))).status, 403, 'unknown person');
});

test('SL2 host requests are answered 401 for an unknown secret, never a redirect, and carry no person or foreign host (risk: executor outbox drops rows on any other 4xx; host impersonation)', async () => {
  const h = await host('trial-host'); await host('other-host');
  const poll = {platform: 'darwin', capabilities: [], stores: [], problems: []};
  for (const auth of ['Bearer wrong', 'Bearer ' + SECRET, '']) {
    const r = await call('POST', '/api/hosts/poll', poll, auth ? {authorization: auth} : {});
    assert.equal(r.status, 401, `poll with ${auth ? auth.slice(0, 12) + '…' : 'no header'}: the executor keeps outbox rows only on 3xx/401`);
  }
  assert.equal((await call('POST', '/api/hosts/events', {executionId: 'x', events: []}, {authorization: 'Bearer wrong'})).status, 401, 'events with a wrong secret');
  assert.equal((await h('/api/hosts/poll', poll, {'x-fleet-person': OWNER})).status, 403, 'a host request with a person');
  assert.equal((await h('/api/hosts/poll', {...poll, hostId: 'other-host'})).status, 403, 'a body hostId of another host');
});

test('SL3 a created session reads back; an unknown one is 404 (risk: session row not committed or wrong shape)', async () => {
  const id = await session();
  const s = ok(await browser('GET', `/api/sessions/${id}`));
  assert.equal(s.sessionId, id); assert.equal(s.directedBy, OWNER);
  assert.deepEqual([s.messages, s.executions], [[], []]);
  assert.equal((await browser('GET', `/api/sessions/${randomUUID()}`)).status, 404);
});

test('SL4 a message queues one execution with no host yet; a repeated requestId returns the same execution; a bad body changes nothing (risk: duplicate work on client retry)', async () => {
  const id = await session();
  const e = ok(await browser('POST', `/api/sessions/${id}/messages`, {requestId: 'r1', text: 'hello'}));
  assert.equal(e.state, 'preparing'); assert.equal(e.hostId ?? null, null, 'the host is chosen at a poll'); assert.equal(e.sessionId, id);
  assert.equal(ok(await browser('POST', `/api/sessions/${id}/messages`, {requestId: 'r1', text: 'hello'})).id, e.id);
  for (const bad of [{text: 'x'}, {requestId: 'r2'}, {requestId: 'r2', text: ''}, {requestId: 'r2', text: 'x', extra: 1}, []])
    assert.equal((await browser('POST', `/api/sessions/${id}/messages`, bad)).status, 400, JSON.stringify(bad));
  assert.equal((await browser('POST', `/api/sessions/${randomUUID()}/messages`, {requestId: 'r1', text: 'x'})).status, 404);
  const s = ok(await browser('GET', `/api/sessions/${id}`));
  assert.deepEqual(s.executions.map((x: any) => x.id), [e.id]);
  assert.deepEqual(s.messages.map((m: any) => [m.requestId, m.text, m.executionId]), [['r1', 'hello', e.id]]);
});

test('SL5 a poll of the default host claims a queued execution and delivers its start command; a poll delivers commands of the polling host only (risk: work sent to the wrong machine)', async () => {
  const {executionId, commandId} = await started();
  const other = await host('other-host');
  const {commands} = ok(await other('/api/hosts/poll', {platform: 'linux', capabilities: [], stores: [], problems: []}));
  assert.ok(!commands.some((c: any) => c.id === commandId || c.executionId === executionId), 'another host never gets this command');
  const h = await host('trial-host');
  const again = ok(await h('/api/hosts/poll', {platform: 'darwin', capabilities: [], stores: [], problems: []})).commands.find((c: any) => c.id === commandId);
  assert.equal(again?.kind, 'start'); assert.equal(again?.payload?.text, 'hello'); assert.equal(again?.status, 'pending'); assert.equal(again?.hostId, 'trial-host');
});

test('SL6 receipts move the execution forward only; a repeated terminal receipt is a no-op; a foreign command is 403 (risk: outbox retries and reordering corrupt state)', async () => {
  const {h, sessionId, executionId, commandId} = await started();
  const receipt = (status: string, extra: object = {}) => h('/api/hosts/receipts', {commandId, executionId, status, ...extra});
  const state = async () => ok(await browser('GET', `/api/sessions/${sessionId}`)).executions.find((x: any) => x.id === executionId).state;
  ok(await receipt('accepted')); ok(await receipt('started')); assert.equal(await state(), 'running');
  assert.equal((await receipt('accepted')).status, 409, 'regression');
  assert.equal((await receipt('bogus')).status, 400, 'invalid status');
  ok(await receipt('failed', {error: 'boom'})); assert.equal(await state(), 'failed');
  assert.deepEqual(ok(await receipt('failed', {error: 'boom'})), {ok: true}, 'outbox retry of the terminal receipt');
  assert.equal((await receipt('completed')).status, 409, 'a different terminal status');
  assert.equal(await state(), 'failed');
  const other = await host('other-host');
  assert.equal((await other('/api/hosts/receipts', {commandId, executionId, status: 'started'})).status, 403, 'another host');
});

test('SL7 host events are stored once, in seq order, with their data unchanged; a conflicting or invalid batch stores nothing (risk: renamed event field, partial batch)', async () => {
  const {h, executionId} = await started();
  const ev = (id: string, seq: number, data: object) => ({id, seq, type: 'item.completed', timestamp: 1, data});
  const batch = [ev('c', 2, {item: {type: 'agent_message', text: 'two'}}), ev('a', 0, {item: {type: 'agent_message', text: 'zero'}, fleetNormalized: {kind: 'message'}}), ev('b', 1, {item: {type: 'command_execution', command: 'ls', status: 'completed'}, extra: {nested: [1, 2]}})];
  ok(await h('/api/hosts/events', {executionId, events: batch}));
  ok(await h('/api/hosts/events', {executionId, events: batch}));
  const read = async () => ok(await browser('GET', `/api/executions/${executionId}/events`)).events;
  const got = await read();
  assert.deepEqual(got.map((e: any) => [e.seq, e.type, e.data]), [...batch].sort((a, b) => a.seq - b.seq).map(e => [e.seq, e.type, e.data]));
  assert.equal((await h('/api/hosts/events', {executionId, events: [ev('a', 0, {item: {type: 'agent_message', text: 'changed'}})]})).status, 409, 'same id, other content');
  assert.equal((await h('/api/hosts/events', {executionId, events: [ev('d', 3, {item: {text: 'ok'}}), ev('e', 1, {item: {text: 'seq taken'}})]})).status, 409, 'seq collision');
  assert.equal((await h('/api/hosts/events', {executionId, events: [ev('f', 4, {}), {id: '', seq: 5, type: 'x', timestamp: 1, data: {}}]})).status, 400, 'invalid identity');
  assert.deepEqual((await read()).map((e: any) => e.seq), [0, 1, 2], 'no event of a refused batch is stored');
  const other = await host('other-host');
  assert.equal((await other('/api/hosts/events', {executionId, events: [ev('g', 9, {})]})).status, 403, 'another host');
});

test('SL8 the stream sends ready with a cursor, then each committed change with id = seq; a resume from a cursor replays exactly the changes after it (risk: lost or repeated live updates)', async () => {
  const s1 = await stream(OWNER);
  assert.equal(s1.response.status, 200); assert.match(s1.response.headers.get('content-type') ?? '', /^text\/event-stream/);
  assert.ok(await s1.until(e => e.some(x => x.event === 'ready')), 'ready');
  const cursor = s1.events.find(x => x.event === 'ready')!.data.cursor as number;
  assert.ok(Number.isSafeInteger(cursor));
  const a = await session(), b = await session();
  assert.ok(await s1.until(e => [a, b].every(id => e.some(x => x.data?.id === id))), 'both sessions arrive live');
  const live = s1.events.filter(x => !x.event);
  s1.close();
  for (const x of live) assert.equal(x.id, String(x.data.seq), 'SSE id is the change seq');
  const seqs = live.map(x => x.data.seq);
  assert.deepEqual(seqs, [...seqs].sort((p, q) => p - q), 'changes arrive in order');
  assert.ok(seqs.every(x => x > cursor));
  const at = live.find(x => x.data.id === a)!.data.seq;
  const s2 = await stream(OWNER, at);
  assert.ok(await s2.until(e => e.some(x => x.data?.id === b)), 'resume delivers the change after the cursor');
  s2.close();
  const resumed = s2.events.filter(x => !x.event).map(x => x.data);
  assert.ok(resumed.every(x => x.seq > at), 'nothing at or before the cursor is replayed');
  assert.ok(!resumed.some(x => x.id === a));
  assert.equal(s2.events[0].event, 'ready'); assert.equal(s2.events[0].data.cursor, at);
  assert.equal((await call('GET', '/api/stream?cursor=-1', undefined, as(OWNER))).status, 400);
});

test('SL9 a person without grants sees no owner row, by read or by stream (risk: row-level security scope not applied)', async () => {
  const s = await stream(OUTSIDER);
  assert.ok(await s.until(e => e.some(x => x.event === 'ready')));
  const id = await session();
  assert.equal((await browser('GET', `/api/sessions/${id}`, undefined, OUTSIDER)).status, 404);
  const owner = await stream(OWNER, 0);
  assert.ok(await owner.until(e => e.some(x => x.data?.id === id)), 'the owner sees the change');
  owner.close();
  await new Promise(r => setTimeout(r, 300));
  s.close();
  assert.deepEqual(s.events.filter(x => !x.event), [], 'the outsider stream carries no change');
});

test('SL10 concurrent messages: sessions written at once each get their execution, one requestId sent at once gives one, none fails (risk: transaction and lock layer under concurrency)', async () => {
  const ids = await Promise.all(Array.from({length: 16}, () => session()));
  const distinct = await Promise.all(ids.map((id, i) => browser('POST', `/api/sessions/${id}/messages`, {requestId: 'd' + i, text: 'm' + i})));
  assert.deepEqual(distinct.map(r => r.status), distinct.map(() => 200));
  assert.equal(new Set(distinct.map(r => r.body.id)).size, 16);
  const id = await session();
  const same = await Promise.all(Array.from({length: 8}, () => browser('POST', `/api/sessions/${id}/messages`, {requestId: 'same', text: 'x'})));
  assert.deepEqual(same.map(r => r.status), same.map(() => 200));
  assert.equal(new Set(same.map(r => r.body.id)).size, 1);
  const s = ok(await browser('GET', `/api/sessions/${id}`));
  assert.equal(s.executions.length, 1); assert.equal(s.messages.length, 1);
});

test('SL11 concurrent event batches of one execution all land, with no seq lost (risk: execution-scope lock and serialization retry)', async () => {
  const {h, executionId} = await started();
  const rs = await Promise.all(Array.from({length: 12}, (_, i) => h('/api/hosts/events', {executionId, events: [{id: 'p' + i, seq: i, type: 'item.completed', timestamp: i, data: {item: {text: String(i)}}}]})));
  assert.deepEqual(rs.map(r => r.status), rs.map(() => 200));
  assert.deepEqual(ok(await browser('GET', `/api/executions/${executionId}/events`)).events.map((e: any) => e.seq), Array.from({length: 12}, (_, i) => i));
});
