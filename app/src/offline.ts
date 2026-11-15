// Read-only cache; authored input belongs to api.ts and is never deleted here.
import { accessGeneration, isAccessCurrent, onAccessInvalidated } from './access.ts';
const DB = 'fleet-app', STORE = 'views';
let dbp: Promise<IDBDatabase> | null = null, cleared = false, deleting = false, clearPromise: Promise<void> | null = null;
let storageEpoch = 0;
const transactions = new Set<IDBTransaction>();
const open = () => dbp ??= new Promise((ok, no) => {
  const epoch = storageEpoch, r = indexedDB.open(DB, 1);
  r.onupgradeneeded = () => r.result.createObjectStore(STORE);
  r.onsuccess = () => {
    if (epoch !== storageEpoch || deleting) { r.result.close(); no(Error('Cache fenced')); return; }
    r.result.onversionchange = () => { r.result.close(); dbp = null; }; ok(r.result);
  };
  r.onerror = () => { dbp = null; no(r.error); };
});
const tx = async <T>(mode: IDBTransactionMode, fn: (s: IDBObjectStore) => IDBRequest<T>) => {
  const generation = accessGeneration(), epoch = storageEpoch;
  const allowed = () => !cleared && !deleting && epoch === storageEpoch && isAccessCurrent(generation);
  if (!allowed()) return undefined;
  try {
    const db = await open(); if (!allowed()) return undefined;
    const value = await new Promise<T>((ok, no) => {
      const t = db.transaction(STORE, mode); transactions.add(t); let value: T;
      const r = fn(t.objectStore(STORE)); r.onsuccess = () => { value = r.result; };
      t.oncomplete = () => { transactions.delete(t); ok(value); };
      t.onabort = t.onerror = () => { transactions.delete(t); no(t.error); };
    });
    return allowed() ? value : undefined;
  } catch { return undefined; }
};
export type Saved<T> = { data: T; cursor: number | null; at: number };
const timers = new Map<string, ReturnType<typeof setTimeout>>();
export function save<T>(key: string, data: T | (() => T), cursor: number | null) {
  const generation = accessGeneration(), epoch = storageEpoch;
  if (cleared || deleting || !isAccessCurrent(generation)) return;
  clearTimeout(timers.get(key));
  timers.set(key, setTimeout(() => {
    timers.delete(key); if (epoch !== storageEpoch || !isAccessCurrent(generation)) return;
    const value = typeof data === 'function' ? (data as () => T)() : data;
    void tx('readwrite', s => s.put({ data: value, cursor, at: Date.now() } satisfies Saved<T>, key));
  }, 1000));
}
export const load = <T>(key: string) => tx<Saved<T> | undefined>('readonly', s => s.get(key));
export const onClear = new Set<() => void>();
export async function pruneKeys(keep: (key: string) => boolean) {
  const generation = accessGeneration();
  const keys = await tx<IDBValidKey[]>('readonly', s => s.getAllKeys());
  if (!isAccessCurrent(generation)) return;
  for (const k of keys ?? []) if (typeof k !== 'string' || !keep(k)) await tx('readwrite', s => s.delete(k));
}
/** Fence synchronously. After two seconds a blocked clear permits authorized memory only. */
export function clearAll(stopSaving = false): Promise<void> {
  if (stopSaving) cleared = true;
  if (clearPromise) return clearPromise;
  let finish!: () => void;
  clearPromise = new Promise<void>(resolve => { finish = resolve; });
  const result = clearPromise;
  storageEpoch++; deleting = true;
  for (const t of timers.values()) clearTimeout(t); timers.clear();
  for (const t of transactions) { try { t.abort(); } catch { /* already complete */ } }
  transactions.clear();
  for (const f of onClear) f();
  const old = dbp; dbp = null;
  const deletion = (async () => {
    const db = old ? await old.catch(() => null) : null; db?.close();
    if (typeof indexedDB !== 'undefined') await new Promise<void>(resolve => {
      const q = indexedDB.deleteDatabase(DB); q.onsuccess = () => { deleting = false; resolve(); };
      q.onerror = () => resolve(); // failed deletion keeps persistence disabled
      q.onblocked = () => {}; // completion continues in the background
    }); else deleting = false;
  })();
  const timer = setTimeout(finish, 2000);
  deletion.finally(() => { clearTimeout(timer); clearPromise = null; finish(); });
  return result;
}
onAccessInvalidated.add(() => {
  if (clearPromise) { storageEpoch++; for (const t of timers.values()) clearTimeout(t); timers.clear(); for (const t of transactions) { try { t.abort(); } catch {} } transactions.clear(); for (const fn of onClear) fn(); }
  void clearAll();
});
export async function checkGrants(version: string) {
  // The access validator already fenced and joined the one clear operation.
  await clearAll();
  try { localStorage.setItem('fleet-grants', version); } catch { /* optional cache */ }
}
