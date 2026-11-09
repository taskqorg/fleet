// Entity store (docs/designs/fleet-view-urls-and-list-path.md §2) against a fake /api/ops: paging, ordering, clearing.
import { test } from 'node:test';
import assert from 'node:assert/strict';
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
  const data = await answer(name, body);
  return new Response(JSON.stringify(data), { status: 200, headers: { 'content-type': 'application/json' } });
};
(globalThis as any).fetch = realFetch;
const row = (id: string, title = id) => ({ id, title, sortKey: ['1', -Number(id.replace(/\D/g, '') || 0), id] });
const page = (rows: any[], nextAfter: unknown = null) => ({ tasks: rows, counts: { queued: rows.length }, total: rows.length, groups: [], nextAfter });
const deferred = () => { let ok!: (v: any) => void; const p = new Promise(r => ok = r); return { p, ok }; };

test('review #33-1: paging uses the shown list\'s own params and waits while that list is loading', async () => {
  await clearAll();
  const A = { view: 'tasks', state: ['failed'] } as const, B = { view: 'tasks', state: ['queued'] } as const;
  answer = async (_n, b) => b.after ? page([row('t9')]) : page([row('t1')], ['1', -1, 't1']);
  await loadList('tasks', 'A', A);
  const slow = deferred(); answer = async (_n, b) => b.filter.state[0] === 'queued' ? slow.p : page([row('t2')]);
  const pendingB = loadList('tasks', 'B', B); // a new filter is pending while A is on screen
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
  const db = { transaction: () => ({ objectStore: () => store }), createObjectStore: () => store, close() {}, onversionchange: null };
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
    assert.ok(idb.data.has('list:O'));
    await clearAll(); idb.data.set('list:O', { data: { ids: ['t8'], keys: { t8: ['1', -8, 't8'] }, counts: {}, total: 1, groups: [], nextAfter: null, at: 1, rows: [row('t8', 'stored')] }, cursor: 0, at: 1 });
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
