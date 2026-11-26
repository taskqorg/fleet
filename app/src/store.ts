// Entity store (operator-app D4; docs/designs/fleet-view-urls-and-list-path.md §2): shared in-memory lists and docs,
// kept current from the change feed. Lists reconcile affected rows and their summaries in place.
import { AccessChanged, accessGeneration, ensureAccessGeneration, isAccessCurrent, assertAccessGeneration } from './access.ts';
import { createEffect, createMemo, createSignal, runWithOwner, untrack } from 'solid-js';
import { cell, readValue, type Cell, type MaybeAccessor } from './signal.ts';
import { op, online, signedOut, OpError, type Ops, type OpName } from './api.ts';
import type { ViewName } from '../../contracts/generated/ops.ts';
import { subscribe, feedState, type Change } from './feed.ts';
import { compareKeys, type Row, type SortKey } from './lists.ts';
import type { ListGroup, MembershipAnswer, TaskRow, SessionRow } from '../../contracts/generated/ops.ts';
import { onClear } from './offline.ts';
import { persist, restore } from './persist.ts';
import { share } from './share.ts';
import { listKey, toPageParams, type View } from '../../packages/view-url/view-url.ts';
import { withFocus, prefsRead } from './prefs.ts';

export type Freshness = 'current' | 'updating' | 'offline' | 'signed-out';
const errMsg = (e: unknown) => e instanceof Error ? e.message : typeof e === 'object' && e !== null && 'message' in e && typeof (e as { message: unknown }).message === 'string' ? (e as { message: string }).message : String(e);
export const freshness = (restored: boolean): Freshness => signedOut.value ? 'signed-out' : !online.value ? 'offline' : restored ? 'updating' : 'current';

export type Kind = 'tasks' | 'sessions';
type Params = ReturnType<typeof toPageParams>;
type Group = ListGroup;
/** restored: the state came from the read mirror (persist.ts) and no read of this page has replaced it yet. */
/** Conversations: the first page's Started by counts (origin left out) and its hidden steps (sessions_list origins, hiddenCount). */
export type ListFacets = { origins: Record<string, number>; hidden: number };
export type ListState = { rows: Row[]; counts: Record<string, number>; total: number; groups: Group[]; nextAfter: unknown; at: number; error: string | null; restored?: boolean; facets?: ListFacets };
type Entry = {
  kind: Kind; view: View; params: Params; state: Cell<ListState | null>; busy: Cell<boolean>;
  started: number; mounted: number; loading: boolean; dirty: boolean; paging: boolean; cursorReset: boolean;
  pageError: boolean; fetchedAt: number; affected: Set<string>; lastMembershipAt: number;
  reconciling?: Promise<void>; throttleTimer?: ReturnType<typeof setTimeout>;
};
type Doc = { data: unknown; error: string | null; status?: number | null; restored?: boolean };
type DocEntry = { signal: Cell<Doc | null>; fetchedAt: number; dirty: boolean; dirtySeq: number; name?: OpName; request?: unknown };

const FRESH_MS = 30_000;
const MAX_UNMOUNTED_DOCS = 100;
const MAX_UNMOUNTED_LISTS = 30;
const APPLY_THROTTLE_MS = 1000;
const MAX_MEMBERSHIP_IDS = 1000;
const PREPARED_PREPARING_MS = 5_000;
const PREPARED_POLL_MS = 10_000;

const lists = new Map<string, Entry>();
const docs = new Map<string, DocEntry>();
/** Bumped when stored data is cleared: mounted docs re-read. */
const generation = cell(0);

function touchList(key: string, e: Entry) { lists.delete(key); lists.set(key, e); }
function evictLists() {
  let unmounted = 0;
  for (const e of lists.values()) if (!e.mounted) unmounted++;
  if (unmounted <= MAX_UNMOUNTED_LISTS) return;
  for (const [k, e] of lists) {
    if (unmounted <= MAX_UNMOUNTED_LISTS) break;
    if (!e.mounted) { clearTimeout(e.throttleTimer); e.throttleTimer = undefined; lists.delete(k); unmounted--; }
  }
}

function touchDoc(name: string, e: DocEntry) { docs.delete(name); docs.set(name, e); }
function evictDocs() {
  let unmounted = 0;
  for (const k of docs.keys()) if (!flights.get(k)?.readers.size) unmounted++;
  if (unmounted <= MAX_UNMOUNTED_DOCS) return;
  for (const k of docs.keys()) {
    if (unmounted <= MAX_UNMOUNTED_DOCS) break;
    if (!flights.get(k)?.readers.size) { docs.delete(k); unmounted--; }
  }
}

/**
 * Forget everything held in memory (sign-out, grants change). Entries and doc signals stay because mounted hooks hold
 * them; their values reset and mounted lists and docs load again.
 */
export function clearMemory() {
  for (const [key, e] of lists) {
    clearTimeout(e.throttleTimer); e.throttleTimer = undefined;
    e.started++; e.loading = false; e.paging = false; e.reconciling = undefined;
    e.affected.clear(); e.dirty = false; e.fetchedAt = 0; e.lastMembershipAt = 0;
    e.state.value = null; e.busy.value = false;
    if (!e.mounted) lists.delete(key);
    else if (isAccessCurrent(accessGeneration())) void loadList(e.kind, key, e.view);
  }
  discardDocReads();
  for (const [key, d] of docs) {
    d.signal.value = null; d.fetchedAt = 0; d.dirty = false; d.dirtySeq++;
    if (!flights.get(key)?.readers.size) docs.delete(key);
  }
  generation.value++;
  // What stays mounted shows the mirror's copy again until its read answers, as at load: the access check every load
  // starts with clears memory, and a screen mounted before it (Lab's role check) waited for the network otherwise. The
  // mirror is the validated owner's only (persist.ts): another person or changed grants purge it before it is read.
  for (const [key, e] of lists) restoreList(key, e);
  for (const [key, d] of docs) restoreDoc(key, d);
}
onClear.add(clearMemory);

