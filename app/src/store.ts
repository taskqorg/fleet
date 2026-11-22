// Entity store (operator-app D4; docs/designs/fleet-view-urls-and-list-path.md §2): shared in-memory lists and docs,
// kept current from the change feed via `touches` and list reloads. Nothing is ever blanked while reloading: a view
// keeps showing its last rows while the next answer loads, and swaps in place.
import { AccessChanged, accessGeneration, ensureAccessGeneration, isAccessCurrent, assertAccessGeneration } from './access.ts';
import { signal, effect, type Signal } from '@preact/signals';
import { useEffect, useRef, useState } from 'preact/hooks';
import { op, online, signedOut, OpError, type Ops, type OpName } from './api.ts';
import { subscribe, feedState, type Change } from './feed.ts';
import { type Row } from './lists.ts';
import type { ListGroup, TaskRow, SessionRow } from '../../contracts/generated/ops.ts';
import { onClear } from './offline.ts';
import { listKey, toPageParams, type View } from '../../packages/view-url/view-url.ts';
import { withFocus, prefsRead } from './prefs.ts';

export type Freshness = 'current' | 'updating' | 'offline' | 'signed-out';
const errMsg = (e: unknown) => e instanceof Error ? e.message : typeof e === 'object' && e !== null && 'message' in e && typeof (e as { message: unknown }).message === 'string' ? (e as { message: string }).message : String(e);
export const freshness = (restored: boolean): Freshness => signedOut.value ? 'signed-out' : !online.value ? 'offline' : restored ? 'updating' : 'current';

export type Kind = 'tasks' | 'sessions';
type Params = ReturnType<typeof toPageParams>;
type Group = ListGroup;
export type ListState = { rows: Row[]; counts: Record<string, number>; total: number; groups: Group[]; nextAfter: unknown; at: number; error: string | null };
type Entry = { kind: Kind; view: View; params: Params; state: Signal<ListState | null>; busy: Signal<boolean>; started: number; shown: number; mounted: number; loading: number; dirty: boolean; paging: boolean; cursorReset: boolean; pageError: boolean; fetchedAt: number; lastReloadAt: number; throttleTimer?: ReturnType<typeof setTimeout> };
type Doc = { data: unknown; error: string | null; status?: number | null };
type DocEntry = { signal: Signal<Doc | null>; fetchedAt: number; dirty: boolean; dirtySeq: number; name?: OpName; request?: unknown };

const FRESH_MS = 30_000;
const MAX_UNMOUNTED_DOCS = 100;
const MAX_UNMOUNTED_LISTS = 30;
const APPLY_THROTTLE_MS = 1000;
const PREPARED_PREPARING_MS = 5_000;
const PREPARED_POLL_MS = 10_000;

const lists = new Map<string, Entry>();
const docs = new Map<string, DocEntry>();
/** Bumped when stored data is cleared: mounted docs re-read. */
const generation = signal(0);

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
    e.state.value = null; e.shown = e.started; e.dirty = false; e.fetchedAt = 0; e.lastReloadAt = 0;
    if (!e.mounted) lists.delete(key);
    else if (isAccessCurrent(accessGeneration())) void loadList(e.kind, key, e.view);
  }
  discardDocReads();
  for (const [key, d] of docs) {
    d.signal.value = null; d.fetchedAt = 0; d.dirty = false; d.dirtySeq++;
    if (!flights.get(key)?.readers.size) docs.delete(key);
  }
  generation.value++;
}
onClear.add(clearMemory);

function entry(kind: Kind, key: string, view: View): Entry {
  let e = lists.get(key);
  if (!e) {
    lists.set(key, e = { kind, view, params: toPageParams(view), state: signal(null), busy: signal(false), started: 0, shown: 0, mounted: 0, loading: 0, dirty: false, paging: false, cursorReset: false, pageError: false, fetchedAt: 0, lastReloadAt: 0 });
    evictLists();
  }
  return e;
}
const rowsOf = (r: Ops['task_page']['reply'] | Ops['sessions_list']['reply']): Row[] => 'tasks' in r ? r.tasks : r.sessions;
const opName = (k: Kind) => k === 'tasks' ? 'task_page' : 'sessions_list';

