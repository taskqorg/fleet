// Read mirror check (operator-app D3; app/src/persist.ts): a reload renders the page from IndexedDB before any read
// answers, and a different person's copy never shows.
// Every API answer is held back by RTT ms (default 400, as through Cloudflare Access) so the order is visible.
// Prints a report; exits 1 when a claim fails. Run: pnpm run app:bundle && node tests/ui/mirror.check.ts
import assert from 'node:assert/strict';
import http from 'node:http';
import type { AddressInfo } from 'node:net';
import { chromium } from 'playwright';
import { checkServer } from './seed.ts';

const RTT = Number(process.env.RTT ?? 400);
const { origin, seed, close } = await checkServer();
seed.bulk(() => {
  for (let i = 0; i < 40; i++) seed.task({ title: 'Mirror task ' + i, projectId: ['cs', 'fleet'][i % 2], expectedOutcome: 'A result.' });
});
// A slow lane in front of the check proxy: each /api answer waits RTT ms; /api/stream is passed through at once.
const up = new URL(origin);
const slow = http.createServer((req, res) => {
  const go = () => {
    const r = http.request({ host: up.hostname, port: up.port, path: req.url, method: req.method, headers: req.headers }, a => { res.writeHead(a.statusCode!, a.headers); a.pipe(res); });
    r.on('error', () => { res.writeHead(502); res.end(); }); req.pipe(r);
  };
  if (req.url?.startsWith('/api/') && !req.url.startsWith('/api/stream')) setTimeout(go, RTT); else go();
});
await new Promise<void>(r => slow.listen(0, '127.0.0.1', r));
const site = `http://127.0.0.1:${(slow.address() as AddressInfo).port}`;