function entry(kind: Kind, key: string, view: View): Entry {
  let e = lists.get(key);
  if (!e) {
    const made: Entry = e = { kind, view, params: toPageParams(view), state: cell<ListState | null>(null), busy: cell(false), started: 0, mounted: 0, loading: false, dirty: false, paging: false, cursorReset: false, pageError: false, fetchedAt: 0, affected: new Set(), lastMembershipAt: 0 };
    lists.set(key, made);
    evictLists();
    restoreList(key, made);
  }
  return e;
}
/** The mirror's copy of a list shows until the first read of this page answers; a read that answered first wins. */
function restoreList(key: string, made: Entry) {
  const cleared = generation.peek();
  void restore('list:' + key).then(v => {
    if (v && cleared === generation.peek() && lists.get(key) === made && !made.state.peek()) made.state.value = { ...(v.data as ListState), error: null, restored: true };
  });
}
const rowsOf = (r: Ops['task_page']['reply'] | Ops['sessions_list']['reply']): Row[] => 'tasks' in r ? r.tasks : r.sessions;
const opName = (k: Kind) => k === 'tasks' ? 'task_page' : 'sessions_list';

type ListRead = { signal?: AbortSignal; quiet?: boolean; cursorReset?: boolean; preserveExtent?: boolean };
const PAGE_SIZE = 100;

/** Replace the authorized snapshot. Quiet recovery keeps the loaded extent in one state update. */
/** The first page's facets, when its list answers them (sessions_list). */
function facetsOf(page: unknown): ListFacets | undefined {
  const p = page as { origins?: Record<string, number>; hiddenCount?: number };
  return p.origins ? { origins: p.origins, hidden: p.hiddenCount ?? 0 } : undefined;
}

export async function loadList(kind: Kind, key: string, view: View, opts: ListRead = {}) {
  const e = entry(kind, key, view), read = ++e.started, generation = accessGeneration();
  const extent = opts.quiet || opts.preserveExtent ? e.state.peek()?.rows.length ?? 0 : 0;
  const interrupted = e.dirty || e.affected.size > 0 || !!e.reconciling;
  clearTimeout(e.throttleTimer); e.throttleTimer = undefined;
  e.affected.clear(); e.dirty = false; e.cursorReset = !!opts.cursorReset;
  e.loading = true; e.busy.value = !opts.quiet;
  const current = () => read === e.started && isAccessCurrent(generation) && lists.get(key) === e;
  try {
    await ensureAccessGeneration(); assertAccessGeneration(generation);
    const fetchPage = (after?: unknown[]) => op(opName(kind), { ...e.params, limit: PAGE_SIZE, after }, { signal: opts.signal });
    const first = await fetchPage();
    const rows = rowsOf(first).slice();
    let nextAfter = first.nextAfter;
    if (rows.length < extent && nextAfter) {
      const seen = new Set(rows.map(r => r.id));
      while (rows.length < extent && nextAfter && current()) {
        const page = await fetchPage(nextAfter as unknown[]);
        for (const r of rowsOf(page)) if (!seen.has(r.id)) { seen.add(r.id); rows.push(r); }
        if (page.nextAfter && canonical(page.nextAfter) === canonical(nextAfter)) throw Error('Paging cursor did not advance');
        nextAfter = page.nextAfter;
      }
    }
    if (!current()) return;
    const now = Date.now();
    // A reread keeps the row objects that did not change (share.ts), so rows on screen are not rebuilt.
    const was = e.state.peek();
    e.state.value = { rows: share(was?.rows ?? rows, rows), counts: share(was?.counts ?? first.counts, first.counts), total: first.total, groups: share(was?.groups ?? first.groups ?? [], first.groups ?? []), nextAfter, at: now, error: null, facets: facetsOf(first) };
    persist('list:' + key, e.state.peek());
    e.fetchedAt = now;
    e.pageError = false;
    touchList(key, e);
  } catch (err) {
    if (!current()) return;
    if (err instanceof AccessChanged || err instanceof OpError && err.message === 'aborted') {
      if (interrupted) e.dirty = true;
      return;
    }
    const s = e.state.peek(), msg = errMsg(err); e.pageError = false; e.fetchedAt = 0;
    e.state.value = s ? { ...s, error: msg } : { rows: [], counts: {}, total: 0, groups: [], nextAfter: null, at: 0, error: msg };
  } finally {
    if (read === e.started) {
      e.loading = false; e.busy.value = false;
      if (isAccessCurrent(generation)) await settleChanges(key, e);
    }
  }
}

/**
 * The next page of the list stored under key, with that list's own params. A refused cursor (400)
 * reloads the first page once per list load, keeping rows on screen.
 */
