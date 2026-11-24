// Shell navigation of the seven-section rail (docs/design-system/controls/nav-rail.md, patterns/app-shell.md): at 1440 px
// the rail lists Home, Live, Conversations, Projects, Lab, Library, System; at 390 px the bottom bar of five replaces it
// (OR-60); every earlier route lands on its target route with the section that owns it highlighted (OR-57); an unknown
// path shows the app's not-found page inside the shell. Not part of CI; run after `pnpm run build`:
//   node tests/ui/shell-nav.check.ts            (FLEET_BROWSER_SHOTS=<dir> keeps screenshots)
import assert from 'node:assert/strict';
import { mkdirSync } from 'node:fs';
import { chromium, type Page } from 'playwright';
import { checkServer } from './seed.ts';

const SECTIONS = ['Home', 'Live', 'Conversations', 'Projects', 'Lab', 'Library', 'System'];
// Earlier route, its target route and the rail section that owns it.
const ROUTES: [string, string, string][] = [
  ['/', '/home', 'home'], ['/overview', '/home', 'home'],
  ['/tasks', '/projects/tasks', 'projects'], ['/results', '/projects/results', 'projects'], ['/delivery', '/projects/delivery', 'projects'],
  ['/publishing', '/projects/delivery', 'projects'], ['/projects/FLT/tasks/T-FLT-42', '/projects/tasks/T-FLT-42', 'projects'],
  ['/personal-lab', '/lab/mine', 'lab'], ['/bots', '/library/bots', 'library'], ['/agents', '/library/agents', 'library'],
  ['/pipelines', '/library/pipelines', 'library'], ['/catalogue', '/library/methods', 'library'], ['/changes', '/library/changes', 'library'],
  ['/hosts', '/system', 'system'], ['/hosts/limits', '/system/accounts', 'system'], ['/runners', '/system/runners', 'system'],
  ['/keys', '/system/connections', 'system'], ['/spend', '/system/spend', 'system'], ['/humans', '/system/people', 'system'],
  ['/conversations', '/conversations', 'conversations'], ['/projects', '/projects', 'projects'], ['/lab', '/lab', 'lab'], ['/live', '/live', 'live'],
];

const shots = process.env.FLEET_BROWSER_SHOTS;
if (shots) mkdirSync(shots, { recursive: true });
const { origin, close } = await checkServer();
const browser = await chromium.launch({ headless: true });
const failures: string[] = [];
const check = (ok: boolean, what: string) => { console.log(`${ok ? 'ok  ' : 'FAIL'} ${what}`); if (!ok) failures.push(what); };
const current = (p: Page, where: string) => p.locator(`${where} [aria-current="page"]`).first().evaluate(e => e.closest('[data-id]')?.getAttribute('data-id') ?? e.textContent?.trim() ?? '').then(t => t.trim());
try {
  const wide = await browser.newPage({ viewport: { width: 1440, height: 900 }, colorScheme: 'dark' });
  await wide.goto(origin + '/'); await wide.waitForSelector('[data-ui=nav-rail] [aria-current=page]');
  const rail = await wide.locator('[data-ui=nav-rail] .rail-g:not(.pinned) .nav-i .nm').allTextContents();
  check(JSON.stringify(rail) === JSON.stringify(SECTIONS), `1440 px rail lists the seven sections: ${rail.join(', ')}`);
  check(await wide.locator('[data-ui=nav-rail] .rail-h').count() === 0, 'the rail has no group headings');
  check(!await wide.locator('[data-ui=bottom-bar]').isVisible(), 'no bottom bar at 1440 px');
  if (shots) await wide.screenshot({ path: shots + '/rail-1440.png' });
  for (const [earlier, target, section] of ROUTES) {
    await wide.goto(origin + earlier);
    await wide.waitForFunction(t => location.pathname === t, target, { timeout: 10000 }).catch(() => {});
    const at = new URL(wide.url()).pathname;
    await wide.waitForSelector('[data-ui=nav-rail] [aria-current=page]', { timeout: 10000 }).catch(() => {});
    const cur = await current(wide, '[data-ui=nav-rail]').catch(() => '');
    check(at === target && cur === section, `${earlier} lands on ${at} with ${cur || 'nothing'} highlighted (want ${target}, ${section})`);
  }
  await wide.goto(origin + '/tasks?q=keep%20me'); await wide.waitForFunction(() => location.pathname === '/projects/tasks');
  check(new URL(wide.url()).searchParams.get('q') === 'keep me', 'the redirect keeps the query');
  const status = (await fetch(origin + '/no/such/page')).status;
  await wide.goto(origin + '/no/such/page');
  await wide.getByText('This page does not exist.').waitFor({ timeout: 10000 }).catch(() => {});
  check(status === 200 && await wide.getByText('This page does not exist.').isVisible() && await wide.locator('[data-ui=nav-rail]').isVisible(), `an unknown path shows the not-found page inside the shell (HTTP ${status})`);
  if (shots) await wide.screenshot({ path: shots + '/not-found-1440.png' });
  await wide.goto(origin + '/hosts'); await wide.waitForSelector('[data-ui=nav-rail] [data-id=system] [aria-current=page]');
  if (shots) await wide.screenshot({ path: shots + '/system-hosts-1440.png' });

  const phone = await browser.newPage({ viewport: { width: 390, height: 844 }, hasTouch: true, isMobile: true, colorScheme: 'dark' });
  await phone.goto(origin + '/home'); await phone.waitForSelector('[data-ui=bottom-bar]');
  const tabs = await phone.locator('[data-ui=bottom-bar] > .tab-i .nm').allTextContents();
  check(JSON.stringify(tabs) === JSON.stringify(['Home', 'Live', 'Conversations', 'Projects', 'More']), `390 px bottom bar of five: ${tabs.join(', ')}`);
  check(!await phone.locator('[data-ui=nav-rail]').isVisible(), 'no rail at 390 px');
  check(await phone.evaluate(() => document.documentElement.scrollWidth <= innerWidth), 'nothing scrolls sideways at 390 px');
  if (shots) await phone.screenshot({ path: shots + '/home-390.png' });
  await phone.getByRole('button', { name: 'More', exact: true }).click();
  const more = (await phone.getByRole('menu', { name: 'More sections' }).getByRole('menuitem').allTextContents()).map(t => t.trim());
  check(JSON.stringify(more) === JSON.stringify(['Lab', 'Library', 'System']), `More holds ${more.join(', ')}`);
  if (shots) await phone.screenshot({ path: shots + '/more-390.png' });
  await phone.getByRole('menuitem', { name: 'System' }).click(); await phone.waitForURL('**/system');
  check(await current(phone, '[data-ui=bottom-bar]') === 'More', 'a System screen marks More');
  await phone.goto(origin + '/tasks'); await phone.waitForURL('**/projects/tasks');
  await phone.waitForSelector('[data-ui=bottom-bar] [aria-current=page]');
  check((await phone.locator('[data-ui=bottom-bar] [aria-current=page] .nm').textContent()) === 'Projects', 'Tasks marks Projects in the bottom bar');
  if (shots) await phone.screenshot({ path: shots + '/tasks-390.png' });
} finally { await browser.close(); await close(); }
assert.deepEqual(failures, []);
console.log('ok shell navigation: rail, bottom bar, redirects, not-found');
