// Per-person conveniences kept in the browser until people exist (Phase 6): pinned rows, saved views, pinned sections.
import { useState } from 'preact/hooks';
import type { View } from '../../packages/view-url/view-url.ts';

const read = <T>(k: string, d: T): T => { try { const v = localStorage.getItem(k); return v ? JSON.parse(v) : d; } catch { return d; } };
const write = (k: string, v: unknown) => { try { localStorage.setItem(k, JSON.stringify(v)); } catch { /* storage unavailable */ } };

export function usePins(list: string) {
  const key = 'fleet-pins:' + list;
  const [ids, setIds] = useState<string[]>(() => read(key, []));
  const set = new Set(ids);
  return { has: (id: string) => set.has(id), toggle: (id: string) => { const n = set.has(id) ? ids.filter(x => x !== id) : [...ids, id]; setIds(n); write(key, n); } };
}

/** A saved preset: a name and the view keys it sets (docs/designs/fleet-view-urls-and-list-path.md §1). */
export type SavedView = { name: string; view: View };
// Before view URLs a preset was { name, states, group }; it is read as a tasks view and written back in the new form.
const upgrade = (v: any): SavedView => v && v.view ? v : { name: v.name, view: { view: 'tasks', state: v.states ?? [], group: v.group } };
export function useViews(list: string, defaults: SavedView[]) {
  const key = 'fleet-views:' + list;
  const [views, setViews] = useState<SavedView[]>(() => { const raw: any[] = read(key, defaults); const up = raw.map(upgrade); if (raw.some(v => !v.view)) write(key, up); return up; });
  const keep = (v: SavedView[]) => { setViews(v); write(key, v); };
  return { list: views, save: (v: SavedView) => keep([...views.filter(x => x.name !== v.name), v]), remove: (n: string) => keep(views.filter(x => x.name !== n)) };
}
