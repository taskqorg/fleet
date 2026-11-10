// Operator app check (docs/designs/fleet-operator-app.md §9): starts the Go coordinator (tests/harness.ts) with fixtures behind a local proxy
// that injects the Bearer the way the access guard does, then opens the built app (pnpm run build) in Chromium. Prints a report:
// routes render their book parts, no console errors, click-to-detail latency, change-to-screen latency. Never fails the build.
// Run: node tests/ui/app.check.ts [--shots dir]
import { mkdirSync } from 'node:fs';
import { chromium } from 'playwright';
import { checkServer } from './seed.ts';
import { layoutFindings } from './layout.check.ts';

const shots = process.argv.includes('--shots') ? process.argv[process.argv.indexOf('--shots') + 1] : 'design-report/app';
mkdirSync(shots, { recursive: true });
const out = { push: (l: string) => console.log('    ' + l) };
const { origin, seed, close } = await checkServer();

// Fixtures: a few hundred tasks across projects, states and priorities; sessions with a transcript; hosts; open questions.
let waiting = '', running = '';
seed.bulk(() => {
  for (let i = 0; i < 240; i++) {
    const p = ['dashq', 'game', 'fleet'][i % 3];
    const id = seed.task({ title: ['Render prop batch', 'Fix CI cache key', 'Rework hero copy', 'Write release notes', 'Compare 3D methods'][i % 5] + ' ' + i, projectId: p, priority: ['urgent', 'normal', 'background'][i % 3], expectedOutcome: 'A reviewed result linked from the changelog.', ...(i % 7 === 0 ? { state: 'in_progress' } : i === 3 ? { state: 'waiting', waitReason: 'acceptance' } : {}) });
    if (i % 7 === 0) { seed.execution({ id: 'run-' + i, kind: 'agent_run', taskId: id, projectId: p, hostId: ['pds18', 'mac', 'windows'][i % 3], harness: ['codex', 'claude', 'antigravity'][i % 3], state: 'running' }); if (!running) running = id; }
    if (i === 3) { waiting = id; seed.question({ kind: 'acceptance', taskId: id, text: 'Accept this result?', options: ['accepted', 'rejected'] }); }
  }
  for (const [id, platform, ago] of [['pds18', 'linux', 2000], ['mac', 'darwin', 120000], ['windows', 'win32', 5000]] as const) seed.put('host-observation', { id, platform, lastSeenAt: Date.now() - ago, capabilities: ['codex'] });
  seed.put('session', { id: 'sess-1', createdAt: Date.now() - 600000, agentConfig: { id: 'baseline', harness: 'codex' } });
  seed.execution({ id: 'sx-1', kind: 'agent_run', sessionId: 'sess-1', hostId: 'pds18', harness: 'codex', state: 'succeeded' });
  seed.put('message', { id: 'msg-1', sessionId: 'sess-1', executionId: 'sx-1', text: 'Rework the hero copy; keep it short.' });
  for (let i = 0; i < 12; i++) seed.put('event', { id: 'ev-' + i, sessionId: 'sess-1', executionId: 'sx-1', type: 'item.completed', data: { fleetNormalized: { blocks: [i % 2 ? { kind: 'tool', name: 'npm test', input: null, output: 'ok', state: 'completed' } : { kind: 'text', role: 'agent', markdown: 'Step ' + i + ' done; checking the next file.' }] } } });
});