export async function nextPage(key: string) {
  const e = lists.get(key);
  if (!e || e.paging || e.loading) return;
  const generation = accessGeneration();
  if (!isAccessCurrent(generation)) return;
  e.paging = true;
  if (e.reconciling) await e.reconciling;
  if (!isAccessCurrent(generation) || lists.get(key) !== e) return;
  const st = e.state.peek();
  if (!st?.nextAfter || e.loading) { e.paging = false; await settleChanges(key, e); return; }
  const read = e.started;
  const current = () => isAccessCurrent(generation) && read === e.started && e.state.peek()?.nextAfter === st.nextAfter;
  try {
    const r = await op(opName(e.kind), { ...e.params, limit: PAGE_SIZE, after: st.nextAfter as unknown[] });
    assertAccessGeneration(generation);
    const now = e.state.peek();
    if (!current() || !now) return;
    const have = new Set(now.rows.map(x => x.id)), add = rowsOf(r).filter(x => !have.has(x.id));
    e.state.value = { ...now, rows: [...now.rows, ...add], nextAfter: r.nextAfter, error: e.pageError ? null : now.error };
    persist('list:' + key, e.state.peek());
    e.pageError = false;
  } catch (err) {
    const now = e.state.peek();
    if (!current() || !now) return;
    if (err instanceof OpError && err.status === 400 && !e.cursorReset) {
      await loadList(e.kind, key, e.view, { quiet: true, cursorReset: true });
      const fresh = e.state.peek();
      if (isAccessCurrent(generation) && fresh && !fresh.error && fresh.nextAfter && fresh.nextAfter !== st.nextAfter) {
        e.paging = false; await nextPage(key);
      }
      return;
    }
    e.state.value = { ...now, error: errMsg(err) }; e.pageError = true;
  } finally {
    if (isAccessCurrent(generation)) { e.paging = false; await settleChanges(key, e); }
  }
}

// Task rows read task and task_derived (both emit task changes). Session projections also read
// execution, question, message and non-delta event data. The feed's event kind covers messages too.
function affectedRow(c: Change, kind: Kind): string | null | undefined {
  // A delivery pass names a project, not the tasks whose pull request moved (task rows carry its stage): reread the list.
  if (kind === 'tasks') return c.kind === 'task' ? c.taskId || c.id || null : c.kind === 'delivery' ? null : undefined;
  if (c.kind === 'session') return c.sessionId || c.id || null;
  if (c.kind === 'execution' || c.kind === 'question' || c.kind === 'event') return c.sessionId || undefined;
  return undefined;
}

function sortKey(value: unknown): value is SortKey {
  return Array.isArray(value) && value.length === 3 && typeof value[0] === 'string'
    && typeof value[1] === 'number' && Number.isFinite(value[1]) && typeof value[2] === 'string';
}

function reconcileRows(s: ListState, ids: Set<string>, answer: MembershipAnswer, kind: Kind): ListState | null {
  if (answer.name !== kind || !Array.isArray(answer.rows) || !answer.counts
    || (answer.groups != null && !Array.isArray(answer.groups))) return null;
  const boundary = s.nextAfter;
  if (boundary != null && !sortKey(boundary)) return null;
  const changed = new Map<string, Row>();
  for (const row of answer.rows) {
    if (!ids.has(row.id) || changed.has(row.id) || !sortKey(row.sortKey) || row.sortKey[2] !== row.id) return null;
    changed.set(row.id, row);
  }
  const rows: Row[] = [];
  const insert = (row: Row) => {
    if (boundary == null || compareKeys(row.sortKey, boundary) <= 0) rows.push(row);
  };
  let reorder = false;
  for (const previous of s.rows) {
    if (!ids.has(previous.id)) { rows.push(previous); continue; }
    const row = changed.get(previous.id);
    changed.delete(previous.id);
    if (row && compareKeys(previous.sortKey, row.sortKey) === 0) {
      rows.push(canonical(previous) === canonical(row) ? previous : row);
    } else if (row) {
      insert(row); reorder = true;
    }
  }
  for (const row of changed.values()) insert(row);
  if (rows.length === 0 && boundary != null) return null; // there is no visible edge left from which to page
  if (reorder || changed.size > 0) rows.sort((a, b) => compareKeys(a.sortKey, b.sortKey));
  const counts = answer.counts, groups = answer.groups ?? [];
  const total = Object.values(counts).reduce((n, value) => n + value, 0);
  if (!Number.isSafeInteger(total) || total < 0) return null;
  if (rows.length === s.rows.length && rows.every((r, i) => r === s.rows[i])
    && canonical(counts) === canonical(s.counts) && canonical(groups) === canonical(s.groups)) return s;
  return { ...s, rows, counts, total, groups, at: Date.now() };
}

async function reconcileList(key: string, e: Entry, s: ListState, ids: Set<string>) {
  const generation = accessGeneration(), read = e.started;
  const current = () => isAccessCurrent(generation) && read === e.started && lists.get(key) === e && !!e.mounted;
  try {
    // Cursors are opaque, in delivery order rather than numeric order. Only one membership read
    // may land per list at a time. Changes delivered during this read go into the next batch.
    const { lists: [answer] } = await op('list_membership', { ids: [...ids], lists: [{ name: e.kind, ...e.params }] });
    if (!current()) return;
    const next = answer && reconcileRows(s, ids, answer, e.kind);
    if (next) {
      if (next !== s) { e.state.value = next; persist('list:' + key, next); }
      e.fetchedAt = Date.now();
      return;
    }
  } catch {
    if (!current()) return;
  }
  // A failed or unproven membership answer cannot update the loaded window.
  if (current()) await loadList(e.kind, key, e.view, { quiet: true });
}

async function settleChanges(key: string, e: Entry) {
  if (!e.mounted || e.loading || e.paging || e.reconciling || !isAccessCurrent(accessGeneration())) return;
  if (!e.dirty && !e.affected.size) return;
  const s = e.state.peek();
  if (e.dirty || !s || !s.at || (s.error && !e.pageError)) {
    await loadList(e.kind, key, e.view, { quiet: true });
    return;
  }
  const wait = e.lastMembershipAt + APPLY_THROTTLE_MS - Date.now();
  if (wait > 0) {
    e.throttleTimer ??= setTimeout(() => {
      e.throttleTimer = undefined;
      void settleChanges(key, e);
    }, wait);
    return;
  }
  clearTimeout(e.throttleTimer); e.throttleTimer = undefined;
  const ids = e.affected; e.affected = new Set();
  e.lastMembershipAt = Date.now();
  const read = e.reconciling = reconcileList(key, e, s, ids);
  try { await read; }
  finally {
    if (e.reconciling === read) { e.reconciling = undefined; await settleChanges(key, e); }
  }
}

