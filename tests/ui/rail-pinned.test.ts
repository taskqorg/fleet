// The Pinned zone's order (nav-rail.md, Pinned): the person's stored order first, then pins it does not name yet; a drag
// moves one key to a slot (reorder.tsx).
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { railPinOrder } from '../../packages/ui/src/controls/rail-pinned-rows.ts';
import { reorderTo } from '../../packages/ui/src/controls/reorder-keys.ts';

test('railPinOrder: stored order first, unpinned keys drop out, new pins go last', () => {
  assert.deepEqual(railPinOrder(['session:b', 'bot:x', 'session:gone', 'session:b'], ['bot:x', 'session:a', 'session:b']), ['session:b', 'bot:x', 'session:a']);
  assert.deepEqual(railPinOrder([], ['bot:x', 'session:a']), ['bot:x', 'session:a']);
});

test('reorderTo: a key to a slot among the others, clamped at the ends', () => {
  assert.deepEqual(reorderTo(['a', 'b', 'c'], 'c', 0), ['c', 'a', 'b']);
  assert.deepEqual(reorderTo(['a', 'b', 'c'], 'a', 1), ['b', 'a', 'c']);
  assert.deepEqual(reorderTo(['a', 'b', 'c'], 'a', 9), ['b', 'c', 'a']);
  assert.deepEqual(reorderTo(['a', 'b', 'c'], 'b', -1), ['b', 'a', 'c']);
});
