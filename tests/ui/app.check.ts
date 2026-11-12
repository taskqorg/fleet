// Operator app check (docs/designs/fleet-operator-app.md §9): starts the Go coordinator (tests/harness.ts) with fixtures behind a local proxy
// that injects the Bearer the way the access guard does, then opens the built app (pnpm run build) in Chromium. Prints a report:
// routes render their book parts, no console errors, click-to-detail latency, change-to-screen latency. Never fails the build.
// Run: node tests/ui/app.check.ts [--shots dir]
import { mkdirSync } from 'node:fs';
import assert from 'node:assert/strict';
import { chromium } from 'playwright';
import { checkServer } from './seed.ts';
import { layoutFindings } from './layout.check.ts';

const shots = process.argv.includes('--shots') ? process.argv[process.argv.indexOf('--shots') + 1] : 'design-report/app';
mkdirSync(shots, { recursive: true });
const out = { push: (l: string) => console.log('    ' + l) };
const { c, origin, seed, close } = await checkServer();

// Fixtures: a few hundred tasks across projects, states and priorities; sessions with a transcript; hosts; open questions.
let waiting = '', running = '';
seed.bulk(() => {
  for (let i = 0; i < 240; i++) {
    const p = ['cs', 'game', 'fleet'][i % 3];
    const id = seed.task({ title: ['Render prop batch', 'Fix CI cache key', 'Rework hero copy', 'Write release notes', 'Compare 3D methods'][i % 5] + ' ' + i, projectId: p, priority: ['urgent', 'normal', 'background'][i % 3], expectedOutcome: 'A reviewed result linked from the changelog.', ...(i % 7 === 0 ? { state: 'in_progress' } : i === 3 ? { state: 'waiting', waitReason: 'acceptance' } : {}) });
    if (i % 7 === 0) { seed.execution({ id: 'run-' + i, kind: 'agent_run', taskId: id, projectId: p, hostId: ['pds18', 'mac', 'windows'][i % 3], harness: ['codex', 'claude', 'antigravity'][i % 3], state: 'running' }); if (!running) running = id; }
    if (i === 3) { waiting = id; seed.question({ kind: 'acceptance', taskId: id, text: 'Accept this result?', options: ['accepted', 'rejected'] }); }
  }
  for (let i = 0; i < 4; i++) seed.question({ kind: 'operator_decision', text: 'Pick a release slot ' + i, options: ['today', 'tomorrow'] });
  for (const [id, platform, ago] of [['pds18', 'linux', 2000], ['mac', 'darwin', 120000], ['windows', 'win32', 5000]] as const) seed.put('host-observation', { id, platform, lastSeenAt: Date.now() - ago, capabilities: ['codex'] });
  seed.put('session', { id: 'sess-1', createdAt: Date.now() - 600000, agentConfig: { id: 'baseline', harness: 'codex' } });
  seed.execution({ id: 'sx-1', kind: 'agent_run', sessionId: 'sess-1', hostId: 'pds18', harness: 'codex', state: 'succeeded' });
  seed.put('message', { id: 'msg-1', sessionId: 'sess-1', executionId: 'sx-1', text: 'Rework the hero copy; keep it short.' });
  for (let i = 0; i < 12; i++) seed.put('event', { id: 'ev-' + i, sessionId: 'sess-1', executionId: 'sx-1', type: 'item.completed', data: { fleetNormalized: { blocks: [i % 2 ? { kind: 'tool', name: 'npm test', input: null, output: 'ok', state: 'completed' } : { kind: 'text', role: 'agent', markdown: 'Step ' + i + ' done; checking the next file.' }] } } });
});

