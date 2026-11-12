// Person preferences (owner-rules OR-33, OR-34, OR-41; fleet-people-access.md section 8 "Preferences"): stored on the server
// through person_settings_set, never in the browser; focus filters lists that name no project.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { prefs, savePrefs, withFocus, useViews, useWidth, usePins, saveFocusPreset, removeFocusPreset, focusIds } from '../../app/src/prefs.ts';

const sent: [string, any][] = [];
// The server applies the patch; the mock answers with what the tab shows, which is enough to test what is sent.
(globalThis as any).fetch = async (url: string, init: any) => {
  if (url.endsWith('_set')) sent.push([url, JSON.parse(init.body)]);
  await new Promise(r => setTimeout(r, 5)); const reply = prefs.value; return { type: 'basic', status: 200, ok: true, json: async () => reply };
};
const settle = () => new Promise(r => setTimeout(r, 30));

test('a save sends one item, never a whole list, so items saved elsewhere (an agent, another device) survive', async () => {
  useViews('tasks', []).save({ name: 'Early', view: { view: 'tasks' } });
  assert.deepEqual(prefs.value.views.map(v => v.name), ['Early']); // shown at once
  await settle();
  assert.deepEqual(sent, [['/api/ops/person_settings_set', { viewPut: { list: 'tasks', name: 'Early', view: { view: 'tasks' } } }]]);
  useViews('tasks', []).remove('Early'); usePins('tasks').toggle('t1'); saveFocusPreset('Core', ['fleet']); removeFocusPreset('Core');
  await settle();
  assert.deepEqual(sent.slice(1).map(x => x[1]), [{ viewDelete: { list: 'tasks', name: 'Early' } }, { rowPin: { list: 'tasks', id: 't1', pinned: true } },
    { focusPresetPut: { name: 'Core', projects: ['fleet'] } }, { focusPresetDelete: 'Core' }]);
  assert.equal(sent.some((x: any) => x[0].endsWith('_get')), false);
});

test('OR-34 focus applies to every list keyed by project (tasks, results, conversations) and becomes projectIds of feeds', () => {
  prefs.value = { ...prefs.value, focus: ['fleet'] };
  for (const v of ['tasks', 'results', 'conversations'] as const) assert.deepEqual(withFocus({ view: v }).project, ['fleet']);
  assert.equal(withFocus({ view: 'hosts' }).project, undefined);
  assert.deepEqual(focusIds(), ['fleet']);
  prefs.value = { ...prefs.value, focus: [] };
  assert.equal(focusIds(), undefined);
});

test('OR-34 focus is the project filter of a view that names no project; an explicit project wins', () => {
  prefs.value = { ...prefs.value, focus: ['fleet', '-'] };
  assert.deepEqual(withFocus({ view: 'tasks' }).project, ['fleet', null]);
  assert.deepEqual(withFocus({ view: 'tasks', project: ['other'] }).project, ['other']);
  prefs.value = { ...prefs.value, focus: [] };
  assert.equal(withFocus({ view: 'tasks' }).project, undefined);
});

test('OR-33 system presets come first and cannot be overwritten; personal views are written to the server', async () => {
  sent.length = 0;
  const v = useViews('tasks', [{ name: 'Running', view: { view: 'tasks', state: ['queued'] } }]);
  v.save({ name: 'Running', view: { view: 'tasks' } });
  assert.equal(sent.length, 0);
  v.save({ name: 'Mine', view: { view: 'tasks', state: ['failed'] } });
  await settle();
  assert.deepEqual(sent[0][1], { viewPut: { list: 'tasks', name: 'Mine', view: { view: 'tasks', state: ['failed'] } } });
  assert.deepEqual(useViews('tasks', [{ name: 'Running', view: { view: 'tasks' } }]).list.map(x => x.name), ['Running', 'Mine']);
});

test('OR-41 a width is sent by key only; 0 resets it', async () => {
  sent.length = 0;
  savePrefs(() => ({ widths: { nav: 200 } }), { widths: { nav: 200 } });
  const [w, set] = useWidth('nav', 240); assert.equal(w, 200);
  await new Promise(r => setTimeout(r, 30));
  set(0); assert.equal(useWidth('nav', 240)[0], 240);
  await new Promise(r => setTimeout(r, 30));
  assert.deepEqual(sent.at(-1)?.[1], { widths: { nav: 0 } });
});

test('OR-33 OR-34 OR-41 preferences never use browser storage', () => {
  assert.doesNotMatch(readFileSync(new URL('../../app/src/prefs.ts', import.meta.url), 'utf8'), /localStorage|sessionStorage|indexedDB/);
});
