// Projects section to its Target (docs/design-system/pages/projects.md): the catalogue, the cross-project summary, a
// project on Sessions, a task opened inside the Tasks facet, Results and Delivery facets, Rules & methods, and the
// earlier routes landing on the right facet; J4 (docs/designs/fleet-ia-and-journeys.md §3) walked through a project task
// with its execution, result and delivery. Not part of CI; run after `pnpm run build`:
//   node tests/ui/projects-facets.check.ts           (FLEET_BROWSER_SHOTS=<dir> keeps screenshots; FLEET_KEEP=1 leaves the server up)
import { mkdirSync } from 'node:fs';
import { randomUUID } from 'node:crypto';
import { chromium, type Page } from 'playwright';
import { checkServer } from './seed.ts';

const shots = process.env.FLEET_BROWSER_SHOTS;
if (shots) mkdirSync(shots, { recursive: true });
const { origin, seed, close } = await checkServer({ store: true });
const call = async (name: string, body: object) => {
  const r = await fetch(origin + '/api/ops/' + name, { method: 'POST', headers: { 'content-type': 'application/json' }, body: JSON.stringify(body) });
  if (r.status !== 200) throw Error(`${name}: ${r.status} ${await r.text()}`);
  return r.json() as Promise<any>;
};

// Three projects with codes; fleet has an environment (ready), the others not.
for (const [id, name, code, env] of [['fleet', 'fleet', 'FLT', true], ['cyprus', 'cyprus.bz', 'CBZ', false], ['tight', 'tight', 'TGT', false]] as const) {
  await call('project_create', { id, requestId: 'pf-' + id, content: { settings: { id, name, contextFiles: ['rules.md'], roles: { pm: 'pm' }, ...(env ? { environments: [{ hostId: 'pds18', checkoutPath: '/srv/fleet' }], integration: { mergeMethod: 'rebase' } } : {}) }, contextFiles: [{ path: 'rules.md', text: 'Rules.' }] } });
  await call('project_code_set', { projectId: id, code });
}
// J4: a PM creates a project task; an execution runs it, stores a result; one question waits for the person.
const now = Date.now();
const t1 = seed.task({ title: 'Fix receipt factory rounding', projectId: 'fleet', state: 'in_progress' });
seed.task({ title: 'Usability rules v2', projectId: 'fleet', state: 'queued' });
seed.task({ title: 'Retain initiating person', projectId: 'fleet', state: 'completed' });
seed.task({ title: 'Landing page copy', projectId: 'cyprus', state: 'in_progress' });
seed.task({ title: 'Import old bookings', projectId: 'tight', state: 'failed' });
seed.put('session', { id: 's-pm', projectId: 'fleet', createdAt: now - 7200e3, title: 'Home screen in three states', agentConfig: { id: 'pm', harness: 'claude' } });
seed.put('session', { id: 's-w1', projectId: 'fleet', taskId: t1, createdAt: now - 1800e3, title: 'Fix receipt factory: review running', agentConfig: { harness: 'codex' } });
seed.put('session', { id: 's-old', projectId: 'fleet', createdAt: now - 86400e3, title: 'Design board review: canvases drawn' });
seed.put('session', { id: 's-c', projectId: 'cyprus', createdAt: now - 600e3, title: 'Landing page copy draft' });
seed.execution({ id: 'x-pm', projectId: 'fleet', sessionId: 's-pm', hostId: 'pds18', state: 'waiting', startedAt: now - 600e3, harness: 'claude' });
seed.execution({ id: 'x-w1', projectId: 'fleet', taskId: t1, sessionId: 's-w1', hostId: 'pds18', state: 'running', startedAt: now - 700e3, harness: 'codex' });
seed.execution({ id: 'x-old', projectId: 'fleet', sessionId: 's-old', hostId: 'pds18', state: 'completed', startedAt: now - 90000e3, finishedAt: now - 86000e3, harness: 'claude' });
seed.execution({ id: 'x-c', projectId: 'cyprus', sessionId: 's-c', hostId: 'pds18', state: 'running', startedAt: now - 500e3, harness: 'claude' });
seed.question({ kind: 'operator_decision', text: 'Keep the compact card or the wide card with progress?', sessionId: 's-pm', executionId: 'x-pm', options: ['Compact', 'Wide'] });
seed.artifact({ id: randomUUID(), executionId: 'x-w1', name: 'report.md', mediaType: 'text/markdown', projectId: 'fleet', taskId: t1, bytes: Buffer.from('# Receipts\n\nReceipts now round after the currency split; 14 tests added.\n') });

