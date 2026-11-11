// Requirement: laboratory.md Create experiment. Risk: UI starts paid work, loses a draft, or repeats an uncertain POST.
// CI only: the owner Mac must not launch Chromium, including through a wrapper.
import assert from 'node:assert/strict';
import { mkdirSync, readFileSync } from 'node:fs';
import { chromium } from 'playwright';
import { checkServer } from './seed.ts';
if (!process.env.CI) throw Error('CI-only browser check; local browser launches are prohibited.');
const { origin, seed, close } = await checkServer({ store: true });
const bytes = readFileSync('config/lab/examples/fleet-typed-decision-smoke.json');
seed.execution({ id: 'example-source', kind: 'tool_run', state: 'succeeded' });
seed.artifact({ id: 'approved-example', executionId: 'example-source', name: 'UI approved example', mediaType: 'application/json', bytes });
// Import the approved lab artifact through the real signed-S3 core route; the initial artifact registers the fake store.
const imported = await fetch(origin + '/api/ops/lab_examples_add', { method: 'POST', headers: { 'content-type': 'application/json', origin },
  body: JSON.stringify({ examples: [JSON.parse(bytes.toString())], approved: true }) });
const importedBody = await imported.json() as { examples: { artifactId: string }[]; message?: string };
assert.equal(imported.status, 200, JSON.stringify(importedBody));
const exampleId = importedBody.examples[0].artifactId;
const counts = () => seed.rows("SELECT (SELECT count(*) FROM execution) AS executions,(SELECT count(*) FROM document WHERE kind='command') AS commands,(SELECT count(*) FROM document WHERE kind='pipeline-run') AS runs")[0];
const before = counts();
const browser = await chromium.launch();
try {
  const context = await browser.newContext({ viewport: { width: 1440, height: 900 }, hasTouch: true });
  const p = await context.newPage();
  const errors: string[] = []; p.on('pageerror', e => errors.push(e.message));
  let posts = 0; p.on('request', r => { if (r.url().endsWith('/api/ops/lab_experiment_create')) posts++; });
  const pick = async (button: string | RegExp, group: string, id: string, multiple = false) => {
    await p.getByRole('button', { name: button, exact: typeof button === 'string' }).click();
    await p.getByRole('searchbox', { name: `Search ${group}` }).fill(id);
    const row = p.getByRole('grid', { name: group, exact: true }).getByRole('row').filter({ has: p.locator('.id', { hasText: new RegExp('^' + id + '$') }) });
    if (group === 'Projects') { await row.getByRole('button').focus(); await p.keyboard.press('Enter'); } else await row.getByRole('button').click();
    if (group === 'Projects') await p.waitForFunction(() => document.activeElement?.textContent?.startsWith('Project:'));
    if (multiple) await p.getByRole('button', { name: 'Done choosing', exact: true }).click();
  };
  const fill = async (kind: 'single' | 'ab' | 'loop') => {
    await p.goto(origin + '/lab/new');
    await pick(/^Project:/, 'Projects', 'hello-world');
    await pick(/^Pipeline:/, 'Pipelines', 'typed-decision');
    await p.getByRole('group', { name: 'Experiment kind', exact: true }).getByRole('button', { name: kind, exact: true }).click();
    await pick(/^Approved examples:/, 'Approved examples', exampleId, true);
    await p.getByRole('textbox', { name: 'Goal', exact: true }).fill('Create only; retain the real experiment record');
    await p.getByRole('textbox', { name: 'Hypothesis', exact: true }).fill('Draft creation performs no execution or provider work');
    if (kind === 'ab') { await p.getByRole('button', { name: 'Add variant', exact: true }).click(); await p.getByRole('textbox', { name: 'Variant 2 label', exact: true }).fill('B'); }
    if (kind === 'loop') { await p.getByRole('button', { name: 'Add evaluator', exact: true }).click(); await pick(/^Primary evaluator:/, 'Primary evaluators', 'script:example.checks:pass_fail'); }
  };
  mkdirSync('design-report', { recursive: true });
  const created = async () => { await p.getByRole('button', { name: 'Create draft', exact: true }).click(); await p.waitForURL(/\/lab\/[a-f0-9-]+$/); await p.getByRole('button', { name: 'Launch', exact: true }).waitFor(); };
  await p.route('**/api/ops/lab_examples_list', route => route.request().postDataJSON()?.state === 'approved'
    ? route.fulfill({ json: { examples: [] } }) : route.continue());
  const emptyRead = p.waitForResponse(r => r.url().endsWith('/api/ops/lab_examples_list') && r.request().postDataJSON()?.state === 'approved');
  await p.goto(origin + '/lab/new'); await emptyRead;
  await p.getByRole('button', { name: /^Approved examples:/ }).click();
  await p.getByText('No available choices. Check access and the required definitions or approved examples.', { exact: true }).waitFor();
  assert.equal(await p.getByRole('button', { name: 'Create draft', exact: true }).getAttribute('aria-disabled'), 'true');
  await p.unroute('**/api/ops/lab_examples_list');
  await p.goto(origin + '/lab');
  await p.getByRole('button', { name: 'New experiment', exact: true }).click();
  await p.getByRole('button', { name: 'Create draft', exact: true }).waitFor();
  // All server-supported kinds remain available, and creating any of them never launches.
  for (const kind of ['single', 'ab', 'loop'] as const) {
    if (kind === 'single') await p.setViewportSize({ width: 600, height: 900 });
    await fill(kind); if (kind === 'single') {
      assert.ok((await p.getByRole('spinbutton', { name: 'Repeats', exact: true }).boundingBox())!.height >= 44, 'TextField meets the existing touch target minimum');
      assert.ok(await p.getByRole('article', { name: 'Create experiment', exact: true }).evaluate(e => e.scrollWidth <= e.clientWidth));
      assert.ok(await p.evaluate(() => document.documentElement.scrollHeight <= innerHeight), 'the work area, not the page, scrolls');
      await p.screenshot({ path: 'design-report/lab-create-form.png' });
    } await created();
    const id = new URL(p.url()).pathname.split('/').pop()!;
    const [row] = seed.rows(`SELECT state,kind FROM experiment WHERE id='${id}'`);
    assert.deepEqual(row, { state: 'draft', kind });
    await p.setViewportSize({ width: 1440, height: 900 });
  }
  assert.equal(posts, 3); assert.deepEqual(counts(), before, 'creation must produce no executions, commands or pipeline steps');
  // Confirmed rejection preserves edits and permits correction, rather than widening the submitted request.
  await fill('single'); await p.getByRole('textbox', { name: 'Variant 1 label', exact: true }).fill('invalid/label');
  await p.getByRole('button', { name: 'Create draft', exact: true }).click();
  await p.getByText('Invalid variant label', { exact: true }).waitFor();
  assert.equal(await p.getByRole('textbox', { name: 'Goal', exact: true }).inputValue(), 'Create only; retain the real experiment record');
  await p.getByRole('textbox', { name: 'Variant 1 label', exact: true }).fill('A');
  // A confirmed create plus failed GET is not an unknown creation or a reason to POST again.
  let releaseRead!: () => void;
  const heldRead = new Promise<void>(resolve => { releaseRead = resolve; });
  await p.route('**/api/ops/lab_experiment_get', async route => {
    await heldRead; try { await route.abort('failed'); } catch { /* reload already closed the old request */ }
  });
  await p.getByRole('button', { name: 'Create draft', exact: true }).click();
  await p.waitForFunction(() => Object.keys(sessionStorage).some(k => { if (!k.startsWith('fleet-lab-create:')) return false; const x = JSON.parse(sessionStorage.getItem(k)!); return x.state === 'created' && x.id && !x.message; }));
  const afterConfirmed = posts;
  await p.reload();
  await p.getByRole('button', { name: 'Read details again', exact: true }).waitFor();
  releaseRead();
  await p.unroute('**/api/ops/lab_experiment_get');
  await p.getByRole('button', { name: 'Read details again', exact: true }).click(); await p.waitForURL(/\/lab\/[a-f0-9-]+$/);
  assert.equal(posts, afterConfirmed);
  // Lose the reply after the real transaction commits; double activation and reload must not replay it.
  await fill('single');
  await p.route('**/api/ops/lab_experiment_create', async route => { await route.fetch(); await route.abort('failed'); });
  const beforeLost = posts, draftsBefore = seed.rows('SELECT count(*) AS n FROM experiment')[0].n;
  await p.getByRole('button', { name: 'Create draft', exact: true }).dblclick();
  await p.getByText('Creation unconfirmed', { exact: true }).waitFor();
  await p.reload(); await p.getByText('Creation unconfirmed', { exact: true }).waitFor();
  assert.equal(await p.getByRole('button', { name: 'Create draft', exact: true }).getAttribute('aria-disabled'), 'true');
  assert.equal(posts, beforeLost + 1); assert.equal(Number(seed.rows('SELECT count(*) AS n FROM experiment')[0].n), Number(draftsBefore) + 1);
  assert.deepEqual(counts(), before); assert.deepEqual(errors, []);
  mkdirSync('design-report', { recursive: true });
  await p.screenshot({ path: 'design-report/lab-create-uncertain.png' });
  await p.unroute('**/api/ops/lab_experiment_create');
  await p.getByRole('button', { name: 'Discard local draft', exact: true }).click();
  await p.getByRole('textbox', { name: 'Goal', exact: true }).fill('Actor-scoped draft');
  await p.reload();
  await p.waitForFunction(() => [...document.querySelectorAll('button')].some(b => b.textContent?.startsWith('Project:') && !b.hasAttribute('aria-disabled')));
  assert.equal(await p.getByRole('textbox', { name: 'Goal', exact: true }).inputValue(), 'Actor-scoped draft');
  // Grant change invalidates the key even if another tab already updated the common grant marker.
  await p.getByRole('button', { name: /^Project:/ }).click();
  await p.getByRole('searchbox', { name: 'Search Projects' }).waitFor();
  await p.route('**/api/ops/me', async route => { const response = await route.fetch(); const m = await response.json(); await route.fulfill({ json: { ...m, grantsVersion: 'changed-for-fixture' } }); });
  await p.evaluate(() => window.dispatchEvent(new Event('focus')));
  await p.getByText('Access changed. Reload before creating a draft.', { exact: true }).waitFor();
  assert.equal(await p.getByRole('textbox', { name: 'Goal', exact: true }).inputValue(), '');
  assert.equal(await p.getByRole('grid', { name: 'Projects', exact: true }).count(), 0);
  assert.equal(await p.getByRole('searchbox', { name: 'Search Projects' }).count(), 0);
  assert.deepEqual(counts(), before);
  await p.unroute('**/api/ops/me');
  await p.goto(origin + '/lab/new');
  await p.getByRole('textbox', { name: 'Goal', exact: true }).fill('Only the original actor may restore this');
  await p.route('**/api/ops/me', async route => { const response = await route.fetch(); const m = await response.json(); await route.fulfill({ json: { ...m, email: 'agents@trial.test' } }); });
  await p.reload();
  await p.waitForFunction(() => [...document.querySelectorAll('button')].some(b => b.textContent?.startsWith('Project:') && !b.hasAttribute('aria-disabled')));
  assert.equal(await p.getByRole('textbox', { name: 'Goal', exact: true }).inputValue(), '');
  assert.equal(await p.evaluate(() => Object.keys(sessionStorage).some(k => k.startsWith('fleet-lab-create:owner@trial.test:'))), false);
  await context.close();
} finally { await browser.close(); await close(); }
console.log('Lab creation: all kinds, draft-only readback, rejection, uncertain POST and grant-clear passed; zero provider work.');