/** Queue one authoritative read per active list, deduplicating entity IDs across a burst. */
export async function applyChanges(cs: Change[]) {
  if (!isAccessCurrent(accessGeneration())) return;
  const reads: Promise<void>[] = [];
  for (const [key, e] of lists) {
    for (const c of cs) {
      if (e.dirty) break;
      const id = affectedRow(c, e.kind);
      if (id === undefined) continue;
      // The feed identifies the changed entity, but not the old project or whether it used to
      // contribute to a summary. Let the server decide which IDs count in this scope.
      if (!e.mounted || id === null) { e.dirty = true; break; }
      e.affected.add(id);
      if (e.affected.size > MAX_MEMBERSHIP_IDS) { e.affected.clear(); e.dirty = true; }
    }
    // A hidden tab settles its lists when it shows again (startStore).
    if (e.mounted && !hiddenTab()) reads.push(settleChanges(key, e));
  }
  await Promise.all(reads);
}

let started = false;
/** One feed subscription for all lists (started once from main.tsx). */
export function startStore() {
  if (started) return; started = true;
  const reloadMounted = (retryOnly = false) => {
    for (const [key, e] of lists) {
      const s = e.state.peek();
      // A hidden tab settles its lists when it shows again (the visibility handler below).
      if (e.mounted && !retryOnly && hiddenTab()) e.dirty = true;
      else if (e.mounted && !e.loading && (!retryOnly || !s || s.error)) {
        void loadList(e.kind, key, e.view, { quiet: !!s?.at });
      } else if (!e.mounted && !retryOnly) e.dirty = true;
    }
    if (!retryOnly) for (const [k, d] of docs) if (!flights.get(k)?.readers.size) { d.dirty = true; d.dirtySeq++; }
  };
  subscribe({ resync: () => reloadMounted(), changes: applyChanges });
  if (typeof document !== 'undefined') document.addEventListener('visibilitychange', () => {
    if (hiddenTab()) return;
    for (const [key, e] of lists) if (e.mounted && (e.dirty || e.affected.size)) void settleChanges(key, e);
  });
  runWithOwner(null, () => createEffect(() => feedState.value, state => { if (state === 'live') reloadMounted(true); }));
}

/** The row type of each list kind. */
export type RowOf<K extends Kind> = K extends 'tasks' ? TaskRow : SessionRow;
export function useList<K extends Kind>(kind: K, urlViewInput: MaybeAccessor<View>) {
  const view = createMemo(() => withFocus(readValue(urlViewInput)));
  const key = createMemo(() => listKey(view()));
  // A clear (generation) may drop an entry whose view was between mounts: both memos then take the new one.
  const currentEntry = createMemo(() => (generation.value, entry(kind, key(), view())));
  const [last, setLast] = createSignal<string | null>(untrack(() => (currentEntry().state.peek() ? key() : null)), { ownedWrite: true });
  let previousKey: string | undefined;
  const shown = createMemo(() => (currentEntry().state.value ? key() : last()));
  createEffect(() => shown(), s => { if (s !== untrack(last)) setLast(s); });
  createEffect(
    () => [key(), prefsRead.value] as const,
    ([k, ready]) => {
      if (!ready) return;
      const v = untrack(view);
      const e = entry(kind, k, v);
      e.mounted++;
      touchList(k, e);
      const c = new AbortController();
      const cur = e.state.peek();
      const fresh = feedState.peek() === 'live' && !!cur && !cur.error && !e.dirty && !e.affected.size && e.fetchedAt > 0 && (Date.now() - e.fetchedAt < FRESH_MS);
      const warm = !!cur?.at;
      const quiet = warm && (previousKey === undefined || previousKey === k);
      previousKey = k;
      if (!fresh) void loadList(kind, k, v, { signal: c.signal, quiet, preserveExtent: warm });
      return () => {
        e.mounted--;
        c.abort();
        if (!e.mounted) {
          clearTimeout(e.throttleTimer); e.throttleTimer = undefined;
          if (e.affected.size || e.reconciling) { e.affected.clear(); e.dirty = true; }
          evictLists();
        }
      };
    },
  );
  const shownEntry = createMemo(() => {
    const s = shown(); void generation.value;
    return s ? lists.get(s) : undefined;
  });
  const cur = createMemo(() => shownEntry()?.state.value ?? null);
  return {
    get rows() { return (cur()?.rows ?? []) as RowOf<K>[]; },
    get counts() { return cur()?.counts ?? {}; },
    get total() { return cur()?.total ?? 0; },
    get groups() { return cur()?.groups ?? []; },
    get facets() { return cur()?.facets; },
    get at() { return cur()?.at ?? 0; },
    get error() { return cur()?.error ?? null; },
    get pending() { return !prefsRead.value || shown() !== key() || currentEntry().busy.value; },
    get shownKey() { return shown(); },
    get shownView() { return shownEntry()?.view ?? view(); },
    more: () => prefsRead.peek() && untrack(shown) ? nextPage(untrack(shown)!) : Promise.resolve(),
    reload: () => prefsRead.peek() ? loadList(kind, untrack(key), untrack(view), { preserveExtent: true }) : Promise.resolve(),
    get state() { return freshness(!!cur()?.restored); },
  };
}

/**
 * Readers of one doc name share its reads. Every request in one task becomes one request; reads are never held back by
 * a running one, and an answer is shown only when it started after the one on screen.
 */
