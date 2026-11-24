// Design report PR comment (docs/designs/fleet-docs-governance.md, "Report"): leads with what this pull request changed
// against main, folds the standing report. Information only: it never fails, and a missing input becomes a line of text.
// Node standard library only.
// Usage: node scripts/design-report-comment.ts --pr <dir> [--base <dir>] [--base-label <text>] [--run <url>] [--issues <url>]
//          [--drop-drift] [--out <file>]   |   --self-check
// <dir> holds report.txt and docs-report.txt (the design-report artifact's design-report/ folder).
import assert from 'node:assert/strict';
import { existsSync, readFileSync, writeFileSync } from 'node:fs';
import { join } from 'node:path';

export type Diff = { added: string[]; fixed: string[]; changed: [string, string][] };

// Lines that differ on every run without meaning anything (the docs report's timing header).
const NOISE = [/^Docs report \(.*\), \d+ ms\.$/];
// docs.delta lines describe the change itself against its own base, so they are always "this PR".
const OWN = [/ docs\.delta /];

// Two lines are the same finding when they differ only in a source line number (path.ts:12 → path.ts:14).
const finding = (l: string) => l.trim().replace(/^([^\s:]+):\d+\b/, '$1');
// Two lines are the same figure when they differ only in their numbers (576 KB → 580 KB, median 33 ms → 35 ms).
const figure = (l: string) => finding(l).replace(/\d+(\.\d+)?/g, '#');

// Multiset difference by finding, then pairs of leftover lines with the same figure shape become changed figures.
export function diffReport(base: string, pr: string): Diff {
  const lines = (t: string) => t.split('\n').map(l => l.trimEnd()).filter(l => l.trim() && !NOISE.some(r => r.test(l.trim())));
  const left = lines(base), added: string[] = [];
  const pool = new Map<string, number>();
  for (const l of left) if (!OWN.some(r => r.test(l))) pool.set(finding(l), (pool.get(finding(l)) ?? 0) + 1);
  for (const l of lines(pr)) {
    const k = finding(l), n = pool.get(k) ?? 0;
    if (!OWN.some(r => r.test(l)) && n > 0) pool.set(k, n - 1); else added.push(l);
  }
  const fixed: string[] = [];
  for (const l of left) { if (OWN.some(r => r.test(l))) continue; const k = finding(l), n = pool.get(k) ?? 0; if (n > 0) { fixed.push(l); pool.set(k, n - 1); } }
  const changed: [string, string][] = [];
  for (let i = added.length - 1; i >= 0; i--) {
    const j = fixed.findIndex(f => figure(f) === figure(added[i]));
    if (j < 0 || OWN.some(r => r.test(added[i]))) continue;
    changed.unshift([fixed[j].trim(), added[i].trim()]);
    fixed.splice(j, 1); added.splice(i, 1);
  }
  return { added: added.map(l => l.trim()), fixed: fixed.map(l => l.trim()), changed };
}

// The operator app check (tests/ui/app.check.ts) prints "not run (<cause>)" under its heading when it could not start.
export function appNotRun(report: string): string | undefined {
  return report.match(/^- operator app[^\n]*:\n(?:\s+[^\n]*\n)*?\s+not run \((.*)\)\s*$/m)?.[1];
}

// GitHub caps a comment at 65536 characters; each block keeps well under a quarter of that.
const clip = (t: string, max = 14000) => t.length <= max ? t : t.slice(0, max).replace(/\n[^\n]*$/, '') + '\n… (full report in the design-report artifact)';
const block = (t: string, lang = '') => '```' + lang + '\n' + clip(t) + '\n```';

function section(title: string, report: string | undefined, base: string | undefined, baseLabel: string, link: string) {
  if (report === undefined) return `**${title}** · not produced (see the job log).`;
  const head = `**${title}** (warnings only, never blocks merge)${link}`;
  const full = `<details><summary>Full ${title.toLowerCase()} (${report.trimEnd().split('\n').length} lines)</summary>\n\n${block(report.trimEnd())}\n\n</details>`;
  if (base === undefined) return `${head}\n\nNo \`main\` baseline found (${baseLabel}); the full report:\n\n${block(report.trimEnd())}`;
  const d = diffReport(base, report);
  const parts = [`${head}\n\nAgainst ${baseLabel}:`];
  if (!d.added.length && !d.fixed.length && !d.changed.length) parts.push('No change against `main`.');
  if (d.added.length) parts.push(`New (${d.added.length}):\n${block(d.added.map(l => '+ ' + l).join('\n'), 'diff')}`);
  if (d.fixed.length) parts.push(`Fixed (${d.fixed.length}):\n${block(d.fixed.map(l => '- ' + l).join('\n'), 'diff')}`);
  if (d.changed.length) parts.push(`Changed figures (${d.changed.length}):\n${block(d.changed.map(([a, b]) => `- ${a}\n+ ${b}`).join('\n'), 'diff')}`);
  parts.push(full);
  return parts.join('\n\n');
}

