// Entity store (docs/designs/fleet-view-urls-and-list-path.md §2) against a fake /api/ops: paging, ordering, clearing.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { registerAccessValidator, registerAccessBaseline, ensureAccessGeneration } from '../../app/src/access.ts';
registerAccessValidator(async () => ({ identityKey: 'unit@fixture.test', grantsVersion: 'one' }));
registerAccessBaseline(async () => ({ cursor: 0, activate() {}, dispose() {} }));
await ensureAccessGeneration();
import { loadList, nextPage, applyChanges, peek } from '../../app/src/store.ts';
import { clearAll } from '../../app/src/offline.ts';
import { op } from '../../app/src/api.ts';
import { nextUrl } from '../../app/src/url.ts';

type Call = { name: string; body: any };
let calls: Call[] = [];
let answer: (name: string, body: any) => Promise<any> = async () => ({});
const realFetch = async (url: string, init: any) => {
  const name = url.split('/api/ops/')[1], body = JSON.parse(init.body);
  calls.push({ name, body });
  const { httpStatus: status = 200, ...data } = await answer(name, body) ?? {};
  return new Response(JSON.stringify(data), { status, headers: { 'content-type': 'application/json' } });
};
(globalThis as any).fetch = realFetch;
const row = (id: string, title = id) => ({ id, title, sortKey: ['1', -Number(id.replace(/\D/g, '') || 0), id] });
const page = (rows: any[], nextAfter: unknown = null) => ({ tasks: rows, counts: { queued: rows.length }, total: rows.length, groups: [], nextAfter });
const deferred = () => { let ok!: (v: any) => void; const p = new Promise(r => ok = r); return { p, ok }; };

test('review #33-1: paging uses the shown list\'s own params and waits while that list is loading', async () => {
  await clearAll();
  const A = { view: 'tasks' as const, state: ['failed'] }, B = { view: 'tasks' as const, state: ['queued'] };
  answer = async (_n, b) => b.after ? page([row('t9')]) : page([row('t1')], ['1', -1, 't1']);
  await loadList('tasks', 'A', A);
  const slow = deferred(); answer = async (_n, b) => b.filter.state[0] === 'queued' ? slow.p : page([row('t2')]);
  const pendingB = loadList('tasks', 'B', B); await new Promise(r => setImmediate(r)); // a new filter is pending while A is on screen
  calls = []; answer = async (_n, b) => b.filter.state[0] === 'queued' ? slow.p : page([row('t9')]);
  await nextPage('A');
  assert.deepEqual(calls.map(c => c.body.filter.state), [['failed']]);
  assert.deepEqual(peek.list('A')!.ids, ['t1', 't9']);
  slow.ok(page([row('t3')])); await pendingB;
  // While A itself is loading, nextPage does nothing.
  const slowA = deferred(); answer = async () => slowA.p; const reloading = loadList('tasks', 'A', A);
  calls = []; await nextPage('A'); assert.equal(calls.length, 0); // no page request while A reloads
  slowA.ok(page([row('t1')], ['1', -1, 't1'])); await reloading;
});

test('a refused page cursor reloads the first page once, keeping rows, and pages on; a second refusal shows the error', async () => {
  await clearAll();
  const V = { view: 'conversations' as const }, refused = { httpStatus: 400, message: 'Invalid after' };
  answer = async () => page([row('t1'), row('t2')], ['old', 't2']); // a cursor of an older shape
  await loadList('sessions', 'C', V);
  const slow = deferred(); calls = [];
  answer = async (_n, b) => b.after?.[0] === 'old' ? refused : b.after ? page([row('t4')]) : slow.p;
  const paging = nextPage('C'); await new Promise(r => setImmediate(r));
  assert.deepEqual(calls.map(c => c.body.after?.[0] ?? null), ['old', null], 'the refused cursor is followed by one first-page request');
  assert.deepEqual(peek.list('C')!.ids, ['t1', 't2'], 'rows stay on screen while the first page reloads');
  assert.equal(peek.list('C')!.error, null);
  slow.ok(page([row('t1'), row('t3')], ['g', 's', 't3'])); await paging;
  assert.deepEqual(calls.map(c => c.body.after?.[0] ?? null), ['old', null, 'g'], 'the person is at the end: it pages on with the fresh cursor');
  assert.deepEqual(peek.list('C')!.ids, ['t1', 't3', 't4']); assert.equal(peek.list('C')!.error, null);
  // A server that refuses its own fresh cursor: one reload per list load, then the error, rows kept.
  answer = async (_n, b) => b.after ? refused : page([row('t1'), row('t3')], ['g', 's', 't3']);
  await loadList('sessions', 'C', V); calls = []; await nextPage('C');
  assert.deepEqual(calls.map(c => !!c.body.after), [true, false, true], 'no second reload in the same list load');
  assert.equal(peek.list('C')!.error, 'Invalid after');
  assert.deepEqual(peek.list('C')!.ids, ['t1', 't3']);
  calls = []; await nextPage('C'); assert.deepEqual(calls.map(c => !!c.body.after), [true], 'scrolling again does not reload');
});

