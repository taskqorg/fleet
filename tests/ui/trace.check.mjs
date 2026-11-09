// Traceability check (D10): every control and pattern a page spec links must render in that page's mockup,
// and every page must render app-shell and nav-rail, and every list row must start with its state (OR-22, D12). Reads data-ui markers from the live DOM, so minified
// names do not matter. Prints a report; never fails the build. Run after npm run book.
import { chromium } from 'playwright';
import { readFileSync, readdirSync } from 'node:fs';

const dir = new URL('../../docs/design-system/pages/', import.meta.url);
const mockups = new URL('mockups.html', dir).href;
const specs = readdirSync(dir).filter(f => f.endsWith('.md') && f !== 'README.md');
const b = await chromium.launch();
const lines = [];
let missingTotal = 0;
try {
  for (const f of specs) {
    const md = readFileSync(new URL(f, dir), 'utf8');
    const routes = [...(md.match(/\*\*Mockup routes:\*\*([^\n]*)/)?.[1] ?? '').matchAll(/`#(\/[^`]*)`/g)].map(m => m[1]);
    const want = new Set(['app-shell', 'nav-rail', ...[...md.matchAll(/\]\(\.\.\/(?:controls|patterns)\/([a-z-]+)\.md/g)].map(m => m[1])]);
    if (!routes.length) { lines.push(`${f}: no "Mockup routes" line`); missingTotal++; continue; }
    const seen = new Set();
    const badLists = new Set();
    for (const r of routes) {
      const p = await b.newPage({ viewport: { width: 1440, height: 900 } });
      await p.goto(`${mockups}#${r}`); await p.waitForTimeout(150);
      const grab = async () => {
        (await p.$$eval('[data-ui]', els => els.map(e => e.getAttribute('data-ui')))).forEach(x => seen.add(x));
        // OR-22: in every list the first column is the state badge. Group headers and log entries are not item rows.
        (await p.$$eval('.vl-row:not(.grp):not(.dyn)', rows => rows.filter(r => { const c = r.firstElementChild; return !c || !(c.matches('[data-ui=state-badge]') || c.querySelector('[data-ui=state-badge]')); })
          .map(r => (r.closest('[role=grid]')?.getAttribute('aria-label') || r.closest('.panel')?.querySelector('h2')?.textContent || 'unnamed list')))).forEach(l => badLists.add(l));
      };
      await grab();
      for (const tab of await p.$$('[role=tab]')) { await tab.click(); await p.waitForTimeout(50); await grab(); }
      await p.close();
    }
    // The owner views pages inline at narrow widths: the rail must stay visible there too (D10).
    const n = await b.newPage({ viewport: { width: 600, height: 800 } });
    await n.goto(`${mockups}#${routes[0]}`); await n.waitForTimeout(150);
    const narrowRail = await n.locator('[data-ui=nav-rail]').isVisible();
    await n.close();
    const missing = [...want].filter(x => !seen.has(x));
    if (!narrowRail) missing.push('nav-rail at 600 px');
    for (const l of badLists) missing.push(`state not first column in list "${l}"`);
    missingTotal += missing.length;
    lines.push(`${f} (${routes.join(', ')}): ${missing.length ? `MISSING ${missing.join(', ')}` : `ok, ${want.size} linked items rendered`}`);
  }
} finally { await b.close(); }
console.log(lines.join('\n'));
console.log(missingTotal ? `${missingTotal} gaps between page specs and mockups` : 'all page specs match their mockups');
