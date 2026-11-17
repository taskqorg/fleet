// Filter-path check (docs/designs/fleet-view-urls-and-list-path.md §4). For each size: a fresh Go coordinator (tests/harness.ts) with
// that many tasks, operation latency over HTTP, then in Chromium every list change (state counter, grouping,
// "All", preset, focus, row check, search, Back) watched each animation frame; then a search and Clear filters end the
// preset, a bulk action that fails for one row keeps only that row selected, and collapsed groups stay collapsed (and a
// default-collapsed group the person opened stays open) from the first frame after leaving the section and after a reload. Prints a report. With --assert it exits 1
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
      const p = ['cs', 'game', 'fleet'][i % 3];
      const id = seed.task({ title: ['Render prop batch', 'Fix CI cache key', 'Rework hero copy', 'Write release notes', 'Compare 3D methods'][i % 5] + ' ' + i, projectId: p, priority: ['urgent', 'normal', 'background'][i % 3], ...(i % 5 === 0 ? { state: 'in_progress' } : {}) });
      if (i % 5 === 0) seed.execution({ id: 'run-' + i, kind: 'agent_run', taskId: id, projectId: p, hostId: ['pds18', 'mac', 'windows'][i % 3], harness: ['codex', 'claude', 'antigravity'][i % 3], state: 'running' });
    }
    // Two completed tasks: the Completed group of state grouping (collapsed by default) for the remembered-groups check.
    for (let i = 0; i < 2; i++) seed.task({ title: 'Shipped notes ' + i, projectId: 'fleet', priority: 'normal', state: 'completed' });
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
    await step('open group choices', `document.querySelector('[data-ui=group-control] button').click()`, false);
    await step('click group Agent', `[...document.querySelectorAll('[data-ui=group-control] [role=menuitemradio]')].find(b=>b.textContent==='Agent').click()`);
    // The navigation returns to a section as it was left: Hosts and back to Tasks keeps the grouping (prefs.ts rememberView).
    const grouped = await url();
    await page.locator('.rail [data-id="hosts"] a').first().click(); await page.waitForURL('**/hosts**');
    await page.locator('.rail [data-id="tasks"] a').first().click(); await page.waitForURL('**/tasks**');
    rule(N, await url() === grouped, `back to Tasks lost the view: ${await url()} for ${grouped}`);
    // It is stored for the person: after a reload, Hosts then Tasks from the rail still opens the grouped list.
    await page.locator('.rail [data-id="hosts"] a').first().click(); await page.waitForURL('**/hosts**');
    const prefsRead = page.waitForResponse(r => r.url().endsWith('/api/ops/person_settings_get'));
    await page.reload(); await prefsRead; await page.waitForSelector('.rail [data-id="tasks"] a');
    await page.locator('.rail [data-id="tasks"] a').first().click(); await page.waitForURL('**/tasks**');
    rule(N, await url() === grouped, `after a reload Tasks lost the view: ${await url()} for ${grouped}`);
    // The reloaded list settles before the next step measures it (no skeleton, rows on screen, the same rows twice).
    await page.waitForSelector('[data-ui=data-list] .vl-row .row-link'); await page.waitForFunction(() => !document.querySelector('[data-ui=list-view] .skeleton, .skeleton'));
    await page.waitForTimeout(500);
    await step('click All', `document.querySelector('.counter.sigma').click()`);
    // The sort chip's leading icon flips the direction: oldest first puts sort=changed in the URL, a second click removes it.
    await step('flip sort', `document.querySelector('[data-ui=sort-control] .pick-dir').click()`);
    rule(N, new URL(origin + await url()).searchParams.get('sort') === 'changed', 'sort direction did not flip');
    // The icon draws the ages the rows show: oldest first lists big ages first, so its bars go wide to narrow.
    rule(N, await page.$eval('[data-ui=sort-control] .pick-dir svg', e => e.getAttribute('class') ?? '').then(c => c.includes('wide-narrow')), 'oldest-first icon does not show big ages first');
    await step('flip sort back', `document.querySelector('[data-ui=sort-control] .pick-dir').click()`);
    rule(N, !new URL(origin + await url()).searchParams.has('sort'), 'sort direction did not flip back');
    rule(N, await page.$eval('[data-ui=sort-control] .pick-dir svg', e => e.getAttribute('class') ?? '').then(c => c.includes('narrow-wide')), 'newest-first icon does not show small ages first');
    // A system preset sets the state filter only: the grouping and sort the person chose stay.
    const groupBefore = new URL(origin + await url()).searchParams.get('group');
    rule(N, !!groupBefore && groupBefore !== 'project', `the check needs a non-default grouping before the preset: ${groupBefore}`);
    await step('pick preset', `[...document.querySelectorAll('[data-ui=saved-views] button')].find(b=>/Running/.test(b.textContent)).click()`);
    { const u = new URL(origin + await url()).searchParams; rule(N, u.get('group') === groupBefore && !!u.get('state'), `system preset changed the grouping: ${u}`); }
    rule(N, await page.$eval('[data-ui=saved-views] .preset.on', e => /Running/.test(e.textContent ?? '')).catch(() => false), 'Running is not shown active with another grouping');
    const beforeFocus = await url();
    await step('focus first row', `document.querySelector('.vl-row .row-link').click()`, false);
    const focused = await url();
    rule(N, new URL(origin + focused).searchParams.get('sel') === new URL(origin + beforeFocus).searchParams.get('sel'), 'focus changed sel');
    // OR-37: hovering a row moves nothing in it (no hover-only affordances over or beside the row facts).
    // Facts are right-aligned and their text changes with time (now, 1m), so the left edge moves by itself: compare the
    // right edge of the same rows (a hover affordance over or beside the facts would move or cover it).
    const facts = async () => page.evaluate(() => [...document.querySelectorAll('.vl-row')].filter(r => r.querySelector('.row-facts')).slice(0, 3).map(r => `${r.getAttribute('data-key')}:${Math.round(r.querySelector('.row-facts')!.getBoundingClientRect().right)}`).join('|'));
    const factsBefore = await facts(); await page.hover('.vl-row:has(.row-facts) .tt'); await page.waitForTimeout(100);
    rule(N, factsBefore === await facts(), 'hovering a row moved its facts');
    // Selection mode: a shift click selects a row and shows the checkbox column; the checkbox is the view URL's sel.
    await step('check a row', `document.querySelectorAll('.vl-row .row-link')[1].dispatchEvent(new MouseEvent('click', { bubbles: true, cancelable: true, shiftKey: true }))`, false);
    rule(N, await page.evaluate(() => document.querySelectorAll('.vl-row .row-check:checked').length === 1 && !!document.querySelector('[data-ui=select-bar]')), 'shift click did not enter selection mode');
    const checked = await url();
    rule(N, checked.split('?')[0] === focused.split('?')[0], 'checking a row changed focus');
    rule(N, /[?&]sel=/.test(checked), 'checked row not in the URL');
    // A copied link reproduces focus, sel, filters and grouping in a second page.
    const other = await browser.newPage({ viewport: { width: 1440, height: 900 } });
    await other.goto(origin + checked); await other.waitForSelector('[data-ui=detail] .detail-title');
    // The list's page arrives after the detail at 20,000 tasks: wait for the focused row, up to 5 s, before reading the view.
    await other.waitForSelector('.vl-row.sel', { timeout: 5000 }).catch(() => {});
    const there = await other.evaluate(() => ({ url: location.pathname + location.search, checked: document.querySelectorAll('.vl-row .row-check:checked').length, focusedRow: !!document.querySelector('.vl-row.sel') }));
    await other.close();
    rule(N, there.url === checked && there.checked === 1 && there.focusedRow, `copied link did not reproduce the view: ${JSON.stringify(there)} for ${checked}`);
    // Back: rows are on screen in the first frame after popstate.
    const back = await page.evaluate(async () => { history.back(); await new Promise(r => addEventListener('popstate', r, { once: true })); await new Promise(r => requestAnimationFrame(r)); return document.querySelectorAll('.vl-row .row-link').length; });
    rule(N, back > 0, 'Back: no rows in the first frame');
    // Pinned rows stay whatever the filter (OR-12): pin the first task, search for nothing, the pinned task is still listed.
    await page.goto(origin + '/tasks'); await page.waitForSelector('.vl-row .row-link');
    const pinnedTitle = await page.locator('.vl-row .row-link').first().textContent();
    await page.locator('.vl-row .row-link').first().focus(); await page.keyboard.press('p'); await page.waitForSelector('[data-ui=row-pin]');
    await page.goto(origin + '/tasks?q=no-such-task-anywhere'); await page.waitForSelector('[data-ui=row-pin]', { timeout: 10000 }).catch(() => {});
    rule(N, await page.locator('.vl-row:has([data-ui=row-pin]) .row-link').first().textContent().catch(() => null) === pinnedTitle, 'a pinned task left the list under a search that does not match it');
    // A pinned row outside the filtered page is still part of selection and its actions.
    await page.locator('.vl-row:has([data-ui=row-pin]) .row-link').first().focus(); await page.keyboard.press('x');
    await page.waitForFunction(() => document.querySelector('[data-ui=select-bar]')?.textContent?.includes('1 selected'));
    rule(N, await page.locator('[data-ui=select-bar] button[aria-label=Unpin]').isEnabled(), 'the visible pinned row was excluded from selection');
    await page.locator('[data-ui=select-bar] button[aria-label=Unpin]').click();
    await page.waitForFunction(() => !document.querySelector('[data-ui=row-pin]'));
    await page.getByRole('button', { name: 'Leave selection', exact: true }).click();
    // Search: one request per typing pause, one Back leaves the search.
    await page.goto(origin + '/tasks'); await page.waitForSelector('.vl-row .row-link');
    await page.locator('.fbar button[aria-label^="Search"]').first().click(); // search is an icon until used (OR-31)
    reqs.length = 0; await page.locator('.fbar input').first().pressSequentially('hero', { delay: 60 }); await page.waitForTimeout(800);
    const pages = reqs.filter(r => r === 'task_page').length;
    console.log(`type "hero": requests=${reqs.length} [${reqs.join(',')}] url=${await url()}`);
    rule(N, pages === 1, `typing "hero" sent ${pages} task_page requests`);
    await page.goBack(); await page.waitForTimeout(300);
    rule(N, (await url()) === '/tasks', `one Back after a search should return to /tasks, got ${await url()}`);
    // After Back, a new search starts a new history entry instead of replacing the one Back returned to.
    if (!await page.locator('.fbar input').count()) await page.locator('.fbar button[aria-label^="Search"]').first().click();
    await page.locator('.fbar input').first().fill(''); await page.locator('.fbar input').first().pressSequentially('copy', { delay: 60 }); await page.waitForTimeout(800);
    await page.goBack(); await page.waitForTimeout(300);
    rule(N, (await url()) === '/tasks', `a search after Back replaced the entry Back returned to (Back now at ${await url()})`);
    // A new search query ends the active preset (OR-33): the states stay, the preset leaves the URL.
    await page.goto(origin + '/tasks'); await page.waitForSelector('.vl-row .row-link');
    await page.locator('[data-ui=saved-views] button', { hasText: 'Running' }).click();
    await page.waitForFunction(() => new URLSearchParams(location.search).get('preset') === 'Running');
    if (!await page.locator('.fbar input').count()) await page.locator('.fbar button[aria-label^="Search"]').first().click();
    await page.locator('.fbar input').first().pressSequentially('hero', { delay: 60 });
    await page.waitForFunction(() => new URLSearchParams(location.search).get('q') === 'hero', null, { timeout: 5000 }).catch(() => {});
    { const u = new URL(origin + await url()).searchParams; rule(N, u.get('q') === 'hero' && !u.has('preset') && !!u.get('state'), `a search kept the preset or lost the states: ${u}`); }
    // The empty state's Clear filters clears states, search, projects and the preset.
    await page.locator('[data-ui=saved-views] button', { hasText: 'Running' }).click();
    await page.waitForFunction(() => new URLSearchParams(location.search).get('preset') === 'Running');
    await page.goto(origin + await url() + '&q=no-such-task-anywhere'); // a link: the preset stays with the query
    const clear = page.getByRole('button', { name: 'Clear filters', exact: true });
    const clearShown = await clear.waitFor({ timeout: 10000 }).then(() => true, () => false);
    rule(N, clearShown, 'the empty state under a preset and a search shows no Clear filters button');
    if (clearShown) { await clear.click(); await page.waitForFunction(() => !location.search.includes('q='), null, { timeout: 5000 }).catch(() => {}); }
    { const u = new URL(origin + await url()).searchParams; rule(N, !u.has('preset') && !u.has('q') && !u.has('state'), `Clear filters left filters or the preset: ${u}`); }
    // A bulk action that fails for one row: the rows that succeeded leave the selection, the failed row stays selected.
    await page.goto(origin + '/tasks?group=none&state=queued'); await page.waitForSelector('.vl-row .row-link');
    const [okId, badId] = await page.$$eval('.vl-row:not(.grp)', rs => rs.slice(0, 2).map(r => r.getAttribute('data-key')!));
    await page.route('**/api/ops/task_update', route => JSON.parse(route.request().postData() ?? '{}').taskId === badId
      ? route.fulfill({ status: 409, contentType: 'application/json', body: JSON.stringify({ message: 'forced failure' }) }) : route.continue());
    for (const k of [okId, badId]) await page.locator(`.vl-row[data-key="${k}"] .row-link`).click({ modifiers: ['Shift'] });
    await page.waitForFunction(() => document.querySelector('[data-ui=select-bar]')?.textContent?.includes('2 selected'));
    await page.locator('[data-ui=select-bar] button[aria-label=Pause]').click();
    const banner = await page.waitForFunction(() => /did not pause/.test(document.querySelector('[data-ui=list-view]')?.textContent ?? ''), null, { timeout: 10000 }).then(() => true, () => false);
    const bulkText = await page.locator('[data-ui=list-view]').textContent() ?? '';
    rule(N, banner && bulkText.includes('1 of 2 did not pause: forced failure'), `partial bulk failure: no "1 of 2 did not pause: forced failure" banner (banner shown: ${banner})`);
    await page.waitForFunction(b => new URLSearchParams(location.search).get('sel') === b, badId, { timeout: 5000 }).catch(() => {});
    { const sel = new URL(origin + await url()).searchParams.get('sel'); rule(N, sel === badId, `after a partial bulk failure sel should be only the failed row ${badId}, got ${sel}`); }
    await page.unroute('**/api/ops/task_update');
    // Collapsed groups are remembered per person (list-view.md, prefs listGroups), both ways: under state grouping In
    // progress (3) and Queued (4) are collapsed by the person, Completed (7, collapsed by default) is opened. Leaving the
    // section and coming back, and a reload, show them so from the first frame the headers are on screen.
    const header = (k: string) => page.locator(`.vl-row[data-key="g:${k}"] .group-h`);
    await page.goto(origin + '/tasks?group=state'); await header('3').waitFor();
    rule(N, await header('3').getAttribute('aria-expanded') === 'true', 'In progress is not open by default');
    await header('3').click(); await header('4').click(); await header('7').waitFor();
    rule(N, await header('7').getAttribute('aria-expanded') === 'false', 'Completed does not start collapsed under state grouping');
    await header('7').click();
    rule(N, await header('7').getAttribute('aria-expanded') === 'true', 'Completed did not open');
    const want = 'false,false,true';
    // Every frame's headers 3, 4 and 7 (aria-expanded, - when not on screen), recorded from a start until the list settles.
    const RECORD = `window.__groups = []; const tick = () => { const s = ['3', '4', '7'].map(k => document.querySelector('.vl-row[data-key="g:' + k + '"] .group-h')?.getAttribute('aria-expanded') ?? '-').join(','); if (window.__groups.at(-1) !== s) window.__groups.push(s); requestAnimationFrame(tick); }; requestAnimationFrame(tick);`;
    const flashed = (seen: string[]) => seen.filter(s => s.split(',').some((x, i) => x !== '-' && x !== want.split(',')[i]));
    await page.locator('.rail [data-id="hosts"] a').first().click(); await page.waitForURL('**/hosts**');
    // Every toggle reached the server.
    const stored = () => page.evaluate(() => fetch('/api/ops/person_settings_get', { method: 'POST', headers: { 'content-type': 'application/json' }, body: '{}' }).then(r => r.json()).then(s => JSON.stringify(s.listGroups?.['tasks:state'] ?? [])));
    for (let i = 0; i < 100 && await stored() !== '["3","4","7"]'; i++) await page.waitForTimeout(50);
    rule(N, await stored() === '["3","4","7"]', `leaving the section did not store the toggles: ${await stored()}`);
    const returned = await page.evaluate(async rec => {
      (0, eval)(rec); (document.querySelector('.rail [data-id="tasks"] a') as HTMLElement).click();
      for (let f = 0; f < 300 && !(window as any).__groups.at(-1)?.endsWith('true'); f++) await new Promise(r => requestAnimationFrame(r));
      return (window as any).__groups as string[];
    }, RECORD);
    console.log(`groups 3,4,7 per frame back to Tasks: ${returned.join(' > ')}`);
    rule(N, returned.at(-1) === want && !flashed(returned).length, `back to Tasks: groups not as left from the first frame: ${returned.join(' > ')}`);
    await page.addInitScript(RECORD);
    await page.reload(); await header('7').waitFor(); await page.waitForTimeout(300);
    const reloaded = await page.evaluate(() => (window as any).__groups as string[]);
    console.log(`groups 3,4,7 per frame after a reload: ${reloaded.join(' > ')}`);
    rule(N, reloaded.at(-1) === want && !flashed(reloaded).length, `after a reload: groups not as left from the first frame: ${reloaded.join(' > ')}`);
    // Grouping by project and back to state keeps them too.
    await page.locator('[data-ui=group-control] button').first().click();
    await page.locator('[data-ui=group-control] [role=menuitemradio]', { hasText: 'Project' }).click(); await page.waitForURL(u => !u.search.includes('group=state'));
    await page.locator('[data-ui=group-control] button').first().click();
    await page.locator('[data-ui=group-control] [role=menuitemradio]', { hasText: 'State' }).click(); await header('7').waitFor();
    rule(N, [await header('3').getAttribute('aria-expanded'), await header('4').getAttribute('aria-expanded'), await header('7').getAttribute('aria-expanded')].join(',') === want, 'switching the grouping back lost the collapsed groups');
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
