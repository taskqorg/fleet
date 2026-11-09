// Per-person conveniences kept in the browser until people exist (Phase 6): pinned rows, saved views, pinned sections.
import { useState } from 'preact/hooks';

const read = <T>(k: string, d: T): T => { try { const v = localStorage.getItem(k); return v ? JSON.parse(v) : d; } catch { return d; } };
const write = (k: string, v: unknown) => { try { localStorage.setItem(k, JSON.stringify(v)); } catch { /* storage unavailable */ } };

export function usePins(list: string) {
  const key = 'fleet-pins:' + list;
  const [ids, setIds] = useState<string[]>(() => read(key, []));
  const set = new Set(ids);
  return { has: (id: string) => set.has(id), toggle: (id: string) => { const n = set.has(id) ? ids.filter(x => x !== id) : [...ids, id]; setIds(n); write(key, n); } };
}

export type SavedView = { name: string; states: string[]; group: string };
export function useViews(list: string, defaults: SavedView[]) {
  const key = 'fleet-views:' + list;
  const [views, setViews] = useState<SavedView[]>(() => read(key, defaults));
  const keep = (v: SavedView[]) => { setViews(v); write(key, v); };
  return { list: views, save: (v: SavedView) => keep([...views.filter(x => x.name !== v.name), v]), remove: (n: string) => keep(views.filter(x => x.name !== n)) };
}
