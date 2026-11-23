// Entity store (docs/designs/fleet-view-urls-and-list-path.md §2) against a fake /api/ops: paging, ordering, clearing.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { registerAccessValidator, registerAccessBaseline, ensureAccessGeneration, invalidateAccess } from '../../app/src/access.ts';
registerAccessValidator(async () => ({ identityKey: 'unit@fixture.test', grantsVersion: 'one' }));
registerAccessBaseline(async () => ({ cursor: 0, activate() {}, dispose() {} }));
await ensureAccessGeneration();
import { loadList, nextPage, applyChanges, peek, holdDoc, readDoc, clearMemory, acquirePrepared, preparedKey, olderPreparedVersion } from '../../app/src/store.ts';
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
const idsOf = (key: string) => peek.list(key)!.rows.map(r => r.id);
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
  assert.deepEqual(idsOf('A'), ['t1', 't9']);
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
  assert.deepEqual(idsOf('C'), ['t1', 't2'], 'rows stay on screen while the first page reloads');
  assert.equal(peek.list('C')!.error, null);
  slow.ok(page([row('t1'), row('t3')], ['g', 's', 't3'])); await paging;
  assert.deepEqual(calls.map(c => c.body.after?.[0] ?? null), ['old', null, 'g'], 'the person is at the end: it pages on with the fresh cursor');
  assert.deepEqual(idsOf('C'), ['t1', 't3', 't4']); assert.equal(peek.list('C')!.error, null);
  // A server that refuses its own fresh cursor: one reload per list load, then the error, rows kept.
  answer = async (_n, b) => b.after ? refused : page([row('t1'), row('t3')], ['g', 's', 't3']);
  await loadList('sessions', 'C', V); calls = []; await nextPage('C');
  assert.deepEqual(calls.map(c => !!c.body.after), [true, false, true], 'no second reload in the same list load');
  assert.equal(peek.list('C')!.error, 'Invalid after');
  assert.deepEqual(idsOf('C'), ['t1', 't3']);
  calls = []; await nextPage('C'); assert.deepEqual(calls.map(c => !!c.body.after), [true], 'scrolling again does not reload');
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
  answer = async () => ({ httpStatus: 500, message: 'busy' }); await nextPage('F'); // a page error, then a failed load
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

const taskChange = (seq: number, id: string) => ({ seq, kind: 'task' as const, id, taskId: id, sessionId: null, hostId: null, op: 'U', touches: [] });
const membership = (rows: any[], counts: Record<string, number>, groups: object[] = []) => ({ lists: [{ name: 'tasks', rows, counts, groups }] });

test('live task membership retains the paging boundary, confirmed order and unchanged snapshot identity', async () => {
  // Requirement: scoped live row and summary updates. Risk: a burst replaces a page, skips a
  // boundary row or renders an identical answer. Cover an insertion, a move past the window and a removal.
  await clearAll();
  const P = { view: 'tasks', group: 'state', sort: 'created', state: ['queued'] } as const;
  const view = { ...P, state: [...P.state] };
  const ordered = (id: string, order: number) => ({ ...row(id), sortKey: ['4', order, id] });
  const a = ordered('a', 1), b = ordered('b', 2), c = ordered('c', 3), d = ordered('d', 4);
  const groups = [{ key: '4', label: 'queued', count: 4 }];
  const snapshot = { ...page([a, b], b.sortKey), counts: { queued: 4, completed: 0 }, groups, total: 4 };
  const unmount = peek.mount('tasks', 'live', view);
  try {
    answer = async () => snapshot;
    await loadList('tasks', 'live', view);
    const before = peek.list('live');
    calls = [];
    await applyChanges([{ ...taskChange(9, a.id), kind: 'artifact' }]);
    assert.equal(calls.length, 0, 'an artifact cannot affect the task list');
    answer = async () => membership([a], snapshot.counts, groups);
    await applyChanges(Array.from({ length: 36 }, (_, i) => taskChange(10 + i, a.id)));
    assert.deepEqual(calls, [{ name: 'list_membership', body: { ids: ['a'], lists: [{ name: 'tasks', filter: { state: ['queued'] }, group: 'state', sort: 'created' }] } }]);
    assert.equal(peek.list('live'), before, 'no new state signal for an unchanged answer');
    // Read the next page using the original server tuple after a row moved out of that window.
    // An independent mounted key lets the second case run without waiting for the burst throttle.
  } finally { unmount(); }

  const unmountWindow = peek.mount('tasks', 'window', view);
  try {
    answer = async () => snapshot;
    await loadList('tasks', 'window', view);
    const groupsAfter = [{ key: '4', label: 'queued', count: 3 }];
    const inside = ordered('new', 0), outside = ordered('b', 5);
    answer = async () => membership([outside, inside], { queued: 3, completed: 1 }, groupsAfter);
    calls = [];
    await applyChanges([taskChange(51, 'new'), taskChange(39, 'b'), taskChange(54, 'a')]);
    assert.deepEqual(calls.map(call => call.name), ['list_membership']);
    assert.deepEqual(idsOf('window'), ['new']);
    const changed = peek.list('window')!;
    assert.deepEqual(changed.nextAfter, b.sortKey);
    assert.deepEqual(changed.groups, groupsAfter);
    assert.deepEqual(changed.counts, { queued: 3, completed: 1 });
    assert.equal(changed.total, 4);
    answer = async (_name, body) => { assert.deepEqual(body.after, b.sortKey); return page([c, d, outside]); };
    await nextPage('window');
    assert.deepEqual(idsOf('window'), ['new', 'c', 'd', 'b']);
  } finally { unmountWindow(); }
});

