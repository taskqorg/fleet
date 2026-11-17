// Shell header and focus chip in the built app (owner rules OR-43, OR-31, OR-34; docs/design-system/patterns/app-shell.md,
// controls/focus-control.md). Called by conversations.check.ts.
// Requirement: one header right of the navigation shows the section title and a search icon that opens Spotlight; the focus
// chip is toned when a focus is set; an ignored project leaves the list and the picker, shows in "Ignored (n)" and can be
// restored; items that need the person outside the focus (never in an ignored project) show as "N need you outside focus"
// and open the Overview unfocused.
// Risk: the header loses the title or search, a set focus looks like "All projects", or an ignored project stays visible.
// With HEADER_SHOTS=<dir> it also saves dark screenshots at 1440x900 and 390 px: the header with All projects (header-all),
// with a focus of two projects and the outside indicator (header-focus), and the open panel with Ignored expanded (header-panel).
import assert from 'node:assert/strict';
import type { Browser } from 'playwright';
import type { seeder } from './seed.ts';

const until = async (f: () => Promise<boolean>, ms = 8000) => { const s = Date.now(); while (Date.now() - s < ms) { if (await f()) return true; await new Promise(r => setTimeout(r, 50)); } return false; };

export async function checkShellHeader(browser: Browser, origin: string, seed: ReturnType<typeof seeder>) {
  const call = async (name: string, body: object) => {
    const r = await fetch(origin + '/api/ops/' + name, { method: 'POST', headers: { 'content-type': 'application/json' }, body: JSON.stringify(body) });
    const d: any = await r.json(); assert.equal(r.status, 200, JSON.stringify(d)); return d;
  };
  const projects = [['hdr-alpha', 'Alpha'], ['hdr-bravo', 'Bravo'], ['hdr-charlie', 'Charlie']] as const;
  for (const [id, name] of projects)
    await call('project_create', { id, requestId: 'shell-header-' + id, content: { settings: { id, name, contextFiles: ['rules.md'], roles: { pm: 'pm' } }, contextFiles: [{ path: 'rules.md', text: 'x' }] } });
  for (const [id, name] of projects) seed.task({ title: `Header check ${name} task`, projectId: id });
  // One open question in Charlie: it needs the person outside a focus of Alpha and Bravo.
  seed.sql(`INSERT INTO question(id,kind,project_id,text,options,asked_by) VALUES('q-hdr-charlie','operator_decision','hdr-charlie','Ship Charlie today?','["yes","no"]','agent')`);
  const shots = process.env.HEADER_SHOTS;
  const context = await browser.newContext({ colorScheme: 'dark', viewport: { width: 1440, height: 900 } }), p = await context.newPage();
  const errors: string[] = []; p.on('pageerror', e => errors.push(e.message));
  const head = p.locator('[data-ui=shell-header]'), chip = head.locator('.focus-btn'), panel = head.getByRole('group', { name: 'Focus projects' });
  const row = (name: string) => p.locator('.vl-row', { hasText: `Header check ${name} task` });
  const shoot = async (name: string) => {
    if (!shots) return;
    if (name !== 'header-panel') await p.evaluate(() => (document.activeElement as HTMLElement | null)?.blur());
    for (const width of [1440, 390]) { await p.setViewportSize({ width, height: width > 400 ? 900 : 844 }); await p.waitForTimeout(250); await p.screenshot({ path: `${shots}/${name}-${width}.png` }); }
    await p.setViewportSize({ width: 1440, height: 900 });
  };
  try {
    await call('person_settings_set', { focus: [], ignored: [] });
    await p.goto(origin + '/tasks'); await row('Alpha').waitFor();
    assert.equal((await head.locator('h1').textContent())?.trim(), 'Tasks', 'the header shows the section title');
    assert.equal(await p.locator('.shell-main h1').count(), 0, 'the column has no heading of its own');
    // Search is the header icon (OR-31): it opens Spotlight, Escape closes it.
    const dialog = p.getByRole('dialog', { name: 'Search' });
    await head.getByRole('button', { name: 'Search' }).click(); await dialog.waitFor();
    assert.ok(await until(async () => await p.evaluate(() => document.activeElement?.getAttribute('role') === 'combobox')), 'the palette takes the focus');
    await p.keyboard.press('Escape'); assert.ok(await until(async () => await dialog.count() === 0), 'Escape closes the palette');
    assert.equal(await p.locator('[data-ui=nav-rail] a[href="#search"], [data-ui=nav-rail] [data-ui=focus-control]').count(), 0, 'the rail has no search item or focus');
    // All projects is neutral; a focus is toned and names its projects.
    assert.equal(await chip.getAttribute('aria-label'), 'Focus: All projects');
    const neutral = await chip.evaluate(e => getComputedStyle(e).backgroundColor);
    await shoot('header-all');
    await chip.click(); await panel.waitFor();
    await panel.getByRole('checkbox', { name: 'Alpha' }).check();
    await panel.getByRole('checkbox', { name: 'Bravo' }).check();
    assert.ok(await until(async () => await chip.getAttribute('aria-label') === 'Focus: Alpha, Bravo'), 'the chip names the focused projects');
    assert.ok(await until(async () => await row('Charlie').count() === 0), 'a project outside the focus leaves the list');
    await panel.getByRole('button', { name: 'Save focus preset' }).click();
    await panel.getByRole('textbox', { name: 'Preset name' }).fill('Core'); await p.keyboard.press('Enter');
    await p.keyboard.press('Escape'); assert.equal(await panel.count(), 0, 'Escape closes the panel');
    assert.ok(await p.locator('[data-ui=focus-control].set').count() === 1, 'a set focus marks the chip');
    assert.notEqual(await chip.evaluate(e => getComputedStyle(e).backgroundColor), neutral, 'a set focus is toned, unlike All projects');
    // What needs the person outside the focus: Charlie's question.
    const outside = head.locator('.focus-outside');
    assert.ok(await until(async () => await outside.count() === 1), 'the outside-focus indicator shows');
    assert.match(await outside.getAttribute('aria-label') ?? '', /^\d+ need you outside focus/);
    await shoot('header-focus');
    // Phone width: the header keeps its icons and pushes nothing out sideways (OR-43).
    await p.setViewportSize({ width: 390, height: 844 }); await p.waitForTimeout(250);
    const narrow = await p.evaluate(() => { const h = document.querySelector('[data-ui=shell-header]')!, r = h.getBoundingClientRect();
      return { out: [...h.children].filter(c => c.getBoundingClientRect().right > r.right + 1).map(c => c.className), page: document.documentElement.scrollWidth - innerWidth,
        icons: ['.focus-btn', '.focus-outside', '.shell-search'].every(s => (h.querySelector(s)?.getBoundingClientRect().width ?? 0) > 0) }; });
    assert.deepEqual(narrow, { out: [], page: 0, icons: true }, 'at 390 px the header fits with its icons');
    await p.setViewportSize({ width: 1440, height: 900 });
    // Ignore Bravo from its row in the panel: it leaves the focus, the list and the picker, and shows under Ignored.
    await chip.click(); await panel.waitFor();
    await panel.getByRole('button', { name: 'Ignore Bravo' }).click();
    assert.ok(await until(async () => await panel.getByRole('checkbox', { name: 'Bravo' }).count() === 0), 'an ignored project is not offered in the picker');
    assert.ok(await until(async () => await row('Bravo').count() === 0), 'an ignored project leaves the list');
    const ignoredHead = panel.getByRole('button', { name: /Ignored/ });
    assert.match((await ignoredHead.textContent()) ?? '', /Ignored\s*1/);
    await ignoredHead.click();
    await shoot('header-panel');
    // With every project in focus the ignored one stays hidden.
    await panel.getByRole('button', { name: 'All projects' }).click();
    assert.ok(await until(async () => await row('Charlie').count() === 1 && await row('Alpha').count() === 1), 'All projects shows the others');
    assert.equal(await row('Bravo').count(), 0, 'an ignored project stays hidden with All projects');
    const stored = await call('person_settings_get', {});
    assert.deepEqual([stored.ignored, stored.focus], [['hdr-bravo'], []], 'the ignore is stored on the server');
    await panel.getByRole('button', { name: 'Restore Bravo' }).click();
    assert.ok(await until(async () => await row('Bravo').count() === 1), 'a restored project shows again');
    await p.keyboard.press('Escape');
    // The outside indicator opens the Overview without the focus.
    await call('person_settings_set', { focus: ['hdr-alpha'] });
    await p.reload(); assert.ok(await until(async () => await outside.count() === 1), 'the indicator shows after a reload');
    // Other checks of the same run leave needs-you items of their own (some outside projects): compare counts, not zero.
    const n = async () => await outside.count() ? Number((await outside.getAttribute('aria-label'))?.split(' ')[0]) : 0;
    const before = await n();
    await outside.click(); await p.waitForURL('**/overview?all');
    await p.getByText('Ship Charlie today?').waitFor();
    assert.ok(await p.getByText('Focus not applied').count() === 1, 'its head says the focus is off');
    // Ignore is full silence: an ignored project's question is not counted outside the focus.
    await call('person_settings_set', { ignore: { project: 'hdr-charlie', ignored: true } });
    await p.goto(origin + '/tasks'); await row('Alpha').waitFor();
    assert.ok(await until(async () => await n() === before - 1, 5000), `an ignored project is not counted (${before} before, ${await n()} after)`);
    assert.deepEqual(errors, []);
    console.log('ok Shell header: title, search icon opens Spotlight, focus chip toned, ignore hides a project from list and picker and restores, outside-focus indicator opens the Overview');
  } finally {
    await call('person_settings_set', { focus: [], ignored: [], focusPresetDelete: 'Core' }).catch(() => {});
    await context.close();
  }
}

