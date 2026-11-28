// Undo check: a pull request that reverts another merged pull request's change in a file: it deletes the lines that
// pull request added and puts back the lines it removed. A rebase or a conflict resolved to the branch's side can do
// that silently (2026-11-26: cefb2712 in #1200 undid #1199 in four files, opened after #1199 merged, and no check
// noticed). Only pull requests merged in the last --days (14) are looked at. This only reports, as a PR comment; it
// never fails a run (help, not block). Usage: node scripts/undo-check.ts --base <ref> --head <ref> [--days 14] [--out f]
import { execFileSync } from 'node:child_process';
import { writeFileSync } from 'node:fs';

const arg = (name: string, fallback = '') => { const i = process.argv.indexOf(name); return i > 0 ? process.argv[i + 1] : fallback; };
const git = (...a: string[]) => execFileSync('git', a, { encoding: 'utf8', maxBuffer: 64 << 20 });

const base = arg('--base', 'origin/main'), head = arg('--head', 'HEAD'), out = arg('--out');
const since = Date.now() - Number(arg('--days', '14')) * 864e5;

const mergeBase = git('merge-base', base, head).trim();
// Deleted line ranges per file, in the base's numbering (unified=0: one hunk header per change).
const diff = git('diff', '--unified=0', '--no-color', '--no-renames', `${mergeBase}..${head}`);
const ranges = new Map<string, [number, number][]>();
const prDel = new Map<string, Set<string>>(), prAdd = new Map<string, Set<string>>();
const lineKey = (l: string) => l.slice(1).trim();
let file = '';
for (const line of diff.split('\n')) {
  if (line.startsWith('--- ')) file = line.startsWith('--- a/') ? line.slice(6) : '';
  else if (file && line.startsWith('-') && lineKey(line)) prDel.set(file, (prDel.get(file) ?? new Set()).add(lineKey(line)));
  else if (file && line.startsWith('+') && !line.startsWith('+++') && lineKey(line)) prAdd.set(file, (prAdd.get(file) ?? new Set()).add(lineKey(line)));
  const h = /^@@ -(\d+)(?:,(\d+))? \+/.exec(line);
  if (h && file) {
    const start = Number(h[1]), n = h[2] === undefined ? 1 : Number(h[2]);
    if (n > 0) ranges.set(file, [...(ranges.get(file) ?? []), [start, start + n - 1]]);
  }
}

// The pull request that merged a commit into main, and when: the first merge on the way from it to the base.
const merged = new Map<string, { pr: string; at: number; sha: string } | null>();
function mergeOf(commit: string): { pr: string; at: number; sha: string } | null {
  if (merged.has(commit)) return merged.get(commit)!;
  const lines = git('log', '--merges', '--ancestry-path', '--format=%H%x09%cI%x09%s', `${commit}..${mergeBase}`).trim().split('\n').filter(Boolean);
  const first = lines.at(-1)?.split('\t');
  const pr = first ? /Merge pull request #(\d+)/.exec(first[2])?.[1] : undefined;
  const v = first && pr ? { pr, at: Date.parse(first[1]), sha: first[0] } : null;
  merged.set(commit, v);
  return v;
}

const candidates = new Map<string, { sha: string; files: Set<string> }>(); // pr -> merge and files whose lines go
for (const [f, rs] of ranges) {
  for (const [a, b] of rs) {
    let blame = '';
    try { blame = git('blame', '--line-porcelain', '-L', `${a},${b}`, mergeBase, '--', f); } catch { continue; }
    for (const l of blame.split('\n')) {
      const m = /^([0-9a-f]{40}) \d+ \d+/.exec(l);
      if (!m) continue;
      const mg = mergeOf(m[1]);
      if (!mg || mg.at <= since) continue;
      const c = candidates.get(mg.pr) ?? { sha: mg.sha, files: new Set<string>() };
      c.files.add(f);
      candidates.set(mg.pr, c);
    }
  }
}

// A revert, not an edit: of the lines that pull request added to the file, this one deletes most (>= 60%, at least 3),
// and of the lines it removed there, this one puts most back (>= 60%). A deleted or moved file is no revert.
const found = new Map<string, Map<string, number>>(); // pr -> file -> its added lines this one deletes
for (const [pr, c] of candidates) {
  for (const f of c.files) {
    let d = '';
    try { d = git('diff', '--unified=0', '--no-color', `${c.sha}^1`, c.sha, '--', f); } catch { continue; }
    const added = new Set<string>(), removed = new Set<string>();
    for (const l of d.split('\n')) {
      if (l.startsWith('+') && !l.startsWith('+++') && lineKey(l)) added.add(lineKey(l));
      if (l.startsWith('-') && !l.startsWith('---') && lineKey(l)) removed.add(lineKey(l));
    }
    const gone = [...added].filter(x => prDel.get(f)?.has(x)).length;
    const back = [...removed].filter(x => prAdd.get(f)?.has(x)).length;
    if (gone >= 3 && gone >= 0.6 * added.size && removed.size > 0 && back >= 0.6 * removed.size) {
      const byFile = found.get(pr) ?? new Map<string, number>();
      byFile.set(f, gone);
      found.set(pr, byFile);
    }
  }
}

const marker = '<!-- fleet-undo-check -->';
let body = '';
if (found.size) {
  body = `${marker}\n**Undo check** · this pull request reverts what other merged pull requests changed: it deletes the lines they added and puts back the lines they removed. ` +
    'If you did not mean to change them, a rebase or a conflict resolution took the wrong side: inspect `git diff origin/main...HEAD --stat` (AGENTS.md, step 1). This is information, not a gate.\n\n' +
    [...found].map(([pr, files]) => `- undoes lines from #${pr}: ${[...files].map(([f, n]) => `\`${f}\` (${n})`).join(', ')}`).join('\n') + '\n';
}
console.log(body || 'undo-check: no lines from newer pull requests deleted');
if (out) writeFileSync(out, body);