type DocReader = { fetch: () => Promise<unknown> };
type DocFlight = { readers: Set<DocReader>; started: number; shown: number; failed: number; scheduled: boolean; cleared: number };
const flights = new Map<string, DocFlight>();
function docEntry(name: string): DocEntry {
  let e = docs.get(name);
  if (!e) {
    const made: DocEntry = e = { signal: cell<Doc | null>(null), fetchedAt: 0, dirty: false, dirtySeq: 0 };
    docs.set(name, made); evictDocs();
    restoreDoc(name, made);
  }
  return e;
}
/** The mirror's copy of a read shows until the first read of this page answers; a read that answered first wins. */
function restoreDoc(name: string, made: DocEntry) {
  const cleared = generation.peek();
  void restore(name).then(v => {
    if (v && cleared === generation.peek() && docs.get(name) === made && !made.signal.peek()) made.signal.value = { data: v.data, error: null, restored: true };
  });
}
function docSignal(name: string) { return docEntry(name).signal; }
async function startDoc(name: string, f: DocFlight) {
  const reader = [...f.readers].pop(); if (!reader) return;
  const read = ++f.started, generation = accessGeneration(), entry = docEntry(name), s = entry.signal, dirtyAtStart = entry.dirtySeq;
  const newest = () => flights.get(name) === f && read > f.shown && isAccessCurrent(generation);
  try {
    await ensureAccessGeneration(); assertAccessGeneration(generation);
    const d = await reader.fetch();
    if (newest()) {
      f.shown = read;
      // Patch, not replace: parts equal to what is on screen keep their objects (share.ts), so a live reread never
      // remounts the rows, cards and inputs it did not change.
      s.value = { data: share(s.value?.data ?? d, d), error: f.failed > read ? s.value?.error ?? null : null };
      persist(name, d);
      if (f.failed <= read) { entry.fetchedAt = Date.now(); if (entry.dirtySeq === dirtyAtStart) entry.dirty = false; }
      touchDoc(name, entry);
    }
  } catch (e) {
    if (!newest() || read < f.failed) return;
    f.failed = read;
    entry.fetchedAt = 0;
    s.value = { data: s.value?.data ?? null, error: errMsg(e), status: e instanceof OpError ? e.status : null, restored: s.value?.restored };
  }
}
/** Ask for a fresh read of a mounted doc; every ask in the same task sends one request. */
export function readDoc(name: string) {
  const f = flights.get(name); if (!f || f.scheduled) return;
  f.scheduled = true;
  queueMicrotask(() => { f.scheduled = false; void startDoc(name, f); });
}
/** Mount a reader and fetch unless fresh cached data is already held while live. Returns the release. */
export function holdDoc(name: string, fetch: () => Promise<unknown>, opts?: { name?: OpName; request?: unknown; maxAge?: number }) {
  let f = flights.get(name); if (!f) flights.set(name, f = { readers: new Set(), started: 0, shown: 0, failed: 0, scheduled: false, cleared: 0 });
  const flight = f, reader: DocReader = { fetch }; flight.readers.add(reader);
  const entry = docEntry(name);
  if (opts?.name) { entry.name = opts.name; entry.request = opts.request; }
  touchDoc(name, entry);
  const v = entry.signal.value, maxAge = opts?.maxAge ?? FRESH_MS;
  const fresh = feedState.value === 'live' && !!v && v.data !== null && !v.error && !entry.dirty && entry.fetchedAt > 0 && (Date.now() - entry.fetchedAt < maxAge);
  // Another reader's read already on its way (started after any change this entry saw) answers this one too.
  const onItsWay = flight.scheduled || (flight.started > Math.max(flight.shown, flight.failed) && !entry.dirty);
  if (!fresh && !onItsWay) readDoc(name);
  return () => { flight.readers.delete(reader); queueMicrotask(() => { if (!flight.readers.size && flights.get(name) === flight) { flights.delete(name); evictDocs(); } }); };
}
function discardDocReads() { for (const f of flights.values()) { f.shown = f.failed = f.started; f.cleared++; } }

/** JSON with object keys sorted: equal requests name one read whatever their key order. */
export const canonical = (v: unknown) => JSON.stringify(v, (_k, x) => x && typeof x === 'object' && !Array.isArray(x) ? Object.fromEntries(Object.entries(x).sort(([a], [b]) => a < b ? -1 : a > b ? 1 : 0)) : x);
/** The cache key of a read: the operation and its canonical request, shared by every view that asks the same. */
export const opKey = (name: OpName, request: unknown) => name + ':' + canonical(request);

const SCOPE: { field: string; of: (c: Change) => string | null | undefined }[] = [
  { field: 'taskId', of: c => c.taskId ?? (c.kind === 'task' ? c.id : null) },
  { field: 'sessionId', of: c => c.sessionId ?? (c.kind === 'session' ? c.id : null) },
  { field: 'hostId', of: c => c.hostId ?? (c.kind === 'host' ? c.id : null) },
  { field: 'artifactId', of: c => c.kind === 'artifact' ? c.id : undefined },
  { field: 'executionId', of: c => c.kind === 'execution' ? c.id : undefined },
  { field: 'experimentId', of: c => c.kind === 'experiment' ? c.id : undefined },
  { field: 'questionId', of: c => c.kind === 'question' ? c.id : undefined },
  { field: 'projectId', of: c => c.kind === 'resource_summary' ? c.id : c.kind === 'definition' && c.id.startsWith('project/') && c.id.length > 8 ? c.id.slice(8) : undefined },
];
const CARD_FIELD: Record<string, string> = { task: 'taskId', conversation: 'sessionId', host: 'hostId', result: 'artifactId', execution: 'executionId', experiment: 'experimentId', question: 'questionId' };
const CARD_OWN: Record<string, (c: Change, id: string) => boolean> = {
  project: (c, id) => c.kind === 'definition' && c.id === 'project/' + id,
  agent: (c, id) => c.kind === 'definition' && c.id === 'agent/' + id,
  pipeline: (c, id) => c.kind === 'definition' && c.id === 'pipeline/' + id,
  pull_request: c => c.kind === 'delivery',
  release: c => c.kind === 'delivery' || c.kind === 'host',
};
let identityKey: () => string | undefined = () => undefined;
/** main.tsx: whose personal settings a setting-change must name to touch this tab's reads. */
export function setIdentity(read: () => string | undefined) { identityKey = read; }

