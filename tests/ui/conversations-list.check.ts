// Conversations list controls (docs/design-system/pages/conversations.md, list-view pattern; OR-12, OR-33, OR-35, OR-37):
// a system preset sets only the state filter and keeps the grouping, group and sort change the server order, a pinned row
// stays under a filter that does not match it, checkboxes show only in selection mode, and the Archived toggle switches to
// archived conversations, where the bar offers Unarchive. Called by conversations.check.ts.
// With CONV_SHOTS=<dir> it also saves three dark screenshots: the grouped list (conv-a.png), selection mode (conv-b.png) and
// the archived list (conv-archived.png).
import assert from 'node:assert/strict';
import type { Browser } from 'playwright';
import type { seeder } from './seed.ts';

const until = async (f: () => Promise<boolean>, ms = 8000) => { const s = Date.now(); while (Date.now() - s < ms) { if (await f()) return true; await new Promise(r => setTimeout(r, 50)); } return false; };

export async function checkConversationsList(browser: Browser, origin: string, seed: ReturnType<typeof seeder>) {
  // Six conversations in two projects and one outside projects; activity from 1 to 6 hours ago, so the default order is known.
  const rows = [
    ['cl-1', 'site-redesign', 'Refresh the landing hero copy', 'running', 'claude', 1],
    ['cl-2', 'site-redesign', 'Compare three pricing layouts', 'waiting', 'codex', 2],
    ['cl-3', 'billing-api', 'Fix the invoice rounding bug', 'failed', 'codex', 3],
    ['cl-4', 'billing-api', 'Write the webhook retry notes', 'succeeded', 'claude', 4],
    ['cl-5', null, 'Plan the next release train', null, null, 5],
    ['cl-6', 'site-redesign', 'Audit image sizes on the blog', 'succeeded', 'codex', 6],
  ] as const;
  seed.bulk(() => {
    for (const [id, project, title, state, harness, hours] of rows) {
      seed.put('session', { id, ...(project ? { projectId: project } : {}), createdAt: Date.now() - hours * 3600e3, agentConfig: { harness: harness ?? 'claude' } });
      seed.put('message', { id: id + '-m', sessionId: id, ...(project ? { projectId: project } : {}), text: title });
      // Executions count as activity by their start time.
      const at = `now()-interval '${hours} hours'`;
      // The last one inserted (x0) gives the conversation its state.
      if (state) for (let i = id === 'cl-1' ? 2 : 0; i >= 0; i--) seed.sql(`INSERT INTO execution(id,kind,state,project_id,session_id,host_id,harness,started_at) VALUES('${id}-x${i}','agent_run','${i ? 'succeeded' : state}',${project ? `'${project}'` : 'NULL'},'${id}','pds18','${harness}',${at}-interval '${i} minutes')`);
      seed.sql(`UPDATE document SET updated_at=${at} WHERE id IN ('${id}','${id}-m')`);
    }
  });
  seed.question({ kind: 'agent_question', text: 'Keep the old layout as a fallback?', options: ['yes', 'no'], executionId: 'cl-2-x0', sessionId: 'cl-2', askedBy: 'agent' });

  const context = await browser.newContext({ colorScheme: 'dark', viewport: { width: 1440, height: 900 } }), p = await context.newPage();
  const errors: string[] = []; p.on('pageerror', e => errors.push(e.message));
  const titles = () => p.locator('.vl-row .row-link').allTextContents();
  const mine = async () => (await titles()).filter(t => rows.some(r => r[2] === t));
  // Live executions (running, waiting) may still change while the check runs: order is asserted on the other rows.
  const settled = async () => (await mine()).filter(t => rows.some(r => r[2] === t && (r[3] === null || !['running', 'waiting'].includes(r[3]))));
  const groups = () => p.locator('.vl-row.grp').allTextContents();
  const url = () => new URL(p.url());
  const settle = async (f: () => Promise<boolean>, what: string) => assert.ok(await until(f), what);
  try {
    await p.goto(origin + '/conversations'); await p.waitForSelector('.vl-row .row-link');
    // Default: grouped by project, latest activity first inside each group; projects in key order, no project last.
    await settle(async () => (await mine()).length === rows.length, 'all seeded conversations listed');
    assert.deepEqual(await settled(), ['Fix the invoice rounding bug', 'Write the webhook retry notes', 'Audit image sizes on the blog', 'Plan the next release train']);
    const heads = await groups();
    assert.ok(heads.some(h => h.includes('site-redesign') && h.includes('3')) && heads.some(h => h.includes('billing-api')) && heads.some(h => h.includes('No project')), 'project group headers with counts: ' + heads.join(' | '));
    // OR-37: no checkbox outside selection mode; the facts show executions, open questions and the agent.
    assert.equal(await p.locator('.vl-row .row-check').count(), 0, 'checkboxes outside selection mode');
    const hero = p.locator('.vl-row', { hasText: 'Refresh the landing hero copy' });
    assert.ok(await hero.locator('.row-facts .fact[data-tip="3 executions"]').count() === 1, 'executions fact');
    assert.ok(await hero.locator('.row-facts [data-tip="Last activity"]').count() === 1, 'last activity fact');
    assert.ok(await p.locator('.vl-row', { hasText: 'Compare three pricing layouts' }).locator('.row-facts .t-attention').textContent() === '1', 'open questions fact');

    // Group by state (attention order), then the system preset "Needs me": only the states change, the grouping stays.
    await p.locator('[data-ui=group-control] .pick-b').click();
    await p.locator('[data-ui=group-control] [role=menuitemradio]', { hasText: 'State' }).click();
    await settle(async () => url().searchParams.get('group') === 'state' && (await groups())[0]?.includes('Waiting'), 'grouped by state, waiting first');
    assert.deepEqual((await mine()).slice(0, 2), ['Compare three pricing layouts', 'Fix the invoice rounding bug']);
    await p.locator('[data-ui=saved-views] button', { hasText: 'Needs me' }).click();
    await settle(async () => url().searchParams.get('state') === 'failed,waiting', 'Needs me sets the states');
    assert.equal(url().searchParams.get('group'), 'state', 'Needs me kept the grouping');
    await settle(async () => (await mine()).join() === 'Compare three pricing layouts,Fix the invoice rounding bug', 'Needs me lists waiting and failed');
    assert.ok(await p.locator('[data-ui=saved-views] button[aria-pressed=true]', { hasText: 'Needs me' }).count() === 1, 'Needs me shows active');

    // No grouping, then the sort direction: the server order flips (oldest activity first).
    await p.goto(origin + '/conversations?group=none'); await p.waitForSelector('.vl-row .row-link');
    const newest = ['Fix the invoice rounding bug', 'Write the webhook retry notes', 'Plan the next release train', 'Audit image sizes on the blog'];
    await settle(async () => (await groups()).length === 0 && (await settled()).join() === newest.join(), 'ungrouped list, latest activity first');
    await p.locator('[data-ui=sort-control] .pick-dir').click();
    await settle(async () => url().searchParams.get('sort') === 'activity' && (await settled()).join() === [...newest].reverse().join(), 'oldest activity first after the flip');
    await p.locator('[data-ui=sort-control] .pick-dir').click();
    await settle(async () => !url().searchParams.has('sort'), 'sort back to the default');

    // Pin by the row menu key: the pinned row stays first under a search that matches nothing.
    await p.goto(origin + '/conversations'); await p.waitForSelector('.vl-row .row-link');
    await p.locator('.vl-row .row-link', { hasText: 'Write the webhook retry notes' }).focus(); await p.keyboard.press('p');
    await p.waitForSelector('[data-ui=row-pin]');
    assert.equal(await p.locator('.vl-row:has([data-ui=row-pin]) .row-link').first().textContent(), 'Write the webhook retry notes');
    assert.ok((await groups())[0].includes('Pinned'), 'Pinned group first');
    await p.goto(origin + '/conversations?q=no-such-conversation'); await p.waitForSelector('[data-ui=row-pin]', { timeout: 10000 });
    assert.equal(await p.locator('.vl-row:has([data-ui=row-pin]) .row-link').first().textContent(), 'Write the webhook retry notes', 'pinned row kept under a search');
    await p.goto(origin + '/conversations'); await p.waitForSelector('[data-ui=row-pin]');
    await settle(async () => (await mine()).length === rows.length, 'list back');
    if (process.env.CONV_SHOTS) await p.screenshot({ path: process.env.CONV_SHOTS + '/conv-a.png' });

    // Selection mode: the Select square opens the checkbox column and the action bar; Shift-click and x select too.
    await p.getByRole('button', { name: 'Select', exact: true }).click();
    await settle(async () => await p.locator('.vl-row .row-check').count() > 0 && await p.locator('[data-ui=select-bar]').count() === 1, 'checkboxes in selection mode');
    await p.locator('.vl-row .row-link', { hasText: 'Plan the next release train' }).click();
    await p.locator('.vl-row .row-link', { hasText: 'Audit image sizes on the blog' }).focus(); await p.keyboard.press('x');
    await settle(async () => (await p.locator('[data-ui=select-bar]').textContent())!.includes('2 selected'), '2 selected');
    if (process.env.CONV_SHOTS) { await p.evaluate(() => (document.activeElement as HTMLElement | null)?.blur()); await p.screenshot({ path: process.env.CONV_SHOTS + '/conv-b.png' }); }
    await p.getByRole('button', { name: 'Leave selection', exact: true }).click();
    await settle(async () => await p.locator('.vl-row .row-check').count() === 0, 'checkboxes gone after leaving');
    await p.locator('.vl-row .row-link', { hasText: 'Plan the next release train' }).click({ modifiers: ['Shift'] });
    await settle(async () => await p.locator('.vl-row .row-check:checked').count() === 1, 'Shift-click enters selection mode');
    // Archive from the bar (session_archive per conversation), confirmed inline; the row leaves the list.
    await p.locator('[data-ui=select-bar] button[aria-label=Archive]').click();
    await p.getByRole('button', { name: 'Yes, archive' }).click();
    await settle(async () => !(await mine()).includes('Plan the next release train'), 'archived conversation left the list');
    // The Archived toggle (pressed while on) puts archived=1 in the URL and lists the archived conversation; the pinned
    // active row stays out of it.
    const archivedToggle = p.getByRole('button', { name: 'Archived', exact: true });
    assert.equal(await archivedToggle.getAttribute('aria-pressed'), 'false', 'Archived toggle off');
    await archivedToggle.click();
    await settle(async () => url().searchParams.get('archived') === '1' && (await titles()).join() === 'Plan the next release train', 'archived list after the toggle: ' + (await titles()).join());
    assert.equal(await archivedToggle.getAttribute('aria-pressed'), 'true', 'Archived toggle pressed');
    assert.equal(await p.locator('[data-ui=row-pin]').count(), 0, 'pinned active row not in the archived list');
    if (process.env.CONV_SHOTS) { await p.mouse.move(0, 0); await p.screenshot({ path: process.env.CONV_SHOTS + '/conv-archived.png' }); }
    // In the archived list the bar offers Unarchive (session_unarchive) instead of Archive; the list then has none left.
    await p.locator('.vl-row .row-link', { hasText: 'Plan the next release train' }).click({ modifiers: ['Shift'] });
    assert.equal(await p.locator('[data-ui=select-bar] button[aria-label=Archive]').count(), 0, 'no Archive in the archived list');
    await p.locator('[data-ui=select-bar] button[aria-label=Unarchive]').click();
    await settle(async () => await p.locator('.empty p', { hasText: 'No archived conversations.' }).count() === 1, 'archived list empty after Unarchive');
    // Toggle off: the key leaves the URL and the conversation is back among the active ones.
    await archivedToggle.click();
    await settle(async () => !url().searchParams.has('archived') && (await mine()).includes('Plan the next release train'), 'active list after the toggle off');
    assert.equal(await archivedToggle.getAttribute('aria-pressed'), 'false', 'Archived toggle off again');
    // Unpin, so other checks see the list as seeded.
    await p.waitForSelector('[data-ui=row-pin]'); await p.locator('[data-ui=row-pin]').first().click();
    await settle(async () => await p.locator('[data-ui=row-pin]').count() === 0, 'unpinned');
    assert.deepEqual(errors, []);
    console.log('ok Conversations list: project groups, preset keeps grouping, group and sort order, pin under a filter, selection mode, archive, archived toggle and unarchive');
  } finally { await context.close(); }
}
