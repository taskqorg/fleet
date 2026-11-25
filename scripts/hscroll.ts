/// <reference lib="dom" />
// Horizontal overflow walk (docs/design-system/owner-rules.md, OR-66): Fleet has no horizontal scroll, because a
// trackpad's sideways swipe over a scroller turns into browser Back or Forward. This walks the deployed site, dark, at
// 1440, 1100, 768 and 390 px, over every section route and the first detail each list links to, and lists each element
// that overflows sideways: the page itself, a box that scrolls sideways (overflow-x auto/scroll with wider content) and
// content that sticks out past the viewport. A tool, not a gate. It opens https://fleet.ufobe.com as site-agents with the
// Access service token sourced from ~/.config/ufobe-agents/site-access.env inside the process (AGENTS.md); it never
// prints the token.
//
//   ( . ~/.config/ufobe-agents/site-access.env; export CF_ACCESS_CLIENT_ID CF_ACCESS_CLIENT_SECRET;
//     node scripts/hscroll.ts [path...] )
//
// No paths: every section route. HSCROLL_WIDTHS=390,768 picks widths; HSCROLL_DETAILS=0 skips the detail links.
// Prints one line per finding: width, path, kind, element, overflow in px; exits 0 either way.
import { chromium, type Page } from 'playwright';
import { hscrollScan as scan, type Finding } from '../tests/ui/hscroll.ts';
import { ORIGIN, guardOutput, prodContext, redact, token } from './prod-browser.ts';

guardOutput();
const ROUTES = ['/home', '/live', '/conversations', '/projects', '/projects/FLT', '/projects/FLT/rules', '/projects/FLT/access', '/projects/tasks', '/projects/results', '/projects/delivery',
  '/lab', '/lab/mine', '/library/methods', '/library/agents', '/library/pipelines', '/library/changes', '/library/bots',
  '/library/skills', '/system', '/system/hosts', '/system/runners', '/system/access', '/system/connections', '/system/spend',
  '/system/people', '/system/accounts'];
const paths = process.argv.slice(2).length ? process.argv.slice(2) : ROUTES;
const widths = (process.env.HSCROLL_WIDTHS ?? '1440,1100,768,390').split(',').map(Number);
const details = process.env.HSCROLL_DETAILS !== '0';
const cred = token();
if (!cred) { console.error('source ~/.config/ufobe-agents/site-access.env first'); process.exit(64); }

/** Opens a path and lets it settle; false when it did not load. */
async function open(p: Page, path: string) {
  try { await p.goto(ORIGIN + path, { waitUntil: 'domcontentloaded', timeout: 20_000 }); } catch { return false; }
  await settle(p);
  return true;
}

async function settle(p: Page) {
  // Live pages keep a stream open, so network idle may never come: wait for it at most 4 s, then for layout.
  try { await p.waitForLoadState('networkidle', { timeout: 4000 }); } catch { /* streaming */ }
  await p.waitForTimeout(1200);
}

/** The first details the list opens: links one level deeper than the route in the main content, else the path a click
 *  on the first list row opens. */
async function detailLinks(p: Page, path: string): Promise<string[]> {
  const hrefs = await p.evaluate(path => {
    const main = document.querySelector('main') ?? document.body;
    const hs = [...main.querySelectorAll('a[href]')].map(a => new URL((a as HTMLAnchorElement).href).pathname)
      .filter(h => h.startsWith(path.replace(/\/$/, '') + '/') && h !== path);
    return [...new Set(hs)].slice(0, 2);
  }, path);
  if (hrefs.length) return hrefs;
  const row = await p.$('main :is(.vl-row .row-link, .t-row .t-label, .kv li .row-link, [data-ui=data-list] [role=row] button)');
  if (!row) return [];
  const before = p.url();
  try { await row.click({ timeout: 3000 }); await p.waitForTimeout(800); } catch { return []; }
  const after = new URL(p.url()).pathname;
  return p.url() !== before && after !== path ? [after + new URL(p.url()).search] : [];
}

const browser = await chromium.launch();
const ctx = await prodContext(browser, cred, { colorScheme: 'dark' });
let total = 0;
const lines: string[] = [];
const report = (w: number, path: string, fs: Finding[]) => {
  for (const f of fs) lines.push(`${String(w).padStart(4)}  ${path.padEnd(40)} ${f.kind.padEnd(10)} ${String(f.px).padStart(5)}px  ${f.el}${f.cause ? '  <- ' + f.cause : ''}`);
  total += fs.length;
};
// One page per width, walked at the same time.
await Promise.all(widths.map(async w => {
  const page = await ctx.newPage();
  await page.setViewportSize({ width: w, height: 900 });
  for (const path of paths) {
    if (!await open(page, path)) { lines.push(`${String(w).padStart(4)}  ${path.padEnd(40)} not opened`); continue; }
    try {
      report(w, path, await page.evaluate(scan));
      if (!details) continue;
      for (const d of await detailLinks(page, path)) {
        if (await open(page, d)) report(w, d, await page.evaluate(scan));
      }
    } catch (e) { lines.push(`${String(w).padStart(4)}  ${path.padEnd(40)} stopped: ${redact(e)}`); }
  }
  await page.close();
}));
console.log(lines.sort((a, b) => a.slice(6).localeCompare(b.slice(6)) || a.localeCompare(b)).join('\n'));
console.log(`${total} horizontal overflow finding(s)`);
await browser.close();
