// Operator app check (docs/designs/fleet-operator-app.md §9): starts the Go coordinator (tests/harness.ts) with fixtures behind a local proxy
// that injects the Bearer the way the access guard does, then opens the built app (pnpm run build) in Chromium. Prints a report:
// routes render their book parts, no console errors, click-to-detail latency, change-to-screen latency. Never fails the build.
// Run: node tests/ui/app.check.ts [--shots dir]
import { mkdirSync } from 'node:fs';
import assert from 'node:assert/strict';
import { chromium } from 'playwright';
import type { HomeViewReply } from '../../contracts/generated/ops-people.ts';
import { lit } from '../harness.ts';
import { checkServer } from './seed.ts';
import { layoutFindings } from './layout.check.ts';
import { openWaiting } from './home-card.ts';
import { hscrollScan } from './hscroll.ts';

const shots = process.argv.includes('--shots') ? process.argv[process.argv.indexOf('--shots') + 1] : 'design-report/app';
mkdirSync(shots, { recursive: true });
const out = { push: (l: string) => console.log('    ' + l) };
const { c, origin, seed, close } = await checkServer({ store: true });

// Fixtures: a few hundred tasks across projects, states and priorities; sessions with a transcript; hosts; open questions.
let waiting = '', running = '', delivered = '';
seed.bulk(() => {
  for (let i = 0; i < 240; i++) {
    const p = ['cs', 'game', 'fleet'][i % 3];
    const id = seed.task({ title: ['Render prop batch', 'Fix CI cache key', 'Rework hero copy', 'Write release notes', 'Compare 3D methods'][i % 5] + ' ' + i, projectId: p, priority: ['urgent', 'normal', 'background'][i % 3], expectedOutcome: 'A reviewed result linked from the changelog.', ...(i % 7 === 0 ? { state: 'in_progress' } : i === 3 ? { state: 'waiting', waitReason: 'acceptance' } : {}) });
    if (i % 7 === 0) { seed.execution({ id: 'run-' + i, kind: 'agent_run', taskId: id, projectId: p, hostId: ['pds18', 'mac', 'windows'][i % 3], harness: ['codex', 'claude', 'antigravity'][i % 3], state: 'running' }); if (!running) running = id; }
    if (i === 3) { waiting = id; seed.question({ kind: 'acceptance', taskId: id, text: 'Accept this result?', options: ['accepted', 'rejected'] }); }
  }
  for (let i = 0; i < 4; i++) { seed.question({ kind: 'operator_decision', text: 'Pick a release slot ' + i, options: ['today', 'tomorrow'] }); }
  for (const [id, platform, ago] of [['pds18', 'linux', 2000], ['mac', 'darwin', 120000], ['windows', 'win32', 5000]] as const) seed.put('host-observation', { id, platform, lastSeenAt: Date.now() - ago, capabilities: ['codex'] });
  seed.put('session', { id: 'sess-1', createdAt: Date.now() - 600000, agentConfig: { id: 'baseline', harness: 'codex' } });
  seed.execution({ id: 'sx-1', kind: 'agent_run', sessionId: 'sess-1', hostId: 'pds18', harness: 'codex', state: 'succeeded' });
  seed.put('message', { id: 'msg-1', sessionId: 'sess-1', executionId: 'sx-1', text: 'Rework the hero copy; keep it short.' });
  for (let i = 0; i < 12; i++) seed.put('event', { id: 'ev-' + i, sessionId: 'sess-1', executionId: 'sx-1', type: 'item.completed', data: { fleetNormalized: { blocks: [i % 2 ? { kind: 'tool', name: 'npm test', input: null, output: 'ok', state: 'completed' } : { kind: 'text', role: 'agent', markdown: 'Step ' + i + ' done; checking the next file.' }] } } });
  delivered = seed.task({ title: 'Add the regression check', projectId: 'fleet', state: 'waiting', waitReason: 'acceptance' });
  seed.execution({
    id: 'run-acceptance', taskId: delivered, projectId: 'fleet', state: 'completed',
    report: { outcome: 'succeeded', at: Date.now(), summary: 'Added a regression check for repeat submissions.\nVerified: application and Go tests passed.\nPR #138 shipped in v1.2.3.' },
  });
  const artifactId = '3933f0e9-15da-4c1b-8bd4-562565b9c771';
  seed.artifact({
    id: artifactId, executionId: 'run-acceptance', taskId: delivered, projectId: 'fleet',
    name: 'Verification report', mediaType: 'text/plain', bytes: Buffer.from('Test results'),
  });
  const acceptanceId = seed.question({
    kind: 'acceptance', taskId: delivered, executionId: 'run-acceptance', text: 'Accept the regression check?', options: ['accepted', 'rejected'],
  });
  seed.sql(`UPDATE question SET data=${lit({ artifactIds: [artifactId] })}::jsonb WHERE id=${lit(acceptanceId)}`);
  const published = new Date().toISOString();
  seed.sql(`INSERT INTO delivery_pr(id, project_id, number, payload) VALUES('delivery-pr-138', 'fleet', 138, ${lit({
    title: 'Add regression coverage', url: 'https://github.com/taskqorg/fleet/pull/138', number: 138, release: 'v1.2.3',
    opened: published, merged: published, released: published, installed: { coordinator: published }, durations: {},
  })}::jsonb)`);
  seed.sql(`INSERT INTO delivery_release(id, project_id, tag, payload) VALUES('delivery-release-fixture', 'fleet', 'v1.2.3', ${lit({
    tag: 'v1.2.3', published, installed: { coordinator: published },
  })}::jsonb)`);
});

