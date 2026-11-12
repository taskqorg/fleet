// OR-32: Cmd+K opens the Spotlight palette; sections, projects and tasks come from their providers (tasks through the
// shared `search` operation), Enter opens the item in the work area, Escape closes it. Called by the Conversations CI browser entry.
import assert from 'node:assert/strict';
import type {Browser} from 'playwright';
export async function checkSpotlight(browser: Browser, origin: string) {
  const context = await browser.newContext({colorScheme: 'dark'}), p = await context.newPage();
  const errors: string[] = []; p.on('pageerror', e => errors.push(e.message));
  const r = await fetch(origin + '/api/ops/task_create', {method: 'POST', headers: {'content-type': 'application/json'}, body: JSON.stringify({title: 'Spotlight needle task'})});
  assert.equal(r.status, 200, await r.clone().text()); const created = await r.json() as any, task = created.id;
  try {
    await p.goto(origin + '/overview'); await p.getByRole('link', {name: 'Search'}).waitFor();
    const dialog = p.getByRole('dialog', {name: 'Search'});
    await p.keyboard.press('ControlOrMeta+k'); await dialog.waitFor();
    const input = dialog.getByRole('combobox', {name: 'Search'});
    await input.fill('host'); await dialog.getByRole('option', {name: 'Hosts'}).waitFor();
    await input.press('Enter'); await p.waitForURL('**/hosts'); assert.equal(await dialog.count(), 0);
    await p.getByRole('link', {name: 'Search'}).click(); await input.fill('needle');
    await dialog.getByRole('option', {name: /Spotlight needle task/}).waitFor();
    await input.press('Enter'); await p.waitForURL('**/tasks/' + task);
    // Code jump (D17): the code finds the task, shown with its code; Enter opens it.
    await p.goto(origin + '/overview'); await p.getByRole('link', {name: 'Search'}).waitFor();
    await p.keyboard.press('ControlOrMeta+k'); await input.fill(created.code.toLowerCase());
    await dialog.getByRole('option', {name: new RegExp(created.code)}).waitFor();
    await input.press('Enter'); await p.waitForURL('**/tasks/' + task);
    await p.keyboard.press('ControlOrMeta+k'); await input.fill('agent:nobody needle');
    await dialog.getByText('Nothing found').waitFor();
    await input.press('Escape'); assert.equal(await dialog.count(), 0);
    assert.deepEqual(errors, []); console.log('ok Spotlight Cmd+K: sections, tasks via search, code jump, Enter opens, Escape closes');
  } finally { await context.close(); }
}
