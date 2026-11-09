// Entity store (operator-app D4; docs/designs/fleet-view-urls-and-list-path.md §2). Records by id are shared by every list;
// lists hold ordered ids and their own sort keys per canonical list key; docs hold detail reads. Nothing is ever reset:
// a view keeps showing what it has while the next answer loads, and swaps it in place. IndexedDB (D3) restores lists and
// docs on first use. One feed subscription asks list_membership about changed ids for every mounted list.
import { signal, type Signal } from '@preact/signals';
import { useEffect, useRef, useState } from 'preact/hooks';
import { op, online, signedOut, OpError } from './api';
import { subscribe, currentCursor, type Change } from './feed';
import { applyMembership, chunks, touched, type Row, type SortKey } from './lists';
import { save, load } from './offline';
import { listKey, toPageParams, type View } from '../../packages/view-url/view-url';

export type Freshness = 'current' | 'updating' | 'offline' | 'signed-out';
export const freshness = (restored: boolean): Freshness => signedOut.value ? 'signed-out' : !online.value ? 'offline' : restored ? 'updating' : 'current';

export type Kind = 'tasks' | 'sessions';
type Group = { key: string; label: string; count: number };
export type ListState = { ids: string[]; keys: Record<string, SortKey>; counts: Record<string, number>; total: number; groups: Group[]; nextAfter: unknown; at: number; restored: boolean; error: string | null };
type Saved = Omit<ListState, 'restored' | 'error'> & { rows: Row[] };

/** Records by id; a version signal makes every reader re-render when any record changes. */
const records = new Map<string, Row>();
export const recordsVersion = signal(0);
const putRows = (rows: Row[]) => { for (const r of rows) records.set(r.id, r); if (rows.length) recordsVersion.value++; };

const lists = new Map<string, Signal<ListState | null>>();
const listSignal = (key: string) => { let s = lists.get(key); if (!s) lists.set(key, s = signal(null)); return s; };
/** Mounted lists, with what list_membership needs to ask about them. */
const mounted = new Map<string, { kind: Kind; params: ReturnType<typeof toPageParams>; count: number }>();
const MEMBERSHIP_IDS = 1000; // the server's per-call bound

const opName = (k: Kind) => k === 'tasks' ? 'task_page' : 'sessions_list';
const rowsOf = (r: any): Row[] => r.tasks ?? r.sessions;
const persist = (key: string, s: ListState) => save<Saved>('list:' + key, { ids: s.ids, keys: s.keys, counts: s.counts, total: s.total, groups: s.groups, nextAfter: s.nextAfter, at: s.at, rows: s.ids.map(id => records.get(id)!).filter(Boolean) }, currentCursor());
const fromPage = (r: any): Omit<ListState, 'at' | 'restored' | 'error'> => {
  const rows = rowsOf(r); putRows(rows);
  return { ids: rows.map(x => x.id), keys: Object.fromEntries(rows.map(x => [x.id, x.sortKey])), counts: r.counts, total: r.total, groups: r.groups ?? [], nextAfter: r.nextAfter };
};

async function restore(key: string) {
  const s = listSignal(key);
  if (s.value) return;
  const saved = await load<Saved>('list:' + key);
  if (s.value || !saved) return; // the server answered first, or nothing stored
  putRows(saved.data.rows);
  const { rows: _, ...rest } = saved.data;
  s.value = { ...rest, restored: true, error: null };
}

/** Load the first page of a list into the store; signal aborts a superseded request. */
async function fetchPage(kind: Kind, key: string, params: ReturnType<typeof toPageParams>, signal?: AbortSignal) {
  const s = listSignal(key);
  try {
    const r = await op(opName(kind), { ...params, limit: 100 }, { signal });
    const next: ListState = { ...fromPage(r), at: Date.now(), restored: false, error: null };
    s.value = next; persist(key, next);
  } catch (e: any) {
    if (e instanceof OpError && e.message === 'aborted') return;
    if (s.value) s.value = { ...s.value, error: e.message }; else s.value = { ids: [], keys: {}, counts: {}, total: 0, groups: [], nextAfter: null, at: 0, restored: false, error: e.message };
  }
}

