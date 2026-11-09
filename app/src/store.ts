// Entity store (operator-app D4; docs/designs/fleet-view-urls-and-list-path.md §2). Records by id are shared by every list;
// lists hold ordered ids and their own sort keys per canonical list key; docs hold detail reads. Nothing is ever reset:
// a view keeps showing what it has while the next answer loads, and swaps it in place. IndexedDB (D3) restores lists and
// docs on first use. One feed subscription asks list_membership about changed ids for every mounted list.
//
// Order: every record remembers the change cursor its data reflects. A page answer is applied at the cursor its request
// started from, a membership answer at the cursor of its change batch; an older answer never overwrites a newer record.
// Changes that arrive while a list's page is loading are kept and asked about once the page has landed.
import { signal, type Signal } from '@preact/signals';
import { useEffect, useRef, useState } from 'preact/hooks';
import { op, online, signedOut, OpError } from './api';
import { subscribe, currentCursor, type Change } from './feed';
import { applyMembership, chunks, touched, type Row, type SortKey } from './lists';
import { save, load, onClear, pruneKeys } from './offline';
import { listKey, toPageParams, type View } from '../../packages/view-url/view-url';

export type Freshness = 'current' | 'updating' | 'offline' | 'signed-out';
export const freshness = (restored: boolean): Freshness => signedOut.value ? 'signed-out' : !online.value ? 'offline' : restored ? 'updating' : 'current';

export type Kind = 'tasks' | 'sessions';
type Params = ReturnType<typeof toPageParams>;
type Group = { key: string; label: string; count: number };
export type ListState = { ids: string[]; keys: Record<string, SortKey>; counts: Record<string, number>; total: number; groups: Group[]; nextAfter: unknown; at: number; restored: boolean; error: string | null };
type Saved = Omit<ListState, 'restored' | 'error'> & { rows: Row[] };
type Entry = { kind: Kind; params: Params; state: Signal<ListState | null>; busy: Signal<boolean>; mounted: number; loading: number; touchedWhileLoading: Set<string>; paging: boolean };

const records = new Map<string, { row: Row; seq: number }>();
const lists = new Map<string, Entry>();
const docs = new Map<string, Signal<{ data: unknown; restored: boolean; error: string | null } | null>>();
const MEMBERSHIP_IDS = 1000, MEMBERSHIP_LISTS = 2; // the server's per-call bounds
const cursorNow = () => currentCursor() ?? 0;

/** Forget everything held in memory (sign-out, grants change): IndexedDB is cleared by offline.ts at the same time. */
export function clearMemory() { records.clear(); lists.clear(); docs.clear(); }
onClear.add(clearMemory);

function entry(kind: Kind, key: string, params: Params): Entry {
  let e = lists.get(key);
  if (!e) lists.set(key, e = { kind, params, state: signal(null), busy: signal(false), mounted: 0, loading: 0, touchedWhileLoading: new Set(), paging: false });
  return e;
}

/** Store rows at a cursor; rows already newer are kept. Re-renders every list holding a changed id. */
function putRows(rows: Row[], seq: number) {
  const changed = new Set<string>();
  for (const r of rows) { const cur = records.get(r.id); if (cur && cur.seq > seq) continue; records.set(r.id, { row: r, seq }); changed.add(r.id); }
  if (!changed.size) return;
  for (const e of lists.values()) { const s = e.state.value; if (s && s.ids.some(id => changed.has(id))) e.state.value = { ...s }; }
}
/** Drop records no list holds any more. */
function pruneRecords() {
  const held = new Set<string>(); for (const e of lists.values()) for (const id of e.state.value?.ids ?? []) held.add(id);
  for (const id of records.keys()) if (!held.has(id)) records.delete(id);
}
const rowsOf = (r: any): Row[] => r.tasks ?? r.sessions;
const opName = (k: Kind) => k === 'tasks' ? 'task_page' : 'sessions_list';
const persist = (key: string, s: ListState) => save<Saved>('list:' + key, { ids: s.ids, keys: s.keys, counts: s.counts, total: s.total, groups: s.groups, nextAfter: s.nextAfter, at: s.at, rows: s.ids.map(id => records.get(id)?.row).filter((r): r is Row => !!r) }, currentCursor());
const idsAndKeys = (rows: Row[]) => ({ ids: rows.map(x => x.id), keys: Object.fromEntries(rows.map(x => [x.id, x.sortKey])) });