/**
 * Whether a change can alter the reply of name(request): the server names the operation in the change's touches
 * (contracts: each operation's changes), and the change names no other entity than the request does. A setting-change
 * of another known person touches nothing here, and one of a known non-person target no personal read. This is the only invalidation rule; views write none.
 */
export function touchedBy(c: Change, name: OpName, request: unknown): boolean {
  if (!c.touches?.includes(name)) return false;
  const r = { ...(request && typeof request === 'object' ? request as Record<string, unknown> : {}) };
  if (name === 'entity_card' && typeof r.kind === 'string') {
    if (typeof r.id !== 'string') return false;
    if (CARD_OWN[r.kind]) return CARD_OWN[r.kind](c, r.id);
    if (CARD_FIELD[r.kind]) r[CARD_FIELD[r.kind]] = r.id;
  }
  if (c.kind === 'board' && typeof r.scopeKind === 'string' && typeof r.scopeId === 'string') {
    return c.id === `${r.scopeKind}:${r.scopeId}`;
  }
  for (const { field, of } of SCOPE) { const want = r[field], got = of(c); if (typeof want === 'string' && got !== undefined && got !== want) return false; }
  if (Array.isArray(r.ids) && ![c.taskId, c.sessionId, c.id].some(x => !!x && (r.ids as unknown[]).includes(x))) return false;
  const me = identityKey(), t = c.settingTarget;
  if (c.kind === 'setting-change' && me && t && t.startsWith('person:') && t !== 'person:' && t !== 'person:' + me) return false;
  if (c.kind === 'setting-change' && name.startsWith('person_') && t && t !== 'person:' && !t.startsWith('person:')) return false;
  return true;
}

type Mounted = { name: OpName; request: () => unknown; read: () => void; key?: string };
const mounted = new Set<Mounted>();
let opFeed: (() => void) | null = null;
// A hidden tab reads nothing: a read the change stream asks for while the tab is hidden waits, and the tab reads each
// such read once when it shows again (only what changed meanwhile). The stream itself keeps flowing, so nothing is missed.
const hiddenTab = () => typeof document !== 'undefined' && document.visibilityState === 'hidden';
const deferred = new Set<Mounted>();
let shownHook = false;
function readOrDefer(x: Mounted) { if (hiddenTab()) deferred.add(x); else x.read(); }
function watch(m: Mounted) {
  mounted.add(m);
  if (!shownHook && typeof document !== 'undefined') {
    shownHook = true;
    document.addEventListener('visibilitychange', () => {
      if (hiddenTab()) return;
      const due = [...deferred]; deferred.clear();
      for (const x of due) if (mounted.has(x)) x.read();
    });
  }
  opFeed ??= subscribe({
    resync: () => {
      // A read still on its way started after the new cursor (reads wait for the gate), so it is current already;
      // every other mounted read, a failed one included, reads again.
      for (const x of mounted) { const f = x.key ? flights.get(x.key) : undefined; if (!f || f.started <= Math.max(f.shown, f.failed)) readOrDefer(x); }
      for (const [k, d] of docs) if (!flights.get(k)?.readers.size) { d.dirty = true; d.dirtySeq++; }
    },
    changes: cs => {
      for (const x of mounted) { const r = x.request(); if (cs.some(c => touchedBy(c, x.name, r))) readOrDefer(x); }
      for (const [k, d] of docs) if (!flights.get(k)?.readers.size && d.name && d.request !== undefined && cs.some(c => touchedBy(c, d.name!, d.request))) { d.dirty = true; d.dirtySeq++; }
    },
  });
  return () => { mounted.delete(m); deferred.delete(m); };
}
// One throttle per read and quiet window, shared by every mount of that read: two screens' parts that ask the same view
// (Projects' catalogue and summary) reread it once per window, not once per mount in different phases.
const sharedThrottles = new Map<string, { t: ReturnType<typeof throttle>; users: number }>();
export function sharedThrottle(key: string, read: () => void, quiet = 0) {
  if (!quiet) return { read, stop: () => {} };
  const k = key + '|' + quiet;
  let e = sharedThrottles.get(k);
  if (!e) sharedThrottles.set(k, e = { t: throttle(read, quiet), users: 0 });
  e.users++;
  const entry = e;
  return { read: () => entry.t.read(), stop: () => { if (--entry.users === 0) { entry.t.stop(); sharedThrottles.delete(k); } } };
}
function throttle(read: () => void, quiet = 0) {
  if (!quiet) return { read, stop: () => {} };
  let last = 0, timer: ReturnType<typeof setTimeout> | undefined;
  return {
    read: () => { if (timer) return; const wait = last + quiet - Date.now(); if (wait <= 0) { last = Date.now(); read(); return; } timer = setTimeout(() => { timer = undefined; last = Date.now(); read(); }, wait); },
    stop: () => clearTimeout(timer),
  };
}

export type OpOptions = {
  /** Also read every ms: values that move without a change row (host liveness, telemetry, runners). */
  every?: MaybeAccessor<number | undefined>;
  /** Reread on touches at most once per quiet ms (counters touched by every busy execution). */
  quiet?: MaybeAccessor<number | undefined>;
  /** Fields added to the request for touchedBy only, not the key: the entity a read concerns once its reply named it. */
  scope?: MaybeAccessor<Record<string, unknown> | undefined>;
};
export type OpRead<T> = { data: T | null; restored: boolean; error: string | null; status: number | null; state: Freshness; reload: () => void };

