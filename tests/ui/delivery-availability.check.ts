// Requirement: unavailable Delivery data stays unknown and retained observations stay partial.
// Risk: a provider failure becomes measured zero, a success claim, or hidden known work.
import assert from 'node:assert/strict';
import type {Browser} from 'playwright';

export async function checkDeliveryAvailability(browser: Browser, origin: string) {
  const context = await browser.newContext(), page = await context.newPage();
  const error = 'Amoenum/desiign: github pulls_open: http-404 404';
  const now = new Date().toISOString();
  const item = {projectId: 'fleet', repo: 'taskqorg/fleet', number: 123, title: 'Retained pull request', url: 'https://github.com/taskqorg/fleet/pull/123', stage: 'pr_open', since: now};
  const cases = [
    {name: 'unavailable empty', error, items: [], samples: [], landing: 0},
    {name: 'incomplete retained', error, items: [item], samples: [{projectId: 'fleet', repo: 'taskqorg/fleet', at: now, counts: {pr_open: 1}}], landing: 2},
    {name: 'healthy empty', items: [], samples: [], landing: 0},
    {name: 'healthy real', items: [item], samples: [{projectId: 'fleet', repo: 'taskqorg/fleet', at: now, counts: {pr_open: 1}}], landing: 2},
  ];
  const errors: string[] = [];
  page.on('pageerror', e => errors.push(e.message));
  try {
    for (const fixture of cases) {
      await page.route('**/api/ops/delivery_status', route => route.fulfill({json: {source: 'key', refreshedAt: now, ...fixture}}));
      const response = page.waitForResponse(r => r.url() === origin + '/api/ops/delivery_status');
      await page.goto(origin + '/delivery');
      await (await response).finished();
      await page.evaluate(() => new Promise<void>(resolve => requestAnimationFrame(() => requestAnimationFrame(() => resolve()))));
      // The stage counters are the totals: unknown ('?', named "unknown") while GitHub data is incomplete, never zero.
      const counters = page.getByRole('group', {name: 'Pull requests by stage'});
      await counters.waitFor();
      const open = counters.getByRole('button', {name: /^Open: /});
      const overview = page.locator('[data-ui=delivery-overview]');
      if (fixture.error) {
        await page.getByRole('alert').filter({hasText: error}).waitFor();
        await page.getByText('Some GitHub data is unavailable: current totals are unknown; the rows are the last ones read.', {exact: true}).waitFor();
        assert.equal(await open.getAttribute('aria-label'), 'Open: unknown', fixture.name);
        assert.equal(await counters.getByRole('button', {name: /: \d+$/}).count(), 0, `${fixture.name}: a measured count while incomplete`);
        assert.equal(await page.getByText('Nothing in flight.', {exact: true}).count(), 0);
        assert.equal(await page.getByText('No pull requests in flight.', {exact: true}).count(), 0);
        if (fixture.items.length === 0) assert.equal(await page.getByText('Unavailable. GitHub data is incomplete.', {exact: true}).count(), 2, 'list and Moving now say unavailable');
        else {
          await page.getByRole('link', {name: /Retained pull request/}).waitFor();
          await overview.getByText('The last rows read; GitHub coverage is incomplete.', {exact: true}).waitFor();
        }
      } else {
        assert.equal(await open.getAttribute('aria-label'), `Open: ${fixture.items.length}`, fixture.name);
        if (fixture.items.length === 0) { await page.getByText('No pull requests in flight.', {exact: true}).waitFor(); await overview.getByText('Nothing in flight.', {exact: true}).waitFor(); }
        else await page.getByRole('link', {name: /Retained pull request/}).first().waitFor();
      }
      if (fixture.name === 'healthy real') {
        // Back to a default value drops the key from the link; it never writes the text "undefined" (sort=undefined broke sorting).
        const flip = page.locator('[data-ui=sort-control] .pick-dir');
        await flip.click(); await page.waitForURL(u => u.searchParams.get('sort') === '-since');
        await flip.click(); await page.waitForURL(u => !u.searchParams.has('sort'));
        assert.ok(!page.url().includes('undefined'), page.url());
        // A link saved while the bug was live: the controls show the defaults, and the next change writes a clean link.
        await page.goto(origin + '/delivery?group=undefined&sort=undefined&range=undefined&at=undefined');
        await page.locator('[data-ui=sort-control]').waitFor();
        assert.equal((await page.locator('[data-ui=sort-control] .pick-b').textContent())?.trim(), 'In stage since');
        assert.equal(await page.getByText(/id Dat|Invalid Date/).count(), 0, 'a stale at shows as a date chip');
        await flip.click(); await page.waitForURL(u => u.searchParams.get('sort') === '-since');
        assert.equal(new URL(page.url()).search, '?sort=-since', 'stale keys stay in the link');
      }
      await page.unroute('**/api/ops/delivery_status');
    }
    assert.deepEqual(errors, []);
  } finally { await context.close(); }
}

