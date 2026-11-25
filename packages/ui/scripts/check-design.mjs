// Design report (D6): prints warnings for reviewers and the owner. It fails on one thing only: a raw HTML control in an
// app view beyond the legacy baseline below (screens are built from packages/ui).
// pnpm run check:design builds the book first (it is not committed); run pnpm run book before calling this file directly.
// Usage: pnpm run check:design [files or folders...]   (default: packages/ui)
import { readFileSync, readdirSync, statSync, existsSync } from 'node:fs';
import { join, relative } from 'node:path';
import { execFileSync } from 'node:child_process';
import { mkdirSync, appendFileSync, writeFileSync } from 'node:fs';
import { build } from 'esbuild';
import { contrastProblems } from './tokens.mjs';
import { solidEsbuildPlugin } from './solid-esbuild.mjs';

const root = new URL('../../..', import.meta.url).pathname;
// Everything printed also goes to design-report/report.txt (CI uploads it).
mkdirSync(join(root, 'design-report'), { recursive: true });
const reportFile = join(root, 'design-report/report.txt');
writeFileSync(reportFile, '');
const log = console.log;
console.log = (...a) => { log(...a); appendFileSync(reportFile, a.join(' ') + '\n'); };
const targets = process.argv.slice(2).length ? process.argv.slice(2) : ['packages/ui', 'app'];
const files = [];
const walk = p => { const s = statSync(p); if (s.isDirectory()) { if (!/node_modules|dist/.test(p)) readdirSync(p).forEach(f => walk(join(p, f))); } else if (/\.(tsx?|css|html)$/.test(p)) files.push(p); };
targets.forEach(t => walk(join(root, t)));

