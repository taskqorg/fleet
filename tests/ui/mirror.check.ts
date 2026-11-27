// Read mirror check (operator-app D3; app/src/persist.ts): a reload renders the page from IndexedDB before any read
// answers, and a different person's copy never shows.
// Every API answer is held back by RTT ms (default 400, as through Cloudflare Access) so the order is visible.
// Prints a report; exits 1 when a claim fails. Run: pnpm run app:bundle && node tests/ui/mirror.check.ts
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
  // The mirror writes a read after a second of quiet; the Tasks list settles (its spend, membership) a little later, so
  // wait until the list is in the mirror rather than a fixed time (a fixed 1.5 s made the next claim flaky).
  const mirroredList = () => page.evaluate(() => new Promise<boolean>(res => {
    const r = indexedDB.open('fleet-data');
    r.onsuccess = () => { const t = r.result.transaction('reads').objectStore('reads').getAllKeys(); t.onsuccess = () => res((t.result as IDBValidKey[]).some(k => String(k).includes('\u0000list:'))); t.onerror = () => res(false); };
    r.onerror = () => res(false);
  }));
  for (let waited = 0; waited < 10_000 && !(await mirroredList()); waited += 250) await page.waitForTimeout(250);
  await page.waitForTimeout(1200); // and the views read with it

  // Reload: rows from the mirror before the first API answer could arrive.
  let answered = 0; const count = (r: { url(): string }) => { if (new URL(r.url()).pathname.startsWith('/api/ops/')) answered++; };
  page.on('requestfinished', count);
  t = Date.now();
  await page.reload();
  await row.waitFor({ timeout: 30000 });
  const warm = Date.now() - t, before = answered;
  claim(before === 0, `reload shows the rows before any read answers (${before} answered, ${warm} ms; cold ${cold} ms, RTT ${RTT} ms)`);
  page.off('requestfinished', count);

  // A screen mounted before the person check (Lab waits for the person's role) shows from the mirror too: the check
  // clears memory at every load, and what stays mounted takes the mirror's copy again (store.ts clearMemory).
  {
    const lab = page.getByText('Use cases', { exact: true }).first();
    await page.goto(site + '/lab'); await lab.waitFor({ timeout: 30000 });
    for (let waited = 0; waited < 6000; waited += 250) await page.waitForTimeout(250);
    let labAnswered = 0; const countLab = (r: { url(): string }) => { if (new URL(r.url()).pathname.startsWith('/api/ops/')) labAnswered++; };
    page.on('requestfinished', countLab);
    const t0 = Date.now(); await page.reload(); await lab.waitFor({ timeout: 30000 });
    page.off('requestfinished', countLab);
    claim(labAnswered === 0, `a screen that waits for the person check shows from the mirror (Lab: ${labAnswered} answered, ${Date.now() - t0} ms)`);
    await page.goto(site + '/projects/tasks'); await row.waitFor({ timeout: 30000 });
  }
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

  // A hidden tab reads nothing while changes arrive; when it shows again it reads what changed, once.
  let hiddenReads = 0, shownReads = 0, shown = false;
  const hiddenOps: string[] = [];
  // Writes are not reads: a hiding tab saves its list view (person_settings_set, keepalive) by design; me is the
  // person check, which may still be retrying from the step above.
  const tally = (r: { url(): string }) => { const p = new URL(r.url()).pathname; if (!p.startsWith('/api/ops/') || p.endsWith('_set') || p.endsWith('/me')) return; if (shown) shownReads++; else { hiddenReads++; hiddenOps.push(p.slice(9)); } };
  page.on('request', tally);
  await page.evaluate(() => { Object.defineProperty(document, 'visibilityState', { value: 'hidden', configurable: true }); document.dispatchEvent(new Event('visibilitychange')); });
  await page.waitForTimeout(300);
  seed.task({ title: 'Mirror task hidden', projectId: 'cs', expectedOutcome: 'A result.' });
  await page.waitForTimeout(3000);
  shown = true;
  await page.evaluate(() => { Object.defineProperty(document, 'visibilityState', { value: 'visible', configurable: true }); document.dispatchEvent(new Event('visibilitychange')); });
  await page.getByText('Mirror task hidden', { exact: true }).first().waitFor({ timeout: 15000 }).catch(() => {});
  await page.waitForTimeout(1500);
  page.off('request', tally);
  const arrived = await page.getByText('Mirror task hidden', { exact: true }).first().isVisible().catch(() => false);
  claim(hiddenReads === 0 && shownReads > 0 && arrived, `a hidden tab reads nothing on changes (${hiddenReads} hidden${hiddenOps.length ? ' ' + hiddenOps.join(',') : ''}, ${shownReads} on return, new row ${arrived ? 'shown' : 'missing'})`);

  // The app update (nav-rail.md, Update): a visible-but-unfocused tab never reloads while away; coming back when the last action
  // was 30 s or more ago reloads at once. A clean hidden tab reloads silently in the background after 15-30 s jitter.
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
    Object.defineProperty(document, 'visibilityState', { value: 'visible', configurable: true });
    document.hasFocus = () => false;
    document.dispatchEvent(new Event('visibilitychange')); dispatchEvent(new Event('blur')); dispatchEvent(new Event('focus'));
  });
  await page.waitForTimeout(36_000);
  const stayed = await page.evaluate(() => (window as unknown as { kept?: number }).kept === 1);
  claim(stayed, 'a ready update does not reload a visible-but-unfocused tab (36 s)');
  const reloadedOnReturn = page.waitForEvent('load', { timeout: 10_000 }).then(() => true, () => false);
  await page.evaluate(() => {
    Object.defineProperty(document, 'visibilityState', { value: 'visible', configurable: true });
    document.hasFocus = () => true;
    document.dispatchEvent(new Event('visibilitychange')); dispatchEvent(new Event('focus'));
  });
  const came = await reloadedOnReturn;
  claim(came, 'coming back after 30 s without an action reloads into the new app at once');
  await row.waitFor({ timeout: 30000 });
  await page.getByText(/Updating in/).first().waitFor({ timeout: 15000 });
  const hiddenReloaded = page.waitForEvent('load', { timeout: 36_000 }).then(() => true, () => false);
  await page.evaluate(() => {
    Object.defineProperty(document, 'visibilityState', { value: 'hidden', configurable: true });
    document.hasFocus = () => false;
    document.dispatchEvent(new Event('visibilitychange')); dispatchEvent(new Event('blur'));
  });
  const cameHidden = await hiddenReloaded;
  await page.unroute('**/home');
  claim(cameHidden, 'a clean hidden tab silently reloads in the background (15-30 s jitter)');
  claim(errors.length === 0, 'no page errors' + (errors.length ? ': ' + errors.join('; ') : ''));
} finally {
  await b.close(); slow.close(); await close();
}
if (failures.length) { console.log(`mirror check: ${failures.length} failed`); process.exit(1); }
console.log('mirror check: ok');
