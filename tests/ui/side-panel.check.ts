// Side panels on the built app (docs/design-system/controls/side-panel.md, OR-41, OR-42).
// Requirement: the navigation rail and the session's context panel are one side panel: the toggle sits in the shell header
// at a fixed place, the width is resized by its divider, and open or closed and the width are stored on the server per person
// and survive a reload, a session switch and Home. At phone width the context toggle opens the sheet and stores nothing.
// Risk: a second side panel built apart from the rail (taskqorg/fleet#868) reopens elsewhere, cannot be resized and forgets
// its state, and a source check cannot see it.
// Run by conversations.check.ts.
import type { Browser, Page } from 'playwright';
import type { seeder } from './seed.ts';

const S = 'sp-one', S2 = 'sp-two';
const box = async (p: Page, sel: string) => (await p.locator(sel).first().boundingBox()) ?? { x: -1, y: -1, width: 0, height: 0 };
const settings = (p: Page) => p.evaluate(() => fetch('/api/ops/person_settings_get', { method: 'POST', headers: { 'content-type': 'application/json' }, body: '{}' }).then(r => r.json())) as Promise<{ panels: Record<string, boolean>; widths: Record<string, number>; navCollapsed: boolean }>;
const until = async (f: () => Promise<boolean>, ms = 5000) => { for (const end = Date.now() + ms; Date.now() < end; await new Promise(r => setTimeout(r, 100))) if (await f()) return true; return false; };