async function restore(key: string, e: Entry) {
  if (e.state.value) return;
  const saved = await load<Saved>('list:' + key);
  if (e.state.value || !saved) return; // the server answered first, or nothing stored
  putRows(saved.data.rows, saved.cursor ?? 0);
  const { rows: _, ...rest } = saved.data;
  e.state.value = { ...rest, restored: true, error: null };
}

/** Load the first page of a list. A superseded request is aborted through signal and leaves the list as it was. */
export async function loadList(kind: Kind, key: string, params: Params, abort?: AbortSignal) {
  const e = entry(kind, key, params), from = cursorNow();
  e.loading++; e.busy.value = true;
  try {
    const r = await op(opName(kind), { ...params, limit: 100 }, { signal: abort });
    const rows = rowsOf(r); putRows(rows, from);
    const next: ListState = { ...idsAndKeys(rows), counts: r.counts, total: r.total, groups: r.groups ?? [], nextAfter: r.nextAfter, at: Date.now(), restored: false, error: null };
    e.state.value = next; persist(key, next); pruneRecords();
  } catch (err: any) {
    if (err instanceof OpError && err.message === 'aborted') return;
    const s = e.state.value;
    e.state.value = s ? { ...s, error: err.message } : { ids: [], keys: {}, counts: {}, total: 0, groups: [], nextAfter: null, at: 0, restored: false, error: err.message };
  } finally {
    e.loading--; e.busy.value = e.loading > 0;
    if (!e.loading && e.touchedWhileLoading.size) { const ids = [...e.touchedWhileLoading]; e.touchedWhileLoading.clear(); await ask(e.kind, ids, [[key, e]], cursorNow()); }
  }
}

/** The next page of the list stored under key, with that list's own params; not while its first page is loading. */
export async function nextPage(key: string) {
  const e = lists.get(key), st = e?.state.value;
  if (!e || !st?.nextAfter || e.paging || e.loading) return;
  e.paging = true; const from = cursorNow();
  try {
    const r = await op(opName(e.kind), { ...e.params, limit: 100, after: st.nextAfter });
    const now = e.state.value;
    if (!now || now.nextAfter !== st.nextAfter) return; // a reload or another page got there first
    const rows = rowsOf(r); putRows(rows, from);
    const have = new Set(now.ids), add = idsAndKeys(rows.filter(x => !have.has(x.id)));
    const next = { ...e.state.value!, ids: [...now.ids, ...add.ids], keys: { ...now.keys, ...add.keys }, nextAfter: r.nextAfter };
    e.state.value = next; persist(key, next);
  } catch (err: any) { if (e.state.value) e.state.value = { ...e.state.value, error: err.message }; } finally { e.paging = false; }
}

/** Ask list_membership about ids for the given lists (two lists per call), at the cursor of the changes. */
async function ask(kind: Kind, ids: string[], targets: [string, Entry][], seq: number) {
  for (const pair of chunks(targets, MEMBERSHIP_LISTS)) {
    try {
      const answers: { rows: Row[]; counts: Record<string, number> | null; groups: Group[] | null }[] = pair.map(() => ({ rows: [], counts: null, groups: null }));
      for (const part of chunks(ids, MEMBERSHIP_IDS)) {
        const r = await op('list_membership', { ids: part, lists: pair.map(([, e]) => ({ name: kind, ...e.params })) });
        r.lists.forEach((l: any, i: number) => { answers[i].rows.push(...l.rows); answers[i].counts = l.counts; answers[i].groups = l.groups ?? null; });
      }
      pair.forEach(([key, e], i) => {
        const a = answers[i]; putRows(a.rows, seq);
        const cur = e.state.value; if (!cur) return;
        const placed = applyMembership(cur.ids, cur.keys, ids, a.rows, cur.nextAfter === null);
        const next = { ...cur, ...placed, counts: a.counts ?? cur.counts, total: a.counts ? Object.values(a.counts).reduce((x, n) => x + n, 0) : cur.total, groups: a.groups ?? cur.groups, at: Date.now() };
        e.state.value = next; persist(key, next);
      });
    } catch { for (const [key, e] of pair) loadList(kind, key, e.params); } // never stop updating silently: full reload
  }
}

