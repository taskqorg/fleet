// Structural sharing for replies (app/src/README.md, "Live updates keep what is on screen"): a reread that brings the
// same values keeps the objects already on screen, so <For> and keyed <Show> keep their rows, inputs, focus and caret.

type Json = unknown;
const isObj = (v: Json): v is Record<string, Json> => typeof v === 'object' && v !== null && !Array.isArray(v);
const idOf = (v: Json) => (isObj(v) && (typeof v.id === 'string' || typeof v.id === 'number') ? v.id : undefined);

/**
 * `next` with every part equal to the same part of `prev` replaced by `prev`'s object. Returns `prev` itself when the
 * two are equal. Array items are matched by `id` when every item has one (a row moved or inserted keeps its object),
 * else by position. Only plain JSON (parsed replies) is expected.
 */
export function share<T>(prev: T, next: T): T {
  return shareAny(prev, next) as T;
}

function shareAny(prev: Json, next: Json): Json {
  if (prev === next) return prev;
  if (Array.isArray(prev) && Array.isArray(next)) return shareArray(prev, next);
  if (isObj(prev) && isObj(next)) return shareObject(prev, next);
  return next;
}

function shareArray(prev: Json[], next: Json[]): Json[] {
  const byId = next.length > 0 && prev.length > 0 && next.every(v => idOf(v) !== undefined) && prev.every(v => idOf(v) !== undefined)
    ? new Map(prev.map(v => [idOf(v), v] as const)) : null;
  let same = prev.length === next.length;
  const out = next.map((v, i) => {
    const before = byId ? byId.get(idOf(v)) : i < prev.length ? prev[i] : undefined;
    const r = before === undefined ? v : shareAny(before, v);
    if (r !== prev[i]) same = false;
    return r;
  });
  return same ? prev : out;
}

function shareObject(prev: Record<string, Json>, next: Record<string, Json>): Record<string, Json> {
  const keys = Object.keys(next);
  let same = keys.length === Object.keys(prev).length;
  const out: Record<string, Json> = {};
  for (const k of keys) {
    const r = k in prev ? shareAny(prev[k], next[k]) : next[k];
    if (r !== prev[k] || !(k in prev)) same = false;
    out[k] = r;
  }
  return same ? prev : out;
}