/**
 * Load the first page of a list. An older slow response never overwrites a newer one (`read < e.shown`); a change
 * arriving while a load is running marks the entry dirty so one follow-up reload runs once it lands.
 */
export async function loadList(kind: Kind, key: string, view: View, abort?: AbortSignal, cursorReset = false) {
  const e = entry(kind, key, view), read = ++e.started, generation = accessGeneration();
  const wasDirty = e.dirty;
  clearTimeout(e.throttleTimer); e.throttleTimer = undefined;
  e.dirty = false; e.cursorReset = cursorReset; e.loading++; e.busy.value = true;
  try {
    await ensureAccessGeneration(); assertAccessGeneration(generation);
    const r = await op(opName(kind), { ...e.params, limit: 100 }, { signal: abort });
    assertAccessGeneration(generation);
    if (read < e.shown) return;
    e.shown = read;
    const now = Date.now();
    e.state.value = { rows: rowsOf(r), counts: r.counts, total: r.total, groups: 'groups' in r ? r.groups : [], nextAfter: r.nextAfter, at: now, error: null };
    e.fetchedAt = now;
    e.pageError = false;
    touchList(key, e);
  } catch (err) {
    if (err instanceof AccessChanged || !isAccessCurrent(generation) || err instanceof OpError && err.message === 'aborted') {
      if (wasDirty && read >= e.shown) e.dirty = true;
      return;
    }
    if (read < e.shown) return;
    const s = e.state.value, msg = errMsg(err); e.pageError = false; e.fetchedAt = 0;
    e.state.value = s ? { ...s, error: msg } : { rows: [], counts: {}, total: 0, groups: [], nextAfter: null, at: 0, error: msg };
  } finally {
    e.loading--; e.busy.value = e.loading > 0;
    if (isAccessCurrent(generation) && !e.loading && e.dirty && e.mounted) { e.dirty = false; await loadList(e.kind, key, e.view); }
  }
}

/**
 * The next page of the list stored under key, with that list's own params; not while its first page is loading.
 * A refused cursor (400) reloads from the first page once per list load, keeping rows on screen.
 */
export async function nextPage(key: string) {
  const e = lists.get(key), st = e?.state.value;
  if (!e || !st?.nextAfter || e.paging || e.loading) return;
  e.paging = true; const generation = accessGeneration();
  try {
    const r = await op(opName(e.kind), { ...e.params, limit: 100, after: st.nextAfter as unknown[] });
    assertAccessGeneration(generation);
    const now = e.state.value;
    if (!now || now.nextAfter !== st.nextAfter) return;
    const have = new Set(now.rows.map(x => x.id)), add = rowsOf(r).filter(x => !have.has(x.id));
    e.state.value = { ...now, rows: [...now.rows, ...add], nextAfter: r.nextAfter, error: e.pageError ? null : now.error };
    e.pageError = false;
  } catch (err) {
    if (!isAccessCurrent(generation) || !e.state.value || e.state.value.nextAfter !== st.nextAfter || e.loading) return;
    if (err instanceof OpError && err.status === 400 && !e.cursorReset) {
      await loadList(e.kind, key, e.view, undefined, true);
      const fresh = e.state.value;
      if (fresh && !fresh.error && fresh.nextAfter && fresh.nextAfter !== st.nextAfter) { e.paging = false; await nextPage(key); }
      return;
    }
    e.state.value = { ...e.state.value, error: errMsg(err) }; e.pageError = true;
  } finally { e.paging = false; }
}

