// Per-person preferences, stored server-side in the person's settings (person_settings_get / person_settings_set,
// docs/designs/fleet-people-access.md section 8 "Preferences"): section pins, row pins, saved views, focus and focus
// presets, column widths. Agents read and write the same fields through the same operations.
import { signal } from '@preact/signals';
import { op, type Ops } from './api.ts';
import type { View } from '../../packages/view-url/view-url.ts';

type Settings = Ops['person_settings_get']['reply'];
type Patch = Omit<Ops['person_settings_set']['request'], 'email'>;
const empty: Settings = { email: '', order: 'list', pins: [], views: [], focus: [], focusPresets: [], widths: {}, rowPins: {} };
export const prefs = signal<Settings>(empty);

// ponytail: last write wins; seq drops a stale get reply or a rollback after a newer change.
let seq = 0;
export function loadPrefs() { const n = seq; return op('person_settings_get', {}).then(s => { if (seq === n) prefs.value = s; }).catch(() => {}); }
/** Show the change at once, send it, and roll back if the write fails and nothing newer was sent meanwhile. */
export function savePrefs(local: Partial<Settings>, patch: Patch = local as Patch) {
  const prev = prefs.value, n = ++seq;
  prefs.value = { ...prev, ...local };
  op('person_settings_set', patch).then(s => { if (seq === n) prefs.value = s; }, () => { if (seq === n) prefs.value = prev; });
}

export function usePins(list: string) {
  const ids = prefs.value.rowPins[list] ?? [], set = new Set(ids);
  return { has: (id: string) => set.has(id), toggle: (id: string) => {
    const n = set.has(id) ? ids.filter(x => x !== id) : [...ids, id];
    savePrefs({ rowPins: { ...prefs.value.rowPins, [list]: n } }, { rowPins: { [list]: n } });
  } };
}

/** A saved view: a name and the view keys it sets (docs/designs/fleet-view-urls-and-list-path.md §1). */
export type SavedView = { name: string; view: View; system?: boolean };
/** System presets (fixed, from the app) come first, then the person's own views of this list. */
export function useViews(list: string, system: SavedView[]) {
  const all = prefs.value.views;
  const mine: SavedView[] = all.filter(v => v.list === list).map(v => ({ name: v.name, view: v.view as unknown as View }));
  const keep = (v: SavedView[]) => savePrefs({ views: [...all.filter(x => x.list !== list), ...v.map(x => ({ list, name: x.name, view: x.view as unknown as Record<string, unknown> }))] });
  const isSystem = (n: string) => system.some(s => s.name === n);
  return {
    list: [...system.map(s => ({ ...s, system: true })), ...mine.filter(v => !isSystem(v.name))],
    save: (v: SavedView) => { if (!isSystem(v.name)) keep([...mine.filter(x => x.name !== v.name), v]); },
    remove: (n: string) => keep(mine.filter(x => x.name !== n)),
  };
}

/** The focused projects ('-' is no project); empty: every project. */
export const setFocus = (projects: string[]) => savePrefs({ focus: projects });
/** A view with the focus as its project filter when the view names no project itself (lists and feeds app-wide). */
export function withFocus(v: View): View {
  const f = prefs.value.focus;
  return v.project?.length || !f.length ? v : { ...v, project: f.map(p => p === '-' ? null : p) };
}
export function saveFocusPreset(name: string, projects = prefs.value.focus) {
  savePrefs({ focusPresets: [...prefs.value.focusPresets.filter(p => p.name !== name), { name, projects }] });
}
export const removeFocusPreset = (name: string) => savePrefs({ focusPresets: prefs.value.focusPresets.filter(p => p.name !== name) });

/** A column width in px by key (nav, control); 0 resets to the default. */
export function useWidth(key: string, fallback: number): [number, (px: number) => void] {
  return [prefs.value.widths[key] || fallback, px => {
    const w = { ...prefs.value.widths }; if (px) w[key] = Math.round(px); else delete w[key];
    savePrefs({ widths: w }, { widths: { [key]: Math.round(px) } });
  }];
}
