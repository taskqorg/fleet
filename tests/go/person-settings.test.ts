// Outside tests of person settings (docs/designs/fleet-people-access.md section 8, nav-rail.md pins), against the real
// binary and Postgres:   FLEET_TRIAL_TESTS=tests/go/person-settings.test.ts node tests/trial/run.ts   (or tests/go/run.sh)
import {test as nodeTest} from 'node:test';
import assert from 'node:assert/strict';
import {randomUUID} from 'node:crypto';

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
  assert.equal(s.navCollapsed, false);
  s = await op('person_settings_set', {navCollapsed: true}); assert.equal(s.navCollapsed, true); assert.deepEqual(s.widths, {control: 300});
  assert.equal((await op('person_settings_get', {})).navCollapsed, true);
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

test('P5 lastViews keeps each section\'s last list view, merged by section, removed by an empty string, and only app paths of that section (requirement: the navigation returns to a section as it was left; risk: one section\'s view overwrites another, or a stored value sends the person elsewhere)', async () => {
  let s = await op('person_settings_set', {lastViews: {tasks: '/tasks?group=state&sort=created', hosts: '/hosts'}});
  s = await op('person_settings_set', {lastViews: {tasks: '/tasks?group=none', hosts: ''}});
  assert.deepEqual(s.lastViews, {tasks: '/tasks?group=none'});
  assert.deepEqual((await op('person_settings_get')).lastViews, {tasks: '/tasks?group=none'});
  for (const bad of [{lastViews: {tasks: '/hosts'}}, {lastViews: {tasks: 'https://evil.example/tasks'}}, {lastViews: {tasks: '/tasks//evil'}}, {lastViews: {tasks: '/tasksx'}}, {lastViews: {tasks: '/tasks/T-1'}}, {lastViews: {'Bad Key': '/x'}}]) {
    const b = await fetch(BASE + '/api/ops/person_settings_set', {method: 'POST', headers: {'content-type': 'application/json', authorization: 'Bearer ' + SECRET, 'x-fleet-person': 'owner@trial.test'}, body: JSON.stringify(bad)});
    assert.equal(b.status, 400, JSON.stringify(bad));
  }
  await op('person_settings_set', {lastViews: {tasks: ''}});
});