test('feed IDs delivered while a snapshot is in flight are reconciled once after it lands', async () => {
  // Requirement: cursor fencing. Risk: losing commits delivered during a read or treating opaque
  // change sequence IDs as monotone. This batch is delivered in feed order, with decreasing IDs.
  await clearAll();
  const P = { view: 'tasks' } as const;
  const unmount = peek.mount('tasks', 'refresh', P);
  try {
    const slow = deferred();
    answer = async name => name === 'task_page' ? slow.p : membership([row('t2'), row('t3')], { queued: 3 });
    const loading = loadList('tasks', 'refresh', P);
    await new Promise(r => setImmediate(r));
    calls = [];
    await applyChanges([taskChange(21, 't2'), taskChange(19, 't3'), taskChange(18, 't2')]);
    assert.equal(calls.length, 0, 'the active snapshot is allowed to finish');
    slow.ok(page([row('t1')]));
    await loading;
    assert.deepEqual(calls.map(c => [c.name, c.body.ids]), [['list_membership', ['t2', 't3']]]);
    assert.deepEqual(idsOf('refresh'), ['t3', 't2', 't1']);
    assert.equal(peek.list('refresh')!.counts.queued, 3);
  } finally { unmount(); }
});

test('an unproven membership response recovers the loaded extent with a snapshot', async () => {
  // Requirement: quiet recovery. Risk: first-page fallback drops the rows and cursor already read.
  await clearAll();
  const P = { view: 'tasks' } as const;
  const a = row('t3'), b = row('t2'), c = row('t1');
  const unmount = peek.mount('tasks', 'fallback', P);
  try {
    answer = async (_name, body) => body.after ? page([b, c]) : page([a], a.sortKey);
    await loadList('tasks', 'fallback', P);
    await nextPage('fallback');
    assert.deepEqual(idsOf('fallback'), ['t3', 't2', 't1']);
    answer = async (name, body) => name === 'list_membership' ? { httpStatus: 500, message: 'Unavailable' }
      : body.after ? page([b, { ...c, title: 'Updated' }]) : { ...page([a], a.sortKey), counts: { queued: 3 }, total: 3 };
    calls = [];
    await applyChanges([taskChange(61, c.id)]);
    assert.deepEqual(calls.map(c => [c.name, c.body.after ?? null]), [
      ['list_membership', null], ['task_page', null], ['task_page', a.sortKey],
    ]);
    assert.equal(peek.list('fallback')!.rows[2].title, 'Updated');
    assert.equal(peek.list('fallback')!.counts.queued, 3);
  } finally { unmount(); }
});

test('a late membership answer is discarded when access generation changes', async () => {
  // Requirement: only the current access scope can populate a list. Risk: an old authorized
  // reply arrives after an access reset and restores a row or counts the new scope cannot see.
  await clearAll();
  const view = { view: 'tasks', group: 'none' } as const;
  const unmount = peek.mount('tasks', 'access-list', view);
  try {
    answer = async () => page([row('old-private')]);
    await loadList('tasks', 'access-list', view);
    const pending = deferred(), asked = deferred();
    answer = async () => { asked.ok(undefined); return pending.p; };
    const syncing = applyChanges([taskChange(72, 'old-private')]);
    await asked.p;
    invalidateAccess('fixture grant change');
    assert.equal(peek.list('access-list'), null);
    await nextPage('access-list'); // an old viewport edge can still fire while access is closed
    await ensureAccessGeneration();
    const visible = row('new-visible');
    answer = async () => page([visible], visible.sortKey);
    await loadList('tasks', 'access-list', view);
    const authorized = peek.list('access-list');
    pending.ok(membership([row('old-private', 'late private title')], { queued: 72 }));
    await syncing;
    assert.equal(peek.list('access-list'), authorized);
    assert.deepEqual(idsOf('access-list'), ['new-visible']);
    assert.equal(authorized!.counts.queued, 1);
    answer = async () => page([row('new-next')]);
    await nextPage('access-list');
    assert.deepEqual(idsOf('access-list'), ['new-visible', 'new-next'], 'the reopened list can still page');
  } finally { unmount(); }
});

test('review #33-4: clearing stored data also forgets lists in memory', async () => {
  answer = async () => page([row('t5')]);
  await loadList('tasks', 'M', { view: 'tasks' });
  assert.ok(peek.list('M'));
  await clearAll();
  assert.equal(peek.list('M'), null);
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

test('review #34-1/2: after clearing, mounted lists load again and mounted docs re-read', async () => {
  answer = async () => page([row('t7')]);
  peek.mount('tasks', 'C', { view: 'tasks', state: ['queued'] }); await loadList('tasks', 'C', { view: 'tasks', state: ['queued'] });
  const gen = peek.generation(); calls = [];
  await clearAll(); await new Promise(r => setTimeout(r, 10));
  assert.ok(calls.some(c => c.name === 'task_page'), 'the mounted list was requested again');
  assert.deepEqual(idsOf('C'), ['t7']);
  assert.equal(peek.generation(), gen + 1, 'docs depend on the generation, so mounted docs re-read');
});

// Requirement: one request per prepared key, one pending refresh, last-subscriber cancellation.
// Risk: event bursts multiply requests, unchanged progress erases figures or an old revision wins.
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
