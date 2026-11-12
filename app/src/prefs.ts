// Per-person preferences, stored server-side in the person's settings (person_settings_get / person_settings_set,
// docs/designs/fleet-people-access.md section 8 "Preferences"): section pins, row pins, saved views, focus and focus
// presets, column widths. Agents read and write the same fields through the same operations.
import { signal } from '@preact/signals';
import { op, type Ops } from './api.ts';
import { onClear } from './offline.ts';
import type { View } from '../../packages/view-url/view-url.ts';

type Settings = Ops['person_settings_get']['reply'];
type Patch = Omit<Ops['person_settings_set']['request'], 'email'>;
const empty: Settings = { email: '', order: 'list', pins: [], views: [], focus: [], focusPresets: [], widths: {}, rowPins: {} };
export const prefs = signal<Settings>(empty);

// A cleared store (signed out, another person, new grants) forgets the previous person's preferences too.
onClear.add(() => { seq++; prefs.value = empty; });

// ponytail: last write wins; seq drops a stale reply. A save re-reads the server copy first and builds whole lists (views,
// pins, focus presets) from it, so changes made meanwhile by an agent or another device survive; the gap between that read
// and the write is one round trip. A failed save shows the server copy again (or the state before it, when unreachable).
let seq = 0;
export function loadPrefs() { const n = seq; return op('person_settings_get', {}).then(s => { if (seq === n) prefs.value = s; }).catch(() => {}); }
/** Show the change at once, then send it built from a fresh server copy. */
export function savePrefs(change: (s: Settings) => Partial<Settings>, patch: (s: Settings) => Patch = s => change(s) as Patch) {
  const n = ++seq, before = prefs.value;
  prefs.value = { ...before, ...change(before) };
  op('person_settings_get', {}).then(fresh => op('person_settings_set', patch(fresh)))
    .then(s => { if (seq === n) prefs.value = s; }, () => { if (seq === n) { prefs.value = before; loadPrefs(); } });
}

export function usePins(list: string) {
  const ids = prefs.value.rowPins[list] ?? [], set = new Set(ids);
  return { has: (id: string) => set.has(id), toggle: (id: string) => {
    const next = (s: Settings) => { const xs = s.rowPins[list] ?? []; return xs.includes(id) ? xs.filter(x => x !== id) : [...xs, id]; };
    savePrefs(s => ({ rowPins: { ...s.rowPins, [list]: next(s) } }), s => ({ rowPins: { [list]: next(s) } }));
  } };
}

/** A saved view: a name and the view keys it sets (docs/designs/fleet-view-urls-and-list-path.md §1). */
export type SavedView = { name: string; view: View; system?: boolean };
/** System presets (fixed, from the app) come first, then the person's own views of this list. */
export function useViews(list: string, system: SavedView[]) {
  const mine: SavedView[] = prefs.value.views.filter(v => v.list === list).map(v => ({ name: v.name, view: v.view as unknown as View }));
  type Stored = Settings['views'][number];
  const keep = (edit: (own: Stored[]) => Stored[]) => savePrefs(s => ({ views: [...s.views.filter(x => x.list !== list), ...edit(s.views.filter(x => x.list === list))] }));
  const isSystem = (n: string) => system.some(s => s.name === n);
  return {
    list: [...system.map(s => ({ ...s, system: true })), ...mine.filter(v => !isSystem(v.name))],
    save: (v: SavedView) => { if (!isSystem(v.name)) keep(own => [...own.filter(x => x.name !== v.name), { list, name: v.name, view: v.view as unknown as Record<string, unknown> }]); },
    remove: (n: string) => keep(own => own.filter(x => x.name !== n)),
  };
}

/** The focused projects ('-' is no project); empty: every project. */
export const setFocus = (projects: string[]) => savePrefs(() => ({ focus: projects }));
// Views whose URL codec has a project key, so the focus is part of the list key (store and IndexedDB identity).
const FOCUSED = ['tasks', 'results'];
/** A view with the focus as its project filter when the view names no project itself. */
export function withFocus(v: View): View {
  const f = prefs.value.focus;
  return v.project?.length || !f.length || !FOCUSED.includes(v.view) ? v : { ...v, project: f.map(p => p === '-' ? null : p) };
}
export const saveFocusPreset = (name: string, projects?: string[]) =>
  savePrefs(s => ({ focusPresets: [...s.focusPresets.filter(p => p.name !== name), { name, projects: projects ?? s.focus }] }));
export const removeFocusPreset = (name: string) => savePrefs(s => ({ focusPresets: s.focusPresets.filter(p => p.name !== name) }));

/** A column width in px by key (nav, control); 0 resets to the default. */
export function useWidth(key: string, fallback: number): [number, (px: number) => void] {
  return [prefs.value.widths[key] || fallback, px => {
    savePrefs(s => { const w = { ...s.widths }; if (px) w[key] = Math.round(px); else delete w[key]; return { widths: w }; }, () => ({ widths: { [key]: Math.round(px) } }));
  }];
}
