/// <reference lib="dom" />
// Page budget (docs/designs/fleet-data-layer.md, Proof): what a screen costs on the deployed site, as a person meets it.
// For each page: the API requests, bytes and settle time of a cold open, of a reload (the read mirror renders before
// any answer), of an Alt-Tab (hidden 8 s, then back) and of Back from the next page. A tool, not a gate: data-layer pull
// requests quote its numbers. It opens https://fleet.ufobe.com as site-agents with the Access service token sourced
// from ~/.config/ufobe-agents/site-access.env inside the process (AGENTS.md); it never prints the token.
//
//   ( . ~/.config/ufobe-agents/site-access.env; export CF_ACCESS_CLIENT_ID CF_ACCESS_CLIENT_SECRET;
//     node scripts/page-budget.ts /conversations /live )
//
// PAGE_BUDGET_OPS=1 also lists every read with its time and size.
import { chromium, type Page, type Request } from 'playwright';
import { ORIGIN, guardOutput, prodContext, token } from './prod-browser.ts';

guardOutput();
const paths = process.argv.slice(2);
if (!paths.length) { console.error('usage: node scripts/page-budget.ts <path>...'); process.exit(64); }
const cred = token();
if (!cred) { console.error('source ~/.config/ufobe-agents/site-access.env first'); process.exit(64); }

const browser = await chromium.launch();
const ctx = await prodContext(browser, cred, { viewport: { width: 1440, height: 900 }, colorScheme: 'dark' });
const page = await ctx.newPage();

type Read = { op: string; ms?: number; bytes?: number };
let reads: Read[] = [], inflight = 0, last = Date.now();
const isOp = (r: Request) => new URL(r.url()).pathname.startsWith('/api/ops/');
const open = new Map<Request, Read>();
page.on('request', r => { if (!isOp(r)) return; const x = { op: new URL(r.url()).pathname.slice(9) }; open.set(r, x); reads.push(x); inflight++; last = Date.now(); });
const done = async (r: Request) => {
  const x = open.get(r); if (!x) return; open.delete(r); inflight--; last = Date.now();
  x.ms = Math.round(r.timing().responseEnd);
  try { x.bytes = (await r.sizes()).responseBodySize; } catch { /* aborted */ }
};
page.on('requestfinished', done); page.on('requestfailed', done);

/** Until no read has been in flight for 1.5 s (at most 60 s); the time of the last read's end. */
async function settle(t0: number) {
  await page.waitForTimeout(150);
  while (Date.now() - t0 < 60_000 && (inflight > 0 || Date.now() - last < 1500)) await page.waitForTimeout(100);
  return Math.max(0, last - t0);
}
function line(label: string, path: string, settled: number) {
  const n = reads.length, kb = Math.round(reads.reduce((a, r) => a + (r.bytes ?? 0), 0) / 1024);
  const slow = [...reads].sort((a, b) => (b.ms ?? 0) - (a.ms ?? 0)).slice(0, 3).map(r => `${r.op} ${r.ms ?? '?'}ms`).join(', ');
  console.log(`${path.padEnd(28)} ${label.padEnd(8)} ${String(n).padStart(3)} reads ${String(kb).padStart(6)} KB  settled ${settled} ms${slow ? '  slowest: ' + slow : ''}`);
  if (process.env.PAGE_BUDGET_OPS) console.log('  ' + reads.map(r => `${r.op} ${r.ms ?? '?'}ms ${Math.round((r.bytes ?? 0) / 1024)}KB`).join(', '));
  reads = [];
}
async function hideAndReturn(p: Page) {
  await p.evaluate(() => { Object.defineProperty(document, 'visibilityState', { value: 'hidden', configurable: true }); document.dispatchEvent(new Event('visibilitychange')); dispatchEvent(new Event('blur')); });
  await p.waitForTimeout(8000);
  await p.evaluate(() => { Object.defineProperty(document, 'visibilityState', { value: 'visible', configurable: true }); document.dispatchEvent(new Event('visibilitychange')); dispatchEvent(new Event('focus')); });
}

for (const [i, path] of paths.entries()) {
  reads = []; let t = Date.now();
  await page.goto(ORIGIN + path); line('open', path, await settle(t));
  await page.waitForTimeout(2000); reads = [];
  t = Date.now(); await page.reload(); line('reload', path, await settle(t));
  await page.waitForTimeout(2000); reads = [];
  t = Date.now(); await hideAndReturn(page); await settle(t); line('alt-tab', path, 0);
  const next = paths[i + 1] ?? '/home';
  await page.evaluate(p => { history.pushState(null, '', p); dispatchEvent(new PopStateEvent('popstate')); }, next);
  await settle(Date.now()); reads = [];
  t = Date.now(); await page.evaluate(() => history.back()); line('back', path, await settle(t));
}
await browser.close();
