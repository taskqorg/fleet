// The Pinned zone's order (nav-rail.md, Pinned): the person's stored order first, then pins it does not name yet.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { railPinOrder, railPinMove } from '../../packages/ui/src/controls/rail-pinned-rows.ts';

test('railPinOrder: stored order first, unpinned keys drop out, new pins go last', () => {
  assert.deepEqual(railPinOrder(['session:b', 'bot:x', 'session:gone', 'session:b'], ['bot:x', 'session:a', 'session:b']), ['session:b', 'bot:x', 'session:a']);
  assert.deepEqual(railPinOrder([], ['bot:x', 'session:a']), ['bot:x', 'session:a']);
});

test('railPinMove: one place up or down, unchanged at the ends', () => {
  assert.deepEqual(railPinMove(['a', 'b', 'c'], 'b', -1), ['b', 'a', 'c']);
  assert.deepEqual(railPinMove(['a', 'b', 'c'], 'b', 1), ['a', 'c', 'b']);
  assert.deepEqual(railPinMove(['a', 'b', 'c'], 'a', -1), ['a', 'b', 'c']);
  assert.deepEqual(railPinMove(['a', 'b', 'c'], 'z', 1), ['a', 'b', 'c']);
});
