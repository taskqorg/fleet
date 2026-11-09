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
(globalThis as any).fetch = async (url: string, init: any) => {
  const name = url.split('/api/ops/')[1], body = JSON.parse(init.body);
  calls.push({ name, body });
  const data = await answer(name, body);
  return new Response(JSON.stringify(data), { status: 200, headers: { 'content-type': 'application/json' } });
};
const row = (id: string, title = id) => ({ id, title, sortKey: ['1', -Number(id.replace(/\D/g, '') || 0), id] });
const page = (rows: any[], nextAfter: unknown = null) => ({ tasks: rows, counts: { queued: rows.length }, total: rows.length, groups: [], nextAfter });
const deferred = () => { let ok!: (v: any) => void; const p = new Promise(r => ok = r); return { p, ok }; };

test('review #33-1: paging uses the shown list\'s own params and waits while that list is loading', async () => {
  await clearAll();
  const A = { filter: { state: ['failed'] }, group: 'priority' }, B = { filter: { state: ['queued'] }, group: 'priority' };
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
  const P = { filter: {}, group: 'priority' };
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
  await loadList('tasks', 'M', { filter: {}, group: 'priority' });
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
