// Entity-store list rules (D4): the server decides membership and order; the client merges by sort key.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { applyMembership, compareKeys, touched, type Row } from '../../app/src/lists.ts';

const row = (id: string, g: string, seq: number): Row => ({ id, sortKey: [g, -seq, id] });

test('sort keys compare like the server order (group, newest first, id)', () => {
  assert.ok(compareKeys(['a', -5, 'x'], ['b', -9, 'x']) < 0);
  assert.ok(compareKeys(['a', -9, 'x'], ['a', -5, 'x']) < 0);
  assert.equal(compareKeys(['a', -5, 'x'], ['a', -5, 'x']), 0);
});

// The store keeps ids and per-list sort keys; records are shared.
const list = (rows: Row[]) => ({ ids: rows.map(r => r.id), keys: Object.fromEntries(rows.map(r => [r.id, r.sortKey])) });
const merge = (rows: Row[], changed: string[], matched: Row[], complete: boolean) => { const l = list(rows); return applyMembership(l.ids, l.keys, changed, matched, complete); };

test('membership: removes rows that stopped matching, places new and moved rows by key', () => {
  const rows = [row('t3', 'a', 3), row('t2', 'a', 2), row('t1', 'b', 1)];
  // t2 no longer matches; t4 is new in group a; t1 moved from group b to a.
  const next = merge(rows, ['t2', 't4', 't1'], [row('t4', 'a', 4), row('t1', 'a', 1)], true);
  assert.deepEqual(next.ids, ['t4', 't3', 't1']);
  assert.deepEqual(next.keys.t1, ['a', -1, 't1']);
});

test('membership: a new row beyond the loaded page waits for paging when the list is not complete', () => {
  const rows = [row('t9', 'a', 9), row('t8', 'a', 8)];
  assert.deepEqual(merge(rows, ['t1'], [row('t1', 'b', 1)], false).ids, ['t9', 't8']);
  assert.deepEqual(merge(rows, ['t1'], [row('t1', 'b', 1)], true).ids, ['t9', 't8', 't1']);
});

test('membership keeps unchanged ids in place when a row is inserted above (anchor keeping)', () => {
  const next = merge([row('t3', 'a', 3), row('t2', 'a', 2)], ['t5'], [row('t5', 'a', 5)], true);
  assert.deepEqual(next.ids, ['t5', 't3', 't2']);
});

test('one record, two lists: each list places it by its own sort key', () => {
  // Grouped by priority the task sits in group 1; grouped by agent in group "codex". The record is shared; keys are not.
  const byPriority = list([row('a', '1', 9), row('b', '2', 8)]), byAgent = list([row('b', 'claude', 8), row('c', 'codex', 7)]);
  const moved = { id: 'a', sortKey: ['codex', -9, 'a'] } as Row, movedP = { id: 'a', sortKey: ['3', -9, 'a'] } as Row;
  assert.deepEqual(applyMembership(byAgent.ids, byAgent.keys, ['a'], [moved], true).ids, ['b', 'a', 'c']);
  assert.deepEqual(applyMembership(byPriority.ids, byPriority.keys, ['a'], [movedP], true).ids, ['b', 'a']);
});

test('touched maps changes to the list ids they affect, including parent rows', () => {
  const cs = [{ kind: 'execution', id: 'e1', taskId: 't1', sessionId: 's1' }, { kind: 'question', id: 'q1', taskId: 't2', sessionId: null }, { kind: 'event', id: 'v', taskId: null, sessionId: 's1' }];
  assert.deepEqual(touched(cs, 'tasks'), ['t1', 't2']);
  assert.deepEqual(touched(cs, 'sessions'), ['s1']);
});

test('chunks split membership ids at the server bound', async () => {
  const { chunks } = await import('../../app/src/lists.ts');
  const ids = Array.from({ length: 2500 }, (_, i) => 't' + i);
  assert.deepEqual(chunks(ids, 1000).map(c => c.length), [1000, 1000, 500]);
  assert.deepEqual(chunks([], 1000), []);
});
