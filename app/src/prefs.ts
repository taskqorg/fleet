// Per-person preferences, stored server-side in the person's settings (person_settings_get / person_settings_set,
// docs/designs/fleet-people-access.md section 8 "Preferences"): section pins, row pins, saved views, focus and focus
// presets, column widths, the collapsed navigation, the last list views and the folded list groups. Agents read and write the same fields through the same operations.
import { signal } from '@preact/signals';
import { op, type Ops } from './api.ts';
import { onClear } from './offline.ts';
import type { View } from '../../packages/view-url/view-url.ts';

type Settings = Ops['person_settings_get']['reply'];
type Patch = Omit<Ops['person_settings_set']['request'], 'email'>;
const empty: Settings = { email: '', order: 'list', pins: [], views: [], focus: [], focusPresets: [], widths: {}, rowPins: {}, navCollapsed: false, lastViews: {}, listGroups: {} };
export const prefs = signal<Settings>(empty);
/**
 * The person's preferences are known: read (or the read failed, so lists still load). Focus-dependent loads (useList, the
 * Overview and Delivery docs) wait for it, so a focused person does not load an unfocused list first. A store clear resets it.
 */
export const prefsRead = signal(false);

// A cleared store (signed out, another person, new grants) forgets the previous person's preferences too.
onClear.add(() => { seq++; epoch++; readSeq++; refreshAfterSave = false; pending = 0; prefs.value = empty; prefsRead.value = false; loadPrefs(); });

// ponytail: last write wins; seq drops a stale reply. Each save sends only what changed: one item (viewPut, viewDelete,
// rowPin, focusPresetPut, focusPresetDelete), one width, or a value the person set whole (focus, section pins in their
// order). The server applies it to the stored copy under the person lock, so saves from other devices and agents survive.
// Saves from this tab still run in order. A failed save shows the server copy again (or the state before it, when unreachable).
// epoch: a store clear drops saves still queued for the previous person; pending: a read never overwrites a save in flight.
let readSeq = 0, refreshAfterSave = false;
let seq = 0, epoch = 0, pending = 0, chain: Promise<unknown> = Promise.resolve();
// Any answer of the current person's read opens the gate (one held back by a save keeps the shown copy, the server's plus
// that save); an answer for a person cleared meanwhile (epoch) does not.
export function loadPrefs(): Promise<void> {
  // A setting-change during a local save must be read after the last queued patch settles.
  if (pending) { refreshAfterSave = true; prefsRead.value = true; return Promise.resolve(); }
  const n = seq, e = epoch, read = ++readSeq;
  const done = () => { if (e === epoch) prefsRead.value = true; };
  return op('person_settings_get', {}).then(s => {
    if (e !== epoch || read !== readSeq) return;
    if (seq === n && !pending) prefs.value = s;
    else if (pending) refreshAfterSave = true;
    else void loadPrefs(); // a save finished while this older read was in flight
    done();
  }, done);
}
function refreshAfterWrites() {
  if (!pending && refreshAfterSave) { refreshAfterSave = false; void loadPrefs(); }
}
/**
 * Show the change at once and send its patch. keepalive (pagehide, a hidden tab): the request outlives the page. It still
 * waits for the saves before it, so an older patch of the same key never lands last; with none in flight it starts at once.
 */
export function savePrefs(change: (s: Settings) => Partial<Settings>, patch: Patch, keepalive = false) {
  const n = ++seq, e = epoch, before = prefs.value;
  prefs.value = { ...before, ...change(before) };
  pending++;
  const send = () => e !== epoch ? null : op('person_settings_set', patch, { keepalive });
  chain = chain.then(send)
    .then(s => { if (e !== epoch) return; pending--; if (s && seq === n) prefs.value = s; refreshAfterWrites(); }, () => { if (e !== epoch) return; pending--; if (seq === n) { prefs.value = before; loadPrefs(); } refreshAfterWrites(); });
}

export function usePins(list: string) {
  const ids = prefs.value.rowPins[list] ?? [], set = new Set(ids);
  return { ids, has: (id: string) => set.has(id), toggle: (id: string) => {
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

/**
 * Where the person left each section's list (path and query: sort, grouping, filters, search; never the open item or the
 * checked rows), so the navigation returns to it. This tab's current value is kept here and sent once, when the person
 * leaves the section or the tab hides, through savePrefs like any other preference.
 */
const here = new Map<string, string>();
onClear.add(() => here.clear());
export function noteView(section: string, url: string) { here.set(section, url); }
type Pending = { change: (s: Settings) => Partial<Settings>; patch: Patch };
function viewPatch(section: string): Pending | null {
  const url = here.get(section);
  if (!url || prefs.value.lastViews?.[section] === url) return null;
  return { change: s => ({ lastViews: { ...s.lastViews, [section]: url } }), patch: { lastViews: { [section]: url } } };
}
/** The path the navigation opens for a section: its list as last left (this tab first, then the stored one), or the section. */
export const sectionPath = (section: string) => here.get(section) ?? prefs.value.lastViews?.[section] ?? '/' + section;

/**
 * The groups of each list (`<section>:<grouping>`, `<section>:pinned` for the Pinned group) whose collapse the person
 * flipped away from the page default. A toggle shows at once from this tab's copy and is sent debounced, and at once when
 * the person leaves the section or the tab hides, through savePrefs like any other preference.
 */
const groupsHere = signal<Record<string, string[]>>({});
let groupsTimer: ReturnType<typeof setTimeout> | undefined;
onClear.add(() => { clearTimeout(groupsTimer); groupsHere.value = {}; });
/** The flipped group keys of a list: this tab's unsent toggles first, then the stored ones. */
export const listGroups = (list: string): string[] => groupsHere.value[list] ?? prefs.value.listGroups?.[list] ?? [];
/** At most this many flips per list are kept (the server bound); the oldest go first. */
const MAX_FLIPS = 256;
export function setListGroups(list: string, flipped: string[]) {
  groupsHere.value = { ...groupsHere.value, [list]: flipped.slice(-MAX_FLIPS) };
  clearTimeout(groupsTimer); groupsTimer = setTimeout(saveListGroups, 500);
}
/** Send this tab's unsent toggles when the debounce ends (a section leave and the tab hiding send them with saveOnLeave). */
export function saveListGroups() {
  const p = groupsPatch();
  if (p) savePrefs(p.change, p.patch);
}
function groupsPatch(): Pending | null {
  clearTimeout(groupsTimer); groupsTimer = undefined;
  const here = groupsHere.value, stored = prefs.value.listGroups ?? {};
  const patch = Object.fromEntries(Object.entries(here).filter(([k, v]) => JSON.stringify(v) !== JSON.stringify(stored[k] ?? [])));
  groupsHere.value = {};
  if (!Object.keys(patch).length) return null;
  return { change: s => {
    const next = { ...s.listGroups };
    for (const [k, v] of Object.entries(patch)) if (v.length) next[k] = v; else delete next[k];
    return { listGroups: next };
  }, patch: { listGroups: patch } };
}
/**
 * The person leaves a section, or the page hides or goes away (visibilitychange hidden, pagehide; keepalive, so the
 * request outlives the page): the section's list view and the unsent group toggles go in one request.
 */
export function saveOnLeave(section: string | null, keepalive = true) {
  const ps = [section ? viewPatch(section) : null, groupsPatch()].filter((p): p is Pending => !!p);
  if (ps.length) savePrefs(s => Object.assign({}, ...ps.map(p => p.change(s))), Object.assign({}, ...ps.map(p => p.patch)), keepalive);
}
