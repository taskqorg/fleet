// Thread window state (docs/designs/fleet-conversations.md, Stage 2 item 16). One window of loaded blocks per thread key,
// paged both ways by block key, followed live from the feed cursor of the page read. The server builds every block; this
// file only places them. Nothing here reads native harness records.
import { createEffect, createMemo, createSignal } from 'solid-js';
import { readValue, type MaybeAccessor } from './signal.ts';
import { op } from './api.ts';
import { subscribe, type Change } from './feed.ts';
import type { Block, ThreadEntriesReply } from '../../contracts/generated/ops.ts';

export type { Block };
export type ThreadKey = { task: string } | { session: string; thread?: string };
export type Page = Omit<ThreadEntriesReply, 'key' | 'sessionIds'> & { key?: ThreadKey; sessionIds?: string[] };
/** fresh: entries fed past `last` while newer history is not loaded (after a jump); the pill counts them. */
export type Win = { blocks: Block[]; first: string | null; last: string | null; hasBefore: boolean; hasAfter: boolean; total: number; fresh: number; freshKeys?: string[]; sessionIds?: string[]; key?: ThreadKey };

const parts = (k: string) => k.split(':').map(Number);
export const cmpKey = (a: string, b: string) => { const [x, i] = parts(a), [y, j] = parts(b); return x - y || i - j; };

export const fromPage = (p: Page, total = 0): Win => ({ key: p.key, sessionIds: p.sessionIds, blocks: p.entries, first: p.first, last: p.last, hasBefore: p.hasBefore, hasAfter: p.hasAfter, total: p.total ?? total, fresh: 0 });
/** Older page above the window. */
export function prepend(w: Win, p: Page): Win {
  const add = w.first ? p.entries.filter(b => cmpKey(b.key, w.first!) < 0) : p.entries;
  return { ...w, blocks: [...add, ...w.blocks], first: add[0]?.key ?? w.first, hasBefore: p.hasBefore };
}
/** Newer page below the window. */
export function append(w: Win, p: Page): Win {
  const add = w.last ? p.entries.filter(b => cmpKey(b.key, w.last!) > 0) : p.entries;
  return { ...w, blocks: [...w.blocks, ...add], last: add.at(-1)?.key ?? w.last, hasAfter: p.hasAfter, fresh: p.hasAfter ? w.fresh : 0, freshKeys: p.hasAfter ? w.freshKeys : [] };
}
/**
 * Fed blocks (design "Read path"): a block with a known key replaces it in place; a new block inside [first, last], or after
 * last when nothing newer exists, is inserted by key (a late commit lands where its key says); a block past last while newer
 * history is not loaded only counts in the pill. `removed` keys (streams a closing event replaced) leave the window.
 */
export function fed(w: Win, entries: Block[], removed: string[] = []): Win {
  const blocks = removed.length ? w.blocks.filter(b => !removed.includes(b.key)) : [...w.blocks];
  let { first, last, total, fresh } = w; const freshKeys = [...(w.freshKeys ?? [])];
  total -= w.blocks.length - blocks.length;
  for (const e of entries) {
    const at = blocks.findIndex(b => b.key === e.key);
    if (at >= 0) { blocks[at] = e; continue; }
    const inside = first !== null && cmpKey(e.key, first) >= 0 && (last === null || cmpKey(e.key, last) <= 0);
    const tail = !w.hasAfter && (last === null || cmpKey(e.key, last) > 0);
    // Before first with nothing older to load: no later page read would bring it, so it is placed now.
    const head = !w.hasBefore && first !== null && cmpKey(e.key, first) < 0;
    if (!inside && !tail && !head) { if (last !== null && cmpKey(e.key, last) > 0 && !freshKeys.includes(e.key)) { freshKeys.push(e.key); fresh++; } continue; }
    // ponytail: linear insert by key; a binary search if windows reach tens of thousands of rows.
    let i = blocks.length; while (i > 0 && cmpKey(blocks[i - 1].key, e.key) > 0) i--;
    blocks.splice(i, 0, e); total++;
    if (first === null || cmpKey(e.key, first) < 0) first = e.key;
    if (last === null || cmpKey(e.key, last) > 0) last = e.key;
  }
  return { ...w, blocks, first, last, total, fresh, freshKeys };
}
/** Change-feed rows that can touch a session thread; thread_blocks checks membership on the server. */
export const touches = (c: Change, sessionIds: Set<string>, taskId?: string) => {
  if (c.kind === 'setting-change') return c.settingTarget?.startsWith('publishing:') === true;
  if (c.kind === 'question') return true;
  return ['event', 'execution', 'artifact'].includes(c.kind) &&
    ((!!taskId && c.taskId === taskId) || (!!c.sessionId && sessionIds.has(c.sessionId)));
};