test('a recovery reload that steps aside for a newer membership answer still drops the refused cursor', async () => {
  await clearAll();
  const V = { view: 'conversations' as const };
  answer = async () => page([row('t1')], ['old', 't1']);
  peek.mount('sessions', 'E', V); await loadList('sessions', 'E', V);
  const member = deferred(), first = deferred();
  answer = async (n, b) => n === 'list_membership' ? member.p : b.after?.[0] === 'old' ? { httpStatus: 400, message: 'Invalid after' } : b.after ? page([row('t5')]) : first.p;
  const changing = applyChanges([{ seq: 10, kind: 'session', id: 't2', taskId: null, sessionId: 't2', hostId: null, op: 'I' } as any]);
  await new Promise(r => setImmediate(r));
  const paging = nextPage('E'); await new Promise(r => setImmediate(r));
  member.ok({ lists: [{ rows: [row('t2')], counts: { queued: 2 }, groups: [] }] }); await changing;
  first.ok(page([row('t1')], ['g', 's', 't1'])); await paging;
  assert.ok(peek.list('E')!.ids.includes('t2'), 'the newer membership answer is kept');
  assert.ok(peek.list('E')!.ids.includes('t5'), 'paged on with the fresh cursor'); assert.equal(peek.list('E')!.error, null);
});

test('a page error clears with the next page; a failed first-page load keeps its error', async () => {
  await clearAll();
  const V = { view: 'conversations' as const };
  answer = async () => page([row('t1')], ['g', 's', 't1']);
  await loadList('sessions', 'F', V);
  answer = async () => ({ httpStatus: 500, message: 'busy' }); await nextPage('F');
  assert.equal(peek.list('F')!.error, 'busy');
  answer = async () => page([row('t2')], ['g', 's', 't2']); await nextPage('F');
  assert.equal(peek.list('F')!.error, null);
  answer = async (_n, b) => b.after ? page([row('t3')]) : { httpStatus: 500, message: 'down' };
  await loadList('sessions', 'F', V); await nextPage('F');
  assert.equal(peek.list('F')!.error, 'down', 'a page does not hide the failed load from the back-online reload');
});

test('a refused cursor while a reload runs leaves the cursor to that reload', async () => {
  await clearAll();
  const V = { view: 'conversations' as const };
  answer = async () => page([row('t1')], ['old', 't1']);
  await loadList('sessions', 'D', V);
  const refusal = deferred(), slow = deferred(); calls = [];
  answer = async (_n, b) => b.after ? refusal.p : slow.p;
  const paging = nextPage('D'); await new Promise(r => setImmediate(r));
  const reloading = loadList('sessions', 'D', V); await new Promise(r => setImmediate(r));
  refusal.ok({ httpStatus: 400, message: 'Invalid after' }); await paging;
  assert.equal(calls.length, 2, 'no extra first-page request on top of the running reload');
  assert.equal(peek.list('D')!.error, null);
  slow.ok(page([row('t1')], ['g', 's', 't1'])); await reloading;
});

