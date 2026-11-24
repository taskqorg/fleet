// Authorized commit feed. Every transport callback belongs to one access generation.
import { cell } from './signal.ts';
import { op, accessControlOp } from './api.ts';
import { accessGeneration, ensureAccessGeneration, invalidateAccess, isAccessCurrent, onAccessInvalidated, registerAccessBaseline, type AccessBaseline } from './access.ts';
import type { Change, StreamCursor } from '../../contracts/generated/ops.ts';
export type { Change };
type Sub = { changes: (c: Change[]) => void; resync: () => void };
export const feedState = cell<'connecting' | 'live' | 'polling'>('connecting');
let cursor: number | null = null, source: EventSource | null = null, failures = 0;
let timer: ReturnType<typeof setTimeout> | undefined, frame = 0, started = false;
let pending: Change[] = [];
let unregisterBaseline: (() => void) | undefined;
const seen = new Set<number>(), subs = new Set<Sub>();
export function subscribe(s: Sub) { subs.add(s); return () => { subs.delete(s); }; }
export const currentCursor = () => cursor;
function reset() {
  source?.close(); source = null; clearTimeout(timer); timer = undefined;
  if (frame) cancelAnimationFrame(frame); frame = 0; pending = []; seen.clear(); cursor = null;
  feedState.value = 'connecting';
}
onAccessInvalidated.add(reset);
function deliver(c: Change, generation: number) {
  if (!isAccessCurrent(generation) || seen.has(c.seq)) return;
  seen.add(c.seq); if (seen.size > 5000) seen.delete(seen.values().next().value!);
  pending.push(c);
  if (!frame) frame = requestAnimationFrame(() => {
    frame = 0; const batch = pending; pending = [];
    if (isAccessCurrent(generation)) for (const s of subs) s.changes(batch);
  });
}
function retry() {
  if (!started) return;
  clearTimeout(timer); timer = setTimeout(() => { void ensureAccessGeneration('feed retry').catch(retry); }, 1000);
}
function resync() { invalidateAccess('feed resync'); void ensureAccessGeneration('feed resync').catch(retry); }
async function catchUp(generation: number) {
  for (;;) {
    if (!isAccessCurrent(generation)) return;
    const r = await op('changes_since', { cursor: cursor ?? undefined, limit: 1000 });
    if (!isAccessCurrent(generation)) return;
    if (r.resync) { resync(); return; }
    r.changes.forEach(c => deliver(c, generation)); cursor = r.cursor;
    if (r.changes.length < 1000) return;
  }
}
function reconnect(generation: number) {
  if (!isAccessCurrent(generation)) return;
  clearTimeout(timer);
  timer = setTimeout(async () => {
    if (!isAccessCurrent(generation)) return;
    try { await catchUp(generation); } catch { /* retain offline distinction from api */ }
    if (isAccessCurrent(generation)) connect(generation);
  }, failures >= 3 ? 2000 : 1000);
}
function connect(generation: number) {
  source?.close();
  const es = new EventSource('/api/stream' + (cursor === null ? '' : '?cursor=' + cursor)); source = es;
  const current = () => source === es && isAccessCurrent(generation);
  es.addEventListener('ready', () => { if (current()) { failures = 0; feedState.value = 'live'; } });
  es.addEventListener('resync', () => { if (current()) resync(); });
  es.onmessage = e => { if (current()) { const c = JSON.parse(e.data) as Change; deliver(c, generation); cursor = c.seq; } };
  es.onerror = () => { if (!current()) return; es.close(); failures++; feedState.value = failures >= 3 ? 'polling' : 'connecting'; reconnect(generation); };
}
/** Registers transport only; access.ts alone runs validation and opens the generation. */
export function startFeed(_from: number | null = null) {
  if (started) return; started = true;
  unregisterBaseline = registerAccessBaseline(async ({ generation, signal }) => {
    if (failures >= 3 || typeof EventSource === 'undefined') {
      feedState.value = 'polling';
      const r = await accessControlOp('changes_since', {}, { signal });
      return { cursor: r.cursor, dispose: reset, activate: () => { cursor = r.cursor; for (const s of subs) s.resync(); reconnect(generation); } };
    }
    return new Promise<AccessBaseline>((resolve, reject) => {
      const es = new EventSource('/api/stream'); source = es;
      let active = false, settled = false;
      const current = () => !signal.aborted && generation === accessGeneration() && source === es;
      const dispose = () => { es.close(); signal.removeEventListener('abort', abort); if (source === es) source = null; };
      const fail = (error: Error) => { dispose(); if (!settled) { settled = true; reject(error); retry(); } else if (current()) resync(); };
      const abort = () => fail(Error('Baseline cancelled'));
      signal.addEventListener('abort', abort, { once: true });
      es.addEventListener('ready', e => {
        if (!current() || settled) return;
        const c = (JSON.parse((e as MessageEvent).data) as StreamCursor).cursor;
        settled = true;
        resolve({ cursor: c, dispose, activate: () => {
          if (!current()) throw Error('Baseline superseded');
          active = true; cursor = c; failures = 0; feedState.value = 'live'; for (const s of subs) s.resync();
        } });
      });
      es.addEventListener('resync', () => { if (!current()) return; if (active) resync(); else { dispose(); if (!settled) { settled = true; reject(Error('Baseline resync')); retry(); } else resync(); } });
      es.onmessage = e => {
        if (!current()) return;
        if (!active) { dispose(); if (!settled) { settled = true; reject(Error('Change before baseline')); retry(); } else resync(); return; }
        const c = JSON.parse(e.data) as Change; deliver(c, generation); cursor = c.seq;
      };
      es.onerror = () => {
        if (!current()) return;
        failures++;
        if (!active) { dispose(); if (!settled) { settled = true; reject(Error('Baseline unavailable')); retry(); } else resync(); }
        else { es.close(); feedState.value = failures >= 3 ? 'polling' : 'connecting'; reconnect(generation); }
      };
    });
  });
}

/** Dispose transport, timers and controller registration without delivering old queued events. */
export function stopFeed() { started = false; unregisterBaseline?.(); unregisterBaseline = undefined; reset(); }