const PAGE = 100;
/** One thread's window: newest on open, older and newer pages, jump around a key, and live blocks from the feed. */
export function useThread(keyInput: MaybeAccessor<ThreadKey | null>) {
  const [win, setWin] = createSignal<Win | null>(null, { ownedWrite: true });
  const [error, setError] = createSignal<string | null>(null, { ownedWrite: true });
  let currentWin: Win | null = null;
  let busy = false;
  let gen = 0;
  let again: 'before' | 'after' | null = null;
  let currentKey: ThreadKey | null = null;
  // The sessions of the thread (a task thread spans several): listed with every page, and joined live when an execution of the
  // task starts in another session.
  let follow = { sessions: new Set<string>(), task: undefined as string | undefined };
  const commitWin = (next: Win | null | ((w: Win | null) => Win | null)) => {
    currentWin = typeof next === 'function' ? next(currentWin) : next;
    setWin(currentWin);
  };
  const apply = (f: (w: Win) => Win) => commitWin(w => (w ? f(w) : null));
  let feedRead: Promise<void> = Promise.resolve();
  const fetchBlocks = (cs: Change[], g: number) => {
    // Apply snapshots in read order so a slow earlier reply cannot overwrite newer delivery facts.
    const read = feedRead.then(() => readBlocks(cs, g));
    feedRead = read.catch(() => {});
    return read;
  };
  const readBlocks = async (cs: Change[], g: number) => {
    if (g !== gen || !currentKey) return;
    for (const c of cs) if (follow.task && c.kind === 'execution' && c.taskId === follow.task && c.sessionId) follow.sessions.add(c.sessionId);
    const rows = cs.filter(c => touches(c, follow.sessions, follow.task)).map(c => ({ kind: c.kind, id: c.id }));
    if (!rows.length) return;
    const r = await op('thread_blocks', { key: currentWin?.key ?? currentKey, rows });
    if (g === gen) apply(w => fed(w, r.entries, r.removed));
  };
  // Changes committed after the page's snapshot: read from its feed cursor, so no row is skipped.
  const catchUp = async (cursor: number, g: number) => {
    for (let c: number | null = cursor; c !== null;) {
      const r: { changes: Change[]; cursor: number; resync?: boolean } = await op('changes_since', { cursor: c, limit: 1000 });
      if (r.resync || g !== gen) return;
      await fetchBlocks(r.changes, g); c = r.changes.length === 1000 ? r.cursor : null;
    }
  };
  const load = async (args: object, place: (p: Page) => void) => {
    if (!currentKey) return;
    const g = gen;
    try {
      // full: false: tool blocks come as summaries; a call opened in a tool line loads its input and output (tool_output).
      const p = await op('thread_entries', { key: currentWin?.key ?? currentKey, resolve: true, full: false, limit: PAGE, ...args });
      if (g !== gen) return;
      if (p.key) follow.task = 'task' in p.key ? p.key.task : undefined;
      for (const s of p.sessionIds ?? []) follow.sessions.add(s);
      place(p as Page);
      setError(null);
      await catchUp(p.feedCursor, g);
    } catch (e) {
      if (g === gen) setError(e instanceof Error ? e.message : String(e));
    }
  };
  const nextWindow = () => {
    gen++;
    feedRead = Promise.resolve();
  };
  const newest = () => { nextWindow(); return load({}, p => commitWin(fromPage(p))); };
  const id = createMemo(() => {
    const k = readValue(keyInput);
    return k ? JSON.stringify(k) : null;
  });
  createEffect(
    () => {
      const k = readValue(keyInput);
      return [id(), k] as const;
    },
    ([, k]) => {
      currentKey = k;
      commitWin(null);
      setError(null);
      follow = {
        sessions: new Set(k && 'session' in k ? [k.session] : []),
        task: k && 'task' in k ? k.task : undefined,
      };
      if (!k) { nextWindow(); return; }
      void newest();
      return subscribe({ resync: newest, changes: cs => { if (currentWin) fetchBlocks(cs, gen).catch(() => {}); } });
    },
  );
  const page = async (dir: 'before' | 'after') => {
    // The list asks once per length; a request made while a page loads runs after it, so none is lost.
    if (busy) { again = dir; return; }
    const w = currentWin; if (!w) return;
    if (dir === 'before' ? !w.hasBefore || !w.first : !w.hasAfter || !w.last) return;
    busy = true;
    try { await load({ [dir]: dir === 'before' ? w.first : w.last }, p => apply(x => dir === 'before' ? prepend(x, p) : append(x, p))); }
    finally { busy = false; const d = again; again = null; if (d) void page(d); }
  };
  return {
    get win() { return win(); },
    get error() { return error(); },
    older: () => page('before'),
    newer: () => page('after'),
    newest,
    /** Replace the window with entries around a key (a search hit). */
    jump: (k: string, onWindow: () => void) => {
      nextWindow();
      return load({ around: k }, p => {
        onWindow();
        commitWin(w => fromPage(p, w?.total));
      });
    },
  };
}
