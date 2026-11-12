// Same-origin calls to coordinator operations (docs/designs/fleet-operator-app.md §3, §5). The browser holds no secret:
// Cloudflare Access and the guard add the credential. A redirect, 401 or 403 means the Access session expired: "signed out".
import { signal } from '@preact/signals';
import { useEffect, useRef } from 'preact/hooks';
import type { Ops } from '../../contracts/generated/ops.ts';

export const online = signal(typeof navigator === 'undefined' ? true : navigator.onLine);
export const signedOut = signal(false);
if (typeof window !== 'undefined') {
  addEventListener('online', () => { online.value = true; });
  addEventListener('offline', () => { online.value = false; });
}

export class OpError extends Error { readonly status: number; constructor(message: string, status: number) { super(message); this.status = status; } }

export type { Ops };
export type OpName = keyof Ops;

// op calls one operation of contracts/ops-<domain>.schema.json: the name picks the request and reply types.
export async function op<K extends OpName>(name: K, body: Ops[K]['request'], opts: { signal?: AbortSignal } = {}): Promise<Ops[K]['reply']> {
  let r: Response;
  try {
    r = await fetch('/api/ops/' + name, { method: 'POST', headers: { 'content-type': 'application/json' }, body: JSON.stringify(body), credentials: 'same-origin', redirect: 'manual', signal: opts.signal });
  } catch (e) {
    if (opts.signal?.aborted) throw new OpError('aborted', 0); // superseded by a newer request: not offline
    online.value = false; throw new OpError('offline', 0);
  }
  online.value = true;
  if (r.type === 'opaqueredirect' || r.status === 401 || r.status === 403) { signedOut.value = true; throw new OpError('signed out', r.status); }
  // An abort while the body is read is an abort, and a 2xx body that does not read whole (a fetch cut by a navigation or a
  // dropped connection) is a failure, never an empty success: callers would render a reply without its fields.
  const data = await r.json().catch(() => { if (opts.signal?.aborted) throw new OpError('aborted', 0); if (r.ok) throw new OpError('incomplete reply', r.status); return {}; });
  if (!r.ok) throw new OpError(data?.message || r.statusText, r.status);
  return data as Ops[K]['reply'];
}

// A key request (connections_key_request): the owner's value goes straight to connections_key_add, which answers the
// question with the fingerprint only; the value never reaches questions_answer or the asking agent.
export const addRequestedKey = (questionId: string, connection: string, value: string, signal?: AbortSignal) =>
  online.value ? writeOnce('connections_key_add', { connection, value, questionId }, () => {}, { signal }) : Promise.reject(Error('Offline: the key was not sent'));

/**
 * The outcome of a write is unknown: its reply was cut, the connection dropped before it, or a gateway in front of the
 * coordinator gave up (502/503/504, Cloudflare 520-524) after the coordinator may have committed.
 */
export const unknownOutcome = (e: unknown) => e instanceof OpError && (e.message === 'incomplete reply' || e.message === 'offline' || [502, 503, 504, 520, 521, 522, 523, 524].includes(e.status));
/**
 * A write that carries a requestId, sent again with the same body until its outcome is known: the coordinator returns the
 * first write's result for a known requestId (message_send, session_fork, session_start, project_create/update,
 * pipeline_run_create), and questions_answer and connections_key_add with a questionId answer a repeat of the same answer as
 * done, so a retry never duplicates a write (tests/go: B2, D8, PM10, pm-transport). onUnknown runs
 * before each wait. A definite error stops it, and so does `signal` (the caller unmounting): that rejects as aborted.
 */
// ponytail: fixed wait and no attempt cap; the caller ends it with the signal.
export async function writeOnce<K extends OpName>(name: K, body: Ops[K]['request'], onUnknown: () => void, opts: { signal?: AbortSignal; wait?: number } = {}): Promise<Ops[K]['reply']> {
  for (;;) {
    if (opts.signal?.aborted) throw new OpError('aborted', 0);
    try { return await op(name, body, { signal: opts.signal }); }
    catch (e) { if (!unknownOutcome(e)) throw e; onUnknown(); await new Promise(r => setTimeout(r, opts.wait ?? 1000)); }
  }
}

/** The requestId of the draft being sent in a session: the stored one while the text is unchanged, else a new one; null clears it. */
export function sendingId(session: string, text: string | null): string {
  const k = 'sending:' + session;
  try {
    if (text === null) { localStorage.removeItem(k); return ''; }
    const p = JSON.parse(localStorage.getItem(k) ?? 'null');
    if (p?.text === text) return p.requestId;
    const requestId = crypto.randomUUID(); localStorage.setItem(k, JSON.stringify({ text, requestId })); return requestId;
  } catch { return text === null ? '' : crypto.randomUUID(); /* storage unavailable: one id per attempt */ }
}

/** A signal that aborts when the component unmounts: pass it to writeOnce so a retry loop ends with its view. */
export function useAlive(): AbortSignal {
  const c = useRef<AbortController | null>(null); c.current ??= new AbortController();
  useEffect(() => () => c.current!.abort(), []);
  return c.current.signal;
}
