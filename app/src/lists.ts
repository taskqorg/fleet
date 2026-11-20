// Pure list logic for the entity store (D4): the server decides membership and order; the client only merges.
import type { SortKeyPart, TaskRow, SessionRow } from '../../contracts/generated/ops.ts';
export type SortKey = SortKeyPart[];
/** A row of a server list: the contract's closed task and session rows. */
export type Row = TaskRow | SessionRow;
/** What list merging needs of a row. */
export type Keyed = { id: string; sortKey: SortKey };

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
 * Merge a membership answer into a loaded list of ids (records live in the store; sort keys are per list, because the key
 * depends on the grouping):
 * - changed ids that no longer match are removed;
 * - matching ids are placed by their new sort key;
 * - a new id beyond the loaded window (list not complete and key after the last loaded key) is left for paging.
 */
export function applyMembership(ids: string[], keys: Record<string, SortKey>, changed: Iterable<string>, matched: Keyed[], complete: boolean): { ids: string[]; keys: Record<string, SortKey> } {
  const gone = new Set(changed);
  const last = ids.length ? keys[ids[ids.length - 1]] : undefined;
  const kept = ids.filter(id => !gone.has(id));
  const nextKeys: Record<string, SortKey> = {};
  for (const id of kept) nextKeys[id] = keys[id];
  for (const r of matched) {
    if (!complete && last && compareKeys(r.sortKey, last) > 0) continue;
    let lo = 0, hi = kept.length;
    while (lo < hi) { const mid = (lo + hi) >> 1; if (compareKeys(nextKeys[kept[mid]], r.sortKey) < 0) lo = mid + 1; else hi = mid; }
    kept.splice(lo, 0, r.id); nextKeys[r.id] = r.sortKey;
  }
  return { ids: kept, keys: nextKeys };
}

/** Ids of rows touched by a batch of changes, for one list kind. */
export function touched(changes: { kind: string; id: string; taskId?: string | null; sessionId?: string | null }[], list: 'tasks' | 'sessions'): string[] {
  const out = new Set<string>();
  for (const c of changes) {
    if (list === 'tasks' && c.taskId) out.add(c.taskId);
    if (list === 'sessions' && c.sessionId) out.add(c.sessionId);
  }
  return [...out];
}

/** Split ids into server-sized batches (list_membership accepts at most 1000 ids per call). */
export function chunks<T>(xs: T[], size: number): T[][] {
  const out: T[][] = [];
  for (let i = 0; i < xs.length; i += size) out.push(xs.slice(i, i + size));
  return out;
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
export const flipGroup = (flipped: readonly string[], key: string): string[] => flipped.includes(key) ? flipped.filter(k => k !== key) : [...flipped, key];