// Requirement (docs/design-system/pages/delivery.md, decisions 2026-11-17): the Delivery list is the shared list view, its
// system presets set the stages only, Installed starts collapsed, Pace shows calendar days where a day without install
// times is "not measured" (never zero), and a picked day filters the list to the pull requests merged that day.
// Risk: a preset resets the person's sort, installed rows bury what moves, a missing stage reads as a fast day, or a day
// pick shows the wrong rows.
export async function checkDeliveryList(browser: Browser, origin: string) {
  const context = await browser.newContext(), page = await context.newPage();
  const errors: string[] = [];
  page.on('pageerror', e => errors.push(e.message));
  const now = Date.now(), iso = (t: number) => new Date(t).toISOString(), min = 60e3;
  // Local midnight of today and of n days before, as the page computes them (dayStart in the viewer's zone).
  const dayStart = (n: number) => { const d = new Date(now); d.setHours(0, 0, 0, 0); d.setDate(d.getDate() - n); return d.getTime(); };
  const it = (number: number, title: string, stage: string, since: number, extra: Record<string, unknown> = {}) =>
    ({projectId: 'fleet', repo: 'taskqorg/fleet', number, title, url: `https://github.com/taskqorg/fleet/pull/${number}`, stage, since: iso(since), opened: iso(since - 30 * min), ...extra});
  const items = [
    it(701, 'Checks failed here', 'pr_failed', now - 20 * min), it(702, 'Left the merge queue', 'mq_failed', now - 15 * min),
    it(703, 'Checks running long', 'pr_ci', now - 50 * min), it(704, 'Waiting for review', 'pr_open', now - 3 * 3600e3),
    it(690, 'Installed one', 'installed', now - 4 * 3600e3, {release: 'v1.1.1'}), it(691, 'Installed two', 'installed', now - 5 * 3600e3, {release: 'v1.1.1'}),
  ];
  // History: three days ago merged before install times were recorded (not measured); yesterday measured.
  const pr = (number: number, title: string, merged: number, durations: Record<string, number>) =>
    ({number, title, url: `https://github.com/taskqorg/fleet/pull/${number}`, headSha: 'h', mergeSha: 'm' + number, opened: iso(merged - 30 * min), merged: iso(merged), installed: durations.total ? {coordinator: iso(merged + 5 * min)} : {}, ...(durations.release ? {released: iso(merged + durations.release)} : {}),
      durations, queueRuns: 1, queueRemovals: 0, flakes: 0});
  const prs = [
    pr(650, 'Merged before installs were recorded', dayStart(3) + 12 * 3600e3, {pr_ci: 8 * min, queue: 6 * min, release: 2 * min}),
    // Four days ago: one released before recording began (due, never measured) beside a measured one.
    pr(640, 'Released before recording', dayStart(4) + 10 * 3600e3, {pr_ci: 8 * min, queue: 6 * min, release: 2 * min}),
    {...pr(641, 'Measured early', dayStart(4) + 11 * 3600e3, {pr_ci: 8 * min, queue: 6 * min, release: 2 * min, install: min, total: 17 * min})},
    pr(660, 'Merged yesterday A', dayStart(1) + 11 * 3600e3, {pr_ci: 8 * min, queue: 6 * min, release: 2 * min, install: min, total: 17 * min}),
    pr(661, 'Merged yesterday B', dayStart(1) + 13 * 3600e3, {pr_ci: 10 * min, queue: 7 * min, release: 2 * min, install: min, total: 95 * min}),
    // Installed, but its install time was not matched to its release: unmeasured, so yesterday is partly measured.
    {...pr(662, 'Merged yesterday, no install time', dayStart(1) + 15 * 3600e3, {pr_ci: 9 * min, queue: 6 * min, release: 2 * min}), installed: {coordinator: iso(dayStart(1) + 16 * 3600e3)}},
    // Merged minutes ago and still on its way: pending, never "unmeasured".
    {...pr(663, 'Merged just now', Math.max(now - 10 * min, dayStart(0)), {pr_ci: 9 * min, queue: 6 * min}), released: iso(now)},
  ];
  const stat = (key: string, p50: number, p90: number) => ({key, n: 20, p50Ms: p50 * min, p90Ms: p90 * min});
  const base = {from: iso(dayStart(14)), until: iso(now), counts: {prs: 20, runs: 20, removals: 0, flakes: 0},
    stats: [stat('pr_ci', 7, 40), stat('enqueue', 1, 8), stat('queue', 6, 16), stat('release', 1, 4), stat('install', 1, 2), stat('total', 21, 74)]};
  const settings = {ratio: 1.5, minSamples: 5, minDeltaMs: 60000, baseline: 'rolling'};
  await page.route('**/api/ops/delivery_status', r => r.fulfill({json: {source: 'key', refreshedAt: iso(now), items, samples: [], landing: 0}}));
  await page.route('**/api/ops/delivery_baseline', r => r.fulfill({json: {rolling: base, snapshots: [], trend: [], settings}}));
  await page.route('**/api/ops/delivery_history', r => r.fulfill({json: {repo: 'taskqorg/fleet', backfilledFrom: iso(dayStart(20)), installsFrom: iso(dayStart(2)), prs, runs: [], releases: []}}));
  await page.route('**/api/ops/delivery_regressions', r => r.fulfill({json: {regressions: [{key: 'run', metric: 'p50', baselineMs: 4 * min, currentMs: 7 * min, n: 38, ratio: 1.75, links: []}], settings}}));
  try {
    await page.goto(origin + '/delivery?sort=-opened');
    const list = page.getByRole('grid', {name: 'Pull requests', exact: true});
    await list.getByRole('link', {name: 'Checks failed here'}).waitFor();
    // Installed starts collapsed: its header says 2, its rows are not shown.
    const installed = page.locator('.group-h', {hasText: 'Installed'});
    assert.equal(await installed.getAttribute('aria-expanded'), 'false', 'Installed is collapsed by default');
    assert.equal(await list.getByRole('link', {name: 'Installed one'}).count(), 0, 'installed rows hidden');
    // The red time chip: PR checks for 50m is past its 40m P90; Open has no norm.
    assert.equal(await list.locator('.vl-row', {hasText: 'Checks running long'}).locator('.chip-age.alert').count(), 1, 'past the P90 is red');
    assert.equal(await list.locator('.vl-row', {hasText: 'Waiting for review'}).locator('.chip-age.alert').count(), 0, 'Open has no norm');
    // A system preset sets the stages only: the sort and grouping the person chose stay.
    await page.getByRole('button', {name: 'Needs me', exact: true}).click();
    await page.waitForURL(u => u.searchParams.get('state') === 'mq_failed,pr_failed');
    assert.equal(new URL(page.url()).searchParams.get('sort'), '-opened', 'preset keeps the sort');
    assert.equal(await list.getByRole('link').count(), 2, 'Needs me shows the failed pull requests only');
    await page.getByRole('button', {name: 'In flight', exact: true}).click();
    await page.waitForURL(u => (u.searchParams.get('state') ?? '').split(',').length === 8);
    assert.equal(await list.getByRole('link', {name: 'Waiting for review'}).count(), 1);
    // The Installed preset shows its rows: the group collapsed by default opens for a filter to installed rows.
    await page.getByRole('button', {name: 'Installed', exact: true}).click();
    await list.getByRole('link', {name: 'Installed one'}).waitFor();
    await page.goto(origin + '/delivery');
    // Moving now: a norm bar per pull request in flight, past the norm for the long PR checks.
    const moving = page.getByRole('grid', {name: 'Pull requests in flight'});
    await moving.getByRole('img', {name: /PR checks #703: 50m; norm P50 7m, P90 40m, past the norm/}).waitFor();
    // Pace: a day with merges but no install times is not measured, never zero.
    const chart = page.getByRole('group', {name: 'Open to installed per day, P50 and P90'});
    await chart.waitFor();
    const cols = chart.getByRole('button');
    assert.equal(await cols.count(), 7, 'seven calendar days');
    assert.match((await cols.nth(3).getAttribute('aria-label'))!, /: not measured \(install times recorded from /, 'three days ago is not measured');
    assert.match((await cols.nth(2).getAttribute('aria-label'))!, /· 1 of 2 measured/, 'a release before recording counts against its day');
    assert.match((await cols.nth(5).getAttribute('aria-label'))!, /P50 \d+m, P90 1h 35m · 2 of 3 measured · above the 14-day P90 1h 14m/, 'yesterday is measured in part, its P90 red');
    // A past day measured in part is "partly measured", never "part of the day" (that is today only).
    assert.equal(await cols.nth(5).evaluate(e => e.classList.contains('some') && !e.classList.contains('part')), true, 'yesterday is partly measured');
    assert.match((await cols.nth(6).getAttribute('aria-label'))!, /: not measured \(not installed yet\)/, 'today: a pull request on its way is pending');
    // A regression names its stage: the queue run, not a bare "run".
    await page.getByText('Queue run P50 is 7m, baseline 4m', {exact: true}).waitFor();
    assert.ok(await page.locator('.dc-vals .alert').count() >= 1, 'the P90 above the norm is red in the values row');
    // A picked day filters the list to the pull requests merged that day, with a removable chip.
    await cols.nth(5).click();
    const y = new Date(dayStart(1)), pad = (n: number) => String(n).padStart(2, '0');
    await page.waitForURL(u => u.searchParams.get('at') === `${y.getFullYear()}-${pad(y.getMonth() + 1)}-${pad(y.getDate())}`);
    await list.getByRole('link', {name: 'Merged yesterday A'}).waitFor();
    assert.equal(await list.getByRole('link', {name: 'Checks failed here'}).count(), 0, 'the current rows give way to the day');
    // A link to the day opens it once the history is in, and keeps it in the link.
    const at = new URL(page.url()).searchParams.get('at')!;
    await page.goto(origin + '/delivery?at=' + at);
    await list.getByRole('link', {name: 'Merged yesterday B'}).waitFor();
    assert.equal(new URL(page.url()).searchParams.get('at'), at, 'the picked day stays in the link');
    await page.getByRole('button', {name: /Remove filter Merged/}).click();
    await page.waitForURL(u => !u.searchParams.has('at'));
    // The ranges: 24 h is twelve two-hour buckets, 30 days starts at the first recorded day.
    await page.getByRole('button', {name: '24 h', exact: true}).click();
    await page.waitForURL(u => u.searchParams.get('range') === '24h');
    await page.waitForFunction(() => document.querySelectorAll('[aria-label="Open to installed per day, P50 and P90"] button').length === 12);
    await page.getByRole('button', {name: '30 days', exact: true}).click();
    await page.waitForFunction(() => document.querySelectorAll('[aria-label="Open to installed per day, P50 and P90"] button').length === 21);
    assert.deepEqual(errors, []);
  } finally { await context.close(); }
}

// Standalone: pnpm run build && node tests/ui/delivery-availability.check.ts
if (import.meta.url === (await import('node:url')).pathToFileURL(process.argv[1]!).href) {
  const {chromium} = await import('playwright');
  const {checkServer} = await import('./seed.ts');
  const {origin, close} = await checkServer();
  const b = await chromium.launch();
  try { await checkDeliveryAvailability(b, origin); console.log('ok   delivery availability'); await checkDeliveryList(b, origin); console.log('ok   delivery list and pace'); }
  finally { await b.close(); await close(); }
}
