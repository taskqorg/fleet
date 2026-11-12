// Outside tests of person settings (docs/designs/fleet-people-access.md section 8, nav-rail.md pins), against the real
// binary and Postgres:   FLEET_TRIAL_TESTS=tests/go/person-settings.test.ts node tests/trial/run.ts   (or tests/go/run.sh)
import {test as nodeTest} from 'node:test';
import assert from 'node:assert/strict';

const BASE = process.env.FLEET_TRIAL_URL!, SECRET = process.env.FLEET_TRIAL_BROWSER_SECRET!;
const test = (name: string, fn: () => Promise<void>) => nodeTest(name, {timeout: 30_000}, fn);
async function op(name: string, body: object = {}) {
  const r = await fetch(BASE + '/api/ops/' + name, {method: 'POST', headers: {'content-type': 'application/json', authorization: 'Bearer ' + SECRET, 'x-fleet-person': 'owner@trial.test'}, body: JSON.stringify(body)});
  const out = await r.json(); assert.equal(r.status, 200, JSON.stringify(out)); return out;
}

test('P1 person_settings_set changes only the fields it names; pins keep their order without duplicates and clear with [] (requirement: nav-rail.md "Pins and their order are a person setting"; risk: saving pins resets the dispatch order, or the order is lost)', async () => {
  assert.deepEqual((await op('person_settings_get')).pins, []);
  assert.deepEqual((await op('person_settings_set', {pins: ['hosts', 'tasks', 'hosts']})).pins, ['hosts', 'tasks']);
  const s = await op('person_settings_set', {order: 'soonest_reset'});
  assert.equal(s.order, 'soonest_reset'); assert.deepEqual(s.pins, ['hosts', 'tasks']);
  const p = await op('person_settings_set', {pins: []});
  assert.equal(p.order, 'soonest_reset'); assert.deepEqual(p.pins, []);
  assert.deepEqual(await op('person_settings_get'), p);
  const changes = (await op('setting_changes', {target: 'person:' + p.email})).changes.map((c: any) => c.field).sort();
  assert.deepEqual(changes, ['order', 'pins', 'pins']);
});

test('P2 preferences (saved views, focus, focus presets, widths, row pins) are stored per person, merged by key where keyed, and leave other fields alone (requirement: owner rules OR-33, OR-34, OR-41 "stored server-side per person, readable/writable by agents"; risk: a width or pin change wipes the others, or saving a view resets the pins)', async () => {
  const base = await op('person_settings_set', {pins: ['tasks']});
  assert.deepEqual([base.views, base.focus, base.focusPresets, base.widths, base.rowPins], [[], [], [], {}, {}]);
  const view = {list: 'tasks', name: 'Mine', view: {view: 'tasks', state: ['failed'], group: 'agent'}};
  await op('project_create', {id: 'fleet', requestId: 'prefs-project', content: {settings: {id: 'fleet', name: 'Fleet', contextFiles: ['rules.md'], roles: {pm: 'pm'}}, contextFiles: [{path: 'rules.md', text: 'x'}]}});
  let s = await op('person_settings_set', {views: [view], focus: ['fleet', 'fleet', '-'], focusPresets: [{name: 'Core', projects: ['fleet']}]});
  assert.deepEqual(s.views, [view]); assert.deepEqual(s.focus, ['fleet', '-']); assert.deepEqual(s.pins, ['tasks']);
  s = await op('person_settings_set', {widths: {nav: 240, control: 320}, rowPins: {tasks: ['t1', 't2', 't1']}});
  s = await op('person_settings_set', {widths: {nav: 0, control: 300}, rowPins: {sessions: ['s1']}});
  assert.deepEqual(s.widths, {control: 300}); assert.deepEqual(s.rowPins, {tasks: ['t1', 't2'], sessions: ['s1']});
  s = await op('person_settings_set', {rowPins: {tasks: []}, focus: []});
  assert.deepEqual(s.rowPins, {sessions: ['s1']}); assert.deepEqual(s.focus, []); assert.deepEqual(s.views, [view]);
  assert.deepEqual(await op('person_settings_get'), s);
  const r = await fetch(BASE + '/api/ops/person_settings_set', {method: 'POST', headers: {'content-type': 'application/json', authorization: 'Bearer ' + SECRET, 'x-fleet-person': 'owner@trial.test'}, body: JSON.stringify({views: [view, view]})});
  assert.equal(r.status, 400);
  for (const bad of [{views: [{list: 'tasks', name: 'X', view: {view: 'hosts'}}]}, {views: [{list: 'tasks', name: 'X', view: {view: 'tasks', state: 5}}]}, {focus: ['no-such-project']}]) {
    const b = await fetch(BASE + '/api/ops/person_settings_set', {method: 'POST', headers: {'content-type': 'application/json', authorization: 'Bearer ' + SECRET, 'x-fleet-person': 'owner@trial.test'}, body: JSON.stringify(bad)});
    assert.equal(b.status, 400, JSON.stringify(bad));
  }
});

test('P3 per-item changes (viewPut, viewDelete, rowPin, focusPresetPut, focusPresetDelete) apply to the stored lists, so concurrent saves keep each other (requirement: OR-33, OR-34 stored per person; risk: two devices saving views at once lose one)', async () => {
  const v = (name: string) => ({list: 'tasks', name, view: {view: 'tasks', state: ['failed']}});
  await Promise.all([op('person_settings_set', {viewPut: v('A')}), op('person_settings_set', {viewPut: v('B')}), op('person_settings_set', {rowPin: {list: 'tasks', id: 'x', pinned: true}}), op('person_settings_set', {rowPin: {list: 'tasks', id: 'y', pinned: true}})]);
  let s = await op('person_settings_get');
  assert.deepEqual(s.views.map((x: any) => x.name).filter((n: string) => n !== 'Mine').sort(), ['A', 'B']);
  assert.deepEqual([...s.rowPins.tasks].sort(), ['x', 'y']);
  s = await op('person_settings_set', {viewPut: {...v('A'), view: {view: 'tasks', state: ['queued']}}});
  assert.deepEqual(s.views.find((x: any) => x.name === 'A').view.state, ['queued']);
  s = await op('person_settings_set', {viewDelete: {list: 'tasks', name: 'A'}, rowPin: {list: 'tasks', id: 'x', pinned: false}, focusPresetPut: {name: 'P', projects: ['fleet']}});
  assert.equal(s.views.some((x: any) => x.name === 'A'), false); assert.deepEqual(s.rowPins.tasks, ['y']); assert.ok(s.focusPresets.some((p: any) => p.name === 'P'));
  s = await op('person_settings_set', {focusPresetDelete: 'P'});
  assert.equal(s.focusPresets.some((p: any) => p.name === 'P'), false);
});

test('P4 questions_list, executions_list and delivery_status keep only projectIds when given (requirement: OR-34 focus applies app-wide; risk: Overview and Delivery ignore the focus)', async () => {
  for (const [name, body, key] of [['questions_list', {}, 'questions'], ['executions_list', {}, 'executions']] as const) {
    const all = (await op(name, body))[key], none = (await op(name, {...body, projectIds: ['no-such']}))[key];
    assert.equal(none.length, 0, name); assert.ok(Array.isArray(all));
    const free = (await op(name, {...body, projectIds: ['-']}))[key];
    assert.ok(free.every((r: any) => !r.projectId), name);
  }
  const d = await op('delivery_status', {projectIds: ['no-such']});
  assert.deepEqual([d.items.length, d.samples.length], [0, 0]);
});