const b = await chromium.launch();
try {
  const page = await b.newPage({ viewport: { width: 1440, height: 900 } });
  const errors: string[] = [];
  page.on('pageerror', e => errors.push(e.message)); page.on('console', m => { if (m.type() === 'error') errors.push(m.text()); });
  const ROUTES: [string, string[]][] = [
    ['/overview', ['app-shell', 'nav-rail', 'ops-summary', 'stat-strip', 'needs-you', 'decision-block', 'harness-counts', 'state-counters']],
    ['/tasks', ['app-shell', 'nav-rail', 'list-view', 'filter-bar', 'state-counters', 'data-list', 'state-badge']],
    [`/tasks/${waiting}`, ['list-detail', 'detail', 'outcome-block', 'decision-block', 'number']],
    [`/tasks/${running}`, ['detail', 'execution-tree']],
    ['/conversations', ['list-view', 'data-list', 'state-counters']],
    ['/conversations/sess-1', ['detail', 'transcript-viewer', 'composer']],
    ['/hosts', ['list-detail', 'harness-counts', 'state-badge']],
    ['/hosts/mac', ['detail', 'detail-section', 'fill-bar']],
    ['/lab', ['list-detail', 'state-badge']],
  ];
  page.on('requestfailed', r => { if (!r.url().includes('/api/stream')) errors.push('request failed ' + r.url()); }); // navigation aborts the stream
  page.on('response', r => { if (r.status() >= 400) errors.push(r.status() + ' ' + r.url()); });
  if (process.env.APP_DEBUG) { await page.goto(origin + '/overview'); await page.waitForTimeout(1500); console.log(errors); }
  for (const [n, [route, want]] of ROUTES.entries()) {
    await page.goto(origin + route); await page.waitForTimeout(700);
    const seen = new Set(await page.$$eval('[data-ui]', els => els.map(e => e.getAttribute('data-ui'))));
    const missing = want.filter(w => !seen.has(w));
    const hscroll = await page.evaluate(() => document.documentElement.scrollWidth > innerWidth + 1);
    out.push(`${route.replace(/[0-9a-f-]{36}/, ':id')}: ${missing.length ? 'MISSING ' + missing.join(', ') : 'ok'}${hscroll ? ', horizontal scroll' : ''}`);
    await page.screenshot({ path: `${shots}/${n + 1}-${route.split('/')[1]}${route.split('/')[2] ? '-detail' : ''}-1440.png` });
    // The owner works in the dark theme: every route is also shot in it.
    await page.emulateMedia({ colorScheme: 'dark' });
    await page.screenshot({ path: `${shots}/${n + 1}-${route.split('/')[1]}${route.split('/')[2] ? '-detail' : ''}-1440-dark.png` });
    await page.emulateMedia({ colorScheme: 'light' });
    for (const f of await layoutFindings(page)) out.push(`  ${f}`);
  }
  // Narrow width: the rail stays, the opened item takes the place of the catalogue.
  const narrow = await b.newPage({ viewport: { width: 600, height: 900 } });
  await narrow.goto(origin + `/tasks/${waiting}`); await narrow.waitForTimeout(700);
  const top = await narrow.evaluate(() => document.querySelector('[data-ui=detail]')?.getBoundingClientRect().top ?? null);
  const rail = await narrow.locator('[data-ui=nav-rail]').isVisible();
  out.push(`/tasks/:id @600px: detail top ${top === null ? 'missing' : Math.round(top) + ' px'}${top !== null && top > 780 ? '  OUTSIDE VIEWPORT' : ''}, nav rail ${rail ? 'visible' : 'HIDDEN'}`);
  await narrow.screenshot({ path: `${shots}/tasks-id-600.png` });

  // Click-to-detail latency: row click until the detail title shows.
  const times: number[] = [];
  for (let i = 0; i < 5; i++) {
    await page.goto(origin + '/tasks'); await page.waitForSelector('.vl-row .row-link');
    times.push(await page.evaluate(async i => { const l = document.querySelectorAll<HTMLElement>('.vl-row .row-link')[i]; const t0 = performance.now(); l.click(); while (!document.querySelector('[data-ui=detail] .detail-title')) await new Promise(r => requestAnimationFrame(r)); return performance.now() - t0; }, i));
  }
  times.sort((a, z) => a - z);
  out.push(`click-to-detail: median ${times[2].toFixed(0)} ms of 5 (target <= 100 ms)${times[2] > 100 ? '  OVER TARGET' : ''}`);

  // Change-to-screen: a committed task change until its row shows the new state, through the live feed.
  await page.goto(origin + '/tasks?group=none'); await page.waitForSelector('.vl-row .row-link');
  const firstId = await page.$eval('.vl-row .row-link', a => (a as HTMLAnchorElement).pathname.split('/')[2]);
  const t0 = Date.now(); await fetch(origin + '/api/ops/task_update', { method: 'POST', headers: { 'content-type': 'application/json' }, body: JSON.stringify({ taskId: firstId, action: 'pause' }) });
  let ms = -1; for (let i = 0; i < 100; i++) { if (await page.$(`.vl-row [aria-label^="Paused"]`)) { ms = Date.now() - t0; break; } await page.waitForTimeout(20); }
  out.push(`change-to-screen: ${ms < 0 ? 'NOT SEEN within 2 s' : ms + ' ms'} (target <= 1000 ms)${ms > 1000 ? '  OVER TARGET' : ''}`);
  out.push(errors.length ? `console errors: ${[...new Set(errors)].slice(0, 5).join(' | ')}` : 'console errors: none');
} finally { await b.close(); await close(); }