test('review #33-3: an older page answer never overwrites a newer record; changes during a load are asked after it lands', async () => {
  await clearAll();
  const P = { view: 'tasks' } as const;
  answer = async () => page([row('t1', 'old')]);
  peek.mount('tasks', 'L', P); await loadList('tasks', 'L', P);
  answer = async n => n === 'list_membership' ? { lists: [{ rows: [row('t1', 'new')], counts: { queued: 1 }, groups: [] }] } : page([]);
  await applyChanges([{ seq: 10, kind: 'task', id: 't1', taskId: 't1', sessionId: null, hostId: null, op: 'U' }]);
  assert.equal(peek.record('t1')!.row.title, 'new');
  const slow = deferred(); answer = async n => n === 'list_membership' ? { lists: [{ rows: [row('t1', 'newer'), row('t2')], counts: { queued: 2 }, groups: [] }] } : slow.p;
  const loading = loadList('tasks', 'L', P);
  calls = [];
  await applyChanges([{ seq: 11, kind: 'task', id: 't2', taskId: 't2', sessionId: null, hostId: null, op: 'I' }]);
  assert.equal(calls.filter(c => c.name === 'list_membership').length, 0, 'not asked while the page is loading');
  slow.ok(page([row('t1', 'old')])); await loading;
  assert.notEqual(peek.record('t1')!.row.title, 'old', 'the page answer (cursor 0) did not overwrite the record from change 10');
  assert.equal(calls.filter(c => c.name === 'list_membership').length, 1, 'asked once the page landed');
  assert.ok(peek.list('L')!.ids.includes('t2'));
});

test('review #33-4: clearing stored data also forgets lists and records in memory', async () => {
  answer = async () => page([row('t5')]);
  await loadList('tasks', 'M', { view: 'tasks' });
  assert.ok(peek.list('M') && peek.record('t5'));
  await clearAll();
  assert.equal(peek.list('M'), null); assert.equal(peek.record('t5'), null);
});

test('review #33-6: an abort while the answer body is read is an abort, not an empty success', async () => {
  const ctrl = new AbortController();
  const saved = (globalThis as any).fetch;
  (globalThis as any).fetch = async () => ({ ok: true, status: 200, type: 'basic', json: () => { ctrl.abort(); return Promise.reject(new Error('body aborted')); } });
  await assert.rejects(op('task_page', {}, { signal: ctrl.signal }), (e: any) => e.message === 'aborted');
  (globalThis as any).fetch = saved;
});

test('review #33-2/5: URL changes are built from the URL at the time of the change', () => {
  // A debounced search that fires after a grouping click keeps the grouping (the current URL is read at fire time).
  assert.equal(nextUrl('/tasks?group=agent&state=failed', 'tasks', { q: 'hero' }), '/tasks?group=agent&q=hero&state=failed');
  // Creating or forking a conversation focuses it without dropping the view's filters.
  assert.equal(nextUrl('/conversations?state=running&q=x', 'conversations', { focus: 's1' }), '/conversations/s1?q=x&state=running');
});

// A small in-memory IndexedDB, enough for offline.ts (open, one object store, get/put/getAll/getAllKeys/delete).
function fakeIndexedDB() {
  const data = new Map<string, unknown>();
  const req = (f: () => unknown) => { const r: any = {}; setTimeout(() => { r.result = f(); r.onsuccess?.(); }); return r; };
  const store = { get: (k: string) => req(() => data.get(k)), put: (v: unknown, k: string) => req(() => { data.set(k, v); }), getAll: () => req(() => [...data.values()]),
    getAllKeys: () => req(() => [...data.keys()]), delete: (k: string) => req(() => { data.delete(k); }) };
  const db = { transaction: () => { const t: any = { objectStore: () => store, abort() { t.onabort?.(); } }; setTimeout(() => t.oncomplete?.(), 3); return t; }, createObjectStore: () => store, close() {}, onversionchange: null };
  return { data, open: () => { const r: any = {}; setTimeout(() => { r.result = db; r.onupgradeneeded?.(); r.onsuccess?.(); }); return r; },
    deleteDatabase: () => { const r: any = {}; setTimeout(() => { data.clear(); r.onsuccess?.(); }); return r; } };
}

test('review #34-1/2: after clearing, mounted lists load again and mounted docs re-read', async () => {
  answer = async () => page([row('t7')]);
  peek.mount('tasks', 'C', { view: 'tasks', state: ['queued'] }); await loadList('tasks', 'C', { view: 'tasks', state: ['queued'] });
  const gen = peek.generation(); calls = [];
  await clearAll(); await new Promise(r => setTimeout(r, 10));
  assert.ok(calls.some(c => c.name === 'task_page'), 'the mounted list was requested again');
  assert.deepEqual(peek.list('C')?.ids, ['t7']);
  assert.equal(peek.generation(), gen + 1, 'docs depend on the generation, so mounted docs re-read');
});

