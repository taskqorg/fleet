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
const counts = () => seed.rows("SELECT (SELECT count(*) FROM execution) AS executions,(SELECT count(*) FROM command) AS commands,(SELECT count(*) FROM pipeline_run) AS runs")[0];
const before = counts();
const browser = await chromium.launch();
try {
  const context = await browser.newContext({ viewport: { width: 1440, height: 900 }, hasTouch: true });
  const p = await context.newPage();
  const errors: string[] = []; p.on('pageerror', e => errors.push(e.message));
  let posts = 0; p.on('request', r => { if (r.url().endsWith('/api/ops/lab_experiment_create')) posts++; });
  const pickerOf = (label: string) =>
    p.locator('[data-ui="entity-picker"]').filter({ has: p.locator('.ep-label', { hasText: new RegExp(`^${label}$`) }) });
  const pick = async (label: string, searchName: string, id: string, multiple = false) => {
    const picker = pickerOf(label);
    const trigger = picker.locator('button.ep-btn');
    await trigger.click();
    await picker.getByRole('searchbox', { name: searchName }).fill(id);
    const item = picker.getByRole('tree', { name: label, exact: true }).locator(`[role="treeitem"][data-node-id="${id}"]`);
    if (label === 'Project') {
      await item.focus();
      await p.keyboard.press('Enter');
      await picker.getByRole('searchbox', { name: searchName }).waitFor({ state: 'detached' });
      await p.waitForFunction(el => !!el && document.activeElement === el && !el.hasAttribute('aria-disabled'), await trigger.elementHandle());
    } else {
      await item.click();
      if (multiple) {
        await picker.getByRole('button', { name: 'Done', exact: true }).click();
        await picker.getByRole('searchbox', { name: searchName }).waitFor({ state: 'detached' });
      }
    }
  };
  const fill = async (kind: 'single' | 'ab' | 'loop') => {
    await p.goto(origin + '/lab/new');
    await pick('Project', 'Search projects', 'hello-world');
    await pick('Pipeline', 'Search pipelines', 'typed-decision');
    await p.getByRole('group', { name: 'Experiment kind', exact: true }).getByRole('button', { name: kind, exact: true }).click();
    await pick('Approved examples', 'Search approved examples', exampleId, true);
    await p.getByRole('textbox', { name: 'Goal', exact: true }).fill('Create only; retain the real experiment record');
    await p.getByRole('textbox', { name: 'Hypothesis', exact: true }).fill('Draft creation performs no execution or provider work');
    if (kind === 'ab') { await p.getByRole('button', { name: 'Add variant', exact: true }).click(); await p.getByRole('textbox', { name: 'Variant 2 label', exact: true }).fill('B'); }
    if (kind === 'loop') { await p.getByRole('button', { name: 'Add evaluator', exact: true }).click(); await pick('Primary evaluator', 'Search evaluators', 'script:example.checks:pass_fail'); }
  };
  mkdirSync('design-report', { recursive: true });
  const created = async () => { await p.getByRole('button', { name: 'Create draft', exact: true }).click(); await p.waitForURL(/\/lab\/[a-f0-9-]+$/); await p.getByRole('button', { name: 'Launch', exact: true }).waitFor(); };
  await p.route('**/api/ops/lab_examples_list', route => route.request().postDataJSON()?.state === 'approved'
    ? route.fulfill({ json: { examples: [] } }) : route.continue());
  const emptyRead = p.waitForResponse(r => r.url().endsWith('/api/ops/lab_examples_list') && r.request().postDataJSON()?.state === 'approved');
  await p.goto(origin + '/lab/new'); await emptyRead;
  await pickerOf('Approved examples').locator('button.ep-btn').click();
  await pickerOf('Approved examples').getByText('No items.', { exact: true }).waitFor();
  assert.equal(await p.getByRole('button', { name: 'Create draft', exact: true }).getAttribute('aria-disabled'), 'true');
  await p.unroute('**/api/ops/lab_examples_list');
  await p.goto(origin + '/lab');
  await p.getByRole('button', { name: 'New experiment', exact: true }).click();
  await p.getByRole('button', { name: 'Create draft', exact: true }).waitFor();
  // Focus returns to the invoking EntityPicker trigger after choosing via keyboard.
  const projectTrigger = pickerOf('Project').locator('button.ep-btn');
  await projectTrigger.click();
  await pickerOf('Project').getByRole('searchbox', { name: 'Search projects' }).fill('hello-world');
  await pickerOf('Project').getByRole('tree', { name: 'Project', exact: true }).locator('[role="treeitem"][data-node-id="hello-world"]').focus();
  await p.keyboard.press('Enter');
  await pickerOf('Project').getByRole('searchbox', { name: 'Search projects' }).waitFor({ state: 'detached' });
  await p.waitForFunction(el => document.activeElement === el, await projectTrigger.elementHandle());
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
  await p.getByText('Creation unconfirmed', { exact: true }).waitFor();
  releaseRead();
  await p.unroute('**/api/ops/lab_experiment_get');
  assert.equal(await p.getByRole('button', { name: 'Read details again', exact: true }).count(), 0, 'server receipt identity is not restored from browser input');
  await p.getByRole('button', { name: 'Review experiments', exact: true }).click();
  assert.equal(posts, afterConfirmed);
  await p.goto(origin + '/lab/new'); await p.getByText('Creation unconfirmed', { exact: true }).waitFor();
  await p.getByRole('button', { name: 'Discard local draft', exact: true }).click();
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
  await pickerOf('Project').locator('button.ep-btn:not([aria-disabled])').waitFor();
  assert.equal(await p.getByRole('textbox', { name: 'Goal', exact: true }).inputValue(), 'Actor-scoped draft');
  // Grant change invalidates the key even if another tab already updated the common grant marker.
  await pickerOf('Project').locator('button.ep-btn').click();
  await pickerOf('Project').getByRole('searchbox', { name: 'Search projects' }).waitFor();
  await p.route('**/api/ops/me', async route => { const response = await route.fetch(); const m = await response.json(); await route.fulfill({ json: { ...m, grantsVersion: 'changed-for-fixture' } }); });
  // Focus checks the person quietly; the changed grants in its reply fence (the picker closes), then the draft returns.
  const meChanged = p.waitForResponse(r => r.url().endsWith('/api/ops/me'));
  await p.evaluate(() => window.dispatchEvent(new Event('focus'))); await meChanged;
  await pickerOf('Project').getByRole('tree', { name: 'Project', exact: true }).waitFor({ state: 'detached' });
  await p.waitForFunction(() => [...document.querySelectorAll('textarea')].some(input => input.value === 'Actor-scoped draft'));
  assert.equal(await p.getByRole('textbox', { name: 'Goal', exact: true }).inputValue(), 'Actor-scoped draft', 'same actor input survives fresh target authorization after changed grants');
  assert.equal(await pickerOf('Project').getByRole('tree', { name: 'Project', exact: true }).count(), 0);
  assert.equal(await pickerOf('Project').getByRole('searchbox', { name: 'Search projects' }).count(), 0);
  assert.deepEqual(counts(), before);
  await p.unroute('**/api/ops/me');
  await p.goto(origin + '/lab/new');
  await p.getByRole('textbox', { name: 'Goal', exact: true }).fill('Only the original actor may restore this');
  await p.route('**/api/ops/me', async route => { const response = await route.fetch(); const m = await response.json(); await route.fulfill({ json: { ...m, email: 'agents@trial.test' } }); });
  await p.reload();
  await pickerOf('Project').locator('button.ep-btn:not([aria-disabled])').waitFor();
  assert.equal(await p.getByRole('textbox', { name: 'Goal', exact: true }).inputValue(), '');
  assert.equal(await p.evaluate(() => Object.keys(sessionStorage).some(k => k === 'fleet-lab-create:' + JSON.stringify([1, 'owner@trial.test']))), true, 'original actor input remains quarantined');
  await context.close();
} finally { await browser.close(); await close(); }
console.log('Lab creation: all kinds, draft-only readback, rejection, uncertain POST and grant-clear passed; zero provider work.');