const b = await chromium.launch();
const failures: string[] = [];
const claim = (ok: boolean, text: string) => { console.log((ok ? '  ok   ' : '  FAIL ') + text); if (!ok) failures.push(text); };
try {
  const ctx = await b.newContext({ viewport: { width: 1440, height: 900 } });
  const page = await ctx.newPage();
  const errors: string[] = []; page.on('pageerror', e => errors.push(e.message));
  const row = page.getByText('Mirror task 12', { exact: true }).first();

  let t = Date.now();
  await page.goto(site + '/projects/tasks');
  await row.waitFor({ timeout: 30000 }).catch(async e => { await page.screenshot({ path: process.env.SHOT ?? 'design-report/mirror-cold.png' }); console.log(errors, (await page.textContent('body'))?.slice(0, 600)); throw e; });
  const cold = Date.now() - t;
  await page.waitForTimeout(1500); // the mirror writes after a second of quiet

  // Reload: rows from the mirror before the first API answer could arrive.
  let answered = 0; const count = (r: { url(): string }) => { if (new URL(r.url()).pathname.startsWith('/api/ops/')) answered++; };
  page.on('requestfinished', count);
  t = Date.now();
  await page.reload();
  await row.waitFor({ timeout: 30000 });
  const warm = Date.now() - t, before = answered;
  claim(before === 0, `reload shows the rows before any read answers (${before} answered, ${warm} ms; cold ${cold} ms, RTT ${RTT} ms)`);
  page.off('requestfinished', count);
  // After the reload the page reads each thing once (prod read me x4, person_settings_get x3, most reads x2).
  const sent = new Map<string, number>();
  const note = (r: { url(): string; postData(): string | null }) => { const u = new URL(r.url()); if (u.pathname.startsWith('/api/ops/')) { const k = u.pathname.slice(9) + ' ' + (r.postData() ?? ''); sent.set(k, (sent.get(k) ?? 0) + 1); } };
  page.on('request', note);
  await page.reload(); await row.waitFor({ timeout: 30000 }); await page.waitForTimeout(3000);
  page.off('request', note);
  // me is asked twice by design: the gate's check of the person (a control call) and the shell's read of it. The leaving
  // page saves its list view (pagehide), and that setting-change may reach the new page, which reads the settings again.
  const twice = [...sent].filter(([k, n]) => n > 1 && !k.startsWith('me ') && !k.startsWith('person_settings_get ')).map(([k, n]) => `${k.slice(0, 60)} x${n}`);
  claim(twice.length === 0, `a reload reads each thing once (${[...sent.values()].reduce((a, b) => a + b, 0)} reads${twice.length ? '; twice: ' + twice.join(', ') : ''})`);

  // Another person in the same browser: the previous person's copy never shows.
  await page.evaluate(() => localStorage.setItem('fleet-data-owner', 'another-owner'));
  await page.reload();
  const leaked = await page.getByText('Mirror task 12', { exact: true }).first().isVisible().catch(() => false);
  claim(!leaked, 'a mirror kept for another person is not shown');
  await row.waitFor({ timeout: 30000 });

  // A focus while the coordinator restarts (me answers 503) keeps the page: nothing is cleared or read again.
  await page.waitForTimeout(5500);
  let reads = 0; const any = (r: { url(): string }) => { if (new URL(r.url()).pathname.startsWith('/api/ops/') && !r.url().endsWith('/me')) reads++; };
  await page.route('**/api/ops/me', r => r.fulfill({ status: 503, body: '{"error":"restarting"}', contentType: 'application/json' }));
  page.on('request', any);
  await page.evaluate(() => { dispatchEvent(new Event('blur')); dispatchEvent(new Event('focus')); });
  await page.waitForTimeout(2500);
  const kept = await row.isVisible();
  page.off('request', any); await page.unroute('**/api/ops/me');
  claim(kept && reads === 0, `a failed person check keeps the page (${reads} reads, rows ${kept ? 'shown' : 'gone'})`);

  // The app update (nav-rail.md, Update): a tab the person is away from never reloads; coming back when the last action
  // was 30 s or more ago reloads at once. The site is made to serve another app by answering the update check's page read.
  await page.route('**/home', async r => {
    try {
      if (r.request().resourceType() !== 'fetch') return await r.continue();
      const html = await (await r.fetch()).text();
      await r.fulfill({ body: html.replace(/(\/assets\/main-)([^".]+)/, '$1next'), contentType: 'text/html' });
    } catch { /* the page went away (the reload under test) */ }
  });
  await page.reload(); await row.waitFor({ timeout: 30000 });
  await page.getByText(/Updating in/).first().waitFor({ timeout: 15000 });
  await page.evaluate(() => {
    (window as unknown as { kept: number }).kept = 1;
    Object.defineProperty(document, 'visibilityState', { value: 'hidden', configurable: true });
    document.hasFocus = () => false;
    document.dispatchEvent(new Event('visibilitychange')); dispatchEvent(new Event('blur')); dispatchEvent(new Event('focus'));
  });
  await page.waitForTimeout(36_000);
  const stayed = await page.evaluate(() => (window as unknown as { kept?: number }).kept === 1);
  claim(stayed, 'a ready update does not reload a tab the person is away from (36 s)');
  const reloaded = page.waitForEvent('load', { timeout: 10_000 }).then(() => true, () => false);
  await page.evaluate(() => {
    Object.defineProperty(document, 'visibilityState', { value: 'visible', configurable: true });
    document.hasFocus = () => true;
    document.dispatchEvent(new Event('visibilitychange')); dispatchEvent(new Event('focus'));
  });
  const came = await reloaded;
  await page.unroute('**/home');
  claim(came, 'coming back after 30 s without an action reloads into the new app at once');
  claim(errors.length === 0, 'no page errors' + (errors.length ? ': ' + errors.join('; ') : ''));
} finally {
  await b.close(); slow.close(); await close();
}
if (failures.length) { console.log(`mirror check: ${failures.length} failed`); process.exit(1); }
console.log('mirror check: ok');
