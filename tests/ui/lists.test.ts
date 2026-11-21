// Entity-store list rules (D4): sort-key ordering and per-person collapsed group flips.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { compareKeys, closedGroups, flipGroup } from '../../app/src/lists.ts';

test('sort keys compare like the server order (group, newest first, id)', () => {
  assert.ok(compareKeys(['a', -5, 'x'], ['b', -9, 'x']) < 0);
  assert.ok(compareKeys(['a', -9, 'x'], ['a', -5, 'x']) < 0);
  assert.equal(compareKeys(['a', -5, 'x'], ['a', -5, 'x']), 0);
});

test('collapsed groups are the page defaults with the person\'s flips applied, both ways (requirement: list-view.md, collapsed groups remembered per person; risk: a remembered group opens a default-collapsed one by mistake, or a toggle back to the default stays stored)', () => {
  assert.deepEqual([...closedGroups(['7', '8'], [])].sort(), ['7', '8']);
  assert.deepEqual([...closedGroups(['7', '8'], ['8', 'fleet'])].sort(), ['7', 'fleet']); // cancelled opened, fleet collapsed
  assert.deepEqual([...closedGroups([], ['a'])], ['a']);
  assert.deepEqual(flipGroup(['a'], 'b'), ['a', 'b']);
  assert.deepEqual(flipGroup(['a', 'b'], 'a'), ['b']); // back to the default: nothing stored for it
});