export function comment(o: { pr: { design?: string; docs?: string }; base?: { design?: string; docs?: string }; baseLabel: string; run?: string; issues?: string; dropDrift?: boolean }): string {
  const drop = (t?: string) => t === undefined || !o.dropDrift ? t : t.split('\n').filter(l => !/ docs\.drift /.test(l)).join('\n');
  const out = ['<!-- fleet-design-report -->'];
  const cause = o.pr.design === undefined ? undefined : appNotRun(o.pr.design);
  if (cause !== undefined) out.push(`**Operator app check did not run:** ${cause}. Its routes, console errors and latency are missing from this report.`);
  out.push(section('Design report', o.pr.design, o.base?.design, o.baseLabel, o.run ? ` · [run and artifacts](${o.run})` : ''));
  out.push(section('Docs report', drop(o.pr.docs), drop(o.base?.docs), o.baseLabel, o.issues ? ` · [open docs-order issue](${o.issues})` : ''));
  return out.join('\n\n') + '\n';
}

function selfCheck() {
  const base = ['- packages/ui JavaScript: 576 KB minified (target ≤ 200 KB)  OVER TARGET', '- interaction latency: median 33 ms of 5',
    'app/src/views/a.tsx:10  tokens  raw colour  [#fff]', 'app/src/views/b.tsx:4  OR-8  inline SVG  [<svg]', 'Docs report (x), 1203 ms.'].join('\n');
  const pr = ['- packages/ui JavaScript: 580 KB minified (target ≤ 200 KB)  OVER TARGET', '- interaction latency: median 33 ms of 5',
    'app/src/views/a.tsx:12  tokens  raw colour  [#fff]', 'app/src/views/c.tsx:1  OR-9  select  [<select]', 'Docs report (x), 999 ms.', 'docs 1 docs.delta against abc: 1 added'].join('\n');
  const d = diffReport(base, pr);
  assert.deepEqual(d.added, ['app/src/views/c.tsx:1  OR-9  select  [<select]', 'docs 1 docs.delta against abc: 1 added']);
  assert.deepEqual(d.fixed, ['app/src/views/b.tsx:4  OR-8  inline SVG  [<svg]']);
  assert.deepEqual(d.changed, [['- packages/ui JavaScript: 576 KB minified (target ≤ 200 KB)  OVER TARGET', '- packages/ui JavaScript: 580 KB minified (target ≤ 200 KB)  OVER TARGET']]);
  assert.deepEqual(diffReport(base, base), { added: [], fixed: [], changed: [] });
  const report = '- operator app (dist/app, coordinator on PGlite):\n    not run (timed out after 300 s)\n- legacy: none';
  assert.equal(appNotRun(report), 'timed out after 300 s');
  assert.equal(appNotRun('- operator app (x):\n    /overview: ok'), undefined);
  const c = comment({ pr: { design: report }, baseLabel: 'none: no artifact' });
  assert.match(c.split('\n')[2], /^\*\*Operator app check did not run:\*\* timed out after 300 s\./);
  assert.match(c, /No `main` baseline found \(none: no artifact\)/);
  assert.match(c, /\*\*Docs report\*\* · not produced/);
  console.log('design-report-comment self-check: ok');
}

if (import.meta.main ?? process.argv[1] === import.meta.filename) {
  const argv = process.argv.slice(2);
  if (argv.includes('--self-check')) selfCheck();
  else {
    const arg = (n: string) => argv.includes(n) ? argv[argv.indexOf(n) + 1] : undefined;
    const read = (dir: string | undefined, f: string) => { const p = dir && join(dir, f); return p && existsSync(p) ? readFileSync(p, 'utf8') : undefined; };
    const prDir = arg('--pr'), baseDir = arg('--base');
    const base = baseDir && existsSync(join(baseDir, 'report.txt')) ? { design: read(baseDir, 'report.txt'), docs: read(baseDir, 'docs-report.txt') } : undefined;
    const text = comment({ pr: { design: read(prDir, 'report.txt'), docs: read(prDir, 'docs-report.txt') }, base, baseLabel: arg('--base-label') ?? 'no artifact', run: arg('--run'), issues: arg('--issues'), dropDrift: argv.includes('--drop-drift') });
    const out = arg('--out');
    if (out) writeFileSync(out, text); else process.stdout.write(text);
  }
}
