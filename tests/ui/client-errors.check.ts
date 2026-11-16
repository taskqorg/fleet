// Requirement: fleet-telemetry.md section 9: an error thrown in the app and a render crash reach the coordinator as
// grouped browser errors, readable with client_errors_list and client_error_get and raised as a needs-you problem; the
// render crash shows a recoverable error instead of a blank page. Risk: browser errors stay invisible to agents and owner.
// Run: pnpm run build && node tests/ui/client-errors.check.ts (CI runs it from conversations.check.ts).
import assert from 'node:assert/strict';
import { chromium, type Browser } from 'playwright';
import { checkServer } from './seed.ts';

export async function checkClientErrors(browser: Browser, origin: string) {
  const context = await browser.newContext(), page = await context.newPage();
  const call = async (name: string, body: unknown) => { const r = await fetch(origin + '/api/ops/' + name, { method: 'POST', headers: { 'content-type': 'application/json' }, body: JSON.stringify(body) }); assert.equal(r.status, 200, name); return r.json() as Promise<any>; };
  const group = async (message: string) => {
    for (let i = 0; i < 100; i++) {
      const g = (await call('client_errors_list', {})).errors.find((e: any) => e.message === message);
      if (g) return g;
      await new Promise(r => setTimeout(r, 100));
    }
    assert.fail(`${message}: no browser error reached the coordinator`);
  };
  try {
    // A value only a foreign link could carry: the report keeps the view path, never this query.
    await page.goto(origin + '/tasks?state=failed&token=not-for-telemetry');
    await page.locator('header.page-h, .tr-body, [data-ui]').first().waitFor();
    await page.evaluate(() => { for (let i = 0; i < 5; i++) setTimeout(() => { throw new Error('telemetry browser check'); }); });
    const thrown = await group('telemetry browser check');
    assert.equal(thrown.name, 'Error');
    assert.equal(thrown.source, 'error');
    assert.equal(thrown.lastPath, '/tasks?state=failed');
    assert.ok(thrown.questionId, 'a new browser error is a needs-you problem');
    const detail = await call('client_error_get', { fingerprint: thrown.fingerprint });
    assert.equal(detail.reports.length, 1, 'repeats in the tab are one report');
    assert.equal(detail.reports[0].count, 1);
    assert.match(detail.reports[0].stack, /telemetry browser check/);
    assert.ok(detail.reports[0].version, 'the report names the app version');
    // The tab's counted repeats go out when it unloads (pagehide).
    await page.goto('about:blank');
    for (let i = 0; i < 100 && (await call('client_error_get', { fingerprint: thrown.fingerprint })).error.count < 5; i++) await new Promise(r => setTimeout(r, 100));
    assert.equal((await call('client_error_get', { fingerprint: thrown.fingerprint })).error.count, 5, 'repeats arrive as a count on unload');

    // A render crash: Overview receives a reply it cannot render, shows a recoverable error and reports it.
    await page.route('**/api/ops/questions_list', route => route.fulfill({ json: { questions: null } }));
    await page.goto(origin + '/overview');
    await page.getByText('This page failed to render.', { exact: true }).waitFor();
    await page.getByRole('button', { name: 'Reload' }).waitFor();
    for (let i = 0; i < 100; i++) {
      if ((await call('client_errors_list', {})).errors.some((e: any) => e.source === 'render' && e.lastPath === '/overview')) break;
      await new Promise(r => setTimeout(r, 100));
    }
    assert.ok((await call('client_errors_list', {})).errors.some((e: any) => e.source === 'render' && e.lastPath === '/overview'), 'the render crash reached the coordinator');
    console.log('ok browser errors reach client_errors_list (thrown, repeated, render crash)');
  } finally { await context.close(); }
}

if (process.argv[1]?.endsWith('/client-errors.check.ts')) {
  const server = await checkServer();
  const browser = await chromium.launch(process.env.FLEET_CHROMIUM ? { executablePath: process.env.FLEET_CHROMIUM } : {});
  try { await checkClientErrors(browser, server.origin); }
  finally { await browser.close(); await server.close(); }
}