/**
 * The one way a view reads an operation: useOp(name, request). The cache key is the operation and its canonical request,
 * so two views asking the same share one read; null request: nothing to read yet. It rereads when the server says a
 * change touches the operation (touchedBy), on resync, and every `every` ms; the last value stays visible while it rereads.
 */
export function useOp<K extends OpName>(name: K, request: MaybeAccessor<Ops[K]['request'] | null>, opts: OpOptions = {}): OpRead<Ops[K]['reply']> {
  const resolved = createMemo(() => readValue(request));
  const key = createMemo(() => {
    const req = resolved();
    return req === null ? null : opKey(name, req);
  });
  const s = createMemo(() => {
    const k = key();
    return k ? docSignal(k) : null;
  });
  let currentReq: Ops[K]['request'] | null = null;
  let reloadFn = () => {};
  createEffect(
    () => [key(), readValue(opts.every), readValue(opts.quiet), generation.value] as const,
    ([k, every, quiet]) => {
      currentReq = untrack(resolved);
      if (!k || currentReq === null) {
        reloadFn = () => {};
        return;
      }
      const maxAge = every ? Math.min(FRESH_MS, every) : FRESH_MS;
      const release = holdDoc(k, () => op(name, currentReq!), { name, request: currentReq, maxAge });
      reloadFn = () => readDoc(k);
      const t = sharedThrottle(k, () => readDoc(k), quiet);
      const off = watch({ name, request: () => ({ ...(currentReq as Record<string, unknown>), ...readValue(opts.scope) }), read: t.read, key: k });
      // A hidden tab polls nothing; the next tick after it shows again reads (no burst on return).
      const timer = every ? setInterval(() => { if (typeof document === 'undefined' || document.visibilityState === 'visible') readDoc(k); }, every) : 0;
      return () => { reloadFn = () => {}; release(); off(); t.stop(); clearInterval(timer); };
    },
  );
  return {
    get data() { return (s()?.value?.data ?? null) as Ops[K]['reply'] | null; },
    get restored() { return !!s()?.value?.restored; },
    get error() { return s()?.value?.error ?? null; },
    get status() { return s()?.value?.status ?? null; },
    get state() { return freshness(!!s()?.value?.restored); },
    reload: () => reloadFn(),
  };
}

/**
 * The read of a screen (docs/designs/fleet-data-layer.md): a view operation (contract view: true) composed on the server
 * from summaries. It shows at once from the read mirror, rereads only when the change stream touches it (quiet: at most
 * once per quiet ms), and never polls: a view has no `every`. A new screen adds a view operation to the contract and reads
 * it here; useOp stays for the reads of screens not moved yet and for one-off detail reads.
 */
export function useView<K extends ViewName & OpName>(name: K, request: MaybeAccessor<Ops[K]['request'] | null>, opts: { quiet?: MaybeAccessor<number | undefined> } = {}): OpRead<Ops[K]['reply']> {
  return useOp(name, request, { quiet: opts.quiet });
}

/** For tests: the stored list under key and a doc. */
export const peek = { generation: () => generation.value, doc: (name: string) => docs.get(name)?.signal.value ?? null, list: (key: string) => lists.get(key)?.state.value ?? null, mount: (kind: Kind, key: string, view: View) => { const e = entry(kind, key, view); e.mounted++; return () => { e.mounted--; }; } };

