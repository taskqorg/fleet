// The read mirror (operator-app D3): the last answer of every read this person's tab made, kept in IndexedDB under the
// person and grants that read it, so a reload (an app update, a new tab) renders at once and then catches up.
// Read-only: authored input belongs to api.ts and never lands here. Without IndexedDB (tests, a refused open) every call
// is a no-op and the app reads from the network as before.
const DB = 'fleet-data', STORE = 'reads', OWNER_KEY = 'fleet-data-owner', WRITE_DELAY_MS = 1000;
export type Owner = { identityKey: string; grantsVersion: string };
type Stored = { data: unknown; at: number };

/**
 * The owner as a key: a 64-bit FNV-1a hash of person and grants, so neither the email nor the grants are stored in the
 * browser (localStorage is readable by any script on the page). It names the mirror; it grants nothing.
 */
function ownerOf(o: Owner): string {
  let h = 0xcbf29ce484222325n;
  for (const b of new TextEncoder().encode(o.identityKey + '\n' + o.grantsVersion)) h = BigInt.asUintN(64, (h ^ BigInt(b)) * 0x100000001b3n);
  return h.toString(36);
}
function readHint(): string | null { try { return localStorage.getItem(OWNER_KEY); } catch { return null; } }
function writeHint(v: string | null) { try { if (v === null) localStorage.removeItem(OWNER_KEY); else localStorage.setItem(OWNER_KEY, v); } catch { /* the mirror only lasts this page */ } }

// The owner whose answers are read and written now: the hint left by the last validated page until this page validates.
let owner: string | null = readHint();
// Bumped by every purge: a read or a write begun before it lands nowhere.
let epoch = 0;
let dbp: Promise<IDBDatabase | null> | null = null;
const open = () => dbp ??= new Promise(resolve => {
  if (typeof indexedDB === 'undefined') { resolve(null); return; }
  try {
    const r = indexedDB.open(DB, 1);
    r.onupgradeneeded = () => r.result.createObjectStore(STORE);
    r.onsuccess = () => { r.result.onversionchange = () => { r.result.close(); dbp = null; }; resolve(r.result); };
    r.onerror = () => { dbp = null; resolve(null); };
    r.onblocked = () => resolve(null);
  } catch { resolve(null); }
});
async function run<T>(mode: IDBTransactionMode, fn: (s: IDBObjectStore) => IDBRequest<T> | void): Promise<T | undefined> {
  const db = await open(); if (!db) return undefined;
  return new Promise<T | undefined>(resolve => {
    try {
      const t = db.transaction(STORE, mode); let value: T | undefined;
      const r = fn(t.objectStore(STORE)); if (r) r.onsuccess = () => { value = r.result; };
      t.oncomplete = () => resolve(value); t.onabort = t.onerror = () => resolve(undefined);
    } catch { resolve(undefined); }
  });
}

/** Whether answers of some person may be shown before this page validates (a person validated here before). */
export const mirrored = () => owner !== null;

/** The stored answer of key for the current owner; undefined when there is none or the owner changed meanwhile. */
export async function restore(key: string): Promise<Stored | undefined> {
  const o = owner, e = epoch; if (o === null) return undefined;
  const v = await run<Stored>('readonly', s => s.get(o + '\u0000' + key));
  return e === epoch && o === owner && v && typeof v === 'object' && 'data' in v ? v : undefined;
}

const timers = new Map<string, ReturnType<typeof setTimeout>>();
/** Keep the newest answer of key for the current owner (written after a second of quiet, so bursts write once). */
export function persist(key: string, data: unknown) {
  const o = owner, e = epoch; if (o === null) return;
  clearTimeout(timers.get(key));
  timers.set(key, setTimeout(() => {
    timers.delete(key);
    if (e !== epoch || o !== owner) return;
    void run('readwrite', s => s.put({ data, at: Date.now() } satisfies Stored, o + '\u0000' + key));
  }, WRITE_DELAY_MS));
}

/** Drop every stored answer and pending write (sign-out, another person, changed grants). */
export function purge(): Promise<void> {
  epoch++;
  for (const t of timers.values()) clearTimeout(t); timers.clear();
  return run('readwrite', s => s.clear()).then(() => {});
}

/**
 * The validated person and grants of this page. Returns true when they differ from the mirror's owner: the mirror is
 * purged and what was shown from it must be cleared too (the caller clears memory).
 */
export function adopt(o: Owner): boolean {
  const next = ownerOf(o);
  if (next === owner) return false;
  const had = owner !== null;
  owner = next; writeHint(next); void purge();
  return had;
}

/** Signed out: nothing is kept for anyone. */
export function forget(): Promise<void> { owner = null; writeHint(null); return purge(); }
