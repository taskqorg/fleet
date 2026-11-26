// The app's source of entity cards and live reference states (packages/ui EntityCode; docs/design-system/controls/entity-card.md).
// Cards: one `entity_card` read per open card through useOp (memory and IndexedDB), read again when a change touches it.
// Live states: one registry of every rendered reference, read in batches with `entity_refs` and matched against the change
// feed by entity key, so 200 references cost one read on mount and a map lookup per change, never a poll per reference.
import { createEffect, createMemo } from 'solid-js';
import type { EntityCardRead, EntityCardSource, EntityLive, EntityRef } from '../../packages/ui/src/index.ts';
import type { EntityCardReply, EntityCardRequest } from '../../contracts/generated/ops.ts';
import { cell, readValue, type Cell, type MaybeAccessor } from './signal.ts';
import { useOp } from './store.ts';
import { op } from './api.ts';
import { subscribe, type Change } from './feed.ts';

const NOT_FOUND = 'Not found, or not visible to you';
// Kinds whose state comes from runs, whose changes (totals) arrive many times a second: read every 5 s while open.
const TIMED = new Set<EntityRef['kind']>(['execution', 'conversation', 'host']);
/** At most this many references per entity_refs read (the operation's limit). */
const BATCH = 200;
/** Dirty references are read again at most this often: a run's totals touch its task and conversation many times a second. */
const QUIET_MS = 1000;

type Entry = { key: string; req: EntityCardRequest; live: Cell<EntityLive | null | undefined>; users: number; entity?: string };
const entries = new Map<string, Entry>();
// Entity key (kind:id) to the entries that show it: a code entry joins once its reply named the entity.
const byEntity = new Map<string, Set<Entry>>();
const fresh = new Set<Entry>(), dirty = new Set<Entry>();
let frame = 0, timer: ReturnType<typeof setTimeout> | undefined, lastDirtyRead = 0, unsubscribe: (() => void) | undefined;

const refKey = (r: EntityRef) => r.id ? `${r.kind}:${r.id}` : `code:${r.code!.toUpperCase()}`;
const hidden = () => typeof document !== 'undefined' && document.visibilityState === 'hidden';

function join(e: Entry, entity: string) {
  if (e.entity === entity) return;
  if (e.entity) byEntity.get(e.entity)?.delete(e);
  e.entity = entity;
  let set = byEntity.get(entity);
  if (!set) byEntity.set(entity, set = new Set());
  set.add(e);
}

async function read(batch: Entry[]) {
  for (let i = 0; i < batch.length; i += BATCH) {
    const part = batch.slice(i, i + BATCH);
    try {
      const r = await op('entity_refs', { refs: part.map(e => e.req) });
      const got = new Set<number>();
      for (const it of r.items) {
        const e = part[it.ref];
        if (!e) continue;
        got.add(it.ref);
        join(e, `${it.kind}:${it.id}`);
        e.live.value = { kind: it.kind, id: it.id, code: it.code ?? null, state: it.state ?? null, title: it.title };
      }
      // Left out: unknown or not visible to this person; the reference keeps its code and loses its tone.
      part.forEach((e, n) => { if (!got.has(n)) e.live.value = null; });
    } catch {
      // A failed read keeps what each reference showed; the next change or resync reads it again.
      part.forEach(e => dirty.add(e));
      scheduleDirty();
    }
  }
}

function flushFresh() {
  frame = 0;
  const batch = [...fresh].filter(e => entries.get(e.key) === e); fresh.clear();
  if (batch.length) void read(batch);
}

function flushDirty() {
  timer = undefined;
  if (hidden()) return; // read once the tab shows again (visibilitychange)
  const batch = [...dirty].filter(e => entries.get(e.key) === e); dirty.clear();
  lastDirtyRead = Date.now();
  if (batch.length) void read(batch);
}

function scheduleDirty() {
  if (timer || !dirty.size) return;
  timer = setTimeout(flushDirty, Math.max(0, lastDirtyRead + QUIET_MS - Date.now()));
}

function mark(entity: string) { const set = byEntity.get(entity); if (set) for (const e of set) dirty.add(e); }
function markKind(kind: string) { for (const [k, set] of byEntity) if (k.startsWith(kind + ':')) for (const e of set) dirty.add(e); }

