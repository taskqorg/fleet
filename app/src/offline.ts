// Read-only IndexedDB mirror of what open views last showed (D3). Writes are never queued. Cleared on sign-out and when the
// grants version changes. Stored data only fills the first frame: every view loads fresh data on mount.
const DB = 'fleet-app', STORE = 'views';
let dbp: Promise<IDBDatabase> | null = null;
let cleared = false;
const open = () => dbp ??= new Promise((ok, no) => {
  const r = indexedDB.open(DB, 1);
  r.onupgradeneeded = () => r.result.createObjectStore(STORE);
  // Another window deleting the database (sign-out, grants change) closes this connection so the delete is not blocked.
  r.onsuccess = () => { r.result.onversionchange = () => { r.result.close(); dbp = null; }; ok(r.result); };
  r.onerror = () => no(r.error);
});
const tx = async <T>(mode: IDBTransactionMode, fn: (s: IDBObjectStore) => IDBRequest<T>) => {
  try { const db = await open(); return await new Promise<T>((ok, no) => { const r = fn(db.transaction(STORE, mode).objectStore(STORE)); r.onsuccess = () => ok(r.result); r.onerror = () => no(r.error); }); }
  catch { return undefined as T; } // storage unavailable (private window): the app works without it
};
export type Saved<T> = { data: T; cursor: number | null; at: number };
const timers = new Map<string, ReturnType<typeof setTimeout>>();
/** data may be a function: it is called when the write happens (once per second at most), so a snapshot is built lazily. */
export function save<T>(key: string, data: T | (() => T), cursor: number | null) {
  if (cleared) return; // after sign-out nothing is stored again in this page
  clearTimeout(timers.get(key));
  timers.set(key, setTimeout(() => { timers.delete(key); const value = typeof data === 'function' ? (data as () => T)() : data; tx('readwrite', s => s.put({ data: value, cursor, at: Date.now() } satisfies Saved<T>, key)); }, 1000));
}
export const load = <T>(key: string) => tx<Saved<T> | undefined>('readonly', s => s.get(key));
/** Called when everything stored is deleted (the store forgets what it holds in memory too). */
export const onClear = new Set<() => void>();
/** Delete stored keys that keep() rejects (keys of an older layout); their cursors would hold the feed back. */
export async function pruneKeys(keep: (key: string) => boolean) {
  const keys = await tx<IDBValidKey[]>('readonly', s => s.getAllKeys());
  for (const k of keys ?? []) if (typeof k !== 'string' || !keep(k)) await tx('readwrite', s => s.delete(k));
}
/** Delete everything: cancel pending writes, close this page's connection, and wait for the delete to finish. */
export async function clearAll(stopSaving = false) {
  if (stopSaving) cleared = true;
  for (const f of onClear) f();
  for (const t of timers.values()) clearTimeout(t);
  timers.clear();
  const db = dbp ? await dbp.catch(() => null) : null;
  db?.close(); dbp = null;
  if (typeof indexedDB === 'undefined') return; // storage unavailable: nothing to delete
  await new Promise<void>(r => { const q = indexedDB.deleteDatabase(DB); q.onsuccess = q.onerror = () => r(); q.onblocked = () => { /* other windows close on versionchange */ }; });
}
export async function checkGrants(version: string) {
  let stored: string | null = null; try { stored = localStorage.getItem('fleet-grants'); } catch { /* none */ }
  if (stored !== null && stored !== version) await clearAll();
  try { localStorage.setItem('fleet-grants', version); } catch { /* none */ }
}