type ScopedChange = Change & { projectId?: string | null };
const changeProjectId = (c: ScopedChange): string | null | undefined => {
  if ('projectId' in c && c.projectId !== undefined) return c.projectId;
  if (c.kind === 'resource_summary') return c.id;
  if (c.kind === 'definition' && c.id.startsWith('project/') && c.id.length > 8) return c.id.slice(8);
  return undefined;
};
const listProjectMatches = (c: Change, params: Params): boolean => {
  const pid = changeProjectId(c as ScopedChange);
  if (pid === undefined) return true;
  const inc = params.filter.projectId;
  if (inc?.length && !inc.includes(pid)) return false;
  const exc = params.filter.excludeProjectId;
  if (exc?.length && exc.includes(pid)) return false;
  return true;
};
const listAffected = (c: Change, kind: Kind, params: Params) =>
  (kind === 'tasks' ? !!(c.taskId || c.kind === 'task') : !!(c.sessionId || c.kind === 'session')) && listProjectMatches(c, params);

/** One batch of feed changes: every mounted list with affected rows reloads (coalesced/throttled to 1000 ms). */
export async function applyChanges(cs: Change[]) {
  if (!isAccessCurrent(accessGeneration())) return;
  const reloads: Promise<void>[] = [];
  const now = Date.now();
  for (const [key, e] of lists) {
    if (!cs.some(c => listAffected(c, e.kind, e.params))) continue;
    if (!e.mounted) { e.dirty = true; continue; }
    if (e.loading) { e.dirty = true; continue; }
    const wait = e.lastReloadAt + APPLY_THROTTLE_MS - now;
    if (wait <= 0) {
      clearTimeout(e.throttleTimer); e.throttleTimer = undefined;
      e.lastReloadAt = now;
      reloads.push(loadList(e.kind, key, e.view));
    } else {
      e.dirty = true;
      if (!e.throttleTimer) {
        e.throttleTimer = setTimeout(() => {
          e.throttleTimer = undefined;
          if (!e.mounted || !isAccessCurrent(accessGeneration())) return;
          if (e.loading) { e.dirty = true; return; }
          e.lastReloadAt = Date.now();
          void loadList(e.kind, key, e.view);
        }, wait);
      }
    }
  }
  await Promise.all(reloads);
}

let started = false;
/** One feed subscription for all lists (started once from main.tsx). */
export function startStore() {
  if (started) return; started = true;
  const reloadMounted = (only?: (s: ListState | null) => boolean) => {
    for (const [key, e] of lists) {
      if (e.mounted) { if (!only || only(e.state.value)) void loadList(e.kind, key, e.view); }
      else if (!only) e.dirty = true;
    }
    if (!only) for (const [k, d] of docs) if (!flights.get(k)?.readers.size) { d.dirty = true; d.dirtySeq++; }
  };
  subscribe({ resync: () => reloadMounted(), changes: applyChanges });
  effect(() => { if (feedState.value === 'live') reloadMounted(s => !s || !!s.error); });
}

