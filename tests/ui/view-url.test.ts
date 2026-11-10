// View URL codec (docs/designs/fleet-view-urls-and-list-path.md §1).
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { parseView, formatView, listKey, toPageParams, TASK_GROUPS, type View } from '../../packages/view-url/view-url.ts';

const p = (u: string): View => { const v = parseView(u); assert.ok(v, 'not a view: ' + u); return v!; };
const canon = (u: string) => formatView(p(u));

test('round trip per view, canonical order of keys and values, defaults omitted', () => {
  assert.equal(canon('/tasks?state=waiting,failed&group=priority&q=hero'), '/tasks?q=hero&state=failed,waiting');
  assert.equal(canon('/tasks/u1?sel=b,a,a&group=agent'), '/tasks/u1?group=agent&sel=a,b');
  assert.equal(canon('/conversations?archived=1&state=running'), '/conversations?archived=1&state=running');
  assert.equal(canon('/hosts/mac'), '/hosts/mac');
  assert.equal(canon('/overview'), '/overview');
  for (const u of ['/tasks?group=agent&project=-,fleet&q=a%2Cb&sel=x&state=failed', '/conversations/s1?q=x']) assert.equal(canon(u), u);
});

test('values: percent-encoded comma, no-project marker, invalid values dropped, empties omitted', () => {
  assert.equal(p('/tasks?q=a%2Cb').q, 'a,b');
  assert.deepEqual(p('/tasks?project=fleet,-').project, [null, 'fleet']);
  assert.equal(canon('/tasks?state=bogus&group=nope&sel=&archived=1'), '/tasks?archived=1');
  assert.equal(canon('/conversations?archived=0'), '/conversations');
});

test('review 1: project is sorted like state, so equal views give equal page params', () => {
  assert.deepEqual(toPageParams(p('/tasks?project=game,-,fleet')), toPageParams(p('/tasks?project=fleet,game,-')));
  assert.deepEqual(toPageParams(p('/tasks?project=game,-,fleet')).filter.projectId, [null, 'fleet', 'game']);
  assert.deepEqual(toPageParams({ view: 'tasks', project: ['b', null, 'a'] }).filter.projectId, [null, 'a', 'b']);
});

test('review 2: paths that are not views return null', () => {
  for (const u of ['/legacy/foo', '/', '', 'tasks', '/tasks/a/b', '/tasks/', '/nope?state=failed']) assert.equal(parseView(u), null, u);
});

test("review 3: a focus containing '/' round-trips", () => {
  const v: View = { view: 'tasks', focus: 'a/b' };
  assert.equal(formatView(v), '/tasks/a%2Fb');
  assert.equal(p(formatView(v)).focus, 'a/b');
});

test("review 4: '+' is literal in the path, a space in the query", () => {
  assert.equal(p('/tasks/a+b?q=c+d').focus, 'a+b');
  assert.equal(p('/tasks/a+b?q=c+d').q, 'c d');
  assert.equal(p(formatView({ view: 'tasks', focus: 'a+b', q: 'c+d' })).q, 'c+d');
});

test('review 5: full URLs use their pathname and search', () => {
  assert.deepEqual(p('https://fleet.ufobe.com/tasks/u1?state=failed#x'), { view: 'tasks', focus: 'u1', state: ['failed'] });
  assert.equal(parseView('https://fleet.ufobe.com/legacy'), null);
});

test('review 6: repeated keys merge for lists, last wins for single values', () => {
  assert.deepEqual(p('/tasks?state=failed&state=waiting,failed').state, ['failed', 'waiting']);
  assert.deepEqual(p('/tasks?sel=b&sel=a').sel, ['a', 'b']);
  assert.deepEqual(p('/tasks?project=x&project=-').project, [null, 'x']);
  assert.equal(p('/tasks?q=a&q=b').q, 'b');
});

test('focus and sel are independent; preset kept as an inert label', () => {
  const v = p('/tasks/a?sel=b,c&preset=Needs%20me&state=failed');
  assert.equal(v.focus, 'a'); assert.deepEqual(v.sel, ['b', 'c']); assert.equal(v.preset, 'Needs me');
  assert.equal(formatView({ ...v, sel: [] }), '/tasks/a?preset=Needs%20me&state=failed');
});

test('unknown keys kept in extra, written back, not part of the list key', () => {
  const v = p('/tasks?zz=1&state=failed&aa=2');
  assert.deepEqual(v.extra, { zz: '1', aa: '2' });
  assert.equal(formatView(v), '/tasks?aa=2&state=failed&zz=1');
  assert.equal(listKey(v), 'tasks?state=failed');
});

test('list key ignores focus, sel and preset; equal views give equal keys', () => {
  assert.equal(listKey(p('/tasks/x?sel=a&preset=P&state=b,a')), listKey(p('/tasks?state=a,b')));
  assert.equal(listKey(p('/tasks?group=priority')), 'tasks');
  assert.notEqual(listKey(p('/tasks?group=agent')), listKey(p('/tasks')));
});

test('page params map onto task_page and sessions_list inputs', () => {
  assert.deepEqual(toPageParams(p('/tasks?q=hero&project=-&state=failed')), { filter: { state: ['failed'], search: 'hero', projectId: [null] }, group: 'priority' });
  assert.deepEqual(toPageParams(p('/conversations?state=running')), { filter: { state: ['running'], archived: false } });
});

test("today's links still parse", () => {
  assert.deepEqual(p('/tasks?state=waiting,failed&group=agent&q=x'), { view: 'tasks', state: ['failed', 'waiting'], group: 'agent', q: 'x' });
  assert.equal(p('/tasks/0b6c1f2e-6f1d-4e0a-9d3b-1a2b3c4d5e6f').focus, '0b6c1f2e-6f1d-4e0a-9d3b-1a2b3c4d5e6f');
});

// review 7 (every codec grouping is accepted by task_page) is an outside test now: tests/go/d.test.ts.

test("review #31-7: a project named '-' does not collide with the no-project marker", () => {
  const v: View = { view: 'tasks', project: ['-', null, 'a'] };
  assert.equal(formatView(v), '/tasks?project=-,%2D,a');
  assert.deepEqual(p(formatView(v)).project, [null, '-', 'a']);
  assert.deepEqual(p('/tasks?project=-').project, [null]);
});

test('review #31-10: a full URL is accepted only on the given origin', () => {
  assert.ok(parseView('https://fleet.example/tasks', { origin: 'https://fleet.example' }));
  assert.equal(parseView('https://evil.example/tasks', { origin: 'https://fleet.example' }), null);
  assert.equal(parseView('https://fleet.example/tasks', { origin: null }), null);
  assert.ok(parseView('/tasks', { origin: null }));
});
