// Design report (D6): prints warnings, never fails. Reviewers and the owner read it.
// Run pnpm run book first so the scroll check uses the current book.
// Usage: pnpm run check:design [files or folders...]   (default: packages/ui)
import { readFileSync, readdirSync, statSync, existsSync } from 'node:fs';
import { join, relative } from 'node:path';
import { execFileSync } from 'node:child_process';
import { mkdirSync, appendFileSync, writeFileSync } from 'node:fs';
import { build } from 'esbuild';
import { contrastProblems } from './tokens.mjs';

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
// OR-32: the Spotlight palette (data-ui="spotlight", packages/ui/src/controls.tsx) is the one allowed floating working tool; a second one warns.
let palettes = 0;
for (const f of files.filter(f => /\.tsx$/.test(f))) {
  const t = readFileSync(f, 'utf8');
  for (const m of t.matchAll(/role="dialog"|popover=/g)) {
    const line = t.split('\n')[t.slice(0, m.index).split('\n').length - 1];
    if (/data-ui="spotlight"/.test(line) && /packages\/ui\/src\/controls\.tsx$/.test(f) && ++palettes === 1) continue;
    warn.push(`${relative(root, f)}  OR-14  floating layer found; it may hold only information (OR-32: Spotlight is the only exception)`);
  }
}
// Icons must take the theme colour (currentColor), never a fixed fill or stroke (D12).
for (const [name, svg] of Object.entries(JSON.parse(readFileSync(join(root, 'packages/ui/icons.json'), 'utf8')))) {
  const tag = svg.match(/<svg[^>]*>/)?.[0] ?? '';
  if (!/\sfill=/.test(tag) || /(fill|stroke)="#/.test(svg)) warn.push(`packages/ui/icons.json  icon "${name}"  fixed or default colour; use fill/stroke currentColor`);
}
for (const p of contrastProblems()) warn.push(`tokens.json  contrast  ${p} (needs 4.5:1)`);

// Performance target: initial JavaScript of the UI package (D6: ≤ 200 KB, a target, not a stop).
const ui = await build({ entryPoints: [join(root, 'packages/ui/src/index.ts')], bundle: true, minify: true, write: false, format: 'esm', jsx: 'automatic', jsxImportSource: 'preact', loader: { '.json': 'json' } });
const kb = ui.outputFiles[0].contents.length / 1024;

let tests = 'passed';
try { execFileSync(process.execPath, ['--test', 'tests/ui/format.test.ts'], { cwd: root, stdio: 'pipe' }); } catch { tests = 'FAILED (run node --test tests/ui/format.test.ts)'; }

let scroll;
try { scroll = execFileSync('node', ['tests/ui/scroll.check.mjs'], { cwd: root, stdio: 'pipe' }).toString().trim(); } catch (e) { scroll = `not run (${String(e.message).split('\n')[0]})`; }
let ux;
try { ux = execFileSync('node', ['tests/ui/ux.check.mjs'], { cwd: root, stdio: 'pipe' }).toString().trim(); } catch (e) { ux = `- UX checks not run (${String(e.message).split('\n')[0]})`; }
let appCheck;
try { appCheck = existsSync(join(root, 'dist/app/index.html')) ? execFileSync(process.execPath, ['tests/ui/app.check.ts'], { cwd: root, stdio: 'pipe', timeout: 300000 }).toString().trimEnd() : '    not run (run npm run build first)'; } catch (e) { appCheck = `    not run (${String(e.message).split('\n')[0]})`; }
let breaks;
try { breaks = execFileSync('node', ['tests/ui/layout-break.check.mjs'], { cwd: root, stdio: 'pipe' }).toString().trim(); } catch (e) { breaks = (e.stdout?.toString().trim() || `not run (${String(e.message).split('\n')[0]})`); }
let trace;
try { trace = execFileSync('node', ['tests/ui/trace.check.mjs'], { cwd: root, stdio: 'pipe' }).toString().trim().replace(/\n/g, '\n    '); } catch (e) { trace = `not run (${String(e.message).split('\n')[0]})`; }
console.log(`Design report for ${targets.join(', ')}: ${files.length} files`);
console.log(`- packages/ui JavaScript: ${kb.toFixed(0)} KB minified (target ≤ 200 KB)${kb > 200 ? '  OVER TARGET' : ''}`);
console.log(`- formatter tests: ${tests}`);
console.log(`- scroll anchor on insert (book list): ${scroll}`);
console.log(`- page specs against mockups:\n    ${trace}`);
console.log(ux);
console.log(`- layout checks against injected breaches (OR-24 to OR-27, OR-41, OR-42): ${breaks}`);
console.log(`- operator app (dist/app, coordinator on PGlite):\n${appCheck}`);
console.log(warn.length ? `- ${warn.length} source warnings:\n  ${warn.join('\n  ')}` : '- source checks: no warnings');
console.log('Rules: docs/design-system/owner-rules.md. This report never fails the build.');
