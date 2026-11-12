// Same-origin calls to coordinator operations (docs/designs/fleet-operator-app.md §3, §5). The browser holds no secret:
// Cloudflare Access and the guard add the credential. A redirect, 401 or 403 means the Access session expired: "signed out".
import { signal } from '@preact/signals';
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
export const addRequestedKey = (questionId: string, connection: string, value: string) =>
  online.value ? op('connections_key_add', { connection, value, questionId }) : Promise.reject(Error('Offline: the key was not sent'));

/** The outcome of a write is unknown: its reply was cut, or the connection dropped before it. */
export const unknownOutcome = (e: unknown) => e instanceof OpError && (e.message === 'incomplete reply' || e.message === 'offline');
/**
 * A write whose outcome is unknown, sent again with the same body (so the same requestId) until a reply reads whole: the
 * coordinator returns the first write's result for a known requestId, so the retry never duplicates it. onUnknown runs
 * before each wait. A definite error (4xx/5xx, signed out, abort) stops it.
 */
// ponytail: fixed 1 s wait and no cap; the caller stops waiting by unmounting (abort) once that matters.
export async function writeOnce<K extends OpName>(name: K, body: Ops[K]['request'], onUnknown: () => void, wait = 1000): Promise<Ops[K]['reply']> {
  for (;;) {
    try { return await op(name, body); }
    catch (e) { if (!unknownOutcome(e)) throw e; onUnknown(); await new Promise(r => setTimeout(r, wait)); }
  }
}
