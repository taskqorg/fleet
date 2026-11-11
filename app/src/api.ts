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
  // An abort while the body is read is an abort, not an empty success.
  const data = await r.json().catch(() => { if (opts.signal?.aborted) throw new OpError('aborted', 0); return {}; });
  if (!r.ok) throw new OpError(data.message || r.statusText, r.status);
  return data as Ops[K]['reply'];
}
