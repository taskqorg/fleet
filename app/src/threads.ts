// Thread window state (docs/designs/fleet-conversations.md, Stage 2 item 16). One window of loaded blocks per thread key,
// paged both ways by block key, followed live from the feed cursor of the page read. The server builds every block; this
// file only places them. Nothing here reads native harness records.
import { useEffect, useRef, useState } from 'preact/hooks';
import { op } from './api.ts';
import { subscribe, type Change } from './feed.ts';
import type { Block, ThreadEntriesReply } from '../../contracts/generated/ops.ts';

export type { Block };
export type ThreadKey = { task: string } | { session: string };
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
  let blocks = removed.length ? w.blocks.filter(b => !removed.includes(b.key)) : [...w.blocks];
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
// question and task changes carry no session: the server keeps only those of this thread (a task change may be a delivery of
// one of its sessions, whose stage shows on a result card).
export const touches = (c: Change, sessionIds: Set<string>, taskId?: string) => ['event', 'execution', 'question', 'artifact', 'task'].includes(c.kind) && (c.kind === 'question' || c.kind === 'task' || (!!taskId && c.taskId === taskId) || (!!c.sessionId && sessionIds.has(c.sessionId)));

const PAGE = 100;
/** One thread's window: newest on open, older and newer pages, jump around a key, and live blocks from the feed. */
export function useThread(key: ThreadKey | null) {
  const [win, setWin] = useState<Win | null>(null);
  const [error, setError] = useState<string | null>(null);
  const ref = useRef<Win | null>(null); ref.current = win;
  const busy = useRef(false), gen = useRef(0), again = useRef<'before' | 'after' | null>(null);
  const id = key ? JSON.stringify(key) : null;
  // The sessions of the thread (a task thread spans several): listed with every page, and joined live when an execution of the
  // task starts in another session. A ref, because the feed subscription outlives the render that made it.
  const follow = useRef({ sessions: new Set<string>(), task: undefined as string | undefined });
  const apply = (f: (w: Win) => Win) => setWin(w => w && f(w));
  const fetchBlocks = async (cs: Change[], g: number) => {
    const f = follow.current;
    for (const c of cs) if (f.task && c.kind === 'execution' && c.taskId === f.task && c.sessionId) f.sessions.add(c.sessionId);
    const rows = cs.filter(c => touches(c, f.sessions, f.task)).map(c => ({ kind: c.kind, id: c.id }));
    if (!rows.length || !key) return;
    const r = await op('thread_blocks', { key: ref.current?.key ?? key, rows });
    if (g === gen.current) apply(w => fed(w, r.entries, r.removed));
  };
  // Changes committed after the page's snapshot: read from its feed cursor, so no row is skipped.
  const catchUp = async (cursor: number, g: number) => {
    for (let c: number | null = cursor; c !== null;) {
      const r: { changes: Change[]; cursor: number; resync?: boolean } = await op('changes_since', { cursor: c, limit: 1000 });
      if (r.resync || g !== gen.current) return;
      await fetchBlocks(r.changes, g); c = r.changes.length === 1000 ? r.cursor : null;
    }
  };
  const load = async (args: object, place: (p: Page) => void) => {
    if (!key) return;
    const g = gen.current;
    try { const p = await op('thread_entries', { key: ref.current?.key ?? key, resolve: true, limit: PAGE, ...args }); if (g !== gen.current) return;
      if (p.key) follow.current.task = 'task' in p.key ? p.key.task : undefined; for (const s of p.sessionIds ?? []) follow.current.sessions.add(s); place(p as Page); setError(null); await catchUp(p.feedCursor, g); }
    catch (e: any) { if (g === gen.current) setError(e.message); }
  };
  const newest = () => { gen.current++; return load({}, p => setWin(fromPage(p))); };
  useEffect(() => {
    setWin(null); ref.current = null; follow.current = { sessions: new Set(key && 'session' in key ? [key.session] : []), task: key && 'task' in key ? key.task : undefined }; if (!key) return;
    newest();
    return subscribe({ resync: newest, changes: cs => { if (ref.current) fetchBlocks(cs, gen.current).catch(() => {}); } });
  }, [id]);
  const page = async (dir: 'before' | 'after') => {
    // The list asks once per length; a request made while a page loads runs after it, so none is lost.
    if (busy.current) { again.current = dir; return; }
    const w = ref.current; if (!w) return;
    if (dir === 'before' ? !w.hasBefore || !w.first : !w.hasAfter || !w.last) return;
    busy.current = true;
    try { await load({ [dir]: dir === 'before' ? w.first : w.last }, p => { const next = dir === 'before' ? prepend(w, p) : append(w, p); ref.current = next; apply(x => dir === 'before' ? prepend(x, p) : append(x, p)); }); }
    finally { busy.current = false; const d = again.current; again.current = null; if (d) page(d); }
  };
  return {
    win, error,
    older: () => page('before'), newer: () => page('after'), newest,
    /** Replace the window with entries around a key (a search hit). */
    jump: (k: string) => { gen.current++; return load({ around: k }, p => setWin(w => fromPage(p, w?.total))); },
  };
}
