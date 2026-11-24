// Conversations list controls (docs/design-system/pages/conversations.md, list-view pattern; OR-12, OR-33, OR-35, OR-37):
// a system preset sets only the state filter and keeps the grouping, group and sort change the server order, a pinned row
// stays under a filter that does not match it, a search ends the preset, checkboxes show only in selection mode, and the Archived toggle switches to
// archived conversations, where the bar offers Unarchive; collapsed groups (Pinned too) stay collapsed from the first frame
// after leaving the section and after a reload. Called by conversations.check.ts.
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
      seed.sql(`UPDATE session SET updated_at=${at} WHERE id='${id}'`); seed.sql(`UPDATE message SET updated_at=${at} WHERE id='${id}-m'`);
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
    // Default: Waiting for you first, then grouped by project, latest activity first inside each group; projects in key
    // order, no project last (pages/conversations.md, owner 2026-11-25).
    await settle(async () => (await mine()).length === rows.length, 'all seeded conversations listed');
    assert.deepEqual(await settled(), ['Fix the invoice rounding bug', 'Write the webhook retry notes', 'Audit image sizes on the blog', 'Plan the next release train']);
    const heads = await groups();
    assert.ok(heads.some(h => h.includes('site-redesign') && h.includes('2')) && heads.some(h => h.includes('billing-api')) && heads.some(h => h.includes('No project')), 'project group headers with counts: ' + heads.join(' | '));
    assert.ok(heads.findIndex(h => h.includes('Waiting for you')) === heads.findIndex(h => !h.includes('Pinned')), 'Waiting for you is the first group: ' + heads.join(' | '));
    // OR-37: no checkbox outside selection mode; the facts show the open questions and the last activity, no executions or agent (OR-54).
    assert.equal(await p.locator('.vl-row .row-check').count(), 0, 'checkboxes outside selection mode');
    const hero = p.locator('.vl-row', { hasText: 'Refresh the landing hero copy' });
    assert.ok(await hero.locator('.row-facts .fact[data-tip="3 executions"]').count() === 0, 'no executions fact');
    assert.ok(await hero.locator('.row-facts [data-tip="Last activity"]').count() === 1, 'last activity fact');
    assert.ok(await p.locator('.vl-row', { hasText: 'Compare three pricing layouts' }).locator('.row-facts .t-attention').textContent() === '1 question', 'open questions fact');

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
    // A new search query ends the active preset; the states stay.
    await p.locator('.fbar button[aria-label^="Search"]').first().click();
    await p.locator('.fbar input').first().pressSequentially('pricing', { delay: 60 });
    await settle(async () => url().searchParams.get('q') === 'pricing', 'search in the URL');
    assert.ok(!url().searchParams.has('preset') && url().searchParams.get('state') === 'failed,waiting', 'a search ended the preset and kept the states: ' + url().search);

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
    await settle(async () => url().searchParams.get('archived') === '1' && (await titles()).join() === 'Plan the next release train', 'only the archived conversation listed after the toggle');
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
    // Collapsed groups are remembered per person (list-view.md, prefs listGroups), the Pinned group too: collapse Pinned and
    // billing-api, leave the section and come back, then reload; from the first frame with headers they stay collapsed.
    const headState = () => p.evaluate(() => [...document.querySelectorAll('.vl-row.grp .group-h')].map(b => b.querySelector('b')!.textContent + '=' + b.getAttribute('aria-expanded')).join(','));
    const head = (label: string) => p.locator('.vl-row.grp .group-h', { has: p.locator('b', { hasText: label }) });
    await head('billing-api').click(); await head('Pinned').click();
    const want = await headState();
    assert.ok(want.includes('Pinned=false') && want.includes('billing-api=false') && want.includes('site-redesign=true'), 'collapsed: ' + want);
    // Every distinct frame's headers, from a start until the list settles; a frame that shows Pinned or billing-api open flashed.
    const RECORD = `window.__heads = []; const tick = () => { const s = [...document.querySelectorAll('.vl-row.grp .group-h')].map(b => b.querySelector('b').textContent + '=' + b.getAttribute('aria-expanded')).join(','); if (window.__heads.at(-1) !== s) window.__heads.push(s); requestAnimationFrame(tick); }; requestAnimationFrame(tick);`;
    const flashed = (seen: string[]) => seen.filter(s => /(Pinned|billing-api)=true/.test(s));
    await p.locator('.rail [data-id="system"] a').first().click(); await p.waitForURL('**/system');
    // Leaving the section sent the toggles: the server has them, and this tab shows them from the stored copy.
    await settle(async () => (await p.evaluate(() => fetch('/api/ops/person_settings_get', { method: 'POST', headers: { 'content-type': 'application/json' }, body: '{}' }).then(r => r.json()).then(s => JSON.stringify(s.listGroups)))).includes('billing-api'), 'toggles stored');
    const returned: string[] = await p.evaluate(async rec => {
      (0, eval)(rec); (document.querySelector('.rail [data-id="conversations"] a') as HTMLElement).click();
      for (let f = 0; f < 300 && !(window as any).__heads.at(-1)?.includes('site-redesign'); f++) await new Promise(r => requestAnimationFrame(r));
      return (window as any).__heads;
    }, RECORD);
    assert.ok(returned.at(-1) === want && !flashed(returned).length, 'back to Conversations: groups not as left from the first frame: ' + returned.join(' > '));
    await p.addInitScript(RECORD);
    await p.reload(); await head('site-redesign').waitFor(); await p.waitForTimeout(300);
    const reloaded: string[] = await p.evaluate(() => (window as any).__heads);
    assert.ok(reloaded.at(-1) === want && !flashed(reloaded).length, 'after a reload: groups not as left from the first frame: ' + reloaded.join(' > '));
    await head('billing-api').click(); await head('Pinned').click();
    await settle(async () => !(await headState()).includes('=false'), 'groups open again');

    // Unpin, so other checks see the list as seeded.
    await p.waitForSelector('[data-ui=row-pin]'); await p.locator('[data-ui=row-pin]').first().click();
    await settle(async () => await p.locator('[data-ui=row-pin]').count() === 0, 'unpinned');
    assert.deepEqual(errors, []);
    console.log('ok Conversations list: project groups, preset keeps grouping, group and sort order, pin under a filter, selection mode, archive, archived toggle and unarchive, remembered collapsed groups');
  } finally { await context.close(); }
  await checkLiveListUpdates(browser, origin, seed);
}