// Prepared readers share scheduling by normalized request AND access generation.
import type { ProjectResourcesReply, ProjectResourceItemsReply, ResourceProgress, ProjectResourceItemsRequest } from '../../contracts/generated/ops.ts';
type PreparedReply = ProjectResourcesReply | ProjectResourceItemsReply;
export type PreparedState<T> = { data: T | null; progress: ResourceProgress | null; error: string | null; restored: boolean; state: 'idle' | 'loading' | 'ready' | 'preparing' | 'error' | 'disposed' };
type PreparedFetcher<T> = (context: { signal: AbortSignal; knownVersion?: string }) => Promise<T>;
type PreparedEntry<T> = { value: Cell<PreparedState<T>>; fetcher: PreparedFetcher<T>; subscribers: number; visible: number; controller: AbortController | null; request: number; pending: boolean; version?: string; timer?: ReturnType<typeof setInterval>; pollMs?: number; disposed: boolean; refresh(reason?: string): void };
const prepared = new Map<string, PreparedEntry<PreparedReply>>();
export function preparedKey(operation: 'project_resources' | 'project_resource_items', projectId: string, request: Omit<ProjectResourceItemsRequest, 'projectId' | 'knownVersion'> | null = null) {
  const r = request;
  const key = r?.around?.key;
  const position = !key ? null : key.kind === 'tool-position' ? [key.kind, key.encoded] : key.kind === 'tool' ? [key.kind, key.name, key.connection] : key.kind === 'cost' ? [key.kind, key.currency, key.basis] : [key.kind, key.priceId, key.observedAt];
  return JSON.stringify([operation, projectId, r ? [r.category, r.group ? [r.group.currency, r.group.basis] : null, r.query ?? '', r.limit ?? 50, r.cursor ?? null, r.anchor ?? null, r.around ? [position, r.around.direction] : null] : null, accessGeneration()]);
}
function versionParts(version: string) { const split = version.lastIndexOf(':'); return { epoch: version.slice(0, split), revision: BigInt(version.slice(split + 1)) }; }
export function olderPreparedVersion(candidate: string, current?: string) {
  if (!current) return false;
  const a = versionParts(candidate), b = versionParts(current); return a.epoch === b.epoch && a.revision < b.revision;
}
export function acquirePrepared<T extends PreparedReply>(key: string, fetcher: PreparedFetcher<T>, visible = true, interval = PREPARED_POLL_MS) {
  let entry = prepared.get(key) as unknown as PreparedEntry<T> | undefined;
  const syncTimer = (e: PreparedEntry<T>) => {
    const want = !e.disposed && e.visible > 0 ? (e.value.value.state === 'preparing' ? Math.min(interval, PREPARED_PREPARING_MS) : interval) : 0;
    if (want === (e.pollMs ?? 0) && (!want || e.timer)) return;
    clearInterval(e.timer); e.timer = undefined; e.pollMs = want;
    if (want > 0) {
      e.timer = setInterval(() => {
        if (e.visible && !e.disposed && (typeof document === 'undefined' || document.visibilityState === 'visible')) e.refresh('interval');
      }, want);
    }
  };
  if (!entry) {
    const generation = accessGeneration();
    const e: PreparedEntry<T> = { value: cell<PreparedState<T>>({ data: null, progress: null, error: null, restored: false, state: 'idle' }), fetcher, subscribers: 0, visible: 0, controller: null, request: 0, pending: false, disposed: false,
      refresh: () => {
        if (e.disposed || !e.subscribers) return;
        if (e.controller) { e.pending = true; return; }
        const c = e.controller = new AbortController(), request = ++e.request;
        e.value.value = { ...e.value.peek(), state: e.value.peek().data ? e.value.peek().state : 'loading' };
        void (async () => {
          try {
            await ensureAccessGeneration(); assertAccessGeneration(generation);
            const result = await e.fetcher({ signal: c.signal, knownVersion: e.version });
            assertAccessGeneration(generation);
            if (e.disposed || c.signal.aborted || request !== e.request) return;
            if (result.version && olderPreparedVersion(result.version, e.version)) return;
            const previous = e.value.peek();
            if ('kind' in result && result.kind === 'unchanged') {
              e.value.value = { ...previous, progress: result.progress, error: null, state: previous.data ? 'ready' : 'preparing' };
            } else if ('kind' in result && result.kind === 'preparing') {
              e.version = undefined;
              e.value.value = { data: null, progress: result.progress, error: null, restored: false, state: 'preparing' };
            } else {
              e.version = result.version ?? undefined;
              e.value.value = { data: result, progress: result.progress, error: null, restored: false, state: 'ready' };
            }
            syncTimer(e);
          } catch (error) {
            if (!e.disposed && !c.signal.aborted && isAccessCurrent(generation)) {
              e.value.value = { ...e.value.peek(), error: errMsg(error), state: 'error' };
              syncTimer(e);
            }
          } finally {
            if (e.controller === c) e.controller = null;
            if (e.pending && !e.disposed) { e.pending = false; e.refresh('pending'); }
          }
        })();
      } };
    prepared.set(key, (entry = e) as unknown as PreparedEntry<PreparedReply>);
  }
  const e = entry; e.fetcher = fetcher; e.subscribers++; if (visible) e.visible++;
  syncTimer(e);
  if (e.subscribers === 1) e.refresh('mount');
  return { key, value: e.value, refresh: e.refresh, release: () => {
    e.subscribers--; if (visible) e.visible--;
    syncTimer(e);
    if (!e.subscribers) { e.disposed = true; e.pending = false; e.request++; e.controller?.abort(); clearInterval(e.timer); e.timer = undefined; e.pollMs = 0; prepared.delete(key); e.value.value = { data: null, progress: null, error: null, restored: false, state: 'disposed' }; }
  } };
}
onClear.add(() => { for (const e of prepared.values()) { e.disposed = true; e.request++; e.controller?.abort(); clearInterval(e.timer); e.value.value = { data: null, progress: null, error: null, restored: false, state: 'disposed' }; } prepared.clear(); });
const IDLE_PREPARED: PreparedState<never> = { data: null, progress: null, error: null, restored: false, state: 'idle' };
export function usePreparedOp<K extends 'project_resources' | 'project_resource_items'>(name: K, request: MaybeAccessor<Ops[K]['request'] | null>,
  opts: { visible?: MaybeAccessor<boolean>; read?: PreparedFetcher<Ops[K]['reply']> } = {}) {
  const resolved = createMemo(() => readValue(request));
  const key = createMemo(() => {
    const r = resolved();
    return r === null ? null : preparedKey(name, r.projectId, name === 'project_resource_items' ? r as ProjectResourceItemsRequest : null);
  });
  const visible = createMemo(() => readValue(opts.visible) !== false);
  const [reader, setReader] = createSignal<ReturnType<typeof acquirePrepared<Ops[K]['reply']>> | null>(null, { ownedWrite: true });
  let currentReq: Ops[K]['request'] | null = null;
  createEffect(
    () => [key(), visible()] as const,
    ([k, vis]) => {
      currentReq = untrack(resolved);
      if (!k || currentReq === null) { setReader(null); return; }
      const fetcher: PreparedFetcher<Ops[K]['reply']> = c => opts.read ? opts.read(c) : op(name, { ...currentReq!, knownVersion: c.knownVersion }, { signal: c.signal });
      const r = acquirePrepared(k, fetcher, vis); setReader(r);
      const off = watch({ name, request: () => currentReq, read: () => r.refresh('notification') });
      const focus = () => r.refresh('foreground');
      addEventListener('visibilitychange', focus); addEventListener('online', focus);
      return () => { off(); r.release(); removeEventListener('visibilitychange', focus); removeEventListener('online', focus); };
    },
  );
  const current = createMemo(() => {
    const r = reader(), k = key();
    return (r && r.key === k ? r.value.value : null) ?? (IDLE_PREPARED as PreparedState<Ops[K]['reply']>);
  });
  return {
    get data() { return current().data; },
    get progress() { return current().progress; },
    get error() { return current().error; },
    get restored() { return current().restored; },
    get state() { return current().state; },
    refresh: (reason?: string) => reader()?.refresh(reason),
  };
}
