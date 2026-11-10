// Outside tests of area B of the Go rewrite (sessions and conversation). HTTP only; they run unchanged against the TypeScript
// coordinator and the Go port: FLEET_TRIAL_TESTS=tests/go/b.test.ts node tests/trial/run.ts (FLEET_TRIAL_SERVER selects Go).
// Each test names its requirement and the defect it guards against.
import {test} from 'node:test';
import assert from 'node:assert/strict';
import {randomUUID} from 'node:crypto';

const BASE = process.env.FLEET_TRIAL_URL!, SECRET = process.env.FLEET_TRIAL_BROWSER_SECRET!;
const OWNER = 'owner@trial.test', OUTSIDER = 'outsider@trial.test';
async function call(method: string, path: string, body?: unknown, person = OWNER) {
  const r = await fetch(BASE + path, {method, headers: {authorization: 'Bearer ' + SECRET, 'x-fleet-person': person, ...(body === undefined ? {} : {'content-type': 'application/json'})}, body: body === undefined ? undefined : JSON.stringify(body)});
  const text = await r.text(); let parsed: any = text; try { parsed = JSON.parse(text); } catch {}
  return {status: r.status, body: parsed};
}
const ok = (r: {status: number, body: any}) => { assert.equal(r.status, 200, JSON.stringify(r.body)); return r.body; };
const op = (name: string, b: unknown, person = OWNER) => call('POST', '/api/ops/' + name, b, person);
const session = async () => ok(await call('POST', '/api/sessions', {})).sessionId as string;
const send = (sid: string, text: string, requestId = randomUUID()) => call('POST', `/api/sessions/${sid}/messages`, {requestId, text});

test('B1 queue: a message to a busy session is queued in order, never refused or started twice', async () => {
  // Risk: a second turn starts while one runs (two executions on one native session) or the message is lost.
  const sid = await session();
  const first = ok(await send(sid, 'one'));
  const second = ok(await send(sid, 'two')), third = ok(await send(sid, 'three'));
  assert.equal(first.state, 'queued');
  assert.deepEqual([second.queued, second.position, third.position], [true, 1, 2]);
  const s = ok(await call('GET', '/api/sessions/' + sid));
  assert.equal(s.executions.length, 1);
  assert.deepEqual(s.queued.map((q: any) => q.text), ['two', 'three']);
});

test('B2 idempotent submit: a repeated requestId returns the same execution; other text is a conflict', async () => {
  // Risk: a client retry runs the turn twice, or a reused id silently drops the new text.
  const sid = await session(), rid = randomUUID();
  const a = ok(await send(sid, 'hello', rid)), b = ok(await send(sid, 'hello', rid));
  assert.equal(a.id, b.id);
  assert.equal((await send(sid, 'other', rid)).status, 409);
  assert.equal((await call('POST', `/api/sessions/${sid}/messages`, {requestId: randomUUID(), text: 'x', extra: 1})).status, 400);
});

test('B3 grants: a person without a project grant sees no project and cannot read it', async () => {
  // Risk: project settings and context (outside row-level security) leak to people without a grant.
  const own = ok(await call('GET', '/api/projects'));
  assert.ok(own.projects.some((p: any) => p.id === 'hello-world'));
  assert.equal(ok(await call('GET', '/api/projects', undefined, OUTSIDER)).projects.length, 0);
  assert.equal((await call('GET', '/api/projects/hello-world', undefined, OUTSIDER)).status, 404);
  assert.equal((await op('project_context', {projectId: 'hello-world'}, OUTSIDER)).status, 404);
});

test('B4 task states: state changes only through allowed transitions and the list counts follow', async () => {
  // Risk: an operator action moves a task into an impossible state, or the list counters disagree with the rows.
  const t = ok(await op('task_create', {title: 'B4 ' + randomUUID()}));
  assert.equal(t.state, 'queued');
  assert.equal(ok(await op('task_update', {taskId: t.id, action: 'pause'})).state, 'paused');
  assert.equal((await op('task_update', {taskId: t.id, action: 'pause'})).status, 409);
  assert.equal(ok(await op('task_update', {taskId: t.id, action: 'resume'})).state, 'queued');
  const page = ok(await op('task_page', {filter: {search: t.title}}));
  assert.deepEqual(page.tasks.map((x: any) => x.id), [t.id]);
  assert.equal(page.counts.queued, 1);
});

test('B5 thread: a session thread pages its execution boundary and the operator message by block key', async () => {
  // Risk: the conversation view misses or reorders turns, or a page cursor skips a block.
  const sid = await session();
  const e = ok(await send(sid, 'thread text'));
  const page = ok(await call('GET', `/api/threads/session/${sid}/entries?limit=10`));
  assert.deepEqual(page.entries.map((b: any) => b.kind), ['boundary', 'text']);
  assert.equal(page.entries[0].executionId, e.id);
  assert.equal(page.entries[1].markdown, 'thread text');
  assert.equal(page.total, 2);
  const after = ok(await call('GET', `/api/threads/session/${sid}/entries?limit=10&after=${page.first}`));
  assert.deepEqual(after.entries.map((b: any) => b.key), [page.last]);
  const md = await fetch(BASE + `/api/threads/session/${sid}/export`, {headers: {authorization: 'Bearer ' + SECRET, 'x-fleet-person': OWNER}});
  assert.match(await md.text(), /\*\*You:\*\*\n\nthread text/);
  assert.equal((await call('GET', `/api/threads/session/${randomUUID()}/entries?limit=1`)).status, 404);
});

test('B6 ask and reply: an answer reaches the asking session as its next message', async () => {
  // Risk: an agent waits forever because the reply is recorded but never delivered.
  const sid = await session();
  const e = ok(await send(sid, 'start'));
  const q = ok(await op('ask', {executionId: e.id, requestId: 'q1', question: 'Which colour?'}));
  assert.equal(q.state, 'open');
  const r = ok(await op('reply', {questionId: q.id, answer: 'blue'}));
  assert.equal(r.state, 'answered');
  assert.ok(r.delivery.queueId, 'the asker is busy: the reply is queued');
  const s = ok(await call('GET', '/api/sessions/' + sid));
  assert.match(s.queued[0].text, /blue/);
  assert.equal((await op('reply', {questionId: q.id, answer: 'red'})).status, 409);
});

test('B7 tools: the operation table is served as MCP tools', async () => {
  // Risk: agents and the app lose tools they call by name.
  const list = ok(await call('POST', '/api/tools', {jsonrpc: '2.0', id: 1, method: 'tools/list'}));
  const names = list.result.tools.map((t: any) => t.name);
  for (const n of ['message_send', 'session_get', 'thread_entries', 'task_page', 'sessions_list']) assert.ok(names.includes(n), n);
  const sid = await session();
  const got = ok(await call('POST', '/api/tools', {jsonrpc: '2.0', id: 2, method: 'tools/call', params: {name: 'session_get', arguments: {sessionId: sid}}}));
  assert.equal(JSON.parse(got.result.content[0].text).sessionId, sid);
});
