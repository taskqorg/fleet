// The browser guest receives the release build output and protected synthetic fixtures.
import { chromium, selectors } from '/opt/fleet-design-runtime/playwright/index.mjs';
import { readFileSync } from 'node:fs';
import { openSite, visualReady } from './site.mjs';
import { imageJourney, isImageFlow, registerImageOpenSelector } from './image-journey.mjs';

const options = {
  executablePath: '/opt/fleet-design-runtime/chromium/chrome-headless-shell-linux64/chrome-headless-shell',
  chromiumSandbox: true,
  timeout: 15_000,
};

async function capture(browser, { files, main, scenario, clock }, frame, designer) {
  const { width, height, theme } = frame;
  const site = await openSite(files, scenario, designer);
  let context, journey;
  try {
    context = await browser.newContext({
      viewport: { width, height }, deviceScaleFactor: 1, colorScheme: theme, reducedMotion: 'reduce',
      locale: 'en-US', timezoneId: 'UTC', serviceWorkers: 'block', acceptDownloads: false,
    });
    const page = await context.newPage();
    page.setDefaultTimeout(7_500);
    await page.clock.setFixedTime(clock);
    const observed = [];
    let pageError = false, popup = false, outside = false;
    context.on('page', other => { if (other !== page) { popup = true; void other.close(); } });
    page.on('pageerror', () => { pageError = true; });
    await context.route('**/*', route => {
      if (new URL(route.request().url()).origin === site.origin) return route.continue();
      outside = true;
      return route.abort();
    });

    const absolute = route => site.origin + route;
    const at = async route => {
      await page.waitForURL(absolute(route), { waitUntil: 'domcontentloaded' });
      const url = new URL(page.url());
      observed.push(url.pathname + url.search);
    };
    const settle = () => page.evaluate(async () => {
      await document.fonts.ready;
      await new Promise(resolve => requestAnimationFrame(() => requestAnimationFrame(resolve)));
    });
    let landmark = scenario.landmark;
    let step;
    try {
      const response = await page.goto(absolute(scenario.start));
      if (!response?.ok()) throw new Error('normal app entry failed');
      if (isImageFlow(scenario.flow)) {
        await at(scenario.start);
        journey = await imageJourney(page, scenario, frame, site.origin, settle, value => { step = value; }, site.openSignal);
        if (!designer) await at(scenario.target);
      } else if (scenario.flow === 'bot' || scenario.flow === 'back') {
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
      if (designer && scenario.flow === 'task-details') {
        await page.locator('[data-ui=detail]').getByRole('tab', { name: 'Details', exact: true }).click();
        await page.locator('[data-ui=detail]').getByRole('tabpanel', { name: 'Details', exact: true }).waitFor();
      }
      if (!journey) await page.locator(landmark).waitFor({ state: 'visible' });
      await settle();
      await journey?.confirm();
      if (designer) {
        const surface = journey && scenario.flow !== 'image-returned' ?
          page.getByRole('dialog', { name: 'Image viewer', exact: true }) : page.locator(landmark);
        const visual = await surface.evaluate(surface => {
          function scheme(node) {
            if (!node) return null;
            const probe = document.createElement('span');
            probe.style.color = 'light-dark(rgb(0, 0, 0), rgb(255, 255, 255))';
            probe.style.display = 'none';
            node.append(probe);
            const color = getComputedStyle(probe).color;
            probe.remove();
            return color === 'rgb(0, 0, 0)' ? 'light' : color === 'rgb(255, 255, 255)' ? 'dark' : null;
          }
          return { scheme: scheme(document.querySelector('#app [data-ui=app-shell]')),
            routeScheme: scheme(surface),
            imagesReady: [...document.images].every(image => image.complete && image.naturalWidth > 0),
            fontsReady: document.fonts.status === 'loaded' && [...document.fonts].every(font => font.status !== 'error') };
        });
        if (!visualReady(visual, theme)) throw new Error('assets, fonts, or effective color scheme unconfirmed');
      }
      const png = await page.screenshot({ animations: 'disabled', caret: 'hide' });
      await journey?.confirm();
      const observedEnd = scenario.flow === 'back' ? scenario.start : scenario.target;
      const unmet = site.unmet(), forbidden = site.forbidden();
      if (site.missing.size || unmet.length || forbidden.length || pageError || popup || (designer && outside) ||
          !site.used.has(main) || page.url() !== absolute(observedEnd))
        return { blocked: { observed, missing: [...site.missing], unmet, forbidden, pageError, popup,
          ...(designer && { unmatched: [...site.unmatched], unmatchedOmitted: site.unmatchedOmitted(), outside }) } };
      return { observed, png: png.toString('base64'), ...(journey && { imageJourney: journey.evidence }) };
    } catch {
      return { blocked: { observed, missing: [...site.missing], routeOrLandmark: 'unconfirmed', pageError, popup,
        ...(step && { step }), ...(designer && { unmatched: [...site.unmatched], unmatchedOmitted: site.unmatchedOmitted(), outside }) } };
    }
  } finally {
    try {
      try { await journey?.dispose?.(); } finally { await context?.close(); }
    } finally { await site.close(); }
  }
}

await registerImageOpenSelector(selectors);
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
    for (const frame of input.matrix) frames.push(await capture(browser, input, frame, process.argv[2] === '--designer'));
    process.stdout.write(JSON.stringify({ chromium: browser.version(), frames }));
  }
} finally {
  await browser.close();
}
