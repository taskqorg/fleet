// Live change feed client (D1): SSE with ?cursor=, manual reconnect with backoff, changes_since catch-up, polling fallback after
// three failed connects, resync. Changes are batched per animation frame and handed to every subscriber at once.
import { signal } from '@preact/signals';
import { op, online } from './api.ts';
import type { Change, StreamCursor } from '../../contracts/generated/ops.ts';

export type { Change };
type Sub = { changes: (c: Change[]) => void; resync: () => void };

export const feedState = signal<'connecting' | 'live' | 'polling'>('connecting');
let cursor: number | null = null;
const subs = new Set<Sub>();
let pending: Change[] = [], frame = 0, source: EventSource | null = null, failures = 0, pollTimer = 0;
const seen = new Set<number>();

export function subscribe(s: Sub) { subs.add(s); return () => { subs.delete(s); }; }
export const currentCursor = () => cursor;

function deliver(c: Change) {
  if (seen.has(c.seq)) return;
  seen.add(c.seq); if (seen.size > 5000) seen.delete(seen.values().next().value!);
  pending.push(c);
  if (!frame) frame = requestAnimationFrame(() => { frame = 0; const batch = pending; pending = []; for (const s of subs) s.changes(batch); });
}
function resync() { for (const s of subs) s.resync(); }

async function catchUp() {
  for (;;) {
    const r = await op('changes_since', { cursor: cursor ?? undefined, limit: 1000 });
    if (r.resync) { cursor = r.cursor; resync(); return; }
    r.changes.forEach(deliver); cursor = r.cursor;
    if (r.changes.length < 1000) return;
  }
}

function connect() {
  source?.close();
  feedState.value = failures >= 3 ? 'polling' : 'connecting';
  if (failures >= 3) { // polling fallback: the same read every 3 s until the stream comes back
    clearTimeout(pollTimer);
    pollTimer = setTimeout(async () => { try { await catchUp(); } catch { /* offline */ } failures = 2; connect(); }, 3000) as unknown as number;
    return;
  }
  const es = new EventSource('/api/stream' + (cursor === null ? '' : '?cursor=' + cursor));
  source = es;
  es.addEventListener('ready', e => { const d: StreamCursor = JSON.parse((e as MessageEvent).data); if (cursor === null) cursor = d.cursor; failures = 0; feedState.value = 'live'; });
  es.addEventListener('resync', e => { cursor = (JSON.parse((e as MessageEvent).data) as StreamCursor).cursor; resync(); });
  es.onmessage = e => { const c = JSON.parse(e.data) as Change; deliver(c); cursor = c.seq; };
  es.onerror = () => {
    es.close(); failures++;
    const wait = [1000, 2000, 5000, 10000][Math.min(failures - 1, 3)] * (0.8 + Math.random() * 0.4);
    setTimeout(async () => { if (online.value) { try { await catchUp(); } catch { /* still down */ } } connect(); }, wait);
  };
}

/** Start the feed from a stored cursor (offline restore) or from the server head. */
export function startFeed(from: number | null) { cursor = from; connect(); }
