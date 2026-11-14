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
      const strip = page.getByRole('list', {name: 'Pull requests by stage'});
      await strip.waitFor();
      const numbers = strip.locator('[data-ui=number]');
      assert(await numbers.count() > 0);
      if (fixture.error) {
        await page.getByRole('alert').filter({hasText: error}).waitFor();
        await page.getByText('Some GitHub data is unavailable. Current totals and waiting status cannot be confirmed; retained observations are shown below.', {exact: true}).waitFor();
        assert.equal(await page.getByText('Nothing waits past its limit.', {exact: true}).count(), 0);
        assert.equal(await page.locator('.limit-card').getByText('None.', {exact: true}).count(), 0);
        if (fixture.items.length === 0) {
          assert.equal(await numbers.locator('xpath=self::*[contains(@class,"null")]').count(), await numbers.count(), fixture.name);
          assert.equal(await page.locator('.limit-card .panel-h [data-ui=number]:not(.null)').count(), 0);
          await page.getByText('History unavailable. GitHub data is incomplete.', {exact: true}).waitFor();
          assert.equal(await page.locator('.delivery-flow [data-ui=area-chart]').count(), 0);
        } else {
          const open = strip.getByRole('listitem').filter({hasText: 'Open'}).locator('[data-ui=number]');
          assert.match(await open.getAttribute('class') ?? '', /null/);
          const retained = page.locator('.limit-card[aria-label="Open"] .panel-h');
          assert.match(await retained.textContent() ?? '', /1\s*retained/);
          const landing = strip.getByRole('listitem').filter({hasText: 'workers waiting to land'}).locator('[data-ui=number]');
          assert.match(await landing.getAttribute('class') ?? '', /null/);
          await page.getByText(/Retained observations: 2 workers were waiting to land/).waitFor();
          await page.getByRole('link', {name: /Retained pull request/}).waitFor();
          await page.getByText('Retained historical observations; GitHub coverage is incomplete.', {exact: true}).waitFor();
        }
      } else {
        assert.equal(await numbers.locator('xpath=self::*[contains(@class,"null") or contains(@class,"partial")]').count(), 0);
        await page.getByText('Nothing waits past its limit.', {exact: true}).waitFor();
        const open = strip.getByRole('listitem').filter({hasText: 'Open'}).locator('[data-ui=number]');
        assert.equal((await open.textContent())?.trim(), String(fixture.items.length));
        const landing = strip.getByRole('listitem').filter({hasText: 'workers waiting to land'}).locator('[data-ui=number]');
        assert.equal((await landing.textContent())?.trim(), String(fixture.landing));
      }
      await page.unroute('**/api/ops/delivery_status');
    }
    assert.deepEqual(errors, []);
  } finally { await context.close(); }
}
