// Pure list utilities for ordering and collapsed groups (D4).
import type { SortKeyPart, TaskRow, SessionRow } from '../../contracts/generated/ops.ts';
export type SortKey = SortKeyPart[];
/** A row of a server list: the contract's closed task and session rows. */
export type Row = TaskRow | SessionRow;

export function compareKeys(a: SortKey, b: SortKey): number {
  for (let i = 0; i < Math.max(a.length, b.length); i++) {
    const x = a[i], y = b[i];
    if (x === y) continue;
    if (x === undefined) return -1;
    if (y === undefined) return 1;
    return x < y ? -1 : 1;
  }
  return 0;
}

/**
 * The collapsed groups of a list: its default collapsed groups with the person's flips applied (a stored key collapses a
 * group open by default and opens one collapsed by default).
 */
export function closedGroups(defaults: readonly string[], flipped: readonly string[]): Set<string> {
  const out = new Set(defaults);
  for (const k of flipped) if (out.has(k)) out.delete(k); else out.add(k);
  return out;
}

/** The flips after the person toggles one group: its key is added, or removed when the toggle returns it to its default. */
export const flipGroup = (flipped: readonly string[], key: string): string[] =>
  flipped.includes(key) ? flipped.filter(k => k !== key) : [...flipped, key];
