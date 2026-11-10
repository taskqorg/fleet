// Filter-path check (docs/designs/fleet-view-urls-and-list-path.md §4). For each size: a fresh Go coordinator (tests/harness.ts) with
// that many tasks, operation latency over HTTP, then in Chromium every list change (state counter, grouping,
// "All", preset, focus, row check, search, Back) watched each animation frame. Prints a report. With --assert it exits 1
// when a rule fails at a size up to 20,000 tasks; larger sizes are report-only (growth factor).
// Run: pnpm run build && node tests/ui/filter.check.ts [--assert] [--sizes 3000,20000,100000]
import { chromium, type Page } from 'playwright';
import { checkServer } from './seed.ts';

const argv = process.argv.slice(2);
const ASSERT = argv.includes('--assert');
const sizes = (argv.includes('--sizes') ? argv[argv.indexOf('--sizes') + 1] : argv.find(a => /^\d+$/.test(a)) ?? '3000,20000').split(',').map(Number);
const ASSERTED_UP_TO = 20000, BUDGET_MS = 300;
const failures: string[] = [];
const rule = (size: number, ok: boolean, what: string) => { if (!ok && size <= ASSERTED_UP_TO) failures.push(`${size}: ${what}`); if (!ok) console.log(`  FAIL ${what}`); };
// The 300 ms budgets hold on the operator's machine (§4). A shared CI runner's timing is noise, so on CI (env CI) they are
// written to the job summary and never fail the job; the structural rules stay asserted everywhere.
const timings: string[] = [];
const budget = (size: number, ms: number, what: string) => {
  if (!process.env.CI) return rule(size, size < ASSERTED_UP_TO || ms < BUDGET_MS, `${what} ${ms} ms (budget ${BUDGET_MS} ms)`);
  const over = size === ASSERTED_UP_TO && ms >= BUDGET_MS; if (over) console.log(`  OVER BUDGET (reported only on CI) ${what} ${ms} ms`);
  timings.push(`| ${size} | ${what} | ${ms} | ${size === ASSERTED_UP_TO ? (over ? 'over' : 'ok') : 'report only'} |`);
};
const med = (a: number[]) => [...a].sort((x, y) => x - y)[a.length >> 1];
const time = async (f: () => Promise<unknown>, n = 7) => { const a: number[] = []; for (let i = 0; i < n; i++) { const s = performance.now(); await f(); a.push(performance.now() - s); } return med(a); };
const CASES: [string, any][] = [['all, group priority', { filter: { state: [] }, group: 'priority' }], ['state=in_progress', { filter: { state: ['in_progress'] }, group: 'priority' }],
  ['group agent', { filter: { state: [] }, group: 'agent' }], ['search "hero"', { filter: { state: [], search: 'hero' }, group: 'priority' }]];
const httpMedians: Record<number, Record<string, number>> = {};

