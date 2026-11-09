// Filter-path measurement (docs/designs/web-stack-brief.md §2): coordinator on PGlite with N tasks, then
// (1) operation latency in process (store call) vs over HTTP (fastify + JSON), (2) in Chromium: a state-filter click,
// a group change, typing in search and a row selection, recording requests, whether counters/filter bar/list vanish,
// whether the list element is replaced (remount) and the time until the new rows show. Prints a report; never fails.
// Run: npm run build && npx tsx tests/ui/filter.check.ts [tasks=3000]
import http from 'node:http';
import { chromium } from 'playwright';
import { createStore } from '../../packages/coordinator/store.js';
import { createApp } from '../../packages/coordinator/app.js';

const N = Number(process.argv[2] ?? 3000);
const store = createStore('memory://'); await store.migrate();
let t = performance.now();
await store.tx(async () => {
  for (let i = 0; i < N; i++) {
    const id = await store.tasks.create({ title: ['Render prop batch', 'Fix CI cache key', 'Rework hero copy', 'Write release notes', 'Compare 3D methods'][i % 5] + ' ' + i, projectId: ['dashq', 'game', 'fleet'][i % 3], priority: ['urgent', 'normal', 'background'][i % 3] });
    if (i % 5 === 0) { await store.executions.createExecution({ id: 'run-' + i, kind: 'agent_run', taskId: id, projectId: ['dashq', 'game', 'fleet'][i % 3], hostId: ['pds18', 'mac', 'windows'][i % 3], harness: ['codex', 'claude', 'antigravity'][i % 3], state: 'running' }); await store.tasks.transition(id, 'first_execution').catch(() => {}); }
  }
});
console.log(`fixtures: ${N} tasks in ${((performance.now() - t) / 1000).toFixed(1)} s`);

const med = (a: number[]) => a.sort((x, y) => x - y)[a.length >> 1];
const time = async (f: () => Promise<unknown>, n = 7) => { const a: number[] = []; for (let i = 0; i < n; i++) { const s = performance.now(); await f(); a.push(performance.now() - s); } return med(a); };
const CASES: [string, any][] = [['all, group priority', { filter: { state: [] }, group: 'priority' }], ['state=in_progress', { filter: { state: ['in_progress'] }, group: 'priority' }],
  ['group agent', { filter: { state: [] }, group: 'agent' }], ['search "hero"', { filter: { state: [], search: 'hero' }, group: 'priority' }]];
for (const [name, b] of CASES) console.log(`store.tasks.page ${name}: ${(await time(() => store.read(() => store.tasks.page({ ...b, limit: 100 })))).toFixed(1)} ms`);

let target = 0;
const proxy = http.createServer((req, res) => {
  const up = http.request({ host: '127.0.0.1', port: target, path: req.url, method: req.method, headers: { ...req.headers, authorization: 'Bearer browser-check' } }, r => { res.writeHead(r.statusCode!, r.headers); r.pipe(res); });
  up.on('error', () => { res.writeHead(502); res.end(); }); req.pipe(up);
});
await new Promise<void>(r => proxy.listen(0, '127.0.0.1', r));
const origin = `http://127.0.0.1:${(proxy.address() as any).port}`;
const app = createApp({ store, database: 'memory://', browserSecret: 'browser-check', machineSecret: 'machine-check', hostId: 'pds18', nativeHostIds: [], browserProxyAuth: true, browserOrigin: origin, appDir: 'dist/app' });
await app.listen({ port: 0, host: '127.0.0.1' }); target = (app.server.address() as any).port;
for (const [name, b] of CASES) {
  let bytes = 0;
  const ms = await time(async () => { const r = await fetch(origin + '/api/ops/task_page', { method: 'POST', headers: { 'content-type': 'application/json', origin }, body: JSON.stringify({ ...b, limit: 100 }) }); bytes = (await r.text()).length; });
  console.log(`HTTP /api/ops/task_page ${name}: ${ms.toFixed(1)} ms, ${(bytes / 1024).toFixed(0)} KB`);
}

const b = await chromium.launch();
try {
  const page = await b.newPage({ viewport: { width: 1440, height: 900 } });
  const reqs: string[] = [];
  page.on('request', r => { if (r.url().includes('/api/ops/')) reqs.push(r.url().split('/api/ops/')[1]); });
  await page.goto(origin + '/tasks'); await page.waitForSelector('.vl-row .row-link');
  // Watches the DOM each frame until rows settle: did counters, filter bar or list vanish, was the list element replaced.
  const watch = (act: string) => page.evaluate(async act => {
    const grid0 = document.querySelector('[data-ui=data-list]'), first0 = document.querySelector('.vl-row .row-link')?.textContent;
    const seen = { countersGone: false, fbarGone: false, skeleton: false, listGone: false };
    const t0 = performance.now(); let settled = -1, stable = 0, last = '';
    (0, eval)(act);
    for (let f = 0; f < 300; f++) {
      await new Promise(r => requestAnimationFrame(r));
      if (!document.querySelector('[data-ui=state-counters] .counter:not(.sigma)')) seen.countersGone = true;
      if (!document.querySelector('.fbar')) seen.fbarGone = true;
      if (document.querySelector('.skeleton')) seen.skeleton = true;
      if (!document.querySelector('[data-ui=data-list]')) seen.listGone = true;
      const now = [...document.querySelectorAll('.vl-row .row-link')].slice(0, 5).map(e => e.textContent).join('|');
      if (now && now === last) { if (++stable === 10 && settled < 0) { settled = performance.now() - t0 - 10 * 16.7; break; } } else { stable = 0; last = now; }
    }
    return { ...seen, remounted: grid0 !== document.querySelector('[data-ui=data-list]'), firstChanged: first0 !== document.querySelector('.vl-row .row-link')?.textContent, settledMs: Math.max(0, Math.round(settled)) };
  }, act);
  const run = async (name: string, act: string) => { reqs.length = 0; const r = await watch(act); await page.waitForTimeout(300); console.log(`${name}: ${JSON.stringify(r)} requests=${reqs.length} [${reqs.join(',')}]`); };
  await run('click state counter (in_progress)', `document.querySelector('[data-ui=state-counters] .counter.t-info, [data-ui=state-counters] .counter:not(.sigma)').click()`);
  await run('click group Agent', `[...document.querySelectorAll('.seg')].find(b=>b.textContent==='Agent').click()`);
  await run('click Σ all', `document.querySelector('.counter.sigma').click()`);
  await run('select first row', `document.querySelector('.vl-row .row-link').click()`);
  await run('select second row', `document.querySelectorAll('.vl-row .row-link')[1].click()`);
  await page.goto(origin + '/tasks'); await page.waitForSelector('.vl-row .row-link');
  reqs.length = 0; await page.locator('.fbar input').first().pressSequentially('hero', { delay: 60 }); await page.waitForTimeout(800);
  console.log(`type "hero" in search: requests=${reqs.length} [${reqs.join(',')}]`);
} finally { await b.close(); await app.close(); proxy.close(); }
