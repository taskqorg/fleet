// Navigation counters in the built app (docs/design-system/controls/nav-rail.md, Counters; owner rules OR-34, OR-42).
// Called by conversations.check.ts; alone: pnpm run build && node tests/ui/nav-counter.check.ts.
// Requirement: each section's counter shows its counter view's count from nav_counts: one number without a focus; with a
// focus the in-focus count bright, a separator and the outside remainder dim, said in words; collapsed only the bright
// number as a badge on the icon, with no icon moving; the saved-views menu chooses or stops the counted view; a new item
// reaches the counter through the change stream.
// Risk: the rail shows a number other than the server's, hides the focus split, moves an icon on collapse, or stays stale.
// With NAV_SHOTS=<dir> it also saves dark screenshots at 1440x900 and 390 px: no focus (nav-all), a focus split
// (nav-focus) and collapsed with the focus (nav-collapsed).
import assert from 'node:assert/strict';
import type { Browser } from 'playwright';
import type { seeder } from './seed.ts';

const until = async (f: () => Promise<boolean>, ms = 10_000) => { const s = Date.now(); while (Date.now() - s < ms) { if (await f()) return true; await new Promise(r => setTimeout(r, 100)); } return false; };

export async function checkNavCounter(browser: Browser, origin: string, seed: ReturnType<typeof seeder>) {
  const call = async (name: string, body: object) => {
    const r = await fetch(origin + '/api/ops/' + name, { method: 'POST', headers: { 'content-type': 'application/json' }, body: JSON.stringify(body) });
    const d: any = await r.json(); assert.equal(r.status, 200, JSON.stringify(d)); return d;
  };
  for (const [id, name] of [['nc-alpha', 'Alpha'], ['nc-bravo', 'Bravo']])
    await call('project_create', { id, requestId: 'nav-counter-' + id, content: { settings: { id, name, contextFiles: ['rules.md'], roles: { pm: 'pm' } }, contextFiles: [{ path: 'rules.md', text: 'x' }] } });
  for (let i = 0; i < 2; i++) seed.task({ title: `Nav counter Alpha ${i}`, projectId: 'nc-alpha' });
  for (let i = 0; i < 3; i++) seed.task({ title: `Nav counter Bravo ${i}`, projectId: 'nc-bravo' });
  // A result in each project, a running conversation in Bravo and a host that never polled (offline).
  seed.sql(`INSERT INTO artifact(id,project_id,scope,content_hash,size,media_type,name,state) VALUES ('nc-a1','nc-alpha','prod','nc1',1,'text/plain','a.txt','stored'),
    ('nc-b1','nc-bravo','prod','nc2',1,'text/plain','b.txt','stored'),('nc-b2','nc-bravo','prod','nc3',1,'text/plain','c.txt','stored')`);
  seed.put('session', { id: 'nc-s1', projectId: 'nc-bravo' });
  seed.execution({ id: 'nc-e1', kind: 'agent_run', state: 'running', projectId: 'nc-bravo', sessionId: 'nc-s1' });
  await call('hosts_register', { id: 'nc-offline' });
  const shots = process.env.NAV_SHOTS;
  const context = await browser.newContext({ colorScheme: 'dark', viewport: { width: 1440, height: 900 } }), p = await context.newPage();
  const errors: string[] = []; p.on('pageerror', e => errors.push(e.message));
  const rail = p.locator('[data-ui=nav-rail]'), count = rail.locator('.rail-g:not(.pinned) [data-id=projects] .nav-count');
  // Other checks of the same run seed tasks of their own: the server's count is the expectation, never a fixed number.
  const server = async () => (await call('nav_counts', {})).sections.tasks as { inFocus: number; outside: number; label: string };
  const shows = async (text: string, label: string) => await count.count() === 1 && (await count.textContent())?.trim() === text && await count.getAttribute('aria-label') === label;
  const shoot = async (name: string) => {
    if (!shots) return;
    for (const width of [1440, 390]) { await p.setViewportSize({ width, height: width > 400 ? 900 : 844 }); await p.waitForTimeout(300); await p.screenshot({ path: `${shots}/${name}-${width}.png` }); }
    await p.setViewportSize({ width: 1440, height: 900 });
  };
  try {
    await call('person_settings_set', { focus: [], ignored: [], navCollapsed: false, navCounters: { tasks: '', conversations: '', delivery: '', results: '', hosts: '' } });
    await p.goto(origin + '/tasks'); await p.locator('.vl-row', { hasText: 'Nav counter Alpha 0' }).waitFor();
    // Without a focus: one number, "N running".
    let s = await server();
    assert.ok(s.inFocus >= 5 && s.outside === 0);
    assert.ok(await until(() => shows(s.inFocus.toLocaleString('en'), `${s.inFocus} running`)), `one number without a focus (${await count.textContent()})`);
    // A new task reaches the counter through the change stream, without a reload.
    seed.task({ title: 'Nav counter Alpha live', projectId: 'nc-alpha' });
    // The list shows it first: the change stream delivered it (its own timing is the list checks' concern).
    await p.locator('.vl-row', { hasText: 'Nav counter Alpha live' }).waitFor({ timeout: 30_000 });
    assert.ok(await until(async () => (await server()).inFocus === s.inFocus + 1 && await shows(String(s.inFocus + 1), `${s.inFocus + 1} running`)), `a new task is counted live (${await count.textContent()} shown, ${(await server()).inFocus} on the server, ${s.inFocus} before)`);
    // Hosts: offline hosts, one number in attention tone (hosts have no project).
    const hosts = rail.locator('.rail-g:not(.pinned) [data-id=system] .nav-count');
    assert.ok(await until(async () => await hosts.count() === 1 && await hosts.evaluate(e => e.classList.contains('t-attention')) && /offline$/.test(await hosts.getAttribute('aria-label') ?? '')), 'offline hosts in attention tone');
    await shoot('nav-all');
    // Focus Alpha: bright in focus, dim outside after the separator, said in words.
    await call('person_settings_set', { focus: ['nc-alpha'] });
    await p.reload();
    s = await server();
    assert.deepEqual([s.inFocus, s.outside >= 3], [3, true]);
    assert.ok(await until(() => shows(`${s.inFocus}·${s.outside}`, `${s.inFocus} running in focus, ${s.outside} outside`)), `the focus split (${await count.textContent()})`);
    const tone = await count.evaluate(e => [getComputedStyle(e.querySelector('.nav-in')!).color, getComputedStyle(e.querySelector('.nav-out')!).color]);
    assert.notEqual(tone[0], tone[1], 'the outside remainder is dim');
    await shoot('nav-focus');
    // Collapsed: only the bright number, as a badge on the icon; every icon keeps its place (OR-42).
    const icons = () => rail.locator('.nav-ic').evaluateAll(es => es.map(e => { const r = e.getBoundingClientRect(); return [Math.round(r.left), Math.round(r.top)]; }));
    const open = await icons();
    await p.locator('[data-ui=shell-header] .nav-toggle button').click();
    assert.ok(await until(async () => await rail.evaluate(e => e.classList.contains('mini'))), 'the rail collapses');
    assert.deepEqual(await icons(), open, 'no icon moves on collapse');
    assert.equal(await count.locator('.nav-out').isVisible(), false, 'collapsed: the outside remainder hides');
    assert.equal((await count.locator('.nav-in').textContent())?.trim(), String(s.inFocus));
    const badge = await count.evaluate(e => { const a = e.closest('a')!.getBoundingClientRect(), b = e.getBoundingClientRect(); return getComputedStyle(e).position === 'absolute' && b.right <= a.right + 8 && b.left >= a.left; });
    assert.ok(badge, 'collapsed: a badge on the icon');
    await shoot('nav-collapsed');
    await p.locator('[data-ui=shell-header] .nav-toggle button').click();
    assert.ok(await until(async () => !(await rail.evaluate(e => e.classList.contains('mini')))));
    // The saved-views menu chooses the counted view, and stops counting it.
    const needs = p.locator('[data-ui=saved-views] button', { hasText: 'Needs me' });
    await needs.click({ button: 'right' });
    await p.getByRole('menuitem', { name: 'Count in navigation' }).click();
    assert.ok(await until(async () => (await call('person_settings_get', {})).navCounters.tasks === 'Needs me'), 'the choice is stored on the server');
    assert.ok(await until(async () => { const x = await server(); return x.label === 'need me' && (x.inFocus + x.outside === 0 ? await count.count() === 0 : /need me in focus/.test(await count.getAttribute('aria-label') ?? '')); }), 'the counter counts the chosen view');
    await needs.click({ button: 'right' });
    await p.getByRole('menuitem', { name: 'Stop counting in navigation' }).click();
    assert.ok(await until(async () => (await call('person_settings_get', {})).navCounters.tasks === '-'), 'off is stored on the server');
    assert.ok(await until(async () => await count.count() === 0), 'a counter turned off shows nothing');
    const running = p.locator('[data-ui=saved-views] button', { hasText: 'Running' });
    await running.click({ button: 'right' });
    await p.getByRole('menuitem', { name: 'Count in navigation' }).click();
    assert.ok(await until(async () => (await call('person_settings_get', {})).navCounters.tasks === undefined), 'choosing the default stores no choice');
    assert.ok(await until(async () => await count.count() === 1), 'the default counter is back');
    assert.deepEqual(errors, []);
    console.log('ok Navigation counter: the server count, the focus split in words, a collapsed badge with no icon moving, chosen and stopped from the views menu, live');
  } finally {
    await call('person_settings_set', { focus: [], navCollapsed: false, navCounters: { tasks: '' } }).catch(() => {});
    await context.close();
  }
}

if (import.meta.main) {
  const { chromium } = await import('playwright');
  const { checkServer } = await import('./seed.ts');
  const { origin, seed, close } = await checkServer();
  const b = await chromium.launch();
  try { await checkNavCounter(b, origin, seed); } finally { await b.close(); await close(); }
}
