// Readable forms of stored result text (docs/design-system/controls/result-viewer.md): JSON and JSON lines as key/value
// records, unified diffs as files with numbered lines. Pure functions, so the viewer renders and the tests check one parse.

/** One JSON value as the key/value view shows it: a leaf with its kind, or a branch with its entries in stored order. */
export type KvNode =
  | { kind: 'string' | 'number' | 'boolean' | 'null'; text: string }
  | { kind: 'object' | 'array'; entries: { key: string; node: KvNode }[] };

export function kvOf(v: unknown): KvNode {
  if (v === null || v === undefined) return { kind: 'null', text: 'none' };
  if (Array.isArray(v)) return { kind: 'array', entries: v.map((x, i) => ({ key: String(i + 1), node: kvOf(x) })) };
  if (typeof v === 'object') return { kind: 'object', entries: Object.entries(v as Record<string, unknown>).map(([key, x]) => ({ key, node: kvOf(x) })) };
  if (typeof v === 'number') return { kind: 'number', text: String(v) };
  if (typeof v === 'boolean') return { kind: 'boolean', text: v ? 'true' : 'false' };
  return { kind: 'string', text: String(v) };
}

/**
 * The records of a JSON or JSON-lines text. `complete` is false while more pages are still to come: then a JSON
 * document waits (null), and JSON lines keep only the lines whose newline has arrived. Null when the text is not JSON,
 * so the viewer shows the source instead.
 */
export function jsonRecords(text: string, lines: boolean, complete: boolean): KvNode[] | null {
  if (!lines) {
    if (!complete) return null;
    try { return [kvOf(JSON.parse(text))]; } catch { return null; }
  }
  const all = text.split('\n');
  const whole = complete ? all : all.slice(0, -1);
  const out: KvNode[] = [];
  for (const l of whole) {
    if (!l.trim()) continue;
    try { out.push(kvOf(JSON.parse(l))); } catch { return null; }
  }
  return out;
}

export type DiffLine = { sign: '+' | '-' | ' '; text: string; old: number | null; new: number | null };
export type DiffHunk = { head: string; lines: DiffLine[] };
export type DiffFile = { path: string; added: number; removed: number; hunks: DiffHunk[] };

const HUNK = /^@@ -(\d+)(?:,\d+)? \+(\d+)(?:,\d+)? @@/;
const strip = (p: string) => p.replace(/^[ab]\//, '').replace(/\t.*$/, '');

/** The files of a unified diff (git or plain `diff -u`). A text with no file or hunk header is no diff: empty. */
export function parseDiff(text: string): DiffFile[] {
  const files: DiffFile[] = [];
  let f: DiffFile | null = null, h: DiffHunk | null = null, o = 0, n = 0;
  const open = (path: string) => { f = { path, added: 0, removed: 0, hunks: [] }; files.push(f); h = null; };
  const rows = text.split('\n').map(r => r.replace(/\r$/, ''));
  for (let i = 0; i < rows.length; i++) {
    const l = rows[i];
    const git = /^diff --git a\/(.*) b\/(.*)$/.exec(l);
    if (git) { open(git[2]); continue; }
    // A file header is a "--- " line followed by "+++ "; inside a hunk a lone "--- " is a removed "-- " line.
    if (l.startsWith('--- ') && rows[i + 1]?.startsWith('+++ ')) {
      const from = strip(l.slice(4)), to = strip(rows[i + 1].slice(4));
      const path = to === '/dev/null' ? from : to;
      if (f && !h) (f as DiffFile).path = path; else open(path);
      i++;
      continue;
    }
    const m = HUNK.exec(l);
    if (m) {
      if (!f) open('');
      h = { head: l, lines: [] };
      (f as unknown as DiffFile).hunks.push(h);
      o = Number(m[1]); n = Number(m[2]);
      continue;
    }
    if (!h || !f) continue;
    const s = l[0];
    if (s === '+') { h.lines.push({ sign: '+', text: l.slice(1), old: null, new: n++ }); (f as DiffFile).added++; }
    else if (s === '-') { h.lines.push({ sign: '-', text: l.slice(1), old: o++, new: null }); (f as DiffFile).removed++; }
    else if (s === ' ') { h.lines.push({ sign: ' ', text: l.slice(1), old: o++, new: n++ }); }
    // "\ No newline at end of file", empty lines and other notes are left out.
  }
  return files.filter(x => x.hunks.length > 0);
}