/** One batch of feed changes: every mounted list with touched ids is asked once (lists still loading ask after landing). */
export async function applyChanges(cs: Change[]) {
  const seq = Math.max(0, ...cs.map(c => c.seq));
  for (const kind of ['tasks', 'sessions'] as Kind[]) {
    const ids = touched(cs, kind); if (!ids.length) continue;
    const targets: [string, Entry][] = [];
    for (const [key, e] of lists) {
      if (e.kind !== kind || !e.mounted) continue;
      if (e.loading || !e.state.value) { ids.forEach(id => e.touchedWhileLoading.add(id)); continue; }
      targets.push([key, e]);
    }
    if (targets.length) await ask(kind, ids, targets, seq);
  }
}

let started = false;
/** One feed subscription for all lists (started once from main.tsx); drops IndexedDB keys of the old per-view layout. */
export function startStore() {
  if (started) return; started = true;
  pruneKeys(k => k.startsWith('list:') || k.startsWith('doc:'));
  subscribe({ resync: () => { for (const [key, e] of lists) if (e.mounted) loadList(e.kind, key, e.params); }, changes: applyChanges });
}

/**
 * A server-ordered list for a view. `rows` are the rows of the shown list; while the wanted list (from the URL) has no data
 * yet the previous list stays on screen and `pending` is true. The skeleton is only for a view that never showed anything.
 */
export function useList(kind: Kind, view: View) {
  const key = listKey(view), e = entry(kind, key, toPageParams(view));
  const [shown, setShown] = useState<string | null>(() => e.state.value ? key : null);
  const ctrl = useRef<AbortController | null>(null);
  const wanted = e.state.value;
  useEffect(() => { if (wanted && shown !== key) setShown(key); }, [wanted, key]);
  useEffect(() => {
    e.mounted++;
    ctrl.current?.abort(); const c = ctrl.current = new AbortController();
    restore(key, e); loadList(kind, key, e.params, c.signal);
    return () => { e.mounted--; };
  }, [key]);
  const se = shown ? lists.get(shown) : undefined, cur = se?.state.value ?? null;
  const rows = cur ? cur.ids.map(id => records.get(id)?.row).filter((r): r is Row => !!r) : [];
  return {
    rows, counts: cur?.counts ?? {}, total: cur?.total ?? 0, groups: cur?.groups ?? [], at: cur?.at ?? 0, error: cur?.error ?? null,
    pending: shown !== key || e.busy.value, shownKey: shown, more: () => shown ? nextPage(shown) : Promise.resolve(), reload: () => loadList(kind, key, e.params),
    state: freshness(!!cur?.restored),
  };
}

/** Detail and page reads by name (task_get, session_get, overview, hosts). The last value stays visible while re-reading. */
export function useDoc<T>(name: string | null, fetch: () => Promise<T>, touches: (c: Change) => boolean, refreshMs?: number) {
  const fetchRef = useRef(fetch); fetchRef.current = fetch;
  const touchRef = useRef(touches); touchRef.current = touches;
  const s = name ? (docs.get(name) ?? (docs.set(name, signal(null)), docs.get(name)!)) : null;
  useEffect(() => {
    if (!name || !s) return;
    let live = true, seq = 0;
    const get = async () => {
      const mine = ++seq;
      try { const d = await fetchRef.current(); if (live && mine === seq) { s.value = { data: d, restored: false, error: null }; save('doc:' + name, d, currentCursor()); } }
      catch (e: any) { if (live && mine === seq) s.value = { data: s.value?.data ?? null, restored: s.value?.restored ?? false, error: e.message }; }
    };
    if (!s.value) load<T>('doc:' + name).then(v => { if (live && v && !s.value) s.value = { data: v.data, restored: true, error: null }; });
    get();
    const off = subscribe({ resync: get, changes: cs => { if (cs.some(c => touchRef.current(c))) get(); } });
    const timer = refreshMs ? setInterval(get, refreshMs) : 0;
    return () => { live = false; off(); clearInterval(timer); };
  }, [name, refreshMs]);
  const v = s?.value ?? null;
  return { data: (v?.data ?? null) as T | null, restored: !!v?.restored, error: v?.error ?? null, state: freshness(!!v?.restored) };
}

/** For tests: the stored list under key and a record. */
export const peek = { list: (key: string) => lists.get(key)?.state.value ?? null, record: (id: string) => records.get(id) ?? null, mount: (kind: Kind, key: string, params: Params) => { entry(kind, key, params).mounted++; } };