if (process.env.FLEET_SERVE) { console.log('server at ' + origin); await new Promise(() => {}); }
const browser = await chromium.launch({ headless: true });
const failures: string[] = [];
const check = (ok: boolean, what: string) => { console.log(`${ok ? 'ok  ' : 'FAIL'} ${what}`); if (!ok) failures.push(what); };
const path = (p: Page) => new URL(p.url()).pathname + new URL(p.url()).search;
// Same-document navigations: wait for the address, and report where the page is when it does not get there.
const at = async (p: Page, ok: (path: string) => boolean, what: string) => {
  const end = Date.now() + 10_000;
  while (Date.now() < end && !ok(new URL(p.url()).pathname)) await p.waitForTimeout(100);
  if (!ok(new URL(p.url()).pathname)) throw Error(`${what}: the page is at ${path(p)}`);
};
const shot = async (p: Page, name: string) => { if (shots) await p.screenshot({ path: `${shots}/${name}.png` }); };
const errors: string[] = [];
try {
  const wide = await browser.newPage({ viewport: { width: 1440, height: 900 }, colorScheme: 'dark' });
  wide.on('pageerror', e => errors.push(e.message));
  await wide.goto(origin + '/projects');
  await wide.locator('[data-ui=project-work] [role=tab]', { hasText: 'Summary' }).waitFor();
  await wide.getByText('Keep the compact card or the wide card with progress?').first().waitFor();
  check(await wide.locator('.vl-row', { hasText: 'fleet' }).first().isVisible(), 'catalogue lists fleet');
  check(await wide.locator('.vl-row', { hasText: 'FLT' }).count() > 0, 'catalogue row shows the project code');
  await shot(wide, '1-idle-1440');

  await wide.locator('.vl-row a.title', { hasText: /^fleet$/ }).click();
  await at(wide, u => u === '/projects/FLT', 'wait');
  await wide.locator('[data-ui=session-card]').first().waitFor();
  check(await wide.locator('[role=tab][aria-selected=true]').textContent() === 'Sessions', 'a project opens on Sessions');
  check((await wide.locator('[data-ui=session-card]').first().textContent() ?? '').includes('PM'), 'the PM card comes first');
  check(await wide.getByRole('button', { name: 'Talk to PM' }).isVisible(), 'project head has Talk to PM');
  await shot(wide, '2-sessions-1440');

  await wide.getByRole('tab', { name: 'Tasks' }).click();
  await at(wide, u => u === '/projects/FLT/tasks', 'wait');
  await wide.locator('.pw-facet .vl-row', { hasText: 'Fix receipt factory rounding' }).waitFor();
  check(await wide.locator('.pw-facet .vl-row', { hasText: 'Landing page copy' }).count() === 0, 'the Tasks facet holds this project only');
  await wide.locator('.pw-facet .vl-row a.title', { hasText: 'Fix receipt factory rounding' }).click();
  await at(wide, u => /^\/projects\/FLT\/tasks\/T-FLT-\d+$/.test(u), 'wait');
  await wide.locator('.pw-facet [data-ui=detail] .detail-title', { hasText: 'Fix receipt factory rounding' }).waitFor();
  check(await wide.locator('[data-ui=project-work] > .dhead').isVisible(), 'the project head stays above the opened task');
  await shot(wide, '3-task-1440');
  await wide.goBack(); await at(wide, u => u === '/projects/FLT/tasks', 'wait');

  await wide.getByRole('tab', { name: 'Results' }).click();
  await at(wide, u => u === '/projects/FLT/results', 'wait');
  await wide.locator('.pw-facet .vl-row', { hasText: 'report' }).first().waitFor();
  await wide.locator('.pw-facet .vl-row a.title').first().click();
  await at(wide, u => /^\/projects\/FLT\/results\/R-FLT-\d+$/.test(u), 'wait');
  check(true, 'a result opens by its code: ' + path(wide));
  await shot(wide, '3b-result-1440');

  await wide.getByRole('tab', { name: 'Delivery' }).click();
  await at(wide, u => u === '/projects/FLT/delivery', 'wait');
  await shot(wide, '3c-delivery-1440');

  await wide.getByRole('tab', { name: 'Rules & methods' }).click();
  await at(wide, u => u === '/projects/FLT/rules', 'wait');
  await wide.locator('[data-ui=diff-row]').first().waitFor();
  await wide.locator('.pw-facet .detail-tabs, .pw-facet .dsec').first().waitFor();
  check(await wide.getByRole('button', { name: 'Save project' }).isVisible(), 'Rules & methods keeps the editor with Save');
  await shot(wide, '4-rules-1440');

  // Earlier routes land on the right facet.
  for (const [from, to] of [['/tasks', '/projects/tasks'], [`/tasks/${t1}`, /^\/projects\/FLT\/tasks\/T-FLT-\d+$/], ['/results', '/projects/results'], ['/delivery', '/projects/delivery'], ['/publishing', '/projects/delivery'], ['/projects/fleet', '/projects/FLT']] as const) {
    await wide.goto(origin + from);
    await at(wide, u => typeof to === 'string' ? u === to : to.test(u), from).catch(() => {});
    check(typeof to === 'string' ? new URL(wide.url()).pathname === to : to.test(new URL(wide.url()).pathname), `${from} lands on ${new URL(wide.url()).pathname}`);
  }

  const phone = await browser.newPage({ viewport: { width: 390, height: 844 }, colorScheme: 'dark', isMobile: true, hasTouch: true });
  phone.on('pageerror', e => errors.push(e.message));
  await phone.goto(origin + '/projects');
  await phone.locator('.vl-row', { hasText: 'fleet' }).first().waitFor();
  await shot(phone, '5-list-390');
  await phone.locator('.vl-row a.title', { hasText: /^fleet$/ }).click();
  await phone.locator('[data-ui=session-card]').first().waitFor();
  check(await phone.evaluate(() => document.scrollingElement!.scrollWidth <= innerWidth), '390 px: nothing scrolls sideways');
  await shot(phone, '6-sessions-390');
  await phone.getByRole('tab', { name: 'Tasks' }).click();
  await phone.locator('.pw-facet .vl-row a.title', { hasText: 'Fix receipt factory rounding' }).click();
  await phone.locator('.pw-facet [data-ui=detail] .detail-title', { hasText: 'Fix receipt factory rounding' }).waitFor();
  await shot(phone, '7-task-390');
  check(errors.length === 0, 'no page errors' + (errors.length ? ': ' + errors.join(' | ') : ''));
} finally {
  if (process.env.FLEET_KEEP) { console.log('server at ' + origin); await new Promise(() => {}); }
  await browser.close(); await close();
}
if (failures.length) { console.log(`${failures.length} failed`); process.exit(1); }
