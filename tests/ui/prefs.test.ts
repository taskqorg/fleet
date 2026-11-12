// Person preferences (owner-rules OR-33, OR-34, OR-41; fleet-people-access.md section 8 "Preferences"): stored on the server
// through person_settings_set, never in the browser; focus filters lists that name no project.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { prefs, savePrefs, withFocus, useViews, useWidth } from '../../app/src/prefs.ts';

const sent: any[] = [];
(globalThis as any).fetch = async (url: string, init: any) => { sent.push([url, JSON.parse(init.body)]); return { type: 'basic', status: 200, ok: true, json: async () => ({ ...prefs.value, ...JSON.parse(init.body) }) }; };

test('OR-34 focus is the project filter of a view that names no project; an explicit project wins', () => {
  prefs.value = { ...prefs.value, focus: ['fleet', '-'] };
  assert.deepEqual(withFocus({ view: 'tasks' }).project, ['fleet', null]);
  assert.deepEqual(withFocus({ view: 'tasks', project: ['other'] }).project, ['other']);
  prefs.value = { ...prefs.value, focus: [] };
  assert.equal(withFocus({ view: 'tasks' }).project, undefined);
});

test('OR-33 system presets come first and cannot be overwritten; personal views are written to the server', () => {
  sent.length = 0;
  const v = useViews('tasks', [{ name: 'Running', view: { view: 'tasks', state: ['queued'] } }]);
  v.save({ name: 'Running', view: { view: 'tasks' } });
  assert.equal(sent.length, 0);
  v.save({ name: 'Mine', view: { view: 'tasks', state: ['failed'] } });
  assert.deepEqual(sent[0], ['/api/ops/person_settings_set', { views: [{ list: 'tasks', name: 'Mine', view: { view: 'tasks', state: ['failed'] } }] }]);
  assert.deepEqual(useViews('tasks', [{ name: 'Running', view: { view: 'tasks' } }]).list.map(x => x.name), ['Running', 'Mine']);
});

test('OR-41 a width is sent by key only; 0 resets it', () => {
  sent.length = 0;
  savePrefs({ widths: { nav: 200 } });
  const [w, set] = useWidth('nav', 240); assert.equal(w, 200);
  set(0); assert.deepEqual(sent.at(-1)[1], { widths: { nav: 0 } }); assert.equal(useWidth('nav', 240)[0], 240);
});

test('OR-33 OR-34 OR-41 preferences never use browser storage', () => {
  assert.doesNotMatch(readFileSync(new URL('../../app/src/prefs.ts', import.meta.url), 'utf8'), /localStorage|sessionStorage|indexedDB/);
});
