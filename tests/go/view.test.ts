// Outside test of the agent operation view_link (docs/designs/fleet-view-urls-and-list-path.md §1), restored after the
// Go port dropped it. The Go codec (go/coordinator/view_link.go) is checked against packages/view-url on the same views.
//   FLEET_TRIAL_TESTS=tests/go/view.test.ts node tests/trial/run.ts
import {test} from 'node:test';
import assert from 'node:assert/strict';
import {startCoordinator} from '../harness.ts';
import {formatView, parseView, TASK_SORTS, type View} from '../../packages/view-url/view-url.ts';

const ORIGIN = 'https://fleet.trial.test';

test('V1 view_link gives agents the operator-app link the app itself writes, and reads a link back (requirement: view-urls §1 view_link, CUJ 3; risk: an agent hands the operator a link that opens another list, or the Go and TypeScript codecs drift)', async () => {
  const c = await startCoordinator({name: 'view', env: {FLEET_BROWSER_ORIGIN: ORIGIN}});
  const op = async (body: unknown) => {
    const r = await fetch(c.base + '/api/ops/view_link', {method: 'POST', headers: {authorization: 'Bearer ' + c.secret, 'x-fleet-person': 'owner@trial.test', 'content-type': 'application/json'}, body: JSON.stringify(body)});
    return {status: r.status, body: await r.json()};
  };
  try {
    assert.deepEqual((await op({view: {view: 'tasks', state: ['failed'], project: ['fleet']}})).body,
      {url: ORIGIN + '/tasks?project=fleet&state=failed', path: '/tasks?project=fleet&state=failed'});
    const views: View[] = [
      {view: 'tasks', state: ['held', 'failed', 'failed'], project: [null, 'b', 'a', '-'], group: 'state', q: 'hero name, 100% & more', sel: ['t2', 't1'], preset: 'mine', focus: 'a/b:c@d'},
      {view: 'tasks', group: 'priority'},
      {view: 'tasks', sort: 'created'}, {view: 'tasks', sort: '-changed'},
      {view: 'conversations', archived: true, state: ['running'], q: 'x+y', extra: {tab: 'log', 'odd key': 'é'}},
      {view: 'results', state: ['stored'], project: ['p1']},
      {view: 'hosts', focus: 'pds18'},
      {view: 'hosts', focus: '..'},
      {view: 'projects'},
      {view: 'projects', focus: 'hello-world'},
    ];
    for (const v of views) {
      const r = await op({view: v});
      assert.equal(r.status, 200, JSON.stringify(r.body));
      assert.equal(r.body.path, formatView(v), JSON.stringify(v));
      const back = await op({url: r.body.url});
      assert.equal(back.status, 200, JSON.stringify(back.body));
      assert.deepEqual(back.body.view, parseView(r.body.path), r.body.path);
    }
    for (const [body, message] of [
      [{view: {view: 'tasks', state: ['nope']}}, /state must be a list of/],
      [{view: {view: 'hosts', q: 'x'}}, /q is not a key of the hosts view/],
      [{view: {view: 'tasks', sort: 'title'}}, new RegExp('sort must be one of ' + [...TASK_SORTS].sort().join(', ') + '$')],
      [{url: 'https://elsewhere.test/tasks'}, /Not a Fleet view URL/],
      [{url: '/settings'}, /Not a Fleet view URL/],
      [{}, /exactly one of view or url/],
      [{url: 123}, /Expected url/],
    ] as const) {
      const r = await op(body);
      assert.equal(r.status, 400, JSON.stringify(body));
      assert.match(r.body.message ?? r.body.error, message);
    }
  } finally { await c.stop(); }
});
