// The order arithmetic of drag to reorder (reorder.tsx); plain TypeScript so tests run it without a JSX transform.

/** The keys after moving `key` to slot `to` among the others (0 = first; clamped to the ends). */
export function reorderTo(keys: readonly string[], key: string, to: number): string[] {
  const rest = keys.filter(k => k !== key), at = Math.max(0, Math.min(rest.length, to));
  return [...rest.slice(0, at), key, ...rest.slice(at)];
}
