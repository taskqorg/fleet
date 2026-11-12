// Per-person preferences, stored server-side in the person's settings (person_settings_get / person_settings_set,
// docs/designs/fleet-people-access.md section 8 "Preferences"): section pins, row pins, saved views, focus and focus
// presets, column widths, the collapsed navigation. Agents read and write the same fields through the same operations.
import { signal } from '@preact/signals';
import { op, type Ops } from './api.ts';
import { onClear } from './offline.ts';
import type { View } from '../../packages/view-url/view-url.ts';

type Settings = Ops['person_settings_get']['reply'];
type Patch = Omit<Ops['person_settings_set']['request'], 'email'>;
const empty: Settings = { email: '', order: 'list', pins: [], views: [], focus: [], focusPresets: [], widths: {}, rowPins: {}, navCollapsed: false };
export const prefs = signal<Settings>(empty);
/**
 * The person's preferences are known: read (or the read failed, so lists still load). Focus-dependent loads (useList, the
 * Overview and Delivery docs) wait for it, so a focused person does not load an unfocused list first. A store clear resets it.
 */
export const prefsRead = signal(false);

// A cleared store (signed out, another person, new grants) forgets the previous person's preferences too.
onClear.add(() => { seq++; epoch++; pending = 0; prefs.value = empty; prefsRead.value = false; loadPrefs(); });

// ponytail: last write wins; seq drops a stale reply. Each save sends only what changed: one item (viewPut, viewDelete,
// rowPin, focusPresetPut, focusPresetDelete), one width, or a value the person set whole (focus, section pins in their
// order). The server applies it to the stored copy under the person lock, so saves from other devices and agents survive.
// Saves from this tab still run in order. A failed save shows the server copy again (or the state before it, when unreachable).
// epoch: a store clear drops saves still queued for the previous person; pending: a read never overwrites a save in flight.
let seq = 0, epoch = 0, pending = 0, chain: Promise<unknown> = Promise.resolve();
// Any answer of the current person's read opens the gate (one held back by a save keeps the shown copy, the server's plus
// that save); an answer for a person cleared meanwhile (epoch) does not.
export function loadPrefs() { const n = seq, e = epoch; const done = () => { if (e === epoch) prefsRead.value = true; }; return op('person_settings_get', {}).then(s => { if (seq === n && !pending) prefs.value = s; done(); }, done); }
/** Show the change at once and send its patch. */
export function savePrefs(change: (s: Settings) => Partial<Settings>, patch: Patch) {
  const n = ++seq, e = epoch, before = prefs.value;
  prefs.value = { ...before, ...change(before) };
  pending++;
  chain = chain.then(() => e !== epoch ? null : op('person_settings_set', patch))
    .then(s => { if (e !== epoch) return; pending--; if (s && seq === n) prefs.value = s; }, () => { if (e !== epoch) return; pending--; if (seq === n) { prefs.value = before; loadPrefs(); } });
}

export function usePins(list: string) {
  const ids = prefs.value.rowPins[list] ?? [], set = new Set(ids);
  return { has: (id: string) => set.has(id), toggle: (id: string) => {
    const pinned = !(prefs.value.rowPins[list] ?? []).includes(id); // the live state, not the render's
    savePrefs(s => { const xs = (s.rowPins[list] ?? []).filter(x => x !== id); return { rowPins: { ...s.rowPins, [list]: pinned ? [...xs, id] : xs } }; }, { rowPin: { list, id, pinned } });
  } };
}

/** A saved view: a name and the view keys it sets (docs/designs/fleet-view-urls-and-list-path.md §1). */
export type SavedView = { name: string; view: View; system?: boolean };
/** System presets (fixed, from the app) come first, then the person's own views of this list. */
export function useViews(list: string, system: SavedView[]) {
  const mine: SavedView[] = prefs.value.views.filter(v => v.list === list).map(v => ({ name: v.name, view: v.view as unknown as View }));
  const isSystem = (n: string) => system.some(s => s.name === n);
  return {
    list: [...system.map(s => ({ ...s, system: true })), ...mine.filter(v => !isSystem(v.name))],
    save: (v: SavedView) => {
      if (isSystem(v.name)) return;
      const item = { list, name: v.name, view: v.view as unknown as Record<string, unknown> };
      savePrefs(s => { const i = s.views.findIndex(x => x.list === list && x.name === v.name); const vs = [...s.views]; if (i < 0) vs.push(item); else vs[i] = item; return { views: vs }; }, { viewPut: item });
    },
    remove: (n: string) => savePrefs(s => ({ views: s.views.filter(x => !(x.list === list && x.name === n)) }), { viewDelete: { list, name: n } }),
  };
}

/** The focused projects ('-' is no project); empty: every project. */
export const setFocus = (projects: string[]) => savePrefs(() => ({ focus: projects }), { focus: projects });
/** The focus as the projectIds filter of an operation (questions_list, executions_list, delivery_status); undefined: all. */
export const focusIds = () => prefs.value.focus.length ? prefs.value.focus : undefined;
// Views whose URL codec has a project key, so the focus is part of the list key (store and IndexedDB identity).
const FOCUSED = ['tasks', 'results', 'conversations'];
/** A view with the focus as its project filter when the view names no project itself. */
export function withFocus(v: View): View {
  const f = prefs.value.focus;
  return v.project?.length || !f.length || !FOCUSED.includes(v.view) ? v : { ...v, project: f.map(p => p === '-' ? null : p) };
}
export function saveFocusPreset(name: string, projects = prefs.value.focus) {
  const item = { name, projects };
  savePrefs(s => { const i = s.focusPresets.findIndex(p => p.name === name); const ps = [...s.focusPresets]; if (i < 0) ps.push(item); else ps[i] = item; return { focusPresets: ps }; }, { focusPresetPut: item });
}
export const removeFocusPreset = (name: string) => savePrefs(s => ({ focusPresets: s.focusPresets.filter(p => p.name !== name) }), { focusPresetDelete: name });

/** A column width in px by key (nav, control); 0 resets to the default. */
export const setWidth = (key: string, px: number) =>
  savePrefs(s => { const w = { ...s.widths }; if (px) w[key] = Math.round(px); else delete w[key]; return { widths: w }; }, { widths: { [key]: Math.round(px) } });
