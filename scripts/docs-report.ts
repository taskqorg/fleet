// Docs report (docs/designs/fleet-docs-governance.md, "Report"): prints warnings, never fails the build.
// Node standard library and git only. Writes design-report/docs-report.txt in the check-design.mjs line format.
// Usage: node scripts/docs-report.ts [--base <ref>] | --self-check
import { execFileSync } from 'node:child_process';
import { existsSync, mkdirSync, readFileSync, statSync, writeFileSync } from 'node:fs';
import { join, normalize, posix } from 'node:path';
import { slug } from './lab-drift.ts';

const root = join(import.meta.dirname, '..');
const git = (...a: string[]) => execFileSync('git', a, { cwd: root, encoding: 'utf8', maxBuffer: 1 << 28, stdio: ['ignore', 'pipe', 'ignore'] });

export type Kind = 'contract' | 'index' | 'paper' | 'evidence' | 'asset' | 'spec' | null;

// Kind rules in order; the first match decides. `linked` answers rule 5 (a design-system file a spec or book/ links).
export function kind(p: string, linked: (p: string) => boolean = () => false): Kind {
  if (p === 'fleet.md') return 'contract';
  if (p === 'AGENTS.md' || (p.startsWith('docs/') && posix.basename(p) === 'README.md')) return 'index';
  const inDocs = p.startsWith('docs/'), inEvidence = p.startsWith('evidence/');
  if (!inDocs && !inEvidence) return null;
  if (!inEvidence && (p.endsWith('-brief.md') || /\d{4}-\d{2}-\d{2}/.test(p) || /(^|\/)(reviews|handoffs|archive|explorations|prototypes)\//.test(p))) return 'paper';
  if (inEvidence) return 'evidence';
  const md = p.endsWith('.md');
  if (!md && (p === 'docs/design-system/book/index.html' || p === 'docs/design-system/pages/mockups.html' || (p.startsWith('docs/design-system/') && linked(p)))) return 'asset';
  return md ? 'spec' : 'paper';
}

// A changed line is a link fix when it differs from its old line only inside ](...) targets or <a id> anchors.
const stripLinks = (l: string) => l.replace(/\]\([^)]*\)/g, ']()').replace(/<a id="[^"]*"><\/a>/g, '<a id="">');
export function linkFixOnly(removed: string[], added: string[]): boolean {
  return removed.length === added.length && removed.every((l, i) => stripLinks(l) === stripLinks(added[i]));
}

// GitHub heading anchors of a Markdown text: slugs (duplicates get -1, -2...) plus explicit <a id>.
export function anchors(text: string): Set<string> {
  const out = new Set<string>(), seen = new Map<string, number>();
  let fenced = false;
  for (const l of text.split('\n')) {
    if (/^\s*(```|~~~)/.test(l)) { fenced = !fenced; continue; }
    if (fenced) continue;
    for (const m of l.matchAll(/<a (?:id|name)="([^"]+)"/g)) out.add(m[1]);
    const h = /^#{1,6} (.*)$/.exec(l);
    if (!h) continue;
    const s = slug(h[1].replace(/`/g, '').replace(/\[([^\]]*)\]\([^)]*\)/g, '$1')), n = seen.get(s) ?? 0;
    seen.set(s, n + 1); out.add(n ? `${s}-${n}` : s);
  }
  return out;
}

