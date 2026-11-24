// OR-32: Cmd+K opens the Spotlight palette; sections, projects and tasks come from their providers (tasks through the
// shared `search` operation), Enter opens the item in the work area, Escape closes it. The palette keeps its query, results
// and highlighted row while closed (per tab, in memory), finds conversations, and its "All N" row opens the list with the
// query (OR-36). Called by the Conversations CI browser entry, whose seed holds the "Alpha question" conversation.
import assert from 'node:assert/strict';
import type {Browser} from 'playwright';
export async function checkSpotlight(browser: Browser, origin: string) {
  const context = await browser.newContext({colorScheme: 'dark'}), p = await context.newPage();
  const errors: string[] = []; p.on('pageerror', e => errors.push(e.message));
  const call = async (name: string, body: object) => {
    const r = await fetch(origin + '/api/ops/' + name, {method: 'POST', headers: {'content-type': 'application/json'}, body: JSON.stringify(body)});
    assert.equal(r.status, 200, await r.clone().text()); return await r.json() as any;
  };
  // Every project: an earlier check may have left a focus, which would move these project-less tasks outside it.
  const before = (await call('person_settings_get', {})).focus ?? [];
  await call('person_settings_set', {focus: []});
  const created = await call('task_create', {title: 'Spotlight needle task'}), task = created.id;
  // More bulk tasks than the palette shows (8 per kind), so the Tasks group ends with "All 10 in Tasks".
  for (let i = 0; i < 10; i++) await call('task_create', {title: `Spotlight bulk ${i}`});
  try {
    await p.goto(origin + '/overview'); await p.getByRole('button', {name: 'Search', exact: true}).waitFor();
    const dialog = p.getByRole('dialog', {name: 'Search'});
    const open = async () => { for (let i = 0; i < 10 && !await dialog.isVisible(); i++) { await p.keyboard.press('ControlOrMeta+k'); await dialog.waitFor({timeout: 1000}).catch(() => {}); } };
    // The shortcut listener mounts after the first paint; press again until the palette shows (a race in the check, not the app).
    await open();
    const input = dialog.getByRole('combobox', {name: 'Search'});
    // Empty: the focus presets and the query terms.
    await dialog.getByRole('group', {name: 'Focus presets'}).getByRole('button', {name: 'All projects'}).waitFor();
    await dialog.getByRole('button', {name: 'agent:'}).waitFor();
    // The field has focus when the palette opens: typing right after the shortcut goes into it.
    await p.keyboard.type('host'); await dialog.getByRole('option', {name: 'Hosts'}).waitFor();
    await input.press('Enter'); await p.waitForURL('**/hosts'); assert.equal(await dialog.count(), 0);
    await p.getByRole('button', {name: 'Search', exact: true}).click(); await input.fill('needle');
    await dialog.getByRole('option', {name: /Spotlight needle task/}).waitFor();
    await input.press('Enter'); await p.waitForURL('**/tasks/' + task);
    // Code jump (D17): the code finds the task, shown with its code; Enter opens it.
    await p.goto(origin + '/overview'); await p.getByRole('button', {name: 'Search', exact: true}).waitFor();
    await open();
    await input.fill(created.code.toLowerCase());
    await dialog.getByRole('option', {name: new RegExp(created.code + '(?!\\d)')}).waitFor();
    await input.press('Enter'); await p.waitForURL('**/tasks/' + task);
    // Kept across navigation: the reopened palette has the same query, selected, so typing replaces it.
    await p.keyboard.press('ControlOrMeta+k'); await dialog.waitFor();
    assert.equal(await input.inputValue(), created.code.toLowerCase(), 'the query is kept after opening an item');
    await p.keyboard.type('agent:nobody needle');
    assert.equal(await input.inputValue(), 'agent:nobody needle', 'typing replaces the selected query');
    await dialog.getByText('Nothing found').waitFor();
    await input.press('Escape'); assert.equal(await dialog.count(), 0);
    // Close and reopen: the same query (selected), the same results at once, the same highlighted row.
    await p.keyboard.press('ControlOrMeta+k'); await dialog.waitFor(); await input.fill('spotlight');
    await dialog.getByRole('option', {name: /Spotlight bulk 9/}).waitFor();
    await dialog.getByRole('option', {name: /All \d+ in Tasks/}).waitFor();
    await input.press('ArrowDown'); await input.press('ArrowDown');
    // Rows by title and code, without the age, which counts on in seconds between the two reads.
    const titles = () => dialog.locator('[role=option]').evaluateAll(os => os.map(o => [o.querySelector('.title')?.textContent, o.querySelector('.id')?.textContent, o.getAttribute('aria-selected')].join('|')));
    const rows = await titles();
    await input.press('Escape'); assert.equal(await dialog.count(), 0);
    await p.keyboard.press('ControlOrMeta+k'); await dialog.waitFor();
    assert.deepEqual(await titles(), rows, 'the results show again at once, with the same row highlighted');
    assert.deepEqual(await input.evaluate((e: HTMLInputElement) => [e.value, e.selectionStart, e.selectionEnd, document.activeElement === e]), ['spotlight', 0, 9, true],
      'the kept query is focused and selected');
    // A conversation is found by its first message and opens in the work area.
    await input.fill('Alpha question');
    await dialog.getByRole('option', {name: /Alpha question/}).waitFor();
    await input.press('Enter'); await p.waitForURL('**/conversations/task-a');
    // "All N in Tasks" opens the Tasks list with the same query as its search (OR-36), showing every match.
    await p.keyboard.press('ControlOrMeta+k'); await dialog.waitFor(); await input.fill('spotlight bulk');
    const all = dialog.getByRole('option', {name: 'All 10 in Tasks'}); await all.waitFor();
    await all.click(); await p.waitForURL(u => u.pathname === '/projects/tasks' && u.searchParams.get('q') === 'spotlight bulk');
    for (let i = 0; i < 10; i++) await p.getByText(`Spotlight bulk ${i}`, {exact: true}).first().waitFor();
    assert.deepEqual(errors, []); console.log('ok Spotlight Cmd+K: sections, tasks via search, code jump, kept query and results, conversation, All N opens the list');
  } finally { await context.close(); await call('person_settings_set', {focus: before}); }
}
