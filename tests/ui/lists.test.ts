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

test('membership: removes rows that stopped matching, places new and moved rows by key', () => {
  const rows = [row('t3', 'a', 3), row('t2', 'a', 2), row('t1', 'b', 1)];
  // t2 no longer matches; t4 is new in group a; t1 moved from group b to a.
  const next = applyMembership(rows, ['t2', 't4', 't1'], [row('t4', 'a', 4), row('t1', 'a', 1)], true);
  assert.deepEqual(next.map(r => r.id), ['t4', 't3', 't1']);
});

test('membership: a new row beyond the loaded page waits for paging when the list is not complete', () => {
  const rows = [row('t9', 'a', 9), row('t8', 'a', 8)];
  assert.deepEqual(applyMembership(rows, ['t1'], [row('t1', 'b', 1)], false).map(r => r.id), ['t9', 't8']);
  assert.deepEqual(applyMembership(rows, ['t1'], [row('t1', 'b', 1)], true).map(r => r.id), ['t9', 't8', 't1']);
});

test('membership keeps unchanged rows and their objects (anchor keeping relies on stable ids)', () => {
  const rows = [row('t3', 'a', 3), row('t2', 'a', 2)];
  const next = applyMembership(rows, ['t5'], [row('t5', 'a', 5)], true);
  assert.equal(next[1], rows[0]);assert.equal(next[2], rows[1]);
});

test('touched maps changes to the list ids they affect, including parent rows', () => {
  const cs = [{ kind: 'execution', id: 'e1', taskId: 't1', sessionId: 's1' }, { kind: 'question', id: 'q1', taskId: 't2', sessionId: null }, { kind: 'event', id: 'v', taskId: null, sessionId: 's1' }];
  assert.deepEqual(touched(cs, 'tasks'), ['t1', 't2']);
  assert.deepEqual(touched(cs, 'sessions'), ['s1']);
});
