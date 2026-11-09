// Live data hooks over the entity store rules (D4): server pages and counts, server membership on change, IndexedDB restore (D3).
// Every async result is applied only if it is still the latest request for that view (sequence guards), and every update is a
// functional update over the current state, so a slow response never overwrites a newer one.
import { useEffect, useRef, useState } from 'preact/hooks';
import { op, online, signedOut } from './api';
import { subscribe, currentCursor, type Change } from './feed';
import { applyMembership, chunks, touched, type Row } from './lists';
import { save, load } from './offline';

export type Freshness = 'current' | 'updating' | 'offline' | 'signed-out';
export const freshness = (restored: boolean): Freshness => signedOut.value ? 'signed-out' : !online.value ? 'offline' : restored ? 'updating' : 'current';

type Page = { rows: Row[]; counts: Record<string, number>; total: number; groups: { key: string; label: string; count: number }[]; nextAfter: unknown; at: number };
const EMPTY: Page = { rows: [], counts: {}, total: 0, groups: [], nextAfter: null, at: 0 };
const MEMBERSHIP_IDS = 1000; // the server's per-call bound

/** A server-ordered list: first page, infinite scroll, membership on change, restore from IndexedDB. */
export function useLiveList(list: 'tasks' | 'sessions', params: { filter: Record<string, unknown>; group?: string }) {
  const opName = list === 'tasks' ? 'task_page' : 'sessions_list';
  const key = `${list}:${JSON.stringify(params)}`;
  const [page, setPage] = useState<Page>(EMPTY);
  const [restored, setRestored] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const seq = useRef(0), paging = useRef(false), keyRef = useRef(key); keyRef.current = key;
  const pick = (r: any): Omit<Page, 'at'> => ({ rows: r.tasks ?? r.sessions, counts: r.counts, total: r.total, groups: r.groups ?? [], nextAfter: r.nextAfter });
  const fresh = async () => {
    const mine = ++seq.current;
    try {
      const r = await op(opName, { ...params, limit: 100 });
      if (mine !== seq.current || keyRef.current !== key) return;
      const p = { ...pick(r), at: Date.now() }; setPage(p); setRestored(false); setError(null); save(key, p, currentCursor());
    } catch (e: any) { if (mine === seq.current) setError(e.message); }
  };
  useEffect(() => {
    let live = true;
    setPage(EMPTY);
    load<Page>(key).then(s => { if (live && s) setPage(cur => { if (cur.at) return cur; setRestored(true); return s.data; }); });
    fresh();
    const off = subscribe({
      resync: fresh,
      changes: async (cs: Change[]) => {
        const ids = touched(cs, list); if (!ids.length) return;
        const mine = seq.current;
        try {
          const matched: Row[] = []; let counts: Record<string, number> | null = null;
          for (const part of chunks(ids, MEMBERSHIP_IDS)) {
            const m = await op('list_membership', { ids: part, lists: [{ name: list, ...params }] });
            matched.push(...m.lists[0].rows); counts = m.lists[0].counts;
          }
          const head = list === 'tasks' && params.group && params.group !== 'none' ? await op(opName, { ...params, limit: 1 }) : null;
          if (!live || mine !== seq.current) return; // a full reload started meanwhile; it already has these changes
          setPage(cur => {
            const next = { ...cur, rows: applyMembership(cur.rows, ids, matched, cur.nextAfter === null), counts: counts ?? cur.counts,
              total: counts ? Object.values(counts).reduce((a, n) => a + n, 0) : cur.total, groups: head ? head.groups : cur.groups, at: Date.now() };
            save(key, next, currentCursor());
            return next;
          });
        } catch { if (live) fresh(); } // never stop updating silently: fall back to a full reload
      },
    });
    return () => { live = false; seq.current++; off(); };
  }, [key]);
  const more = async () => {
    if (paging.current) return;
    const after = page.nextAfter; if (!after) return;
    paging.current = true; const mine = seq.current;
    try {
      const r = await op(opName, { ...params, limit: 100, after });
      if (mine !== seq.current) return;
      setPage(cur => {
        if (cur.nextAfter !== after) return cur; // another page or a reload got there first
        const have = new Set(cur.rows.map(x => x.id));
        return { ...cur, rows: [...cur.rows, ...(r.tasks ?? r.sessions).filter((x: Row) => !have.has(x.id))], nextAfter: r.nextAfter };
      });
    } catch (e: any) { setError(e.message); } finally { paging.current = false; }
  };
  return { ...page, more, restored, error, state: freshness(restored), reload: fresh };
}

/** One record re-read when a change touches it (detail views); refreshMs re-reads on a timer as well. */
export function useLiveRecord<T>(key: string | null, fetch: () => Promise<T>, touches: (c: Change) => boolean, refreshMs?: number) {
  const [data, setData] = useState<T | null>(null);
  const [restored, setRestored] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const fetchRef = useRef(fetch); fetchRef.current = fetch;
  const touchRef = useRef(touches); touchRef.current = touches;
  useEffect(() => {
    if (!key) { setData(null); return; }
    let live = true, seq = 0;
    const get = async () => {
      const mine = ++seq;
      try { const d = await fetchRef.current(); if (live && mine === seq) { setData(d); setRestored(false); setError(null); save(key, d, currentCursor()); } }
      catch (e: any) { if (live && mine === seq) setError(e.message); }
    };
    setData(null);
    load<T>(key).then(s => { if (live && s) setData(d => { if (d) return d; setRestored(true); return s.data; }); });
    get();
    const off = subscribe({ resync: get, changes: cs => { if (cs.some(c => touchRef.current(c))) get(); } });
    const timer = refreshMs ? setInterval(get, refreshMs) : 0;
    return () => { live = false; off(); clearInterval(timer); };
  }, [key, refreshMs]);
  return { data, restored, error, state: freshness(restored) };
}
