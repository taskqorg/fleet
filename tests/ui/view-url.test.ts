// View URL codec (docs/designs/fleet-view-urls-and-list-path.md §1).
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { parseView, formatView, listKey, toPageParams } from '../../packages/view-url/view-url.ts';

const canon = (u: string) => formatView(parseView(u));

test('round trip per view, canonical order of keys and values, defaults omitted', () => {
  assert.equal(canon('/tasks?state=waiting,failed&group=priority&q=hero'), '/tasks?q=hero&state=failed,waiting');
  assert.equal(canon('/tasks/u1?sel=b,a,a&group=agent'), '/tasks/u1?group=agent&sel=a,b');
  assert.equal(canon('/conversations?archived=1&state=running'), '/conversations?archived=1&state=running');
  assert.equal(canon('/hosts/mac'), '/hosts/mac');
  assert.equal(canon('/overview'), '/overview');
  for (const u of ['/tasks?group=agent&project=-,fleet&q=a%2Cb&sel=x&state=failed', '/conversations/s1?q=x']) assert.equal(canon(u), u);
});

test('values: percent-encoded comma, no-project marker, invalid values dropped, empties omitted', () => {
  assert.deepEqual(parseView('/tasks?q=a%2Cb').q, 'a,b');
  assert.deepEqual(parseView('/tasks?project=-,fleet').project, [null, 'fleet']);
  assert.equal(canon('/tasks?state=bogus&group=nope&sel=&archived=1'), '/tasks?archived=1');
  assert.equal(canon('/conversations?archived=0'), '/conversations');
});

test('focus and sel are independent; preset kept as an inert label', () => {
  const v = parseView('/tasks/a?sel=b,c&preset=Needs%20me&state=failed');
  assert.equal(v.focus, 'a'); assert.deepEqual(v.sel, ['b', 'c']); assert.equal(v.preset, 'Needs me');
  assert.equal(formatView({ ...v, sel: [] }), '/tasks/a?preset=Needs%20me&state=failed');
});

test('unknown keys kept in extra, written back, not part of the list key', () => {
  const v = parseView('/tasks?zz=1&state=failed&aa=2');
  assert.deepEqual(v.extra, { zz: '1', aa: '2' });
  assert.equal(formatView(v), '/tasks?aa=2&state=failed&zz=1');
  assert.equal(listKey(v), 'tasks?state=failed');
});

test('list key ignores focus, sel and preset; equal views give equal keys', () => {
  assert.equal(listKey(parseView('/tasks/x?sel=a&preset=P&state=b,a')), listKey(parseView('/tasks?state=a,b')));
  assert.equal(listKey(parseView('/tasks?group=priority')), 'tasks');
  assert.notEqual(listKey(parseView('/tasks?group=agent')), listKey(parseView('/tasks')));
});

test('page params map onto task_page and sessions_list inputs', () => {
  assert.deepEqual(toPageParams(parseView('/tasks?q=hero&project=-&state=failed')), { filter: { state: ['failed'], search: 'hero', projectId: [null] }, group: 'priority' });
  assert.deepEqual(toPageParams(parseView('/conversations?state=running')), { filter: { state: ['running'], archived: false } });
});

test("today's links still parse", () => {
  assert.deepEqual(parseView('/tasks?state=waiting,failed&group=agent&q=x'), { view: 'tasks', state: ['waiting', 'failed'], group: 'agent', q: 'x' });
  assert.equal(parseView('/tasks/0b6c1f2e-6f1d-4e0a-9d3b-1a2b3c4d5e6f').focus, '0b6c1f2e-6f1d-4e0a-9d3b-1a2b3c4d5e6f');
});