test('review #34-3: a failed load still shows the stored rows (offline)', async () => {
  const idb = fakeIndexedDB(); (globalThis as any).indexedDB = idb;
  try {
    answer = async () => page([row('t8', 'stored')]);
    await loadList('tasks', 'O', { view: 'tasks', q: 'offline' });
    await new Promise(r => setTimeout(r, 1100)); // offline.save writes after 1 s
    assert.ok(idb.data.has('list:v3:O'));
    await clearAll(); idb.data.set('list:v3:O', { data: { ids: ['t8'], keys: { t8: ['1', -8, 't8'] }, counts: {}, total: 1, groups: [], nextAfter: null, at: 1, rows: [row('t8', 'stored')] }, cursor: 0, at: 1 });
    answer = async () => { throw new Error('down'); };
    (globalThis as any).fetch = async () => { throw new TypeError('network'); };
    await loadList('tasks', 'O', { view: 'tasks', q: 'offline' }); await new Promise(r => setTimeout(r, 20));
    const l = peek.list('O')!;
    assert.deepEqual([l.ids, l.restored, l.error], [['t8'], true, 'offline']);
  } finally { delete (globalThis as any).indexedDB; (globalThis as any).fetch = realFetch; }
});

test('review #34-4: an older membership answer landing late does not re-add a task that left the list', async () => {
  await clearAll();
  answer = async () => page([row('t1')]);
  peek.mount('tasks', 'R', { view: 'tasks' }); await loadList('tasks', 'R', { view: 'tasks' });
  const late = deferred();
  answer = async (n, b) => n === 'list_membership' ? (b.ids.includes('t1') && calls.filter(c => c.name === 'list_membership').length === 1 ? late.p : { lists: [{ rows: [], counts: {}, groups: [] }] }) : page([]);
  calls = [];
  const older = applyChanges([{ seq: 15, kind: 'task', id: 't1', taskId: 't1', sessionId: null, hostId: null, op: 'U' }]);
  await new Promise(r => setTimeout(r, 5));
  await applyChanges([{ seq: 20, kind: 'task', id: 't1', taskId: 't1', sessionId: null, hostId: null, op: 'U' }]); // t1 left the filter
  assert.deepEqual(peek.list('R')!.ids, []);
  late.ok({ lists: [{ rows: [row('t1')], counts: { queued: 1 }, groups: [] }] }); await older;
  assert.deepEqual(peek.list('R')!.ids, [], 'the answer for change 15 arrived after the one for 20 and was ignored');
});

// Requirement: one request per prepared key, one pending refresh, last-subscriber cancellation.
// Risk: event bursts multiply requests, unchanged progress erases figures or an old revision wins.
import { acquirePrepared, preparedKey, olderPreparedVersion } from '../../app/src/store.ts';
const turn = () => new Promise<void>(resolve => setImmediate(resolve));
test('prepared scheduler shares active reads, coalesces bursts and retains unchanged figures with new progress', async () => {
  const progress = { state: 'current' as const, validatedAt: '2026-11-15T00:00:00Z', publishedAt: null, oldestPendingAt: null, pending: false, errorClass: null };
  const waiting: { ok: (value: any) => void; signal: AbortSignal; version?: string }[] = [];
  const fetcher = ({ signal, knownVersion }: { signal: AbortSignal; knownVersion?: string }) => new Promise<any>(ok => waiting.push({ ok, signal, version: knownVersion }));
  const key = preparedKey('project_resources', 'fixture'), one = acquirePrepared(key, fetcher), two = acquirePrepared(key, fetcher);
  try {
    await turn(); assert.equal(waiting.length, 1);
    for (let i = 0; i < 100; i++) one.refresh('event');
    waiting[0].ok({ kind: 'ready', projectId: 'fixture', version: 'epoch:9007199254740993', header: { executions: 7 }, progress }); await turn();
    assert.equal(waiting.length, 2); assert.equal(waiting[1].version, 'epoch:9007199254740993');
    waiting[1].ok({ kind: 'unchanged', projectId: 'fixture', version: 'epoch:9007199254740993', progress: { ...progress, pending: true, state: 'updating' } }); await turn();
    assert.equal((one.value.value.data as any).header.executions, 7); assert.equal(two.value.value.progress?.state, 'updating');
    one.refresh(); await turn(); assert.equal(waiting.length, 3);
    waiting[2].ok({ kind: 'ready', projectId: 'fixture', version: 'epoch:9007199254740992', header: { executions: 0 }, progress }); await turn();
    assert.equal((one.value.value.data as any).header.executions, 7, 'bigint revision comparison rejects older replies');
    one.refresh(); await turn(); one.release(); assert.equal(waiting[3].signal.aborted, false); two.release(); assert.equal(waiting[3].signal.aborted, true);
    waiting[3].ok({ kind: 'ready', projectId: 'fixture', version: 'epoch:9007199254740994', header: { executions: 9 }, progress }); await turn(); assert.equal(one.value.value.data, null);
  } catch (error) { one.release(); two.release(); throw error; }
  assert.equal(olderPreparedVersion('new:1', 'old:99'), false, 'a replacement epoch is distinct from revision order');
});

