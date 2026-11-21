// In-memory store reset bus; authored input belongs to api.ts and is never deleted here.
import { onAccessInvalidated } from './access.ts';

export const onClear = new Set<() => void>();

/** Synchronously reset all in-memory stores and preference caches on sign-out, identity, or grant changes. */
export function clearAll(_stopSaving = false): Promise<void> {
  for (const f of onClear) f();
  return Promise.resolve();
}

onAccessInvalidated.add(() => { void clearAll(); });