let started = false;
/** One feed subscription for all lists (started once from main.tsx). */
export function startStore() {
  if (started) return; started = true;
  subscribe({
    resync: () => { for (const [key, m] of mounted) fetchPage(m.kind, key, m.params); },
    changes: async (cs: Change[]) => {
      for (const [key, m] of mounted) {
        const ids = touched(cs, m.kind); if (!ids.length) continue;
        const s = listSignal(key), before = s.value; if (!before) continue;
        try {
          const matched: Row[] = []; let counts: Record<string, number> | null = null, groups: Group[] | null = null;
          for (const part of chunks(ids, MEMBERSHIP_IDS)) {
            const r = await op('list_membership', { ids: part, lists: [{ name: m.kind, ...m.params }] });
            matched.push(...r.lists[0].rows); counts = r.lists[0].counts; groups = r.lists[0].groups ?? null;
          }
          putRows(matched);
          const cur = s.value; if (!cur) continue;
          const placed = applyMembership(cur.ids, cur.keys, ids, matched, cur.nextAfter === null);
          const next = { ...cur, ...placed, counts: counts ?? cur.counts, total: counts ? Object.values(counts).reduce((a, n) => a + n, 0) : cur.total, groups: groups ?? cur.groups, at: Date.now() };
          s.value = next; persist(key, next);
        } catch { fetchPage(m.kind, key, m.params); } // never stop updating silently: fall back to a full reload
      }
    },
  });
}

/**
 * A server-ordered list for a view. `rows` are the rows of the shown list; while the wanted list (from the URL) has no data
 * yet the previous list stays on screen and `pending` is true. The skeleton is only for a view that never showed anything.
 */
export function useList(kind: Kind, view: View) {
  const key = listKey(view), params = toPageParams(view);
  const [shown, setShown] = useState<string | null>(() => lists.get(key)?.value ? key : null);
  const [inflight, setInflight] = useState(0);
  const slot = useRef<{ seq: number; ctrl: AbortController | null; paging: boolean }>({ seq: 0, ctrl: null, paging: false });
  recordsVersion.value; // re-render when records change
  const wantedState = listSignal(key).value;
  if (wantedState && shown !== key) queueMicrotask(() => setShown(key));
  useEffect(() => {
    const m = mounted.get(key); mounted.set(key, { kind, params, count: (m?.count ?? 0) + 1 });
    const mine = ++slot.current.seq;
    slot.current.ctrl?.abort(); const ctrl = slot.current.ctrl = new AbortController();
    restore(key);
    setInflight(n => n + 1);
    fetchPage(kind, key, params, ctrl.signal).finally(() => { if (mine === slot.current.seq) setInflight(0); });
    return () => { const x = mounted.get(key); if (x && --x.count <= 0) mounted.delete(key); };
  }, [key]);
  const cur = (shown ? lists.get(shown)?.value : null) ?? null;
  const more = async () => {
    const at = shown, st = at ? lists.get(at)?.value : null;
    if (!at || !st?.nextAfter || slot.current.paging) return;
    slot.current.paging = true;
    try {
      const r = await op(opName(kind), { ...toPageParams(view), limit: 100, after: st.nextAfter });
      const s = listSignal(at), now = s.value;
      if (!now || now.nextAfter !== st.nextAfter) return; // a reload or another page got there first
      const rows = rowsOf(r); putRows(rows);
      const have = new Set(now.ids), add = rows.filter(x => !have.has(x.id));
      const next = { ...now, ids: [...now.ids, ...add.map(x => x.id)], keys: { ...now.keys, ...Object.fromEntries(add.map(x => [x.id, x.sortKey])) }, nextAfter: r.nextAfter };
      s.value = next; persist(at, next);
    } catch (e: any) { const s = listSignal(at); if (s.value) s.value = { ...s.value, error: e.message }; } finally { slot.current.paging = false; }
  };
  const rows = cur ? cur.ids.map(id => records.get(id)).filter((r): r is Row => !!r) : [];
  return {
    rows, counts: cur?.counts ?? {}, total: cur?.total ?? 0, groups: cur?.groups ?? [], at: cur?.at ?? 0, error: cur?.error ?? null,
    pending: shown !== key || inflight > 0, shownKey: shown, more, reload: () => fetchPage(kind, key, params),
    state: freshness(!!cur?.restored),
  };
}

/** Detail and page reads by name (task_get, session_get, overview, hosts). The last value stays visible while re-reading. */
const docs = new Map<string, Signal<{ data: unknown; restored: boolean; error: string | null } | null>>();
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
