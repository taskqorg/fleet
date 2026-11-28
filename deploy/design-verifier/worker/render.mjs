// The browser guest receives the release build output and protected synthetic fixtures.
import { chromium } from '/opt/fleet-design-runtime/playwright/index.mjs';
import { readFileSync } from 'node:fs';
import { openSite } from './site.mjs';

const options = {
  executablePath: '/opt/fleet-design-runtime/chromium/chrome-headless-shell-linux64/chrome-headless-shell',
  chromiumSandbox: true,
  timeout: 15_000,
};

async function capture(browser, { files, main, scenario, clock }, { width, height, theme }) {
  const site = await openSite(files, scenario);
  let context;
  try {
    context = await browser.newContext({
      viewport: { width, height }, deviceScaleFactor: 1, colorScheme: theme, reducedMotion: 'reduce',
      locale: 'en-US', timezoneId: 'UTC', serviceWorkers: 'block', acceptDownloads: false,
    });
    const page = await context.newPage();
    page.setDefaultTimeout(7_500);
    await page.clock.setFixedTime(clock);
    const observed = [];
    let pageError = false, popup = false;
    context.on('page', other => { if (other !== page) { popup = true; void other.close(); } });
    page.on('pageerror', () => { pageError = true; });
    await context.route('**/*', route => new URL(route.request().url()).origin === site.origin ? route.continue() : route.abort());

    const absolute = route => site.origin + route;
    const at = async route => {
      await page.waitForURL(absolute(route), { waitUntil: 'domcontentloaded' });
      const url = new URL(page.url());
      observed.push(url.pathname + url.search);
    };
    let landmark = scenario.landmark;
    try {
      const response = await page.goto(absolute(scenario.start));
      if (!response?.ok()) throw new Error('normal app entry failed');
      if (scenario.flow === 'bot' || scenario.flow === 'back') {
        await at(scenario.start);
        await page.getByRole('button', { name: 'Talk to it', exact: true }).click();
        await at(scenario.target);
        await page.locator(landmark).waitFor({ state: 'visible' });
        if (scenario.flow === 'back') {
          await page.goBack({ waitUntil: 'domcontentloaded' });
          await at(scenario.start);
          landmark = scenario.backLandmark;
        }
      } else {
        await at(scenario.target);
      }
      await page.locator(landmark).waitFor({ state: 'visible' });
      await page.evaluate(async () => {
        await document.fonts.ready;
        await new Promise(resolve => requestAnimationFrame(() => requestAnimationFrame(resolve)));
      });
      const png = await page.screenshot({ animations: 'disabled', caret: 'hide' });
      const observedEnd = scenario.flow === 'back' ? scenario.start : scenario.target;
      const unmet = site.unmet(), forbidden = site.forbidden();
      if (site.missing.size || unmet.length || forbidden.length || pageError || popup || !site.used.has(main) ||
          page.url() !== absolute(observedEnd)) return { blocked: { observed, missing: [...site.missing], unmet, forbidden, pageError, popup } };
      return { observed, png: png.toString('base64') };
    } catch {
      return { blocked: { observed, missing: [...site.missing], routeOrLandmark: 'unconfirmed', pageError, popup } };
    }
  } finally {
    try { await context?.close(); } finally { await site.close(); }
  }
}

const browser = await chromium.launch(options);
try {
  if (process.argv[2] === '--probe') {
    const site = await openSite({ '/index.html': Buffer.from('Fleet sandbox probe').toString('base64') },
      { start: '/home', target: '/home', responses: [] });
    try {
      const page = await browser.newPage();
      await page.goto(site.origin + '/home');
      if (await page.locator('body').innerText() !== 'Fleet sandbox probe') throw new Error('isolated app origin unavailable');
    } finally {
      await site.close();
    }
    process.stdout.write(JSON.stringify({ chromium: browser.version(), node: process.versions.node }));
  } else {
    const input = JSON.parse(readFileSync(0, 'utf8'));
    const frames = [];
    for (const frame of input.matrix) frames.push(await capture(browser, input, frame));
    process.stdout.write(JSON.stringify({ chromium: browser.version(), frames }));
  }
} finally {
  await browser.close();
}
