// In-memory store reset bus; authored input belongs to api.ts and is never deleted here.
import { onAccessInvalidated } from './access.ts';

export const onClear = new Set<() => void>();

let clearing: Promise<void> | null = null;

/** Synchronously reset all in-memory stores and preference caches once per invalidation turn. */
export function clearAll(_stopSaving = false): Promise<void> {
  if (clearing) return clearing;
  const done = Promise.resolve().then(() => { if (clearing === done) clearing = null; });
  clearing = done;
  for (const f of onClear) f();
  return done;
}

onAccessInvalidated.add(() => { void clearAll(); });
