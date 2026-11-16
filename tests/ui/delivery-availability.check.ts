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
      }
      await page.unroute('**/api/ops/delivery_status');
    }
    assert.deepEqual(errors, []);
  } finally { await context.close(); }
}
