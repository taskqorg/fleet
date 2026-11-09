// Pure list logic for the entity store (D4): the server decides membership and order; the client only merges.
export type SortKey = (string | number)[];
export type Row = { id: string; sortKey: SortKey; [k: string]: unknown };

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
 * Merge a membership answer into a loaded list.
 * - changed ids that no longer match are removed;
 * - matching rows are replaced (fresh data) and placed by sort key;
 * - a new row beyond the loaded window (list not complete and key after the last loaded key) is left for paging.
 */
export function applyMembership(rows: Row[], changed: Iterable<string>, matched: Row[], complete: boolean): Row[] {
  const ids = new Set(changed);
  const fresh = new Map(matched.map(r => [r.id, r]));
  const last = rows.at(-1)?.sortKey;
  const kept = rows.filter(r => !ids.has(r.id));
  for (const r of fresh.values()) {
    if (!complete && last && compareKeys(r.sortKey, last) > 0) continue;
    let lo = 0, hi = kept.length;
    while (lo < hi) { const mid = (lo + hi) >> 1; if (compareKeys(kept[mid].sortKey, r.sortKey) < 0) lo = mid + 1; else hi = mid; }
    kept.splice(lo, 0, r);
  }
  return kept;
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