// Requirement: quiet live Conversations and Tasks. Risks: a burst flashes or reloads an
// unchanged list, authoritative row/count changes are missed, or catch-up resets the view.
export async function checkLiveListUpdates(browser: Browser, origin: string, seed: ReturnType<typeof seeder>) {
  const prefix = 'Livefeedfixture';
  seed.bulk(() => {
    for (let i = 0; i < 48; i++) {
      const id = 'livefeed-' + i;
      seed.put('session', { id, title: prefix + ' ' + i, projectId: 'site-redesign', createdAt: Date.now(), titleBy: 'operator' });
      seed.execution({ id: id + '-run', kind: 'agent_run', state: 'running', projectId: 'site-redesign', sessionId: id, startedAt: Date.now() });
    }
    seed.sql("UPDATE execution SET observation='confirmed' WHERE id LIKE 'livefeed-%-run'");
  });
  const context = await browser.newContext({ colorScheme: 'dark', viewport: { width: 1280, height: 640 } });
  await context.addInitScript(() => {
    const streams: EventSource[] = [];
    let received = 0;
    const Source = window.EventSource;
    window.EventSource = class extends Source {
      constructor(url: string | URL, config?: EventSourceInit) {
        super(url, config); streams.push(this);
        this.addEventListener('message', e => {
          const c = JSON.parse(e.data);
          if (c.sessionId === 'livefeed-47' && (c.kind === 'session' || c.kind === 'execution')) received++;
        });
      }
    };
    Object.assign(window, { liveListFeed: { streams, count: () => received } });
  });
  const p = await context.newPage(), errors: string[] = [];
  p.on('pageerror', error => errors.push(error.message));
  const snapshots: string[] = [];
  p.on('request', r => {
    // The idle work area's Latest finished is its own short read (finished states only), not a list snapshot.
    if (r.url().endsWith('/api/ops/sessions_list') && String(r.postDataJSON()?.filter?.state) === 'succeeded,failed,cancelled') return;
    // The list's own reads; the project catalogue's per-project counts (limit 1, Projects facets) are other reads.
    if ((r.url().endsWith('/api/ops/sessions_list') || r.url().endsWith('/api/ops/task_page')) && r.postDataJSON()?.limit !== 1) snapshots.push(r.url().split('/').at(-1)!);
  });
  // Conversations' list is in the control column; Tasks' is in its Projects facet (docs/design-system/pages/projects.md),
  // where the control column holds the project catalogue.
  const frame = p.locator('.split-list:not(:has([data-ui=project-catalogue])) [data-ui=list-view], .pw-facet [data-ui=list-view]');
  const counters = frame.locator('[data-ui=state-counters]');
  const counter = (name: string) => counters.getByRole('button', { name, exact: true });
  const updating = frame.getByRole('progressbar', { name: 'Updating' });
  const row = (id: string) => frame.locator(`.vl-row[data-key="${id}"]`);
  const grid = frame.locator('.vl');
  const membershipFor = (id: string) => p.waitForResponse(r => r.url().endsWith('/api/ops/list_membership')
    && r.ok() && r.request().postDataJSON().ids.includes(id));
  const shot = async (name: string) => {
    if (process.env.CONV_SHOTS) await p.screenshot({ path: process.env.CONV_SHOTS + '/' + name + '.png' });
  };
  const frames = () => p.evaluate(() => new Promise<void>(r => requestAnimationFrame(() => requestAnimationFrame(() => r()))));
  try {
    await p.goto(origin + '/conversations?group=state&q=' + prefix + '&sort=-created&state=preparing,running,waiting');
    await counter('Running: 48').waitFor();
    await updating.waitFor({ state: 'hidden' });
    assert.deepEqual(snapshots, ['sessions_list']);
    await grid.evaluate(el => { el.scrollTop = 430; });
    await p.waitForFunction(() => (document.querySelector('.split-list .vl')?.scrollTop ?? 0) > 0);
    const id = await grid.evaluate(el => {
      const top = el.getBoundingClientRect().top;
      const link = [...el.querySelectorAll<HTMLElement>('.row-link')].find(l => l.getBoundingClientRect().top >= top)!;
      link.focus({ preventScroll: true });
      return link.closest<HTMLElement>('.vl-row')!.dataset.key!;
    });
    await p.keyboard.press('x');
    await frame.getByText(/1 selected/).waitFor();
    const anchor = () => row(id).evaluate(el => el.getBoundingClientRect().top - el.closest('.vl')!.getBoundingClientRect().top);
    const previousOffset = await anchor();
    const previousTop = await grid.evaluate(el => el.scrollTop);
    await p.evaluate(() => {
      const list = document.querySelector('.split-list [data-ui=list-view]')!;
      const bar = list.querySelector('.fbar')!;
      const capture = () => JSON.stringify({
        busy: list.getAttribute('aria-busy'), progress: !!list.querySelector('progress:not([aria-hidden=true])'),
        bar: bar.getBoundingClientRect().toJSON(),
        buttons: [...bar.querySelectorAll('.counter')].map(button => [
          button.getAttribute('aria-label'), button.getBoundingClientRect().toJSON(), getComputedStyle(button).color,
          button.querySelector('svg') && getComputedStyle(button.querySelector('svg')!).color,
        ]),
      });
      const baseline = capture(), seen = new Set<string>();
      let frame = 0, samples = 0;
      const sample = () => { samples++; const value = capture(); if (value !== baseline) seen.add(value); };
      const tick = () => { sample(); frame = requestAnimationFrame(tick); };
      const mutations = new MutationObserver(sample);
      mutations.observe(list, { subtree: true, attributes: true, childList: true });
      frame = requestAnimationFrame(tick);
      Object.assign(window, { stopLiveListSamples: () => { mutations.disconnect(); cancelAnimationFrame(frame); return { samples, changes: [...seen] }; } });
    });
    const membership = membershipFor('livefeed-47');
    // Both trigger kinds reach the real authorized stream. Neither update changes the list projection.
    seed.sql("DO $$ BEGIN FOR i IN 1..18 LOOP UPDATE session SET title=title WHERE id='livefeed-47'; UPDATE execution SET updated_at=updated_at WHERE id='livefeed-47-run'; END LOOP; END $$;");
    await p.waitForFunction(() => (window as any).liveListFeed.count() >= 36);
    const firstMembership = await membership;
    assert.deepEqual(firstMembership.request().postDataJSON().ids, ['livefeed-47'], 'a burst is coalesced by session ID');
    await firstMembership.finished();
    await frames();
    const burst = await p.evaluate(() => (window as any).stopLiveListSamples());
    assert.ok(burst.samples > 0);
    assert.deepEqual(burst.changes, [], 'no progress, counter color or filter geometry changes on any sampled frame');
    assert.deepEqual(snapshots, ['sessions_list'], 'unchanged burst sent no additional full list request');
    assert.equal(await grid.evaluate(el => el.scrollTop), previousTop);
    assert.equal(await row(id).locator('.row-link').evaluate(el => el === document.activeElement), true);
    await shot('live-list-quiet');

    seed.sql("UPDATE execution SET state='waiting' WHERE id='livefeed-47-run';");
    await counter('Waiting: 1').waitFor();
    await counter('Running: 47').waitFor();
    assert.equal(await anchor(), previousOffset, 'a live reorder preserves the visible row offset');
    assert.equal(await row(id).locator('.row-link').evaluate(el => el === document.activeElement), true);
    assert.equal(await row(id).locator('input:checked').count(), 1);
    assert.deepEqual(snapshots, ['sessions_list'], 'state transition used membership');
    await grid.evaluate(el => { el.scrollTop = 0; });
    await frame.locator('.group-h', { hasText: /Waiting.*1/ }).waitFor();
    await row('livefeed-47').waitFor();
    assert.match(await row('livefeed-47').textContent() ?? '', /Livefeedfixture 47/);
    assert.ok(await row('livefeed-47').locator('[data-tip="Waiting"]').count());

    // Force a transport error before committing the next change. Catch-up uses the real saved cursor.
    const catchUp = p.waitForResponse(r => r.url().endsWith('/api/ops/changes_since') && r.ok() && Number.isInteger(r.request().postDataJSON()?.cursor));
    await p.evaluate(() => (window as any).liveListFeed.streams.at(-1).dispatchEvent(new Event('error')));
    seed.sql("UPDATE execution SET state='succeeded' WHERE id='livefeed-47-run';");
    const caught = await (await catchUp).json();
    assert.ok(caught.changes.some((c: { id: string }) => c.id === 'livefeed-47-run'), 'reconnect replayed the committed state change');
    await counter('Done: 1').waitFor();
    await row('livefeed-47').waitFor({ state: 'detached' });
    await updating.waitFor({ state: 'hidden' });
    assert.deepEqual(snapshots, ['sessions_list'], 'resumable reconnect kept the active window');

    let finish!: () => void;
    const held = new Promise<void>(r => { finish = r; });
    await p.route('**/api/ops/sessions_list', async route => {
      if (!route.request().postDataJSON().filter.state) await held;
      await route.continue().catch(() => {});
    });
    try {
      await counters.getByRole('button', { name: /^All:/ }).click();
      await updating.waitFor();
      assert.equal(await row(id).count(), 1, 'old rows remain during an explicit filter change');
      await shot('live-list-explicit-filter');
    } finally { finish(); }
    await updating.waitFor({ state: 'hidden' });
    assert.deepEqual(snapshots, ['sessions_list', 'sessions_list']);


    // Back to an already shown view is automatic revalidation. Make the cache dirty while
    // unmounted and hold the new snapshot to inspect the old rows throughout the read.
    await p.unroute('**/api/ops/sessions_list');
    await p.locator('.rail [data-id="system"] a').first().click();
    await p.waitForURL('**/system');
    await frames();
    const delivered = await p.evaluate(() => (window as any).liveListFeed.count());
    seed.sql("UPDATE session SET title='Livefeedfixture returned' WHERE id='livefeed-47';");
    await p.waitForFunction(n => (window as any).liveListFeed.count() > n, delivered);
    await frames();

    let releaseRemount!: () => void;
    const remountGate = new Promise<void>(r => { releaseRemount = r; });
    await p.route('**/api/ops/sessions_list', async route => { await remountGate; await route.continue().catch(() => {}); });
    const remountRequest = p.waitForRequest(r => r.url().endsWith('/api/ops/sessions_list'));
    const remounted = p.waitForResponse(r => r.url().endsWith('/api/ops/sessions_list') && r.ok());
    try {
      await p.locator('.rail [data-id="conversations"] a').first().click();
      await remountRequest;
      await counter('Running: 47').waitFor();
      assert.equal(await row('livefeed-46').count(), 1, 'cached rows render during the remount read');
      assert.equal(await updating.count(), 0, 'a cached view remount stays quiet');
      await shot('live-list-warm-remount');
    } finally { releaseRemount(); }
    await (await remounted).finished();
    await p.unroute('**/api/ops/sessions_list');
    assert.deepEqual(snapshots, ['sessions_list', 'sessions_list', 'sessions_list']);

    const task = seed.task({ title: 'Livefeedtask owner check', projectId: 'site-redesign', state: 'queued' });
    await p.goto(origin + '/tasks?group=state&q=Livefeedtask&state=queued');
    await row(task).waitFor();
    await counter('Queued: 1').waitFor();
    seed.sql(`UPDATE task SET state='completed' WHERE id='${task}';`);
    await counter('Completed: 1').waitFor();
    await counter('Queued: 0').waitFor();
    await row(task).waitFor({ state: 'detached' });
    assert.deepEqual(snapshots, ['sessions_list', 'sessions_list', 'sessions_list', 'task_page']);
    assert.deepEqual(errors, []);
    console.log('PASS live lists: 36 unchanged feed events, zero progress/color/geometry changes and reloads, status/group/counter changes, scroll/focus/selection, cursor catch-up, quiet warm remount and explicit progress');
  } finally {
    await context.close();
    seed.sql("UPDATE session SET archived_at=now() WHERE id LIKE 'livefeed-%'");
  }
}
