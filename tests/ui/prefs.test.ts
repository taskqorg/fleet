// Person preferences (owner-rules OR-33, OR-34, OR-41; fleet-people-access.md section 8 "Preferences"): stored on the server
// through person_settings_set, never in the browser; focus and ignored projects filter lists that name no project.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { registerAccessValidator, registerAccessBaseline, ensureAccessGeneration } from '../../app/src/access.ts';
registerAccessValidator(async () => ({ identityKey: 'unit@fixture.test', grantsVersion: 'one' }));
registerAccessBaseline(async () => ({ cursor: 0, activate() {}, dispose() {} }));
await ensureAccessGeneration();
import { readFileSync } from 'node:fs';
import { prefs, prefsRead, savePrefs, withFocus, useViews, setWidth, usePins, saveFocusPreset, removeFocusPreset, focusIds, setFocus, setIgnored, knownProjects, scopeReady, outsideFocus, listGroups, setListGroups, saveListGroups, saveOnLeave, noteView } from '../../app/src/prefs.ts';

const sent: [string, any][] = [], kept: any[] = [];
const pendingReplies = new Set<Promise<void>>();
// The server applies the patch; the mock answers with what the tab shows, which is enough to test what is sent.
(globalThis as any).fetch = async (url: string, init: any) => {
  if (url.endsWith('_set')) sent.push([url, JSON.parse(init.body)]); if (init.keepalive) kept.push(JSON.parse(init.body));
  const pending = new Promise<void>(resolve => setTimeout(resolve, 5)); pendingReplies.add(pending);
  await pending; pendingReplies.delete(pending); const reply = prefs.value; return { type: 'basic', status: 200, ok: true, json: async () => reply };
};
// Wait for real mock-reply completion and the next queued write, rather than a fixed wall-clock guess.
const settle = async () => { for (;;) { await Promise.allSettled([...pendingReplies]); await new Promise<void>(resolve => setImmediate(resolve)); if (!pendingReplies.size) return; } };

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

// Risk: an ignored project still shows under "All projects", stays in a focus, or hides a project the view names itself.
test('OR-34 ignored projects leave every list that names no project, also with every project in focus; an explicit project wins', async () => {
  knownProjects.value = ['fleet', 'muted', 'other'];
  prefs.value = { ...prefs.value, focus: [], ignored: ['muted'] };
  assert.deepEqual(focusIds(), ['fleet', 'other', '-']);
  assert.deepEqual(withFocus({ view: 'tasks' }).project, ['fleet', 'other', null]);
  assert.deepEqual(withFocus({ view: 'tasks', project: ['muted'] }).project, ['muted']);
  prefs.value = { ...prefs.value, ignored: ['muted', '-'] };
  assert.deepEqual(focusIds(), ['fleet', 'other']);
  prefs.value = { ...prefs.value, ignored: ['fleet', 'muted', 'other', '-'] };
  assert.deepEqual(withFocus({ view: 'tasks' }).project?.length, 1, 'everything ignored shows nothing, never everything');
  // The project list not read yet: lists wait; failed: nothing is left out rather than nothing shown.
  const wasRead = prefsRead.value; prefsRead.value = true;
  knownProjects.value = null; assert.equal(scopeReady.value, false);
  prefs.value = { ...prefs.value, focus: ['zz'] }; assert.equal(scopeReady.value, true, 'a focus needs no project list');
  prefs.value = { ...prefs.value, focus: [] };
  knownProjects.value = 'failed'; assert.equal(focusIds(), undefined); assert.equal(scopeReady.value, true); prefsRead.value = wasRead;
  knownProjects.value = ['fleet', 'muted'];
  prefs.value = { ...prefs.value, focus: ['fleet', 'muted'], ignored: [] };
  sent.length = 0; setIgnored('muted', true);
  assert.deepEqual([prefs.value.ignored, prefs.value.focus], [['muted'], ['fleet']], 'an ignored project leaves the focus at once');
  setFocus(['fleet', 'muted']); assert.deepEqual(prefs.value.focus, ['fleet'], 'an ignored project is never focused');
  setIgnored('muted', false);
  await settle();
  assert.deepEqual(sent.map(x => x[1]), [{ ignore: { project: 'muted', ignored: true } }, { focus: ['fleet'] }, { ignore: { project: 'muted', ignored: false } }]);
  prefs.value = { ...prefs.value, focus: [], ignored: [] }; knownProjects.value = null;
});

// Risk: the outside-focus count brings an ignored project back, or alerts with no focus set (owner: ignore is full silence).
test('OR-34 the outside-focus count leaves out ignored projects and counts nothing with All projects', () => {
  prefs.value = { ...prefs.value, focus: [], ignored: ['muted'] };
  assert.deepEqual(['other', 'muted', null].map(outsideFocus), [false, false, false], 'All projects: nothing is outside');
  prefs.value = { ...prefs.value, focus: ['fleet'], ignored: ['muted'] };
  assert.deepEqual(['fleet', 'other', 'muted', null].map(outsideFocus), [false, true, false, true]);
  prefs.value = { ...prefs.value, ignored: ['muted', '-'] };
  assert.equal(outsideFocus(null), false, "ignored '-' silences work outside projects");
  knownProjects.value = ['fleet', 'muted', 'other'];
  assert.deepEqual(focusIds(false), ['fleet', 'other'], 'without the focus (Overview ?all) ignored projects stay out');
  knownProjects.value = null;
  prefs.value = { ...prefs.value, focus: [], ignored: [] };
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
  assert.equal(prefs.value.widths.nav, 200);
  await settle();
  setWidth('nav', 0); assert.equal(prefs.value.widths.nav, undefined);
  await settle();
  assert.deepEqual(sent.at(-1)?.[1], { widths: { nav: 0 } });
});

