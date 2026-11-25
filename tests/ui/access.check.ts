// System › Access (docs/design-system/pages/system.md "Access"; board screens/system.bl, frame "System › Access: status
// and onboarding (approved)"): the page lists subscriptions, model keys and services with their state and next step; an
// offered provider with no connection says "Not added"; Add access › Anthropic creates the connection and shows where the
// key comes from and the paste field (no key is sent here). Not part of CI; run after `pnpm run build`:
//   node tests/ui/access.check.ts            (FLEET_BROWSER_SHOTS=<dir> keeps dark screenshots at 1440 px)
import { mkdirSync } from 'node:fs';
import { chromium } from 'playwright';
import { checkServer } from './seed.ts';

const shots = process.env.FLEET_BROWSER_SHOTS;
if (shots) mkdirSync(shots, { recursive: true });
const { origin, c, close } = await checkServer();
const browser = await chromium.launch({ headless: true });
const failures: string[] = [];
const check = (ok: boolean, what: string) => { console.log(`${ok ? 'ok  ' : 'FAIL'} ${what}`); if (!ok) failures.push(what); };
try {
  await fetch(c.base + '/api/ops/account_create', { method: 'POST', headers: { 'content-type': 'application/json', authorization: 'Bearer ' + c.secret, 'x-fleet-person': 'owner@trial.test' },
    body: JSON.stringify({ name: 'codex-tight', harness: 'codex' }) });
  const page = await browser.newPage({ viewport: { width: 1440, height: 900 }, colorScheme: 'dark' });
  const errors: string[] = [];
  page.on('console', m => { if (m.type() === 'error') errors.push(m.text()); });
  await page.goto(origin + '/system/access');
  await page.waitForSelector('[aria-label=Access] [data-ui=data-table]', { timeout: 15000 });
  const text = await page.locator('[aria-label=Access]').innerText();
  check(/subscriptions · login on a host/i.test(text), 'subscriptions group shows');
  check(/codex-tight/.test(text) && /No host is signed in yet/.test(text), 'a subscription without a host says it needs a login');
  check(/model api keys/i.test(text) && /OpenAI/.test(text) && /Not added/.test(text), 'offered providers show Not added');
  check(/need you/.test(text), 'the head counts what needs the owner');
  if (shots) await page.screenshot({ path: shots + '/access-1440.png', fullPage: true });
  await page.getByRole('button', { name: 'Add access' }).click();
  await page.getByLabel('Provider').getByRole('button', { name: 'Anthropic' }).click();
  await page.getByRole('button', { name: 'Next' }).click();
  await page.waitForSelector('[data-ui=text-field] input[type=password]', { timeout: 10000 });
  const flow = await page.locator('[aria-label=Access]').innerText();
  check(/console\.anthropic\.com\/settings\/keys/.test(flow), 'the key flow names where the key comes from');
  check(/Paste the key on System › Access/.test(flow), 'the key flow says where the key goes');
  if (shots) await page.screenshot({ path: shots + '/access-add-key-1440.png', fullPage: true });
  check(errors.length === 0, `no console errors${errors.length ? ': ' + errors.slice(0, 3).join(' | ') : ''}`);
} finally {
  await browser.close();
  await close();
}
if (failures.length) { console.log(`${failures.length} failed`); process.exit(1); }