import { holdDoc, readDoc, clearMemory } from '../../app/src/store.ts';
const docData = (name: string) => (peek.doc(name) as any)?.data;
test('doc readers of one name send one request per task; a newer answer is never overwritten by an older one', async () => {
  const waiting: ((value: unknown) => void)[] = [];
  const fetch = () => new Promise(ok => waiting.push(ok));
  const name = 'shared-doc-' + Math.random(), focus = holdDoc(name, fetch), projects = holdDoc(name, fetch);
  try {
    await turn(); assert.equal(waiting.length, 1, 'two mounts in one task send one request');
    for (let i = 0; i < 100; i++) readDoc(name);
    await turn(); assert.equal(waiting.length, 2, 'one feed batch reaching every reader sends one request, beside the running one');
    waiting[1]({ v: 'newer' }); await turn(); assert.equal(docData(name).v, 'newer');
    waiting[0]({ v: 'older' }); await turn(); assert.equal(docData(name).v, 'newer', 'the older answer arriving later is discarded');
    focus(); focus(); readDoc(name); await turn(); assert.equal(waiting.length, 3, 'a repeated release does not drop the remaining reader');
    projects(); await turn(); waiting[2]({ v: 'after release' }); await turn();
    assert.equal(docData(name).v, 'newer', 'an answer after the last release is discarded');
    readDoc(name); await turn(); assert.equal(waiting.length, 3, 'no reads once every reader released');
  } finally { focus(); projects(); }
});

test('a stalled doc read never blocks later reads; failures show only for the latest read; clearing discards running reads', async () => {
  const waiting: { ok: (value: unknown) => void; fail: (e: unknown) => void }[] = [];
  const fetch = () => new Promise((ok, fail) => waiting.push({ ok, fail }));
  const name = 'stalled-doc-' + Math.random(), release = holdDoc(name, fetch);
  try {
    await turn(); readDoc(name); await turn(); assert.equal(waiting.length, 2, 'a change while one read is stalled still reads');
    waiting[1].ok({ v: 'fresh' }); await turn(); assert.equal(docData(name).v, 'fresh');
    waiting[0].fail(Error('stalled')); await turn(); assert.equal((peek.doc(name) as any).error, null, 'an older failure does not mark newer data failed');
    readDoc(name); await turn(); waiting[2].fail('not an Error'); await turn();
    assert.equal((peek.doc(name) as any).error, 'not an Error'); assert.equal(docData(name).v, 'fresh', 'the last data stays visible');
    readDoc(name); await turn(); readDoc(name); await turn(); waiting[4].fail(Error('newest failed')); await turn();
    waiting[3].ok({ v: 'older ok' }); await turn();
    assert.equal(docData(name).v, 'older ok'); assert.equal((peek.doc(name) as any).error, 'newest failed', 'an older answer does not hide the newest failure');
    readDoc(name); await turn(); clearMemory(); waiting[5].ok({ v: 'before clear' }); await turn();
    assert.equal(peek.doc(name), null, 'a read started before clearing does not write back');
    readDoc(name); await turn(); waiting[6].ok({ v: 'after clear' }); await turn(); assert.equal(docData(name).v, 'after clear');
  } finally { release(); }
});

test('an effect re-run (release and mount in one task) keeps the running doc read', async () => {
  const waiting: ((value: unknown) => void)[] = [];
  const fetch = () => new Promise(ok => waiting.push(ok));
  const name = 'remount-doc-' + Math.random();
  let release = holdDoc(name, fetch);
  try {
    await turn(); release(); release = holdDoc(name, fetch); await turn();
    waiting[0]({ v: 'first' }); await turn(); assert.equal(docData(name).v, 'first', 'the read started before the re-run is still shown');
  } finally { release(); }
});