// Each rule: id, what it looks for, where it applies, the advice.
const RULES = [
  { id: 'tokens', re: /#[0-9a-fA-F]{3,8}\b/g, where: f => !/tokens\.json$/.test(f) && /\.(tsx?|css)$/.test(f), say: 'raw colour; use a colour token (tokens.md)' },
  { id: 'tokens', re: /style=\{\{[^}]*\b\d+px/g, where: f => /\.tsx$/.test(f), say: 'raw px in inline style; use a size token' },
  { id: 'OR-8', re: /<svg\b/g, where: f => /\.tsx?$/.test(f), say: 'inline SVG; use <Icon name> from the registry' },
  { id: 'OR-2', re: /\b(toLocaleDateString|toLocaleTimeString|Intl\.DateTimeFormat|Intl\.RelativeTimeFormat)\b|new Date\([^)]*\)\.(getHours|getDate|getMonth)/g, where: f => /\.tsx?$/.test(f) && !/src\/format\.ts$/.test(f), say: 'date formatting outside packages/ui/src/format.ts' },
  { id: 'OR-25', re: /100d?vh/g, where: () => true, skip: line => /^\.shell\{/.test(line), say: 'viewport height outside the shell; only .shell takes it (patterns/app-shell.md)' },
  { id: 'OR-7', re: /[↑↓▲▼⬆⬇]/g, where: f => /\.(tsx|html)$/.test(f), say: 'direction arrow; show change with the value tint' },
  { id: 'OR-9', re: /<select\b/g, where: f => /\.tsx$/.test(f), say: 'select; long collections use VirtualList or a searchable scope control' },
  { id: 'OR-38', re: /\b\w*[iI]d\)?\.slice\(0, ?\d+\)|>T-\{/g, where: f => /\.tsx$/.test(f), say: 'short UUID or hand-made code shown; render <EntityCode> (D17)' },
  { id: 'OR-36', re: /label="Filter"/g, where: f => /\.tsx$/.test(f), say: 'Filter button; filters are typed in search (query syntax)' },
  { id: 'OR-35', re: /seg-label">Group</g, where: f => /\.tsx$/.test(f), say: '"Group" as a word; use GroupControl (registry icon with tooltip)' },
  { id: 'OR-15', re: /Back to /g, where: f => /\.(tsx|html)$/.test(f), say: 'text back link; use an icon, or rely on browser Back' },
  { id: 'OR-45', re: /badge t-attention.*override/g, where: f => /\.tsx$/.test(f), say: 'override attention badge; show only differing values without override badge or t-attention tone' },
  { id: 'OR-46', re: /<nav[^>]*class="[^"]*\bsub-nav\b/g, where: f => /\.(tsx|html)$/.test(f), say: 'horizontal <nav class="sub-nav"> above columns; use Control Column tree/list and one ShellHeader' },
  // OR-48: Control Column catalogues use ListPagePanel or CatalogueTree; hosts.tsx and runners.tsx use vl-wrap only in Work Area panels.
  { id: 'OR-48', re: /class="vl-wrap"/g, where: f => /app\/src\/views\/[^/]+\.tsx$/.test(f) && !/app\/src\/views\/(hosts|runners)\.tsx$/.test(f), say: 'raw vl-wrap catalogue in view; use ListPagePanel or CatalogueTree in Control Column' },
  { id: 'OR-48', re: /<header[^>]*class="page-h"[^>]*>[\s\S]{0,300}label="New /g, where: f => /app\/src\/views\/[^/]+\.tsx$/.test(f), say: 'section "New …" action inside <header class="page-h">; move it to <ShellActions>' },
  { id: 'OR-49', re: /role="tablist"/g, where: f => /\.tsx$/.test(f) && !/controls\/tables\.tsx$/.test(f), say: 'raw role="tablist"; use TabBar from packages/ui' },
  { id: 'OR-64', re: /label=\{?[`'"](?:Show (?:more|less)|Load more|Older|Newer)\b|label=\{`\+\$\{[^`]*\} more`\}/g, where: f => /\.tsx$/.test(f), say: 'toggle pagination; lists load on scroll (InfiniteList, ListEnd) and card previews open the full list from their title' },
];
const warn = [];
for (const f of files) {
  const text = readFileSync(f, 'utf8');
  for (const r of RULES) if (r.where(f)) for (const m of text.matchAll(r.re)) {
    const line = text.slice(0, m.index).split('\n').length;
    if (r.skip?.(text.split('\n')[line - 1])) continue;
    warn.push(`${relative(root, f)}:${line}  ${r.id}  ${r.say}  [${m[0].slice(0, 30)}]`);
  }
}
// OR-14: interactive elements inside floating layers (markup heuristic on tsx). Sticky or fixed panels are caught on the rendered page (tests/ui/layout.check.ts).
// OR-32: the Spotlight palette (data-ui="spotlight", packages/ui/src/controls/navigation.tsx) is the one allowed floating working tool; a second one warns.
// OR-44: the entity card (data-ui="entity-card", packages/ui/src/controls/overlays.tsx) is an informational dialog: facts, Open and copy actions only.
let palettes = 0, cards = 0;
for (const f of files.filter(f => /\.tsx$/.test(f))) {
  const t = readFileSync(f, 'utf8');
  for (const m of t.matchAll(/role="dialog"|popover=/g)) {
    const line = t.split('\n')[t.slice(0, m.index).split('\n').length - 1];
    if (/data-ui="spotlight"/.test(line) && /packages\/ui\/src\/controls(\/.*)?\.tsx$/.test(f) && ++palettes === 1) continue;
    if (/data-ui="entity-card"/.test(line) && /packages\/ui\/src\/controls(\/.*)?\.tsx$/.test(f) && ++cards === 1) continue;
    warn.push(`${relative(root, f)}  OR-14  floating layer found; it may hold only information (OR-32: Spotlight is the only exception)`);
  }
}
// Raw controls in app views (build screens only from packages/ui): each file may hold at most its legacy count, which only
// shrinks; one more fails the report.
const RAW = /<(button|input|select|textarea|table|dialog|details|form|progress|meter)\b/g;
const RAW_LEGACY = {};
const failures = [];
const viewsDir = join(root, 'app/src/views');
for (const f of existsSync(viewsDir) ? readdirSync(viewsDir).filter(f => f.endsWith('.tsx')) : []) {
  const counts = {};
  for (const m of readFileSync(join(viewsDir, f), 'utf8').matchAll(RAW)) counts[m[1]] = (counts[m[1]] ?? 0) + 1;
  for (const [el, n] of Object.entries(counts)) if (n > (RAW_LEGACY[f]?.[el] ?? 0)) failures.push(`app/src/views/${f}  raw <${el}> ×${n} (legacy ${RAW_LEGACY[f]?.[el] ?? 0}); use a packages/ui control`);
}
// Icons must take the theme colour (currentColor), never a fixed fill or stroke (D12).
for (const [name, svg] of Object.entries(JSON.parse(readFileSync(join(root, 'packages/ui/icons.json'), 'utf8')))) {
  const tag = svg.match(/<svg[^>]*>/)?.[0] ?? '';
  if (!/\sfill=/.test(tag) || /(fill|stroke)="#/.test(svg)) warn.push(`packages/ui/icons.json  icon "${name}"  fixed or default colour; use fill/stroke currentColor`);
}
for (const p of contrastProblems()) warn.push(`tokens.json  contrast  ${p} (needs 4.5:1)`);

// Performance target: initial JavaScript of the UI package (D6: ≤ 200 KB, a target, not a stop).
const ui = await build({ entryPoints: [join(root, 'packages/ui/src/index.ts')], bundle: true, minify: true, write: false, format: 'esm', conditions: ['solid'], plugins: [solidEsbuildPlugin()], loader: { '.json': 'json' } });
const kb = ui.outputFiles[0].contents.length / 1024;

let tests = 'passed';
try { execFileSync(process.execPath, ['--test', 'tests/ui/format.test.ts'], { cwd: root, stdio: 'pipe' }); } catch { tests = 'FAILED (run node --test tests/ui/format.test.ts)'; }

let scroll;
try { scroll = execFileSync('node', ['tests/ui/scroll.check.mjs'], { cwd: root, stdio: 'pipe' }).toString().trim(); } catch (e) { scroll = `not run (${String(e.message).split('\n')[0]})`; }
let ux;
try { ux = execFileSync('node', ['tests/ui/ux.check.mjs'], { cwd: root, stdio: 'pipe' }).toString().trim(); } catch (e) { ux = `- UX checks not run (${String(e.message).split('\n')[0]})`; }
// A failed child's cause is its last stderr line (an exception message), not "Command failed: …"; the PR comment quotes it.
const cause = e => {
  if (e.signal === 'SIGTERM') return 'timed out after 300 s';
  // Node prints an uncaught error as a source frame, a caret line, then the message; log lines come before it.
  const lines = (e.stderr?.toString() ?? '').split('\n').map(l => l.trim());
  const caret = lines.findIndex(l => /^\^+$/.test(l));
  const message = lines.slice(caret + 1).find(l => l && !/^at |^node:internal|^\d{4}\/\d\d\/\d\d /.test(l));
  return (message ?? String(e.message).split('\n')[0]).slice(0, 300);
};
let appCheck;
try { appCheck = existsSync(join(root, 'dist/app/index.html')) ? execFileSync(process.execPath, ['tests/ui/app.check.ts'], { cwd: root, stdio: 'pipe', timeout: 300000 }).toString().trimEnd() : '    not run (run npm run build first)'; } catch (e) { appCheck = `${e.stdout?.toString().trimEnd() ? e.stdout.toString().trimEnd() + '\n' : ''}    not run (${cause(e)})`; }
let breaks;
try { breaks = execFileSync('node', ['tests/ui/layout-break.check.mjs'], { cwd: root, stdio: 'pipe' }).toString().trim(); } catch (e) { breaks = (e.stdout?.toString().trim() || `not run (${String(e.message).split('\n')[0]})`); }
let trace;
try { trace = execFileSync('node', ['tests/ui/trace.check.mjs'], { cwd: root, stdio: 'pipe' }).toString().trim().replace(/\n/g, '\n    '); } catch (e) { trace = `not run (${String(e.message).split('\n')[0]})`; }
// Legacy web platform patterns (modern-web-guidance legacy-scan, pinned in package.json; AGENTS.md, Web UI work). The scan
// covers the guides with deterministic rules only; each finding still needs its keepWhen/dropWhen check before a change.
let legacy;
try {
  const bin = join(root, 'node_modules/.bin/modern-web-guidance');
  const scans = ['app/src', 'packages/ui/src'].map(dir => ({ dir, r: JSON.parse(execFileSync(bin, ['legacy-scan', dir, '--json'], { cwd: root, stdio: 'pipe', timeout: 120000 }).toString()) }));
  const lines = scans.flatMap(({ dir, r }) => (r.guides ?? []).flatMap(g => (g.rules ?? []).map(x => `${dir}: ${x.ruleId} ×${x.count} (${(x.findings ?? []).slice(0, 2).map(f => `${f.file}:${f.line}`).join(', ')}${x.count > 2 ? ', …' : ''})`)));
  legacy = lines.length ? `${lines.length} rules with findings (check each against the guide's keepWhen/dropWhen):\n    ${lines.join('\n    ')}` : 'none';
} catch (e) { legacy = `not run (${String(e.message).split('\n')[0]})`; }
console.log(`Design report for ${targets.join(', ')}: ${files.length} files`);
console.log(`- packages/ui JavaScript: ${kb.toFixed(0)} KB minified (target ≤ 200 KB)${kb > 200 ? '  OVER TARGET' : ''}`);
console.log(`- formatter tests: ${tests}`);
console.log(`- scroll anchor on insert (book list): ${scroll}`);
console.log(`- page specs against mockups:\n    ${trace}`);
console.log(ux);
console.log(`- layout checks against injected breaches (OR-24 to OR-27, OR-41 to OR-43): ${breaks}`);
console.log(`- operator app (dist/app, coordinator on PGlite):\n${appCheck}`);
console.log(`- legacy web patterns (modern-web-guidance legacy-scan): ${legacy}`);
console.log(warn.length ? `- ${warn.length} source warnings:\n  ${warn.join('\n  ')}` : '- source checks: no warnings');
if (failures.length) { console.log(`- FAILED: ${failures.length} raw controls in app views:\n  ${failures.join('\n  ')}`); process.exitCode = 1; }
console.log('Rules: docs/design-system/owner-rules.md. The report fails only on raw controls in app views.');