const b = await chromium.launch();
try {
  const page = await b.newPage({ viewport: { width: 1440, height: 900 } });
  let expectedCatalogueFailure = false;
  const errors: string[] = [];
  page.on('pageerror', e => errors.push(e.message)); page.on('console', m => { if (m.type() === 'error' && !(expectedCatalogueFailure && m.text().includes('503'))) errors.push(m.text()); });
  const ROUTES: [string, string[]][] = [
    // Home (pages/home.md, OR-50): pulse line, composer with its target, session and question cards, answered in place.
    ['/home', ['app-shell', 'nav-rail', 'home', 'pulse-line', 'target-picker', 'session-card', 'question-card']],
    ['/projects/tasks', ['app-shell', 'nav-rail', 'list-view', 'filter-bar', 'state-counters', 'data-list', 'state-badge']],
    [`/projects/tasks/${waiting}`, ['list-detail', 'detail', 'outcome-block', 'decision-block', 'number']],
    [`/projects/tasks/${running}`, ['detail', 'execution-tree']],
    ['/conversations', ['list-view', 'data-list', 'state-counters']],
    ['/conversations/sess-1', ['detail', 'transcript-viewer', 'composer']],
    ['/conversations/new', ['detail', 'project-pick', 'preset-pick', 'composer', 'drop-zone']],
    ['/system', ['list-detail', 'catalogue-tree', 'state-badge']],
    ['/system/hosts/mac', ['detail', 'detail-section', 'host-band']],
    ['/projects', ['list-detail', 'area-chart']],
    ['/library/agents', ['list-detail', 'state-badge']],
    ['/library/pipelines', ['list-detail', 'state-badge']],
    ['/lab', ['list-detail', 'state-badge']],
    ['/system/runners', ['time-strip', 'state-badge', 'harness-counts']],
    ['/projects/delivery', ['list-detail', 'state-counters', 'delivery-overview']],
  ];
  page.on('requestfailed', r => { if (!r.url().includes('/api/stream') && !r.failure()?.errorText.includes('ERR_ABORTED')) errors.push('request failed ' + r.url()); }); // navigation aborts the stream
  page.on('response', r => { if (r.status() >= 400 && !(expectedCatalogueFailure && r.url().includes('/api/ops/catalogue') && r.status() === 503)) errors.push(r.status() + ' ' + r.url()); });
  if (process.env.APP_DEBUG) { await page.goto(origin + '/home'); await page.waitForTimeout(1500); console.log(errors); }
  for (const [n, [route, want]] of ROUTES.entries()) {
    await page.goto(origin + route); await page.waitForTimeout(700);
    const seen = new Set(await page.$$eval('[data-ui]', els => els.map(e => e.getAttribute('data-ui'))));
    const missing = want.filter(w => !seen.has(w));
    const hscroll = await page.evaluate(() => document.documentElement.scrollWidth > innerWidth + 1);
    out.push(`${route.replace(/[0-9a-f-]{36}/, ':id')}: ${missing.length ? 'MISSING ' + missing.join(', ') : 'ok'}${hscroll ? ', horizontal scroll' : ''}`);
    const shot = `${shots}/${n + 1}-${route.slice(1).replace(/[0-9a-f-]{36}/, 'id').replace(/\//g, '-')}-1440`;
    await page.screenshot({ path: shot + '.png' });
    // The owner works in the dark theme: every route is also shot in it.
    await page.emulateMedia({ colorScheme: 'dark' });
    await page.screenshot({ path: shot + '-dark.png' });
    await page.emulateMedia({ colorScheme: 'light' });
    for (const f of await page.evaluate(hscrollScan)) out.push(`  OR-66 1440px: ${f.kind} ${f.px} px ${f.el}`);
    for (const f of await layoutFindings(page)) out.push(`  ${f}`);
  }
  // OR-66 no horizontal scroll: every route at the other checked widths; each sideways overflow is one report line.
  for (const w of [1100, 768, 390]) {
    await page.setViewportSize({ width: w, height: 900 });
    for (const [route] of ROUTES) {
      await page.goto(origin + route); await page.waitForTimeout(500);
      for (const f of await page.evaluate(hscrollScan)) out.push(`OR-66 ${w}px ${route.replace(/[0-9a-f-]{36}/, ':id')}: ${f.kind} ${f.px} px ${f.el}`);
    }
  }
  await page.setViewportSize({ width: 1440, height: 900 });
  // Owners add and revoke a human without sending a replacement roster.
  await page.goto(origin + '/system/people');
  await page.getByRole('button', { name: 'Invite person', exact: true }).click();
  await page.getByLabel('Sign-in email').fill('human-ui@trial.test');
  await page.getByLabel('Name', { exact: true }).fill('Human UI fixture');
  await page.getByRole('button', { name: 'Architect', exact: true }).click();
  await page.getByLabel('Explicit project grants').fill('*');
  await page.getByRole('button', { name: 'Personal Lab', exact: true }).click();
  await page.locator('[data-ui=detail]').getByRole('button', { name: 'Add human', exact: true }).click();
  await page.getByRole('heading', { name: 'Human UI fixture', exact: true }).waitFor();
  assert.ok((await page.locator('[data-ui=detail]').innerText()).includes('All projects'));
  await page.getByRole('button', { name: 'Revoke access', exact: true }).click();
  await page.getByRole('button', { name: 'Confirm revocation', exact: true }).click();
  await page.waitForURL(origin + '/system/people');
  await page.getByText('Human UI fixture', { exact: true }).waitFor({ state: 'hidden' });

  // OR-50, OR-53: Home lists every open question as a card in Waiting for you, with its choices in place.
  await page.goto(origin + '/home'); await page.waitForSelector('[data-ui=question-card]');
  await openWaiting(page);
  const asked = await page.locator('[data-ui=question-card]').count();
  out.push(`home (OR-53): ${asked} question cards${asked < 5 ? '  MISSING QUESTIONS' : ''}`);
  // T-FLT-129: an empty waiting group leaves active work next to the composer, even when the pulse is crowded.
  const homeFixture: HomeViewReply = {
    sessions: [{
      id: 'home-working-fixture', code: null, title: 'Working fixture', state: 'running', harness: 'codex',
      profileId: null, projectId: null, createdAt: null, activityAt: null, executions: 1, openQuestions: 0,
      archivedAt: null, sortKey: [],
    }],
    bots: [{
      id: 'home-asleep-fixture', projectId: 'fleet', name: 'Asleep fixture', purpose: 'Fixture bot', revision: 1,
      paused: false, activeRunId: null, lastRunState: null, lastRunAt: null, needsYou: false, conversationId: null,
    }],
    questions: [], failures: [],
    counts: { doneToday: 123, visitAt: Date.now(), doneSince: 0, mergedSince: 0, failedSince: 0 },
    blockers: [1, 2, 3].map(n => ({
      kind: 'run', cause: `home-fixture-${n}`, n, hostId: null, at: Date.now(),
      text: 'A provider is waiting for capacity and no other worker is available to pick up this run '.repeat(2),
    })),
  };
  await page.route('**/api/ops/home_view', route => route.fulfill({ json: homeFixture }));
  await page.goto(origin + '/home');
  await page.getByText('Working fixture', { exact: true }).waitFor();
  await page.getByRole('region', { name: 'Asleep' }).getByText('Asleep fixture', { exact: true }).waitFor();
  assert.equal(await page.getByRole('region', { name: 'Waiting for you' }).count(), 0);
  assert.equal(await page.getByText('Nothing needs you', { exact: true }).count(), 0);
  assert.equal(await page.getByRole('button', { name: 'Open Live', exact: true }).count(), 0);
  assert.equal(await page.locator('[data-ui=home] > [data-ui=composer] + section').getAttribute('aria-label'), 'Working');
  const pulse = page.getByRole('group', { name: 'Pulse' });
  const live = pulse.getByRole('link', { name: 'Live ›', exact: true });
  assert.equal(await live.getAttribute('href'), '/live');
  await page.evaluate(() => document.fonts.ready.then(() => {}));
  for (const width of [1440, 1366, 1100, 390]) {
    await page.setViewportSize({ width, height: 900 });
    assert.ok(await live.evaluate((link, desktop) => {
      const range = document.createRange();
      range.selectNodeContents(link);
      const text = range.getBoundingClientRect();
      const box = link.getBoundingClientRect();
      const pulseRight = link.closest('[data-ui=pulse-line]')!.getBoundingClientRect().right;
      const headerRight = link.closest('header')!.getBoundingClientRect().right;
      return range.getClientRects().length === 1 && text.left >= box.left - 1
        && text.right <= Math.min(box.right, pulseRight, innerWidth) + 1
        && (!desktop || Math.abs(box.right - headerRight) <= 1);
    }, width >= 1100), `PulseLine Live link clips or leaves the right edge at ${width}px`);
  }
  out.push('home: Working follows the composer when Waiting for you is empty, and the pulse Live label fits at 1440, 1366, 1100 and 390 px');
  await page.unroute('**/api/ops/home_view');
  // OR-39: the task detail shows no total that is unknown (no empty tokens or cost fields).
  await page.setViewportSize({ width: 1440, height: 900 });
  await page.goto(origin + `/projects/tasks/${running}`); await page.waitForSelector('[data-ui=detail] .detail-title');
  const empty = await page.$$eval('[data-ui=detail] .totals .b-unavailable', els => els.length);
  out.push(`task detail (OR-39): ${empty ? empty + ' EMPTY TOTALS' : 'no empty totals'}`);
  // OR-38: an entity code is one line wherever it sits (list rows, detail title); a raw hash or UUID is never a visible name.
  await page.setViewportSize({ width: 900, height: 900 });
  const codes = await page.$$eval('[data-ui=entity-code]', els => els.filter(e => e.textContent && (getComputedStyle(e).whiteSpace !== 'nowrap' || e.getClientRects().length > 1)).length);
  const hashes = await page.$$eval('[data-ui=detail] a, [data-ui=detail] .title', els => els.filter(e => /[0-9a-f]{8}-[0-9a-f]{4}-|[0-9a-f]{24,}/i.test(e.textContent ?? '')).length);
  out.push(`entity codes (OR-38): ${codes ? codes + ' WRAP' : 'never wrap'}, ${hashes ? hashes + ' HASH NAMES' : 'no hash names'}`);
  // Acceptance shows the selected result evidence in its decision and omits absent delivery or artifact references.
  await page.goto(origin + `/projects/tasks/${delivered}`);
  await page.getByRole('tab', { name: 'Details', exact: true }).click();
  const decision = page.locator('[data-ui=decision-block]');
  await decision.getByText('Added a regression check for repeat submissions.', { exact: true }).waitFor();
  await decision.getByText('Verified: application and Go tests passed.', { exact: true }).waitFor();
  await decision.getByRole('button', { name: /^Task T-FLT-/ }).waitFor();
  for (const [name, title] of [[/^Pull request #138/, 'Add regression coverage'], [/^Release v1\.2\.3/, 'Release 1.2.3']] as const) {
    await decision.getByRole('button', { name }).click();
    await page.getByRole('dialog').getByText(title, { exact: true }).waitFor();
    await page.keyboard.press('Escape');
  }
  await decision.getByRole('button', { name: /^Result/ }).click();
  await page.getByRole('dialog').getByText('Verification report', { exact: true }).waitFor();
  assert.doesNotMatch(await decision.innerText(), /3933f0e9-15da-4c1b-8bd4-562565b9c771/);
  await page.goto(origin + `/projects/tasks/${waiting}`);
  await page.getByRole('tab', { name: 'Details', exact: true }).click();
  await decision.getByRole('button', { name: 'accepted', exact: true }).waitFor();
  assert.equal(await decision.getByRole('button', { name: /^(Pull request|Release|Result)/ }).count(), 0);
  await page.setViewportSize({ width: 1440, height: 900 });
  // Narrow width: the rail stays, the opened item takes the place of the catalogue.
  const narrow = await b.newPage({ viewport: { width: 600, height: 900 } });
  await narrow.goto(origin + `/projects/tasks/${waiting}`); await narrow.waitForTimeout(700);
  const top = await narrow.evaluate(() => document.querySelector('[data-ui=detail]')?.getBoundingClientRect().top ?? null);
  const rail = await narrow.locator('[data-ui=nav-rail]').isVisible();
  out.push(`/tasks/:id @600px: detail top ${top === null ? 'missing' : Math.round(top) + ' px'}${top !== null && top > 780 ? '  OUTSIDE VIEWPORT' : ''}, nav rail ${rail ? 'visible' : 'HIDDEN'}`);
  await narrow.screenshot({ path: `${shots}/tasks-id-600.png` });

  // Click-to-detail latency: row click until the detail title shows.
  const times: number[] = [];
  for (let i = 0; i < 5; i++) {
    await page.goto(origin + '/projects/tasks'); await page.waitForSelector('.pw-facet .vl-row .row-link');
    times.push(await page.evaluate(async i => { const l = document.querySelectorAll<HTMLElement>('.pw-facet .vl-row .row-link')[i]; const t0 = performance.now(); l.click(); while (!document.querySelector('[data-ui=detail] .detail-title')) await new Promise(r => requestAnimationFrame(r)); return performance.now() - t0; }, i));
  }
  times.sort((a, z) => a - z);
  out.push(`click-to-detail: median ${times[2].toFixed(0)} ms of 5 (target <= 100 ms)${times[2] > 100 ? '  OVER TARGET' : ''}`);

  // Change-to-screen: a committed task change until its row shows the new state, through the live feed.
  await page.goto(origin + '/projects/tasks?group=none'); await page.waitForSelector('.pw-facet .vl-row .row-link');
  // The row's key is the task id (its link names the task by code).
  const firstId = await page.$eval('.pw-facet .vl-row[data-key]', r => r.getAttribute('data-key') ?? '');
  const t0 = Date.now(); await fetch(origin + '/api/ops/task_update', { method: 'POST', headers: { 'content-type': 'application/json' }, body: JSON.stringify({ taskId: firstId, action: 'pause' }) });
  let ms = -1; for (let i = 0; i < 100; i++) { if (await page.$(`.pw-facet .vl-row [aria-label^="Paused"]`)) { ms = Date.now() - t0; break; } await page.waitForTimeout(20); }
  out.push(`change-to-screen: ${ms < 0 ? 'NOT SEEN within 2 s' : ms + ' ms'} (target <= 1000 ms)${ms > 1000 ? '  OVER TARGET' : ''}`);
  // Pins (nav-rail.md): `p` on the focused item pins it (no pin shows on hover, OR-37), it stays in its group too with its
  // pin shown; Space, ArrowUp, Space reorders (reorder.tsx); the order survives a reload (person settings).
  await page.goto(origin + '/home'); await page.waitForSelector('[data-ui=nav-rail]');
  for (const id of ['projects', 'system']) { await page.locator(`.rail-g:not(.pinned) [data-id=${id}] a`).focus(); await page.keyboard.press('p'); await page.waitForTimeout(150); }
  const pinsOf = () => page.$$eval('.rail-g.pinned .nav-i', els => els.map(e => e.getAttribute('data-id')));
  const twice = await page.locator('.rail-g:not(.pinned) .nav-i.pinned[data-id=projects]').count();
  await page.locator('.rail-g.pinned [data-id=system] a').focus(); for (const k of ['Space', 'ArrowUp', 'Space']) { await page.keyboard.press(k); await page.waitForTimeout(80); } await page.waitForTimeout(300);
  const moved = (await pinsOf()).join(',');
  await page.reload(); await page.waitForTimeout(700);
  const kept = (await pinsOf()).join(',');
  out.push(`pins: in group ${twice ? 'kept' : 'MISSING'}, reorder ${moved === 'system,projects' ? 'ok' : 'WRONG ' + moved}, after reload ${kept === 'system,projects' ? 'kept' : 'LOST ' + kept}`);
  await page.emulateMedia({ colorScheme: 'dark' });
  await page.locator('.rail-g.pinned [data-id=projects]').click({ button: 'right' }); await page.waitForTimeout(200);
  out.push(`pin menu: ${(await page.locator('.rail-menu [role=menuitem]').allInnerTexts()).join(', ') || 'MISSING'}`);
  await page.screenshot({ path: `${shots}/pins-dark.png`, clip: { x: 0, y: 0, width: 520, height: 520 } });
  await page.keyboard.press('Escape'); await page.emulateMedia({ colorScheme: 'light' });
  // Requirement D20: scoped method evidence and a launchable explicit Lab draft; risk: hidden quality basis or unsaved generated record fields.
  await page.goto(origin + '/library/methods/catalogue');
  await page.getByRole('heading', {name: 'Methods', exact: true}).waitFor();
  await page.getByRole('heading', {name: 'Compare methods in Lab', exact: true}).waitFor();
  await page.getByRole('button', {name: 'Hello World', exact: true}).click();
  await page.getByText('Basic source discovery · search-basic', {exact: true}).waitFor();
  assert.ok(await page.getByText('Recommended for', {exact: true}).count());
  // Controlled render fixture: a measured partial comparison score must stay visible with its population.
  const preview = await (await fetch(origin + '/api/ops/catalogue', {method:'POST',headers:{'content-type':'application/json'},body:JSON.stringify({projectId:'hello-world'})})).json();
  preview.methods[0].assessment = {...preview.methods[0].assessment, basis:'lab', score:0.5, scoreForm:'typed', partial:true, eligibleCells:1,expectedCells:2,examples:1,failedCells:1,excludedCells:1,state:'stopped',stopReason:'fixture',variant:'preview-variant',split:'held_out',evaluator:'typed:labels:typed',evaluatorVersion:'fixture-version',reason:'One observed cell; one failed cell is excluded'};
  await page.route('**/api/ops/catalogue', route => route.request().postDataJSON().projectId === 'hello-world' ? route.fulfill({status:200,contentType:'application/json',body:JSON.stringify(preview)}) : route.continue());
  await page.reload();
  await page.getByRole('button', {name:'Hello World',exact:true}).click();
  await page.getByText('0.500 · partial/incomplete', {exact:true}).waitFor();
  assert.ok(await page.getByText('Comparison score', {exact:true}).count());
  for (const colorScheme of ['dark', 'light'] as const) { await page.emulateMedia({colorScheme}); await page.screenshot({path: `${shots}/catalogue-methods-${colorScheme}.png`}); }
  await page.unroute('**/api/ops/catalogue');
  expectedCatalogueFailure = true;
  await page.route('**/api/ops/catalogue', route => route.fulfill({status:503,contentType:'application/json',body:JSON.stringify({message:'fixture catalogue unavailable',error:'Service Unavailable',statusCode:503})}));
  await page.getByRole('button', {name:'cyprus.bz',exact:true}).click();
  await page.getByRole('alert').filter({hasText:'Catalogue unavailable:'}).waitFor();
  assert.equal(await page.getByText('0.500 · partial/incomplete', {exact:true}).count(), 0);
  assert.equal(await page.getByRole('group', {name:'Baseline',exact:true}).getByRole('button').count(), 0);
  assert.equal(await page.getByRole('button', {name:'Recommend for project',exact:true}).count(), 0);
  assert.equal(await page.getByRole('button', {name:'Create comparison',exact:true}).getAttribute('aria-disabled'), 'true');
  await page.screenshot({path:`${shots}/catalogue-fetch-error.png`});
  await page.unroute('**/api/ops/catalogue');
  await page.getByRole('button', {name:'Retry Catalogue',exact:true}).click();
  await page.getByRole('alert').filter({hasText:'Catalogue unavailable:'}).waitFor({state:'hidden'});
  expectedCatalogueFailure = false;
  // A late previous-project response must not repopulate the current project or hidden selections.
  let releasePrevious!: () => void, previousRequested!: () => void;
  const previousGate = new Promise<void>(r => {releasePrevious = r;});
  const previousHit = new Promise<void>(r => {previousRequested = r;});
  await page.route('**/api/ops/catalogue', async route => {
    if (route.request().postDataJSON().projectId !== 'hello-world') return route.continue();
    previousRequested(); await previousGate;
    await route.fulfill({status:200,contentType:'application/json',body:JSON.stringify(preview)});
  });
  await page.getByRole('checkbox', {name:'Select Basic source discovery',exact:true}).click();
  await page.getByRole('button', {name:'Hello World',exact:true}).click();
  try {
    await previousHit;
    assert.equal(await page.getByRole('button', {name:'Recommend for project',exact:true}).count(), 0);
    await page.getByRole('button', {name:'cyprus.bz',exact:true}).click();
    await page.getByText('Evidence project: cs', {exact:true}).waitFor();
  } finally {releasePrevious();}
  await page.waitForResponse(r => r.url().endsWith('/api/ops/catalogue') && r.request().postDataJSON()?.projectId === 'hello-world');
  await page.getByText('Evidence project: cs', {exact:true}).waitFor();
  assert.equal(await page.getByText('0.500 · partial/incomplete', {exact:true}).count(), 0);
  assert.equal(await page.getByRole('group', {name:'Baseline',exact:true}).getByRole('button').count(), 0);
  assert.equal(await page.getByRole('button', {name:'Create comparison',exact:true}).getAttribute('aria-disabled'), 'true');
  await page.screenshot({path:`${shots}/catalogue-project-race.png`});
  await page.unroute('**/api/ops/catalogue');
  c.sql(`INSERT INTO experiment(id,project_id,kind,pipeline_id,variants) VALUES('catalogue-ui-draft','hello-world','single','typed-decision','[{"label":"selected-method","pipelineId":"typed-decision","overrides":{},"config_ref":"digest-ui-fixture","catalogueConfigRef":"identity-ui-fixture","catalogueRef":{"kind":"catalogue","id":"catalogue","revision":1,"contentSha":"fixture"}}]'::jsonb)`);
  await page.goto(origin + '/lab/catalogue-ui-draft');
  await page.getByRole('heading', {name: 'Experiment record', exact: true}).waitFor();
  await page.getByRole('button', {name: 'Executable digest: digest-ui-fixture', exact: true}).waitFor();
  await page.getByRole('button', {name: 'Catalogue identity: identity-ui-fixture', exact: true}).waitFor();
  const launch = page.getByRole('button', {name: 'Launch', exact: true});
  assert.equal(await launch.getAttribute('aria-disabled'), 'true');
  for (const [label, value] of [['Goal', 'Inspect Catalogue comparison'], ['Hypothesis', 'The selected method improves the example outcome']]) {
    const field = page.locator('[data-ui="setting-field"]').filter({has: page.locator('label', {hasText: label})});
    await field.locator('button.value').click(); await field.locator('input').fill(value); await field.getByRole('button', {name:'Save',exact:true}).click();
    await page.waitForFunction(async ({origin, label, value}) => { const r = await fetch(origin + '/api/ops/lab_experiment_get', {method:'POST',headers:{'content-type':'application/json'},body:JSON.stringify({experimentId:'catalogue-ui-draft'})}); const d = await r.json(); return d[label.toLowerCase()] === value; }, {origin,label,value});
  }
  await page.waitForFunction(el => el?.getAttribute('aria-disabled') !== 'true', await launch.elementHandle());
  const saved = await (await fetch(origin + '/api/ops/lab_experiment_get', {method:'POST',headers:{'content-type':'application/json'},body:JSON.stringify({experimentId:'catalogue-ui-draft'})})).json();
  assert.equal(saved.state, 'draft'); assert.equal(saved.trials.length, 0);
  await page.screenshot({path:`${shots}/catalogue-draft-record.png`});
  await launch.click();
  out.push('catalogue: methods basis and recommendations rendered; user record durably saved before explicit Launch');
  out.push(errors.length ? `console errors: ${[...new Set(errors)].slice(0, 5).join(' | ')}` : 'console errors: none');
} finally { await b.close(); await close(); }