test('P5 listGroups keeps the groups each list has folded away from its default, merged by <section>:<grouping>, removed by [], strictly checked and not audited (requirement: list-view.md "collapsed groups are remembered per person"; risk: a section forgets the groups folded in another, a toggle floods the audit, or a bad key is stored)', async () => {
  const me = (await op('person_settings_get')).email;
  const before = (await op('setting_changes', {target: 'person:' + me})).changes.length;
  let s = await op('person_settings_set', {listGroups: {'tasks:project': ['fleet', '~', 'fleet'], 'tasks:state': ['7'], 'delivery:stage': ['installed'], 'tasks:pinned': ['pinned']}});
  assert.deepEqual(s.listGroups, {'tasks:project': ['fleet', '~'], 'tasks:state': ['7'], 'delivery:stage': ['installed'], 'tasks:pinned': ['pinned']});
  s = await op('person_settings_set', {listGroups: {'tasks:project': ['other'], 'tasks:pinned': []}});
  assert.deepEqual(s.listGroups, {'tasks:project': ['other'], 'tasks:state': ['7'], 'delivery:stage': ['installed']});
  assert.deepEqual((await op('person_settings_get')).listGroups, s.listGroups);
  assert.equal((await op('setting_changes', {target: 'person:' + me})).changes.length, before);
  const many = Object.fromEntries(Array.from({length: 65}, (_, i) => ['tasks:g' + i, ['x']]));
  for (const bad of [{listGroups: {tasks: ['x']}}, {listGroups: {'Tasks:project': ['x']}}, {listGroups: {'tasks:pro ject': ['x']}}, {listGroups: {'tasks:project': ['a\u0000b']}}, {listGroups: {'tasks:project': ['x'.repeat(257)]}}, {listGroups: {'tasks:project': Array.from({length: 257}, (_, i) => 'g' + i)}}, {listGroups: many}]) {
    const b = await fetch(BASE + '/api/ops/person_settings_set', {method: 'POST', headers: {'content-type': 'application/json', authorization: 'Bearer ' + SECRET, 'x-fleet-person': 'owner@trial.test'}, body: JSON.stringify(bad)});
    assert.equal(b.status, 400, JSON.stringify(bad).slice(0, 200));
  }
  // The whole document holds at most 64 lists: 3 stored plus 62 new ones are refused, 61 are taken.
  const fill = (n: number) => Object.fromEntries(Array.from({length: n}, (_, i) => ['conversations:g' + i, ['x']]));
  const over = await fetch(BASE + '/api/ops/person_settings_set', {method: 'POST', headers: {'content-type': 'application/json', authorization: 'Bearer ' + SECRET, 'x-fleet-person': 'owner@trial.test'}, body: JSON.stringify({listGroups: fill(62)})});
  assert.equal(over.status, 400);
  assert.equal(Object.keys((await op('person_settings_set', {listGroups: fill(61)})).listGroups).length, 64);
  await op('person_settings_set', {listGroups: Object.fromEntries(Object.keys((await op('person_settings_get')).listGroups).map(k => [k, []]))});
  assert.deepEqual((await op('person_settings_get')).listGroups, {});
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

test('P6 ignored projects are a person setting: ignored and restored one at a time or as a whole list, an ignored project leaves the focus, only visible projects, audited, and agents write it through their tools (requirement: OR-34 ignore stored on the server per person, agents can write it; risk: an ignore is lost between devices, an ignored project stays in the focus, or an agent cannot hide a project for its director)', async () => {
  for (const id of ['ign-a', 'ign-b'])
    await op('project_create', {id, requestId: 'prefs-' + id, content: {settings: {id, name: id.toUpperCase(), contextFiles: ['rules.md'], roles: {pm: 'pm'}}, contextFiles: [{path: 'rules.md', text: 'x'}]}});
  assert.deepEqual((await op('person_settings_get')).ignored, []);
  await op('person_settings_set', {focus: ['ign-a', 'ign-b', '-']});
  await Promise.all([op('person_settings_set', {ignore: {project: 'ign-a', ignored: true}}), op('person_settings_set', {ignore: {project: 'ign-b', ignored: true}})]);
  let s = await op('person_settings_get');
  assert.deepEqual([...s.ignored].sort(), ['ign-a', 'ign-b']); assert.deepEqual(s.focus, ['-'], 'ignored projects left the focus');
  s = await op('person_settings_set', {ignore: {project: 'ign-a', ignored: false}, focus: ['ign-a', 'ign-b']});
  assert.deepEqual(s.ignored, ['ign-b']); assert.deepEqual(s.focus, ['ign-a'], 'a focus never holds an ignored project');
  const fields = (await op('setting_changes', {target: 'person:' + s.email})).changes.map((c: any) => c.field);
  assert.ok(fields.includes('ignored'), 'an ignore is audited');
  for (const bad of [{ignore: {project: 'no-such-project', ignored: true}}, {ignored: ['no-such-project']}, {ignore: {project: '', ignored: true}}, {ignore: {project: 'ign-a'}}]) {
    const b = await fetch(BASE + '/api/ops/person_settings_set', {method: 'POST', headers: {'content-type': 'application/json', authorization: 'Bearer ' + SECRET, 'x-fleet-person': 'owner@trial.test'}, body: JSON.stringify(bad)});
    assert.equal(b.status, 400, JSON.stringify(bad));
  }
  s = await op('person_settings_set', {ignored: []}); assert.deepEqual(s.ignored, []);
  // An agent writes its director's ignore through the same operation, which agents get as a tool.
  const host = await op('hosts_register', {id: 'h-prefs', holder: 'owner@trial.test'});
  const poll = async () => (await (await fetch(BASE + '/api/hosts/poll', {method: 'POST', headers: {authorization: 'Bearer ' + host.secret, 'content-type': 'application/json'}, body: JSON.stringify({platform: 'darwin', capabilities: [], stores: [], problems: []})})).json()) as any;
  await poll();
  const sess = await op('session_start', {projectId: 'ign-a', role: 'pm', requestId: randomUUID(), text: 'Hide ign-b for me'});
  const cmd = (await poll()).commands.find((x: any) => x.sessionId === sess.sessionId && x.kind === 'start');
  assert.ok(cmd, 'start command issued');
  const r = await fetch(BASE + '/api/hosts/agent-tools', {method: 'POST', headers: {authorization: 'Bearer ' + host.secret, 'content-type': 'application/json'}, body: JSON.stringify({hostId: 'h-prefs', commandId: cmd.id, executionId: cmd.executionId, agentToolKey: cmd.payload.agentToolKey,
    request: {jsonrpc: '2.0', id: 1, method: 'tools/call', params: {name: 'person_settings_set', arguments: {ignore: {project: 'ign-b', ignored: true}}}}})});
  const body: any = await r.json();
  assert.equal(r.status, 200, JSON.stringify(body)); assert.ok(!body.error && !body.result?.isError, JSON.stringify(body));
  assert.deepEqual((await op('person_settings_get')).ignored, ['ign-b']);
  await op('person_settings_set', {ignored: [], focus: []});
});