async function run(N: number, browser: Awaited<ReturnType<typeof chromium.launch>>) {
  console.log(`== ${N} tasks`);
  const { origin, seed, close } = await checkServer();
  let t = performance.now();
  seed.bulk(() => {
    for (let i = 0; i < N; i++) {
      const p = ['dashq', 'game', 'fleet'][i % 3];
      const id = seed.task({ title: ['Render prop batch', 'Fix CI cache key', 'Rework hero copy', 'Write release notes', 'Compare 3D methods'][i % 5] + ' ' + i, projectId: p, priority: ['urgent', 'normal', 'background'][i % 3], ...(i % 5 === 0 ? { state: 'in_progress' } : {}) });
      if (i % 5 === 0) seed.execution({ id: 'run-' + i, kind: 'agent_run', taskId: id, projectId: p, hostId: ['pds18', 'mac', 'windows'][i % 3], harness: ['codex', 'claude', 'antigravity'][i % 3], state: 'running' });
    }
  });
  console.log(`fixture load: ${((performance.now() - t) / 1000).toFixed(1)} s`);
  httpMedians[N] = {};
  for (const [name, b] of CASES) {
    let bytes = 0;
    const ms = await time(async () => { const r = await fetch(origin + '/api/ops/task_page', { method: 'POST', headers: { 'content-type': 'application/json', origin }, body: JSON.stringify({ ...b, limit: 100 }) }); bytes = (await r.text()).length; });
    httpMedians[N][name] = ms;
    console.log(`HTTP task_page ${name}: ${ms.toFixed(1)} ms, ${(bytes / 1024).toFixed(0)} KB`);
    budget(N, Math.round(ms), `HTTP task_page ${name} median`);
  }

  const page = await browser.newPage({ viewport: { width: 1440, height: 900 } });
  try {
    const reqs: string[] = [];
    page.on('request', r => { if (r.url().includes('/api/ops/')) reqs.push(r.url().split('/api/ops/')[1]); });
    await page.goto(origin + '/tasks'); await page.waitForSelector('.vl-row .row-link');
    const url = () => page.evaluate(() => location.pathname + location.search);
    // Runs one action and watches every frame until the first rows stay the same for 10 frames.
    const watch = (p: Page, act: string) => p.evaluate(async act => {
      const grid0 = document.querySelector('[data-ui=data-list]'), counters0 = document.querySelectorAll('[data-ui=state-counters] .counter').length;
      const seen = { countersGone: false, listSkeleton: false, listGone: false };
      const t0 = performance.now(); let settled = -1, stable = 0, last = '';
      (0, eval)(act);
      for (let f = 0; f < 600; f++) {
        await new Promise(r => requestAnimationFrame(r));
        if (document.querySelectorAll('[data-ui=state-counters] .counter').length < counters0) seen.countersGone = true;
        if (document.querySelector('[data-ui=list-view] .skeleton')) seen.listSkeleton = true;
        if (!document.querySelector('[data-ui=data-list]')) seen.listGone = true;
        const now = [...document.querySelectorAll('.vl-row .row-link')].slice(0, 5).map(e => e.textContent).join('|') + (document.querySelector('[data-ui=list-view].pending') ? '…' : '');
        // Settled: no request pending and the first rows unchanged for 10 frames. Unsettled after 600 frames reports -1.
        if (now && now === last && !now.endsWith('…')) { if (++stable === 10) { settled = performance.now() - t0 - 10 * 16.7; break; } } else { stable = 0; last = now; }
      }
      return { ...seen, remounted: grid0 !== document.querySelector('[data-ui=data-list]'), settledMs: settled < 0 ? -1 : Math.max(0, Math.round(settled)) };
    }, act);
    const step = async (name: string, act: string, settle = true) => {
      reqs.length = 0; const r = await watch(page, act); await page.waitForTimeout(200);
      console.log(`${name}: ${JSON.stringify(r)} requests=${reqs.length} [${reqs.join(',')}]`);
      rule(N, !r.remounted && !r.listGone, `${name}: list element replaced`);
      rule(N, !r.listSkeleton, `${name}: skeleton after first load`);
      rule(N, !r.countersGone, `${name}: counters vanished`);
      if (settle) { rule(N, r.settledMs >= 0, `${name}: never settled`); if (r.settledMs >= 0) budget(N, r.settledMs, `${name}: settled in`); }
      return r;
    };
    await step('click state counter', `document.querySelector('[data-ui=state-counters] .counter:not(.sigma)').click()`);
    await step('click group Agent', `[...document.querySelectorAll('.seg')].find(b=>b.textContent==='Agent').click()`);
    await step('click All', `document.querySelector('.counter.sigma').click()`);
    await step('pick preset', `[...document.querySelectorAll('[data-ui=filter-bar].presets button')].find(b=>/Running/.test(b.textContent)).click()`);
    const beforeFocus = await url();
    await step('focus first row', `document.querySelector('.vl-row .row-link').click()`, false);
    const focused = await url();
    rule(N, new URL(origin + focused).searchParams.get('sel') === new URL(origin + beforeFocus).searchParams.get('sel'), 'focus changed sel');
    await step('check a row', `document.querySelectorAll('.vl-row .row-check')[1].click()`, false);
    const checked = await url();
    rule(N, checked.split('?')[0] === focused.split('?')[0], 'checking a row changed focus');
    rule(N, /[?&]sel=/.test(checked), 'checked row not in the URL');
    // A copied link reproduces focus, sel, filters and grouping in a second page.
    const other = await browser.newPage({ viewport: { width: 1440, height: 900 } });
    await other.goto(origin + checked); await other.waitForSelector('[data-ui=detail] .detail-title');
    const there = await other.evaluate(() => ({ url: location.pathname + location.search, checked: document.querySelectorAll('.vl-row .row-check:checked').length, focusedRow: !!document.querySelector('.vl-row.sel') }));
    await other.close();
    rule(N, there.url === checked && there.checked === 1 && there.focusedRow, `copied link did not reproduce the view: ${JSON.stringify(there)} for ${checked}`);
    // Back: rows are on screen in the first frame after popstate.
    const back = await page.evaluate(async () => { history.back(); await new Promise(r => addEventListener('popstate', r, { once: true })); await new Promise(r => requestAnimationFrame(r)); return document.querySelectorAll('.vl-row .row-link').length; });
    rule(N, back > 0, 'Back: no rows in the first frame');
    // Search: one request per typing pause, one Back leaves the search.
    await page.goto(origin + '/tasks'); await page.waitForSelector('.vl-row .row-link');
    reqs.length = 0; await page.locator('.fbar input').first().pressSequentially('hero', { delay: 60 }); await page.waitForTimeout(800);
    const pages = reqs.filter(r => r === 'task_page').length;
    console.log(`type "hero": requests=${reqs.length} [${reqs.join(',')}] url=${await url()}`);
    rule(N, pages === 1, `typing "hero" sent ${pages} task_page requests`);
    await page.goBack(); await page.waitForTimeout(300);
    rule(N, (await url()) === '/tasks', `one Back after a search should return to /tasks, got ${await url()}`);
    // After Back, a new search starts a new history entry instead of replacing the one Back returned to.
    await page.locator('.fbar input').first().fill(''); await page.locator('.fbar input').first().pressSequentially('copy', { delay: 60 }); await page.waitForTimeout(800);
    await page.goBack(); await page.waitForTimeout(300);
    rule(N, (await url()) === '/tasks', `a search after Back replaced the entry Back returned to (Back now at ${await url()})`);
  } finally { await page.close(); await close(); }
}

const browser = await chromium.launch();
try { for (const n of sizes) await run(n, browser); } finally { await browser.close(); }
const ns = Object.keys(httpMedians).map(Number).sort((a, b) => a - b);
if (ns.length > 1) for (const name of CASES.map(c => c[0])) {
  const lo = ns.find(n => n === 20000) ?? ns[0], hi = ns[ns.length - 1];
  if (lo !== hi) console.log(`growth ${lo} -> ${hi}, ${name}: x${(httpMedians[hi][name] / httpMedians[lo][name]).toFixed(2)} (reported, not asserted)`);
}
if (timings.length && process.env.GITHUB_STEP_SUMMARY) {
  const { appendFileSync } = await import('node:fs');
  appendFileSync(process.env.GITHUB_STEP_SUMMARY, `### Filter check timings (reported, not asserted on CI; budget ${BUDGET_MS} ms at ${ASSERTED_UP_TO} tasks)\n\n| tasks | case | ms | budget |\n|---|---|---|---|\n${timings.join('\n')}\n`);
}
if (failures.length) { console.log(`\n${failures.length} rule(s) failed:\n` + failures.map(f => '  ' + f).join('\n')); if (ASSERT) process.exit(1); }
else console.log('\nall rules passed');