test('list group toggles show at once, are sent once per flush with only the lists that changed, and the stored copy shows after (requirement: list-view.md collapsed groups remembered per person; risk: every toggle is a request, or the group springs open between the flush and the reply)', async () => {
  await settle(); sent.length = 0;
  setListGroups('tasks:project', ['fleet']); setListGroups('tasks:project', ['fleet', '~']); setListGroups('tasks:state', []);
  assert.deepEqual(listGroups('tasks:project'), ['fleet', '~']);
  assert.equal(sent.length, 0, 'a toggle waits for the debounce');
  saveListGroups();
  assert.deepEqual(listGroups('tasks:project'), ['fleet', '~'], 'shown from the saved copy');
  await settle();
  assert.deepEqual(sent.map(x => x[1]), [{ listGroups: { 'tasks:project': ['fleet', '~'] } }]);
  setListGroups('tasks:project', []); saveListGroups(); await settle();
  assert.deepEqual(sent.at(-1)?.[1], { listGroups: { 'tasks:project': [] } });
  assert.deepEqual(prefs.value.listGroups, {});
  saveListGroups(); await settle();
  assert.equal(sent.length, 2, 'nothing unsent: no request');
  setListGroups('tasks:state', ['7']); await new Promise(r => setTimeout(r, 600)); await settle();
  assert.deepEqual(sent.at(-1)?.[1], { listGroups: { 'tasks:state': ['7'] } }, 'the debounce sends it');
});

test('leaving the page (pagehide, hidden tab) sends the section\'s list view and the unsent group toggles in one keepalive request, after the saves before it (risk: the last toggles are lost when the tab closes, or an older patch lands last)', async () => {
  await settle(); kept.length = 0; sent.length = 0;
  noteView('tasks', '/tasks?group=agent'); setListGroups('tasks:agent', ['codex']); saveOnLeave('tasks');
  await new Promise<void>(resolve => setImmediate(resolve));
  assert.deepEqual(kept, [{ lastViews: { tasks: '/tasks?group=agent' }, listGroups: { 'tasks:agent': ['codex'] } }], 'one request, at once with nothing in flight');
  setWidth('nav', 210); setListGroups('tasks:agent', []); saveOnLeave(null);
  await settle();
  assert.deepEqual(sent.slice(-2).map(x => x[1]), [{ widths: { nav: 210 } }, { listGroups: { 'tasks:agent': [] } }]);
  assert.equal(prefs.value.listGroups['tasks:agent'], undefined);
  saveOnLeave('tasks'); await settle();
  assert.equal(sent.length, 3, 'nothing unsent: no request');
});

test('OR-33 OR-34 OR-41 preferences never use browser storage', () => {
  assert.doesNotMatch(readFileSync(new URL('../../app/src/prefs.ts', import.meta.url), 'utf8'), /localStorage|sessionStorage|indexedDB/);
});

test('the preferences gate opens on the current person\'s read, also with a save in flight, and a store clear closes it until the next read', async () => {
  const { prefsRead, loadPrefs } = await import('../../app/src/prefs.ts');
  prefsRead.value = false;
  const read = loadPrefs(); savePrefs(() => ({ widths: { a: 1 } }), { widths: { a: 1 } });
  await read; assert.equal(prefsRead.value, true);
  const { onClear } = await import('../../app/src/offline.ts');
  for (const f of onClear) f(); // a store clear: another person or new grants
  assert.equal(prefsRead.value, false);
  await loadPrefs(); assert.equal(prefsRead.value, true);
});

// Requirement: settings changed by another device stay current (OR-33/34).
// Risk: an older response or an event during a local save permanently loses remote pins/focus.
test('settings reads discard older replies and defer remote refresh until local saves settle', async () => {
  await settle();
  const { loadPrefs } = await import('../../app/src/prefs.ts');
  const originalFetch = globalThis.fetch;
  const replies: ((value: unknown) => void)[] = [];
  globalThis.fetch = async () => new Promise<Response>(resolve => replies.push(value => resolve(new Response(JSON.stringify(value)))));
  const tick = () => new Promise<void>(resolve => setImmediate(resolve));
  try {
    const old = loadPrefs(), recent = loadPrefs(); await tick();
    replies[1]({ ...prefs.value, focus: ['new'] }); await recent;
    replies[0]({ ...prefs.value, focus: ['old'] }); await old;
    assert.deepEqual(prefs.value.focus, ['new'], 'late old read replaced newer settings');
    savePrefs(() => ({ widths: { nav: 222 } }), { widths: { nav: 222 } }); await tick();
    await loadPrefs();
    assert.equal(replies.length, 3, 'refresh must wait for the pending save');
    replies[2]({ ...prefs.value, focus: ['save-snapshot'] }); await tick();
    assert.equal(replies.length, 4, 'setting-change during save must trigger a fresh read');
    replies[3]({ ...prefs.value, focus: ['remote'] }); await tick();
    assert.deepEqual(prefs.value.focus, ['remote']);
  } finally { globalThis.fetch = originalFetch; }
});