/** The entity keys one change can alter the reference of (its own kind and id, and the task or conversation it runs in). */
export function changedEntities(c: Change): string[] {
  const out: string[] = [];
  switch (c.kind) {
    case 'task': out.push('task:' + c.id); break;
    case 'question': out.push('question:' + c.id); break;
    case 'session': out.push('conversation:' + c.id); break;
    case 'execution': out.push('execution:' + c.id); break;
    case 'artifact': out.push('result:' + c.id); break;
    case 'experiment': out.push('experiment:' + c.id); break;
    case 'host': out.push('host:' + c.id); break;
    case 'definition': { const [k, id] = c.id.split('/', 2); if (id && (k === 'project' || k === 'agent' || k === 'pipeline')) out.push(`${k}:${id}`); break; }
  }
  if (c.taskId) out.push('task:' + c.taskId);
  if (c.sessionId) out.push('conversation:' + c.sessionId);
  return out;
}

function onChanges(cs: Change[]) {
  for (const c of cs) {
    if (c.kind === 'delivery') { markKind('pull_request'); markKind('release'); continue; }
    if (c.kind === 'host') markKind('release');
    for (const k of changedEntities(c)) mark(k);
  }
  scheduleDirty();
}

function ensureFeed() {
  if (unsubscribe) return;
  unsubscribe = subscribe({ changes: onChanges, resync: () => { for (const e of entries.values()) dirty.add(e); scheduleDirty(); } });
  if (typeof document !== 'undefined') document.addEventListener('visibilitychange', () => { if (!hidden()) scheduleDirty(); });
}

/** A rendered reference holds its entity's live state while it is mounted; references to one entity share one entry. */
function live(r: EntityRef) {
  ensureFeed();
  const key = refKey(r);
  let e = entries.get(key);
  if (!e) {
    e = { key, req: r.id ? { kind: r.kind, id: r.id } : { code: r.code! }, live: cell<EntityLive | null | undefined>(undefined), users: 0 };
    entries.set(key, e);
    if (r.id) join(e, key);
    fresh.add(e);
    if (!frame) frame = typeof requestAnimationFrame === 'function' ? requestAnimationFrame(flushFresh) : (setTimeout(flushFresh, 0) as unknown as number);
  }
  e.users++;
  const entry = e;
  let released = false;
  return {
    read: () => entry.live.value,
    release: () => {
      if (released) return; released = true;
      // Kept a moment after the last reference goes: a list that rerenders mounts it again without a new read.
      if (--entry.users === 0) setTimeout(() => {
        if (entry.users || entries.get(entry.key) !== entry) return;
        entries.delete(entry.key); fresh.delete(entry); dirty.delete(entry);
        if (entry.entity) { const set = byEntity.get(entry.entity); set?.delete(entry); if (set && !set.size) byEntity.delete(entry.entity); }
      }, 5000);
    },
  };
}

/** An open card's reply is the newest state of its entity: every reference to it shows the same. */
function seed(c: EntityCardReply) {
  const set = byEntity.get(`${c.kind}:${c.id}`);
  if (set) for (const e of set) e.live.value = { kind: c.kind, id: c.id, code: c.code ?? null, state: c.state ?? null, title: c.title };
  if (c.code) { const e = entries.get('code:' + c.code.toUpperCase()); if (e) { join(e, `${c.kind}:${c.id}`); e.live.value = { kind: c.kind, id: c.id, code: c.code, state: c.state ?? null, title: c.title }; } }
}

function useCard(refInput: MaybeAccessor<EntityRef>): EntityCardRead {
  const ref = createMemo(() => readValue(refInput));
  const resolvedId = createMemo(() => ref().id);
  let learnedId: string | undefined;
  // The server names entity_card in the changes that can alter a card; touchedBy keeps those of this entity (by its id).
  // Execution changes are left out (totals flush many times a second): kinds whose state comes from runs read every 5 s.
  // A card asked by code learns its id from the reply (a legacy or alias id resolves to the one changes carry).
  const doc = useOp(
    'entity_card',
    () => {
      const r = ref();
      return r.id ? { kind: r.kind, id: r.id } : { code: r.code! };
    },
    {
      every: () => (TIMED.has(ref().kind) ? 5000 : undefined),
      scope: () => ({ kind: ref().kind, id: resolvedId() ?? learnedId }),
    },
  );
  createEffect(() => doc.data, d => { if (d) { learnedId = d.id; seed(d); } });
  // An entity now gone or hidden shows as such, never as its stored card.
  return {
    get data() { return doc.status === 404 ? null : doc.data; },
    get error() { return doc.status === 404 ? NOT_FOUND : doc.error; },
    retry: doc.reload,
  };
}

export const entityCards = (go: (path: string) => void): EntityCardSource => ({
  useCard, open: go, live, src: id => '/api/results/' + encodeURIComponent(id),
});

/** For tests and the load check: how many entries the registry holds and the entity keys it matches changes against. */
export const liveRegistry = { size: () => entries.size, entities: () => byEntity.size, onChanges };
