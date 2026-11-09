// Same-origin calls to coordinator operations (docs/designs/fleet-operator-app.md §3, §5). The browser holds no secret:
// Cloudflare Access and the guard add the credential. A redirect, 401 or 403 means the Access session expired: "signed out".
import { signal } from '@preact/signals';

export const online = signal(typeof navigator === 'undefined' ? true : navigator.onLine);
export const signedOut = signal(false);
if (typeof window !== 'undefined') {
  addEventListener('online', () => { online.value = true; });
  addEventListener('offline', () => { online.value = false; });
}

export class OpError extends Error { constructor(message: string, readonly status: number) { super(message); } }

export async function op<T = any>(name: string, body: unknown = {}, opts: { signal?: AbortSignal } = {}): Promise<T> {
  let r: Response;
  try {
    r = await fetch('/api/ops/' + name, { method: 'POST', headers: { 'content-type': 'application/json' }, body: JSON.stringify(body), credentials: 'same-origin', redirect: 'manual', signal: opts.signal });
  } catch (e) {
    if (opts.signal?.aborted) throw new OpError('aborted', 0); // superseded by a newer request: not offline
    online.value = false; throw new OpError('offline', 0);
  }
  online.value = true;
  if (r.type === 'opaqueredirect' || r.status === 401 || r.status === 403) { signedOut.value = true; throw new OpError('signed out', r.status); }
  const data = await r.json().catch(() => ({}));
  if (!r.ok) throw new OpError(data.message || r.statusText, r.status);
  return data as T;
}
