// Cross-message selection state (Stage 3 item 22).
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { click, all, inside, range } from '../../app/src/selection.ts';

test('click starts at a row; shift+click extends in key order either way', () => {
  let s = click(null, '12:0', false);
  assert.deepEqual([s!.start.key, s!.end.key], ['12:0', '12:0']);
  s = click(s, '80:1', true); assert.deepEqual([s!.start.key, s!.end.key], ['12:0', '80:1']);
  s = click(click(null, '80:1', false), '12:0', true); assert.deepEqual([s!.start.key, s!.end.key], ['12:0', '80:1']);
  assert.equal(s!.start.offset, 0); assert.ok(s!.end.offset > 1e9);
});

test('inside covers rows between the ends, including rows not loaded', () => {
  const s = click(click(null, '12:0', false), '80:1', true);
  assert.ok(inside(s, '12:0')); assert.ok(inside(s, '40:2')); assert.ok(inside(s, '80:1')); assert.ok(!inside(s, '80:2')); assert.ok(!inside(s, '9:0')); assert.ok(!inside(null, '12:0'));
});

test('select all and the export range', () => {
  const s = all()!; assert.ok(inside(s, '1:0') && inside(s, '999999:3')); assert.deepEqual(range(s).from, { key: '0:0', offset: 0 });
});