const b = await chromium.launch();
try {
  const page = await b.newPage({ viewport: { width: 1440, height: 900 } });
  let expectedCatalogueFailure = false;
  const errors: string[] = [];
  page.on('pageerror', e => errors.push(e.message)); page.on('console', m => { if (m.type() === 'error' && !(expectedCatalogueFailure && m.text().includes('503'))) errors.push(m.text()); });
  const ROUTES: [string, string[]][] = [
    ['/overview', ['app-shell', 'nav-rail', 'ops-summary', 'stat-strip', 'needs-you', 'decision-block', 'harness-counts', 'state-counters']],
    ['/tasks', ['app-shell', 'nav-rail', 'list-view', 'filter-bar', 'state-counters', 'data-list', 'state-badge']],
    [`/tasks/${waiting}`, ['list-detail', 'detail', 'outcome-block', 'decision-block', 'number']],
    [`/tasks/${running}`, ['detail', 'execution-tree']],
    ['/conversations', ['list-view', 'data-list', 'state-counters']],
    ['/conversations/sess-1', ['detail', 'transcript-viewer', 'composer']],
    ['/hosts', ['list-detail', 'harness-counts', 'state-badge']],
    ['/hosts/mac', ['detail', 'detail-section', 'fill-bar']],
    ['/lab', ['list-detail', 'state-badge']],
    ['/runners', ['time-strip', 'state-badge', 'harness-counts']],
    ['/delivery', ['app-shell', 'stat-strip', 'area-chart']],
  ];
  page.on('requestfailed', r => { if (!r.url().includes('/api/stream') && !r.failure()?.errorText.includes('ERR_ABORTED')) errors.push('request failed ' + r.url()); }); // navigation aborts the stream
  page.on('response', r => { if (r.status() >= 400 && !(expectedCatalogueFailure && r.url().includes('/api/ops/catalogue') && r.status() === 503)) errors.push(r.status() + ' ' + r.url()); });
  if (process.env.APP_DEBUG) { await page.goto(origin + '/overview'); await page.waitForTimeout(1500); console.log(errors); }
  for (const [n, [route, want]] of ROUTES.entries()) {
    await page.goto(origin + route); await page.waitForTimeout(700);
    const seen = new Set(await page.$$eval('[data-ui]', els => els.map(e => e.getAttribute('data-ui'))));
    const missing = want.filter(w => !seen.has(w));
    const hscroll = await page.evaluate(() => document.documentElement.scrollWidth > innerWidth + 1);
    out.push(`${route.replace(/[0-9a-f-]{36}/, ':id')}: ${missing.length ? 'MISSING ' + missing.join(', ') : 'ok'}${hscroll ? ', horizontal scroll' : ''}`);
    await page.screenshot({ path: `${shots}/${n + 1}-${route.split('/')[1]}${route.split('/')[2] ? '-detail' : ''}-1440.png` });
    // The owner works in the dark theme: every route is also shot in it.
    await page.emulateMedia({ colorScheme: 'dark' });
    await page.screenshot({ path: `${shots}/${n + 1}-${route.split('/')[1]}${route.split('/')[2] ? '-detail' : ''}-1440-dark.png` });
    await page.emulateMedia({ colorScheme: 'light' });
    for (const f of await layoutFindings(page)) out.push(`  ${f}`);
  }
  // OR-29, OR-30, OR-40: Overview counters show no zero chip and start with the bold total; Needs you shows 3 and +K.
  await page.goto(origin + '/overview'); await page.waitForSelector('[data-ui=needs-you] [data-ui=decision-block]');
  const ov = await page.evaluate(() => {
    const blocks = [...document.querySelectorAll('[data-ui=ops-summary] [data-ui=state-counters]')];
    const shown = document.querySelectorAll('[data-ui=needs-you] [data-ui=decision-block], [data-ui=needs-you] [data-ui=problem-row]').length;
    return { zero: blocks.some(b => [...b.querySelectorAll('.counter')].some(n => /: 0$/.test(n.getAttribute('aria-label') ?? ''))), sigma: blocks.length > 0 && blocks.every(b => b.firstElementChild?.classList.contains('sigma')),
      shown, more: [...document.querySelectorAll('[data-ui=needs-you] button')].some(x => /^\+\d+$/.test(x.textContent ?? '')), host0: [...document.querySelectorAll('[data-ui=ops-summary] .kv .num')].some(n => /^≥/.test(n.textContent ?? '')) };
  });
  out.push(`overview counters (OR-29): ${ov.zero ? 'ZERO CHIP SHOWN' : 'no zero chip'}, ${ov.sigma ? 'total first' : 'TOTAL NOT FIRST'}, ${ov.host0 ? 'HOST LOWER BOUND SHOWN' : 'no host lower bound'}`);
  out.push(`needs you (OR-30, OR-40): ${ov.shown} shown${ov.shown > 3 ? '  OVER 3' : ''}, ${ov.more ? '+K expander' : 'NO +K EXPANDER'}`);
  // OR-39: the task detail shows no total that is unknown (no empty tokens or cost fields).
  await page.goto(origin + `/tasks/${running}`); await page.waitForSelector('[data-ui=detail] .detail-title');
  const empty = await page.$$eval('[data-ui=detail] .totals .b-unavailable', els => els.length);
  out.push(`task detail (OR-39): ${empty ? empty + ' EMPTY TOTALS' : 'no empty totals'}`);
  // Narrow width: the rail stays, the opened item takes the place of the catalogue.
  const narrow = await b.newPage({ viewport: { width: 600, height: 900 } });
  await narrow.goto(origin + `/tasks/${waiting}`); await narrow.waitForTimeout(700);
  const top = await narrow.evaluate(() => document.querySelector('[data-ui=detail]')?.getBoundingClientRect().top ?? null);
  const rail = await narrow.locator('[data-ui=nav-rail]').isVisible();
  out.push(`/tasks/:id @600px: detail top ${top === null ? 'missing' : Math.round(top) + ' px'}${top !== null && top > 780 ? '  OUTSIDE VIEWPORT' : ''}, nav rail ${rail ? 'visible' : 'HIDDEN'}`);
  await narrow.screenshot({ path: `${shots}/tasks-id-600.png` });

  // Click-to-detail latency: row click until the detail title shows.
  const times: number[] = [];
  for (let i = 0; i < 5; i++) {
    await page.goto(origin + '/tasks'); await page.waitForSelector('.vl-row .row-link');
    times.push(await page.evaluate(async i => { const l = document.querySelectorAll<HTMLElement>('.vl-row .row-link')[i]; const t0 = performance.now(); l.click(); while (!document.querySelector('[data-ui=detail] .detail-title')) await new Promise(r => requestAnimationFrame(r)); return performance.now() - t0; }, i));
  }
  times.sort((a, z) => a - z);
  out.push(`click-to-detail: median ${times[2].toFixed(0)} ms of 5 (target <= 100 ms)${times[2] > 100 ? '  OVER TARGET' : ''}`);

  // Change-to-screen: a committed task change until its row shows the new state, through the live feed.
  await page.goto(origin + '/tasks?group=none'); await page.waitForSelector('.vl-row .row-link');
  const firstId = await page.$eval('.vl-row .row-link', a => (a as HTMLAnchorElement).pathname.split('/')[2]);
  const t0 = Date.now(); await fetch(origin + '/api/ops/task_update', { method: 'POST', headers: { 'content-type': 'application/json' }, body: JSON.stringify({ taskId: firstId, action: 'pause' }) });
  let ms = -1; for (let i = 0; i < 100; i++) { if (await page.$(`.vl-row [aria-label^="Paused"]`)) { ms = Date.now() - t0; break; } await page.waitForTimeout(20); }
  out.push(`change-to-screen: ${ms < 0 ? 'NOT SEEN within 2 s' : ms + ' ms'} (target <= 1000 ms)${ms > 1000 ? '  OVER TARGET' : ''}`);
  // Pins (nav-rail.md): pin from the item, it stays in its group too; Alt+ArrowUp reorders; the order survives a reload (person settings).
  await page.goto(origin + '/overview'); await page.waitForSelector('[data-ui=nav-rail]');
  for (const n of ['Tasks', 'Hosts']) { await page.locator('.rail-g:not(.pinned)').getByRole('button', { name: 'Pin ' + n, exact: true }).click(); await page.waitForTimeout(150); }
  const pinsOf = () => page.$$eval('.rail-g.pinned .nav-i', els => els.map(e => e.getAttribute('data-id')));
  const twice = await page.locator('.rail-g:not(.pinned) .nav-i.pinned[data-id=tasks]').count();
  await page.locator('.rail-g.pinned [data-id=hosts] a').focus(); await page.keyboard.press('Alt+ArrowUp'); await page.waitForTimeout(300);
  const moved = (await pinsOf()).join(',');
  await page.reload(); await page.waitForTimeout(700);
  const kept = (await pinsOf()).join(',');
  out.push(`pins: in group ${twice ? 'kept' : 'MISSING'}, reorder ${moved === 'hosts,tasks' ? 'ok' : 'WRONG ' + moved}, after reload ${kept === 'hosts,tasks' ? 'kept' : 'LOST ' + kept}`);
  await page.emulateMedia({ colorScheme: 'dark' });
  await page.locator('.rail-g.pinned [data-id=tasks]').click({ button: 'right' }); await page.waitForTimeout(200);
  out.push(`pin menu: ${(await page.locator('.rail-menu [role=menuitem]').allInnerTexts()).join(', ') || 'MISSING'}`);
  await page.screenshot({ path: `${shots}/pins-dark.png`, clip: { x: 0, y: 0, width: 520, height: 520 } });
  await page.keyboard.press('Escape'); await page.emulateMedia({ colorScheme: 'light' });
  // Requirement D20: scoped method evidence and a launchable explicit Lab draft; risk: hidden quality basis or unsaved generated record fields.
  await page.goto(origin + '/catalogue/catalogue');
  await page.getByRole('heading', {name: 'Methods', exact: true}).waitFor();
  await page.getByRole('heading', {name: 'Compare methods in Lab', exact: true}).waitFor();
  await page.getByRole('button', {name: 'hello-world', exact: true}).click();
  await page.getByText('Basic source discovery · search-basic', {exact: true}).waitFor();
  assert.ok(await page.getByText('Recommended for', {exact: true}).count());
  // Controlled render fixture: a measured partial comparison score must stay visible with its population.
  const preview = await (await fetch(origin + '/api/ops/catalogue', {method:'POST',headers:{'content-type':'application/json'},body:JSON.stringify({projectId:'hello-world'})})).json();
  preview.methods[0].assessment = {...preview.methods[0].assessment, basis:'lab', score:0.5, scoreForm:'typed', partial:true, eligibleCells:1,expectedCells:2,examples:1,failedCells:1,excludedCells:1,state:'stopped',stopReason:'fixture',variant:'preview-variant',split:'held_out',evaluator:'typed:labels:typed',evaluatorVersion:'fixture-version',reason:'One observed cell; one failed cell is excluded'};
  await page.route('**/api/ops/catalogue', route => route.request().postDataJSON().projectId === 'hello-world' ? route.fulfill({status:200,contentType:'application/json',body:JSON.stringify(preview)}) : route.continue());
  await page.reload();
  await page.getByRole('button', {name:'hello-world',exact:true}).click();
  await page.getByText('0.500 · partial/incomplete', {exact:true}).waitFor();
  assert.ok(await page.getByText('Comparison score', {exact:true}).count());
  for (const colorScheme of ['dark', 'light'] as const) { await page.emulateMedia({colorScheme}); await page.screenshot({path: `${shots}/catalogue-methods-${colorScheme}.png`}); }
  await page.unroute('**/api/ops/catalogue');
  expectedCatalogueFailure = true;
  await page.route('**/api/ops/catalogue', route => route.fulfill({status:503,contentType:'application/json',body:JSON.stringify({message:'fixture catalogue unavailable',error:'Service Unavailable',statusCode:503})}));
  await page.getByRole('button', {name:'cs',exact:true}).click();
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
  await page.getByRole('group', {name:'Select Basic source discovery',exact:true}).getByRole('button').click();
  await page.getByRole('button', {name:'hello-world',exact:true}).click();
  try {
    await previousHit;
    assert.equal(await page.getByRole('button', {name:'Recommend for project',exact:true}).count(), 0);
    await page.getByRole('button', {name:'cs',exact:true}).click();
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
  await page.getByText('Executable digest: digest-ui-fixture', {exact:false}).waitFor();
  await page.getByText('Catalogue identity: identity-ui-fixture', {exact:false}).waitFor();
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
