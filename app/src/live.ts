// Live data hooks over the entity store rules (D4): server pages and counts, server membership on change, IndexedDB restore (D3).
import { useEffect, useRef, useState } from 'preact/hooks';
import { op, online, signedOut } from './api';
import { subscribe, currentCursor, type Change } from './feed';
import { applyMembership, touched, type Row } from './lists';
import { save, load } from './offline';

export type Freshness = 'current' | 'updating' | 'offline' | 'signed-out';
export const freshness = (restored: boolean): Freshness => signedOut.value ? 'signed-out' : !online.value ? 'offline' : restored ? 'updating' : 'current';

type Page = { rows: Row[]; counts: Record<string, number>; total: number; groups: { key: string; label: string; count: number }[]; nextAfter: unknown; at: number };
const EMPTY: Page = { rows: [], counts: {}, total: 0, groups: [], nextAfter: null, at: 0 };

/** A server-ordered list: first page, infinite scroll, membership on change, restore from IndexedDB. */
export function useLiveList(list: 'tasks' | 'sessions', params: { filter: Record<string, unknown>; group?: string }) {
  const opName = list === 'tasks' ? 'task_page' : 'sessions_list';
  const key = `${list}:${JSON.stringify(params)}`;
  const [page, setPage] = useState<Page>(EMPTY);
  const [restored, setRestored] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const ref = useRef(page); ref.current = page;
  const pick = (r: any): Omit<Page, 'at'> => ({ rows: r.tasks ?? r.sessions, counts: r.counts, total: r.total, groups: r.groups ?? [], nextAfter: r.nextAfter });
  const fresh = async () => {
    try { const r = await op(opName, { ...params, limit: 100 }); const p = { ...pick(r), at: Date.now() }; setPage(p); setRestored(false); setError(null); save(key, p, currentCursor()); }
    catch (e: any) { setError(e.message); }
  };
  useEffect(() => {
    let live = true;
    load<Page>(key).then(s => { if (live && s && !ref.current.at) { setPage(s.data); setRestored(true); } });
    fresh();
    const off = subscribe({
      resync: fresh,
      changes: async (cs: Change[]) => {
        const ids = touched(cs, list); if (!ids.length) return;
        try {
          const m = await op('list_membership', { ids, lists: [{ name: list, ...params }] });
          const head = list === 'tasks' && params.group && params.group !== 'none' ? await op(opName, { ...params, limit: 1 }) : null;
          const cur = ref.current, l = m.lists[0];
          const next = { ...cur, rows: applyMembership(cur.rows, ids, l.rows, cur.nextAfter === null), counts: l.counts, total: Object.values<number>(l.counts).reduce((a, n) => a + n, 0), groups: head ? head.groups : cur.groups, at: Date.now() };
          setPage(next); save(key, next, currentCursor());
        } catch { /* offline: the next resync or catch-up refreshes */ }
      },
    });
    return () => { live = false; off(); };
  }, [key]);
  const more = async () => {
    const cur = ref.current; if (!cur.nextAfter) return;
    const r = await op(opName, { ...params, limit: 100, after: cur.nextAfter });
    setPage({ ...cur, rows: [...cur.rows, ...(r.tasks ?? r.sessions)], nextAfter: r.nextAfter });
  };
  return { ...page, more, restored, error, state: freshness(restored), reload: fresh };
}

/** One record re-read when a change touches it (detail views). */
export function useLiveRecord<T>(key: string | null, fetch: () => Promise<T>, touches: (c: Change) => boolean) {
  const [data, setData] = useState<T | null>(null);
  const [restored, setRestored] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const fetchRef = useRef(fetch); fetchRef.current = fetch;
  const touchRef = useRef(touches); touchRef.current = touches;
  useEffect(() => {
    if (!key) { setData(null); return; }
    let live = true;
    const get = async () => { try { const d = await fetchRef.current(); if (live) { setData(d); setRestored(false); setError(null); save(key, d, currentCursor()); } } catch (e: any) { if (live) setError(e.message); } };
    setData(null);
    load<T>(key).then(s => { if (live && s) { setData(d => d ?? s.data); setRestored(true); } });
    get();
    const off = subscribe({ resync: get, changes: cs => { if (cs.some(c => touchRef.current(c))) get(); } });
    return () => { live = false; off(); };
  }, [key]);
  return { data, restored, error, state: freshness(restored) };
}