/** The row type of each list kind. */
export type RowOf<K extends Kind> = K extends 'tasks' ? TaskRow : SessionRow;
export function useList<K extends Kind>(kind: K, urlView: View) {
  const view = withFocus(urlView), key = listKey(view), e = entry(kind, key, view);
  const [last, setLast] = useState<string | null>(() => e.state.value ? key : null);
  const ctrl = useRef<AbortController | null>(null);
  const shown = e.state.value ? key : last;
  useEffect(() => { if (shown !== last) setLast(shown); }, [shown]);
  const ready = prefsRead.value;
  useEffect(() => {
    if (!ready) return;
    e.mounted++;
    touchList(key, e);
    ctrl.current?.abort(); const c = ctrl.current = new AbortController();
    const cur = e.state.value;
    const fresh = feedState.value === 'live' && !!cur && !cur.error && !e.dirty && e.fetchedAt > 0 && (Date.now() - e.fetchedAt < FRESH_MS);
    if (!fresh) void loadList(kind, key, view, c.signal);
    return () => {
      e.mounted--;
      c.abort();
      if (!e.mounted) {
        if (e.throttleTimer) { clearTimeout(e.throttleTimer); e.throttleTimer = undefined; e.dirty = true; }
        evictLists();
      }
    };
  }, [key, ready]);
  const se = shown ? lists.get(shown) : undefined, cur = se?.state.value ?? null;
  return {
    rows: (cur?.rows ?? []) as RowOf<K>[], counts: cur?.counts ?? {}, total: cur?.total ?? 0, groups: cur?.groups ?? [], at: cur?.at ?? 0, error: cur?.error ?? null,
    pending: !ready || shown !== key || e.busy.value, shownKey: shown, shownView: se?.view ?? view,
    more: () => ready && shown ? nextPage(shown) : Promise.resolve(), reload: () => ready ? loadList(kind, key, view) : Promise.resolve(),
    state: freshness(false),
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
  if (!e) { docs.set(name, e = { signal: signal(null), fetchedAt: 0, dirty: false, dirtySeq: 0 }); evictDocs(); }
  return e;
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
      s.value = { data: d, error: f.failed > read ? s.value?.error ?? null : null };
      if (f.failed <= read) { entry.fetchedAt = Date.now(); if (entry.dirtySeq === dirtyAtStart) entry.dirty = false; }
      touchDoc(name, entry);
    }
  } catch (e) {
    if (!newest() || read < f.failed) return;
    f.failed = read;
    entry.fetchedAt = 0;
    s.value = { data: s.value?.data ?? null, error: errMsg(e), status: e instanceof OpError ? e.status : null };
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
  if (!fresh) readDoc(name);
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

type Mounted = { name: OpName; request: () => unknown; read: () => void };
const mounted = new Set<Mounted>();
let opFeed: (() => void) | null = null;
function watch(m: Mounted) {
  mounted.add(m);
  opFeed ??= subscribe({
    resync: () => {
      for (const x of mounted) x.read();
      for (const [k, d] of docs) if (!flights.get(k)?.readers.size) { d.dirty = true; d.dirtySeq++; }
    },
    changes: cs => {
      for (const x of mounted) { const r = x.request(); if (cs.some(c => touchedBy(c, x.name, r))) x.read(); }
      for (const [k, d] of docs) if (!flights.get(k)?.readers.size && d.name && d.request !== undefined && cs.some(c => touchedBy(c, d.name!, d.request))) { d.dirty = true; d.dirtySeq++; }
    },
  });
  return () => { mounted.delete(m); };
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
  every?: number;
  /** Reread on touches at most once per quiet ms (counters touched by every busy execution). */
  quiet?: number;
  /** Fields added to the request for touchedBy only, not the key: the entity a read concerns once its reply named it. */
  scope?: Record<string, unknown>;
};
export type OpRead<T> = { data: T | null; restored: boolean; error: string | null; status: number | null; state: Freshness; reload: () => void };

/**
 * The one way a view reads an operation: useOp(name, request). The cache key is the operation and its canonical request,
 * so two views asking the same share one read; null request: nothing to read yet. It rereads when the server says a
 * change touches the operation (touchedBy), on resync, and every `every` ms; the last value stays visible while it rereads.
 */
export function useOp<K extends OpName>(name: K, request: Ops[K]['request'] | null, opts: OpOptions = {}): OpRead<Ops[K]['reply']> {
  const key = request === null ? null : opKey(name, request);
  const reqRef = useRef(request); reqRef.current = request;
  const scopeRef = useRef(opts.scope); scopeRef.current = opts.scope;
  const reloadRef = useRef<() => void>(() => {});
  const s = key ? docSignal(key) : null;
  const gen = generation.value;
  useEffect(() => {
    if (!key) return;
    const maxAge = opts.every ? Math.min(FRESH_MS, opts.every) : FRESH_MS;
    const release = holdDoc(key, () => op(name, reqRef.current!), { name, request: reqRef.current, maxAge });
    reloadRef.current = () => readDoc(key);
    const t = throttle(() => readDoc(key), opts.quiet);
    const off = watch({ name, request: () => ({ ...reqRef.current, ...scopeRef.current }), read: t.read });
    const timer = opts.every ? setInterval(() => readDoc(key), opts.every) : 0;
    return () => { reloadRef.current = () => {}; release(); off(); t.stop(); clearInterval(timer); };
  }, [key, opts.every, opts.quiet, gen]);
  const v = s?.value ?? null;
  return { data: (v?.data ?? null) as Ops[K]['reply'] | null, restored: false, error: v?.error ?? null, status: v?.status ?? null, state: freshness(false), reload: () => reloadRef.current() };
}

/** For tests: the stored list under key and a doc. */
export const peek = { generation: () => generation.value, doc: (name: string) => docs.get(name)?.signal.value ?? null, list: (key: string) => lists.get(key)?.state.value ?? null, mount: (kind: Kind, key: string, view: View) => { entry(kind, key, view).mounted++; } };

// Prepared readers share scheduling by normalized request AND access generation.
import type { ProjectResourcesReply, ProjectResourceItemsReply, ResourceProgress, ProjectResourceItemsRequest } from '../../contracts/generated/ops.ts';
type PreparedReply = ProjectResourcesReply | ProjectResourceItemsReply;
export type PreparedState<T> = { data: T | null; progress: ResourceProgress | null; error: string | null; restored: boolean; state: 'idle' | 'loading' | 'ready' | 'preparing' | 'error' | 'disposed' };
type PreparedFetcher<T> = (context: { signal: AbortSignal; knownVersion?: string }) => Promise<T>;
type PreparedEntry<T> = { value: Signal<PreparedState<T>>; fetcher: PreparedFetcher<T>; subscribers: number; visible: number; controller: AbortController | null; request: number; pending: boolean; version?: string; timer?: ReturnType<typeof setInterval>; pollMs?: number; disposed: boolean; refresh(reason?: string): void };
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
    const e: PreparedEntry<T> = { value: signal({ data: null, progress: null, error: null, restored: false, state: 'idle' }), fetcher, subscribers: 0, visible: 0, controller: null, request: 0, pending: false, disposed: false,
      refresh: () => {
        if (e.disposed || !e.subscribers) return;
        if (e.controller) { e.pending = true; return; }
        const c = e.controller = new AbortController(), request = ++e.request;
        e.value.value = { ...e.value.value, state: e.value.value.data ? e.value.value.state : 'loading' };
        void (async () => {
          try {
            await ensureAccessGeneration(); assertAccessGeneration(generation);
            const result = await e.fetcher({ signal: c.signal, knownVersion: e.version });
            assertAccessGeneration(generation);
            if (e.disposed || c.signal.aborted || request !== e.request) return;
            if (result.version && olderPreparedVersion(result.version, e.version)) return;
            const previous = e.value.value;
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
              e.value.value = { ...e.value.value, error: errMsg(error), state: 'error' };
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
export function usePreparedOp<K extends 'project_resources' | 'project_resource_items'>(name: K, request: Ops[K]['request'] | null,
  opts: { visible?: boolean; read?: PreparedFetcher<Ops[K]['reply']> } = {}) {
  const key = request === null ? null : preparedKey(name, request.projectId, name === 'project_resource_items' ? request as ProjectResourceItemsRequest : null);
  const reqRef = useRef(request); reqRef.current = request;
  const readRef = useRef(opts.read); readRef.current = opts.read;
  const [reader, setReader] = useState<ReturnType<typeof acquirePrepared<Ops[K]['reply']>> | null>(null);
  useEffect(() => {
    if (!key) { setReader(null); return; }
    const fetcher: PreparedFetcher<Ops[K]['reply']> = c => readRef.current ? readRef.current(c) : op(name, { ...reqRef.current!, knownVersion: c.knownVersion }, { signal: c.signal });
    const r = acquirePrepared(key, fetcher, opts.visible !== false); setReader(r);
    const off = watch({ name, request: () => reqRef.current, read: () => r.refresh('notification') });
    const focus = () => r.refresh('foreground');
    addEventListener('visibilitychange', focus); addEventListener('online', focus);
    return () => { off(); r.release(); removeEventListener('visibilitychange', focus); removeEventListener('online', focus); };
  }, [key, opts.visible]);
  const data = (reader?.key === key ? reader.value.value : null) ?? { data: null, progress: null, error: null, restored: false, state: 'idle' as const };
  return { ...data, refresh: reader?.refresh ?? (() => {}) };
}
