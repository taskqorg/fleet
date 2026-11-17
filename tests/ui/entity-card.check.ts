// Entity references and their cards in the built app (docs/design-system/controls/entity-card.md; OR-38, OR-44).
// Called by conversations.check.ts; runs alone too: `pnpm run build && node tests/ui/entity-card.check.ts`.
// Requirement: a code opens its card on click or Enter (never on hover, never the row); the card shows the entity's state
// and title, copies its code and UUID, opens the entity; Escape, a click outside and the code again close it, focus going
// back to the code; a D17 code in an agent message is a reference too.
// Risk: the card hides behind a hover, a code click opens the row instead, focus is lost on close, or a UUID shows.
import assert from 'node:assert/strict';
import { pathToFileURL } from 'node:url';
import { chromium, type Browser } from 'playwright';
import { checkServer, type seeder } from './seed.ts';

const until = async (f: () => Promise<boolean>, ms = 8000) => { const s = Date.now(); while (Date.now() - s < ms) { if (await f()) return true; await new Promise(r => setTimeout(r, 50)); } return false; };

export async function checkEntityCard(browser: Browser, origin: string, seed: ReturnType<typeof seeder>) {
  const call = async (name: string, body: object) => {
    const r = await fetch(origin + '/api/ops/' + name, { method: 'POST', headers: { 'content-type': 'application/json' }, body: JSON.stringify(body) });
    const d: any = await r.json(); assert.equal(r.status, 200, JSON.stringify(d)); return d;
  };
  await call('person_settings_set', { focus: [], ignored: [] });
  const task = await call('task_create', { title: 'Entity card check task' });
  // A conversation whose agent message names the task by code.
  const sid = 'card-check-session';
  seed.put('session', { id: sid, createdAt: Date.now() });
  seed.put('execution', { id: 'card-x1', executionId: 'card-x1', sessionId: sid, hostId: 'pds18', state: 'completed', model: 'gpt-6.1-sol' });
  seed.put('message', { id: 'card-m1', sessionId: sid, executionId: 'card-x1', text: 'Status?' });
  seed.put('event', { id: 'card-e1', sessionId: sid, executionId: 'card-x1', type: 'item.completed', seq: 0, data: { fleetNormalized: { blocks: [{ kind: 'text', role: 'agent', markdown: `Finished ${task.code}; see the result.` }] } } });

  const context = await browser.newContext({ colorScheme: 'dark', viewport: { width: 1440, height: 900 } });
  await context.grantPermissions(['clipboard-read', 'clipboard-write'], { origin });
  const p = await context.newPage();
  const errors: string[] = []; p.on('pageerror', e => errors.push(e.message));
  try {
    await p.goto(origin + '/tasks');
    const code = p.locator('.vl-row', { hasText: 'Entity card check task' }).getByRole('button', { name: `Task ${task.code}: show details` });
    await code.waitFor();
    const card = p.getByRole('dialog', { name: `Task ${task.code}` });
    // Hover shows no card (the owner's click model); the code is a button with a popup.
    await code.hover(); await p.waitForTimeout(900);
    assert.equal(await card.count(), 0, 'hover opens no card');
    assert.equal(await code.getAttribute('aria-haspopup'), 'dialog');
    // A click opens the card, not the row; the card shows state, title and actions; no UUID is visible.
    await code.click(); await card.waitFor();
    assert.ok(!new URL(p.url()).pathname.startsWith('/tasks/'), 'the code click did not open the row');
    assert.equal(await code.getAttribute('aria-expanded'), 'true');
    await card.getByText('Entity card check task').waitFor();
    await card.getByRole('img', { name: 'Queued' }).waitFor();
    assert.ok(!(await card.innerText()).includes(task.id), 'the UUID is never shown');
    const box = (await card.boundingBox())!, at = (await code.boundingBox())!;
    assert.ok(box.y >= at.y + at.height - 1 || box.y + box.height <= at.y + 1, 'the card sits under or over its code');
    assert.ok(box.x >= 0 && box.x + box.width <= 1440, 'inside the window');
    // Copy code and UUID.
    await card.getByRole('button', { name: 'Copy code' }).click();
    assert.ok(await until(async () => await p.evaluate(() => navigator.clipboard.readText()) === task.code), 'code copied');
    await card.getByRole('button', { name: 'Copy UUID' }).click();
    assert.ok(await until(async () => await p.evaluate(() => navigator.clipboard.readText()) === task.id), 'UUID copied');
    // Escape closes and gives focus back to the code.
    await p.keyboard.press('Escape'); await card.waitFor({ state: 'detached' });
    assert.ok(await code.evaluate(e => document.activeElement === e), 'focus back on the code after Escape');
    // The code again closes it; a click outside closes it.
    await code.click(); await card.waitFor(); await code.click(); await card.waitFor({ state: 'detached' });
    await code.click(); await card.waitFor(); await p.mouse.click(700, 880); await card.waitFor({ state: 'detached' });
    // Keyboard: Enter opens it with focus on Open; Escape returns focus to the code.
    await code.focus(); await p.keyboard.press('Enter'); await card.waitFor();
    assert.ok(await until(() => card.getByRole('link', { name: 'Open' }).evaluate(e => document.activeElement === e)), 'focus moves to Open');
    await p.keyboard.press('Escape'); await card.waitFor({ state: 'detached' });
    assert.ok(await code.evaluate(e => document.activeElement === e), 'focus back on the code');
    // Open goes to the task.
    await code.click(); await card.getByRole('link', { name: 'Open' }).click();
    await p.waitForURL('**/tasks/' + task.id); assert.equal(await card.count(), 0);
    // A code in an agent message is a reference with the same card.
    await p.goto(origin + '/conversations/' + sid);
    const inText = p.locator('.md [data-ui=entity-code]', { hasText: task.code });
    await inText.waitFor(); await inText.click();
    await card.getByText('Entity card check task').waitFor();
    await p.keyboard.press('Escape');
    assert.deepEqual(errors, []);
  } finally { await context.close(); }
}

if (import.meta.url === pathToFileURL(process.argv[1]).href) {
  const { origin, seed, host, close } = await checkServer();
  await host('pds18');
  const b = await chromium.launch();
  try { await checkEntityCard(b, origin, seed); console.log('ok   entity card'); } finally { await b.close(); await close(); }
}