// Relative link targets with line numbers: Markdown links, src and href. Code fences and inline code are skipped.
export function links(text: string): { line: number; target: string }[] {
  const out: { line: number; target: string }[] = [];
  let fenced = false;
  text.split('\n').forEach((raw, i) => {
    if (/^\s*(```|~~~)/.test(raw)) { fenced = !fenced; return; }
    if (fenced) return;
    const l = raw.replace(/`[^`]*`/g, '');
    for (const m of l.matchAll(/\]\(\s*<?([^)\s>]+)>?(?:\s+"[^"]*")?\s*\)|(?:src|href)=["']([^"']+)["']/g)) {
      const t = m[1] ?? m[2];
      if (!/^[a-z][a-z0-9+.-]*:|^\/\//i.test(t)) out.push({ line: i + 1, target: t });
    }
  });
  return out;
}

const CITE = /(?<![A-Za-z0-9_.-])(?<![A-Za-z0-9_-]\/)docs\/[A-Za-z0-9_./-]+\.(?:md|html)(?:#[A-Za-z0-9_-]+)?/g;
const BINARY = /\.(png|jpe?g|gif|webp|ico|pdf|woff2?|ttf|otf|zip|gz|tgz|sqlite|wasm)$/i;
const LOCK = /(^|\/)(pnpm-lock\.yaml|package-lock\.json|yarn\.lock|Cargo\.lock)$/;

function selfCheck() {
  const ok = (c: boolean, m: string) => { if (!c) { console.error('self-check failed:', m); process.exit(1); } };
  ok(kind('fleet.md') === 'contract' && kind('docs/designs/README.md') === 'index', 'contract and index');
  ok(kind('docs/designs/lab-brief.md') === 'paper' && kind('docs/handoffs/x.md') === 'paper' && kind('docs/design-system/reviews/2026-11-10/a.png') === 'paper', 'paper by name');
  ok(kind('evidence/2026-11-09/x.json') === 'evidence' && kind('docs/lab.md') === 'spec' && kind('src/x.ts') === null, 'evidence, spec, outside');
  ok(kind('docs/design-system/x.html', () => true) === 'asset' && kind('docs/design-system/x.html') === 'paper' && kind('docs/design-system/book/index.html') === 'asset', 'asset');
  ok(linkFixOnly(['see [a](old.md#x)'], ['see [a](new.md#y)']) && !linkFixOnly(['see [a](a.md)'], ['saw [a](a.md)']) && !linkFixOnly(['a'], []), 'link fix');
  const a = anchors('# A b\n## A b\n```\n# no\n```\n<a id="x"></a>\n### `C` [d](e.md)');
  ok(a.has('a-b') && a.has('a-b-1') && a.has('x') && a.has('c-d') && !a.has('no'), 'anchors');
  ok(links('[a](b.md#c) `[x](y.md)` [w](https://x) <img src="i.png">').map(l => l.target).join() === 'b.md#c,i.png', 'links');
  ok([...'x docs/a.md#b, ../docs/c.html and mydocs/d.md, ui/docs/e.md'.matchAll(CITE)].map(m => m[0]).join() === 'docs/a.md#b,docs/c.html', 'cite');
  console.log('docs-report self-check passed');
}

function report(baseArg?: string) {
  const t0 = Date.now();
  const files = git('ls-files', '-z').split('\0').filter(Boolean).filter(p => existsSync(join(root, p)));
  const tracked = new Set(files);
  const text = new Map<string, string>();
  const read = (p: string) => { if (!text.has(p)) text.set(p, readFileSync(join(root, p), 'utf8')); return text.get(p)!; };
  const resolve = (from: string, target: string) => normalize(posix.join(posix.dirname(from), decodeURI(target))).replace(/\\/g, '/').replace(/\/$/, '');
  const exists = (p: string) => tracked.has(p) || (existsSync(join(root, p)) && statSync(join(root, p)).isDirectory());
  const out: string[] = [];
  const add = (p: string, line: number, id: string, msg: string) => out.push(`${p}:${line} ${id} ${msg}`);

  // Link graph over docs, fleet.md and AGENTS.md (Markdown and HTML).
  const docText = files.filter(p => (p.startsWith('docs/') || p === 'fleet.md' || p === 'AGENTS.md') && /\.(md|html)$/.test(p));
  const linkedFrom = new Map<string, Set<string>>();
  for (const p of [...docText, ...files.filter(p => p.startsWith('evidence/') && p.endsWith('.md'))]) for (const { target } of links(read(p))) {
    const t = resolve(p, target.split('#')[0] || posix.basename(p));
    if (!linkedFrom.has(t)) linkedFrom.set(t, new Set());
    linkedFrom.get(t)!.add(p);
  }
  const assetLinked = (p: string) => [...(linkedFrom.get(p) ?? [])].some(f => f.startsWith('docs/design-system/book/') || kind(f) === 'spec');
  const kinds = new Map(files.map(p => [p, kind(p, assetLinked)]));
  const specs = files.filter(p => kinds.get(p) === 'spec');

  // docs.unlisted: listed means linked from docs/README.md or a sub-index reached from it through README links.
  const indexes = new Set(['docs/README.md']), queue = ['docs/README.md'], listed = new Set<string>();
  while (queue.length) {
    const ix = queue.shift()!;
    for (const { target } of links(read(ix))) {
      const t = resolve(ix, target.split('#')[0]);
      listed.add(t);
      if (posix.basename(t) === 'README.md' && t.startsWith('docs/') && tracked.has(t) && !indexes.has(t)) { indexes.add(t); queue.push(t); }
    }
  }
  for (const p of specs) if (!listed.has(p)) add(p, 1, 'docs.unlisted', 'specification not linked from docs/README.md or a sub-index it links');

  // docs.link
  for (const p of docText.filter(p => p.endsWith('.md'))) for (const { line, target } of links(read(p))) {
    const [path, hash] = target.split('#');
    const t = path ? resolve(p, path) : p;
    if (!exists(t)) { add(p, line, 'docs.link', `${target} does not resolve`); continue; }
    if (hash && t.endsWith('.md') && tracked.has(t) && !anchors(read(t)).has(decodeURIComponent(hash))) add(p, line, 'docs.link', `${target}: no heading #${hash}`);
  }

  // docs.cite, collecting cited paths per file for docs.touched.
  // This script is skipped: its self-check cites example paths on purpose.
  const citeFiles = files.filter(p => p !== 'scripts/docs-report.ts' && !p.startsWith('docs/') && !p.startsWith('evidence/') && !p.includes('node_modules/') && !LOCK.test(p) && !BINARY.test(p));
  const citesOf = new Map<string, Set<string>>();
  for (const p of citeFiles) {
    let s: string; try { s = read(p); } catch { continue; }
    if (s.includes('\0')) continue;
    const cited = new Set<string>();
    let repo = 'fleet';
    s.split('\n').forEach((l, i) => {
      // A JSON source path of another repository (config/lab/use-cases) is that repository's path, not Fleet's.
      repo = /"repo":\s*"([^"]+)"/.exec(l)?.[1] ?? repo;
      if (repo !== 'fleet') return;
      for (const [m] of l.matchAll(CITE)) {
        const [path, hash] = m.split('#');
        cited.add(path);
        if (!tracked.has(path)) add(p, i + 1, 'docs.cite', `${path} does not exist`);
        else if (hash && path.endsWith('.md') && !anchors(read(path)).has(hash)) add(p, i + 1, 'docs.cite', `${m}: no heading #${hash}`);
      }
    });
    citesOf.set(p, cited);
  }

  // docs.paper and docs.evidence
  for (const p of files) if (kinds.get(p) === 'paper') add(p, 1, 'docs.paper', 'working paper in the tree; its substance belongs in a pull request, task or .egm/');
  const evidence = files.filter(p => kinds.get(p) === 'evidence');
  if (evidence.length) {
    const corpus = files.filter(p => !BINARY.test(p) && !LOCK.test(p)).map(p => { try { return read(p); } catch { return ''; } }).join('\n');
    for (const p of evidence) {
      const dirs = p.split('/').slice(1, -1).map((_, i, a) => 'evidence/' + a.slice(0, i + 1).join('/'));
      if (![...(linkedFrom.get(p) ?? [])].some(f => f !== p) && ![p, ...dirs].some(c => corpus.includes(c))) add(p, 1, 'docs.evidence', 'evidence record no tracked file cites');
    }
  }

  // Base: --base, the merge base with the pull request target, or HEAD^ on a push.
  let base = baseArg;
  if (!base) {
    const target = process.env.GITHUB_BASE_REF;
    try { base = target ? git('merge-base', 'HEAD', `origin/${target}`).trim() : git('rev-parse', 'HEAD^').trim(); } catch { base = undefined; }
  }
  const pr = Boolean(process.env.GITHUB_BASE_REF) || Boolean(baseArg);
  const sizeAt = (p: string) => { try { return Number(git('cat-file', '-s', `${base}:${p}`).trim()); } catch { return null; } };
  const size = (p: string) => statSync(join(root, p)).size;

  if (base) {
    const changed = git('diff', '--name-status', '--no-renames', base, '--').trim().split('\n').filter(Boolean).map(l => l.split('\t'));
    // docs.delta
    let bytes = 0, addedN = 0, removedN = 0;
    for (const [st, p] of changed.filter(([, p]) => p.startsWith('docs/') || p.startsWith('evidence/'))) {
      const before = sizeAt(p) ?? 0, after = st === 'D' ? 0 : size(p);
      bytes += after - before;
      if (st === 'A') { addedN++; add(p, 1, 'docs.delta', `added, ${after} bytes`); }
      if (st === 'D') { removedN++; add(p, 1, 'docs.delta', `removed, ${before} bytes`); }
    }
    add('docs', 1, 'docs.delta', `against ${base.slice(0, 12)}: ${addedN} added, ${removedN} removed, ${bytes >= 0 ? '+' : ''}${bytes} bytes under docs/ and evidence/`);
    if (pr) {
      // docs.touched: docs paths cited by changed non-docs files.
      for (const [st, p] of changed) if (st !== 'D' && citesOf.has(p)) for (const c of citesOf.get(p)!) add(p, 1, 'docs.touched', `cites ${c}; confirm it still matches or edit it in this pull request`);
      // docs.drift candidates; the comment job drops them when the description has a ## Drift heading.
      for (const [st, p] of changed) {
        if (st !== 'M' || kinds.get(p) !== 'spec') continue;
        const diff = git('diff', '-U0', base, '--', p).split('\n').filter(l => !/^(\+\+\+|---) /.test(l));
        if (!linkFixOnly(diff.filter(l => l.startsWith('-')).map(l => l.slice(1)), diff.filter(l => l.startsWith('+')).map(l => l.slice(1))))
          add(p, 1, 'docs.drift', 'specification changed beyond link fixes; add a ## Drift table to the pull request description');
      }
    }
  }

  // docs.size: largest first.
  for (const p of specs.sort((a, b) => size(b) - size(a))) {
    const was = base ? sizeAt(p) : null, now = size(p);
    add(p, 1, 'docs.size', `${now} bytes${was === null ? (base ? ', new' : '') : `, ${now - was >= 0 ? '+' : ''}${now - was}`}`);
  }

  const counts = new Map<string, number>();
  for (const l of out) { const id = l.split(' ')[1]; counts.set(id, (counts.get(id) ?? 0) + 1); }
  const md = files.filter(p => p.startsWith('docs/') && p.endsWith('.md'));
  const head = [
    `Docs report (docs/designs/fleet-docs-governance.md#report), ${Date.now() - t0} ms.`,
    `Counts: ${[...counts].map(([id, n]) => `${id}=${n}`).join(' ') || 'none'}.`,
    `docs/: ${md.length} Markdown files, ${md.reduce((s, p) => s + size(p), 0)} bytes; ${files.filter(p => p.startsWith('docs/') && !p.endsWith('.md')).length} other files.`,
  ];
  const text2 = [...head, ...out, 'This report never fails the build.'].join('\n') + '\n';
  mkdirSync(join(root, 'design-report'), { recursive: true });
  writeFileSync(join(root, 'design-report/docs-report.txt'), text2);
  process.stdout.write(text2);
}

if (import.meta.main ?? process.argv[1] === import.meta.filename) {
  const a = process.argv.slice(2);
  try { if (a.includes('--self-check')) selfCheck(); else report(a[a.indexOf('--base') + 1] && a.includes('--base') ? a[a.indexOf('--base') + 1] : undefined); }
  catch (e) { console.log(`Docs report could not run: ${(e as Error).message}\nThis report never fails the build.`); }
}
