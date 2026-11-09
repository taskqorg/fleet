// Read-only IndexedDB mirror of what open views last showed (D3). Writes are never queued. Cleared on sign-out and when the
// grants version changes; the restore cursor is the lowest one among the restored views.
const DB = 'fleet-app', STORE = 'views';
let dbp: Promise<IDBDatabase> | null = null;
const open = () => dbp ??= new Promise((ok, no) => { const r = indexedDB.open(DB, 1); r.onupgradeneeded = () => r.result.createObjectStore(STORE); r.onsuccess = () => ok(r.result); r.onerror = () => no(r.error); });
const tx = async <T>(mode: IDBTransactionMode, fn: (s: IDBObjectStore) => IDBRequest<T>) => {
  try { const db = await open(); return await new Promise<T>((ok, no) => { const r = fn(db.transaction(STORE, mode).objectStore(STORE)); r.onsuccess = () => ok(r.result); r.onerror = () => no(r.error); }); }
  catch { return undefined as T; } // storage unavailable (private window): the app works without it
};
export type Saved<T> = { data: T; cursor: number | null; at: number };
const timers = new Map<string, number>();
export function save<T>(key: string, data: T, cursor: number | null) {
  clearTimeout(timers.get(key));
  timers.set(key, setTimeout(() => tx('readwrite', s => s.put({ data, cursor, at: Date.now() } satisfies Saved<T>, key)), 1000) as unknown as number);
}
export const load = <T>(key: string) => tx<Saved<T> | undefined>('readonly', s => s.get(key));
export async function lowestCursor(): Promise<number | null> {
  const all = await tx<Saved<unknown>[]>('readonly', s => s.getAll());
  const cs = (all ?? []).map(v => v.cursor).filter((c): c is number => typeof c === 'number');
  return cs.length ? Math.min(...cs) : null;
}
export async function clearAll() { dbp = null; await new Promise(r => { const q = indexedDB.deleteDatabase(DB); q.onsuccess = q.onerror = q.onblocked = () => r(null); }); }
export async function checkGrants(version: string) {
  let stored: string | null = null; try { stored = localStorage.getItem('fleet-grants'); } catch { /* none */ }
  if (stored !== null && stored !== version) await clearAll();
  try { localStorage.setItem('fleet-grants', version); } catch { /* none */ }
}