export async function checkSidePanels(browser: Browser, origin: string, seed: ReturnType<typeof seeder>, check: (ok: boolean, what: string) => void) {
  seed.bulk(() => {
    for (const [id, title] of [[S, 'Side panel one'], [S2, 'Side panel two']]) {
      seed.put('session', { id, createdAt: Date.now() - 600e3, title, titleBy: 'auto', agentConfig: { id: 'claude-worker', harness: 'claude' } });
      seed.put('message', { id: id + '-m', sessionId: id, executionId: id + '-x', text: 'Check the side panel' });
      seed.put('execution', { id: id + '-x', executionId: id + '-x', sessionId: id, hostId: 'pds18', state: 'completed', startedAt: Date.now() - 500e3, finishedAt: Date.now() - 400e3, agentConfig: { harness: 'claude' } });
      seed.put('event', { id: id + '-e', sessionId: id, executionId: id + '-x', type: 'item.completed', seq: 0, data: { fleetNormalized: { blocks: [{ kind: 'text', role: 'agent', markdown: 'Done.' }] } } });
      seed.put('event', { id: id + '-end', sessionId: id, executionId: id + '-x', type: 'execution.ended', seq: 1, data: {} });
    }
  });
  const panel = '.session-view > [data-ui=side-panel]', toggle = '[data-ui=shell-header] .shell-panels button', search = '[data-ui=shell-header] .shell-search';
  const open = async (p: Page, path: string) => { await p.goto(origin + path); await p.locator('.sv-head').waitFor(); await p.locator(toggle).waitFor(); };

  const context = await browser.newContext({ colorScheme: 'dark', viewport: { width: 1440, height: 900 } }), p = await context.newPage();
  await open(p, `/conversations/${S}`);
  check(await p.locator(panel).isVisible(), 'side panel: the context panel is open by default, as a column');
  check(await p.locator(toggle).getAttribute('aria-label') === 'Hide context', 'side panel: the context toggle is in the shell header after search');
  const t0 = await box(p, toggle), s0 = await box(p, search);
  check(t0.x > s0.x, 'side panel: the context toggle comes after search');
  await p.locator(toggle).click();
  check(await until(async () => !(await p.locator(panel).isVisible())), 'side panel: the toggle closes the column');
  check(!(await p.locator('dialog.side-sheet').evaluate(d => (d as HTMLDialogElement).open)), 'side panel: closing at 1440 px opens no sheet');
  const t1 = await box(p, toggle), s1 = await box(p, search);
  check(t1.x === t0.x && s1.x === s0.x, `side panel: toggle and search keep their place (${t0.x}->${t1.x}, ${s0.x}->${s1.x})`);
  check(await until(async () => (await settings(p)).panels['session-context'] === false), 'side panel: closed is stored on the server per person');
  await p.locator(toggle).click();
  check(await until(() => p.locator(panel).isVisible()), 'side panel: the toggle opens the column again, in the same place');
  check(!(await p.locator('dialog.side-sheet').evaluate(d => (d as HTMLDialogElement).open)), 'side panel: reopening at 1440 px opens the column, not a sheet');
  await p.locator(toggle).click();
  await until(async () => (await settings(p)).panels['session-context'] === false);
  await p.reload(); await p.locator(toggle).waitFor();
  check(!(await p.locator(panel).isVisible()) && await p.locator(toggle).getAttribute('aria-label') === 'Show context', 'side panel: closed survives a reload');
  await p.locator(toggle).click(); await p.locator(panel).waitFor();

  // Resize: the divider on the panel's left edge drags it wider; the width is stored and kept after a reload, another session and Home.
  const w0 = (await box(p, panel)).width, d = await box(p, `${panel} > [role=separator]`);
  await p.mouse.move(d.x + d.width / 2, d.y + 200); await p.mouse.down(); await p.mouse.move(d.x + d.width / 2 - 80, d.y + 200, { steps: 5 }); await p.mouse.up();
  const w1 = (await box(p, panel)).width;
  check(Math.abs(w1 - w0 - 80) <= 2, `side panel: the divider resizes the column (${w0} -> ${w1})`);
  check(await until(async () => Math.abs(((await settings(p)).widths['session-context'] ?? 0) - w1) <= 1), 'side panel: the width is stored on the server per person');
  for (const path of [`/conversations/${S}`, `/conversations/${S2}`, `/home/${S2}`]) {
    await open(p, path); await p.locator(panel).waitFor();
    check(Math.abs((await box(p, panel)).width - w1) <= 1, `side panel: ${path} shows the column at the stored width`);
  }
  await p.locator(`${panel} > [role=separator]`).dblclick();
  check(await until(async () => Math.abs((await box(p, panel)).width - w0) <= 1), 'side panel: a double-click on the divider resets the width');

  // The navigation is the same side panel: collapsed and its width are stored and survive a reload.
  await open(p, `/conversations/${S}`);
  const r = await box(p, '[data-ui=nav-rail]'), rd = await box(p, '.shell-col > [role=separator]');
  await p.mouse.move(rd.x + rd.width / 2, rd.y + 300); await p.mouse.down(); await p.mouse.move(rd.x + rd.width / 2 + 40, rd.y + 300, { steps: 5 }); await p.mouse.up();
  const rw = (await box(p, '[data-ui=nav-rail]')).width;
  check(Math.abs(rw - r.width - 40) <= 2, `side panel: the navigation divider resizes it (${r.width} -> ${rw})`);
  await until(async () => Math.abs(((await settings(p)).widths.nav ?? 0) - rw) <= 1);
  await p.locator('.nav-toggle button').click();
  await until(async () => (await settings(p)).navCollapsed);
  await p.reload(); await p.locator(toggle).waitFor();
  check(await p.locator('[data-ui=nav-rail].mini').count() === 1, 'side panel: the collapsed navigation survives a reload');
  await p.locator('.nav-toggle button').click();
  check(await until(async () => Math.abs((await box(p, '[data-ui=nav-rail]')).width - rw) <= 1), 'side panel: the navigation expands at its stored width');
  await p.locator('.shell-col > [role=separator]').dblclick();
  await until(async () => !(await settings(p)).widths.nav);
  await context.close();

  // Phone: the same toggle opens the sheet; it closes on Escape and stores nothing.
  const phone = await browser.newContext({ colorScheme: 'dark', viewport: { width: 390, height: 844 }, hasTouch: true }), q = await phone.newPage();
  await open(q, `/home/${S}`);
  const before = (await settings(q)).panels['session-context'];
  check(!(await q.locator(panel).isVisible()), 'side panel: no column at 390 px');
  await q.locator(toggle).click();
  check(await until(() => q.locator('dialog.side-sheet[open] [data-ui=context-panel]').isVisible()), 'side panel: at 390 px the toggle opens the sheet');
  await q.keyboard.press('Escape');
  check(await until(async () => !(await q.locator('dialog.side-sheet[open]').count())), 'side panel: Escape closes the sheet');
  check((await settings(q)).panels['session-context'] === before, 'side panel: the sheet leaves the stored state as it is');
  await phone.close();
}
