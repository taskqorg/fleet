// R2/R3: verified saves and current readiness survive background definition reads.
// Risk: an older response rewinds the editor or an unchanged reload erases a real host result.
import assert from 'node:assert/strict';
import { chromium, type Browser } from 'playwright';
import { checkServer } from './seed.ts';

export async function checkProjectEditor(browser: Browser, origin: string, host: (id: string) => Promise<(path: string, body: unknown) => Promise<Response>>) {
  const context = await browser.newContext();
  const p = await context.newPage();
  const errors: string[] = [];
  const failures: string[] = [];
  p.on('pageerror', error => errors.push(error.message));
  if (process.env.FLEET_EDITOR_TRACE) {
    p.on('request', request => { if (/\/api\/ops\/project_(update|detail|context|readiness)$/.test(request.url())) console.log('editor request', request.url(), request.postData()); });
    p.on('response', async response => { if (/\/api\/ops\/project_(update|detail|context|readiness)$/.test(response.url())) { try { console.log('editor response', response.status(), response.url(), await response.text()); } catch {} } });
  }
  const call = async (name: string, body: object) => {
    const response = await fetch(origin + '/api/ops/' + name, { method: 'POST', headers: { 'content-type': 'application/json' }, body: JSON.stringify(body) });
    assert.equal(response.status, 200, await response.clone().text());
    return response.json() as Promise<any>;
  };
  const id = 'editor-race-' + crypto.randomUUID();
  const hostId = id + '-host';
  const settle = () => p.evaluate(() => new Promise<void>(resolve => requestAnimationFrame(() => requestAnimationFrame(() => resolve()))));
  let release: (() => void) | undefined;
  try {
    const sendHost = await host(hostId);
    await call('project_create', { id, requestId: crypto.randomUUID(), content: { settings: { id, name: id, roles: { pm: 'pm' }, contextFiles: ['rules.md'], repos: [], environments: [{ hostId, checkoutPath: '/tmp/editor-race' }] }, contextFiles: [{ path: 'rules.md', text: '' }] } });
    await p.goto(origin + '/projects/' + id + '/rules');
    await p.getByRole('heading', { name: id, exact: true }).waitFor();
    // Hold an actual pre-save configuration response, while the real update and readback finish.
    let held!: () => void;
    const captured = new Promise<void>(resolve => { held = resolve; });
    const gate = new Promise<void>(resolve => { release = resolve; });
    let hold = true;
    await p.route(/\/api\/ops\/project_(detail|context)$/, async route => {
      if (!hold) return route.continue();
      hold = false;
      const response = await route.fetch();
      assert.equal((await response.json()).revision, 1);
      held(); await gate; await route.fulfill({ response });
    });
    await p.getByRole('button', { name: 'Reload project', exact: true }).click();
    await captured;
    await p.getByLabel('Name', { exact: true }).click();
    await p.getByLabel('Name', { exact: true }).fill(id + '-saved');
    await p.getByLabel('Name', { exact: true }).press('Enter');
    const update = p.waitForResponse(response => response.url().endsWith('/api/ops/project_update'));
    await p.getByRole('button', { name: 'Save project', exact: true }).click();
    const updateResponse = await update;
    assert.equal(updateResponse.request().postDataJSON().baseRevision, 1);
    const updated = await updateResponse.json();
    await p.locator('.kv-row').filter({ hasText: /Saved version2$/ }).waitFor();
    const saved = await call('project_context', { projectId: id });
    assert.equal(saved.revision, 2); assert.equal(saved.name, id + '-saved');
    assert.equal(saved.contentSha, updated.contentSha);
    const late = p.waitForResponse(response => /\/api\/ops\/project_(detail|context)$/.test(response.url()));
    release!(); await (await late).finished(); await settle();
    const renderedVersion = await p.locator('.kv-row').filter({ hasText: /Saved version/ }).innerText();
    if (!/2$/.test(renderedVersion) || await p.getByRole('heading', { name: id + '-saved', exact: true }).count() !== 1) failures.push('late pre-save response rewound verified editor: ' + renderedVersion);
    if (process.env.FLEET_EDITOR_TRACE) console.log('editor DOM after late read', renderedVersion);
    await p.unroute(/\/api\/ops\/project_(detail|context)$/);
    await p.reload(); await p.getByRole('heading', { name: id + '-saved', exact: true }).waitFor();
    const queued = p.waitForResponse(response => response.url().endsWith('/api/ops/project_readiness'));
    await p.getByRole('button', { name: 'Check readiness', exact: true }).click();
    const check = (await (await queued).json()).hosts[0];
    const posted = await sendHost('/api/hosts/events', { executionId: check.executionId, events: [{ id: 'editor-race-result', seq: 0, type: 'readiness', timestamp: Date.now(), data: { hostId, ok: false, checks: [{ check: 'checkout', target: '/tmp/editor-race', status: 'failed', reason: 'checkout-missing' }] } }] });
    assert.equal(posted.status, 200);
    await p.locator('.kv-row').filter({ hasText: /Not ready$/ }).waitFor();
    const reload = p.waitForResponse(response => /\/api\/ops\/project_(detail|context)$/.test(response.url()));
    await p.getByRole('button', { name: 'Reload project', exact: true }).click();
    await (await reload).finished(); await settle();
    const readyCount = await p.locator('.kv-row').filter({ hasText: /Not ready$/ }).count();
    if (readyCount !== 1 || await p.getByText('/tmp/editor-race · checkout-missing', { exact: true }).count() !== 1) failures.push('unchanged configuration erased actual readiness');
    if (process.env.FLEET_EDITOR_TRACE) console.log('editor DOM after unchanged reload', await p.locator('.kv-row').allTextContents());
    const changed = structuredClone(saved.content); changed.settings.name = id + '-external';
    await call('project_update', { projectId: id, requestId: crypto.randomUUID(), baseRevision: saved.revision, content: changed });
    await p.getByRole('heading', { name: id + '-external', exact: true }).waitFor();
    assert.equal(await p.locator('.kv-row').filter({ hasText: /Not ready$/ }).count(), 0, 'a changed definition invalidates the previous readiness');
    // A queued refresh compares with the verified base even before the next render,
    // while input authored during the save remains local and the busy control stays disabled.
    let written!: () => void;
    const writing = new Promise<void>(resolve => { written = resolve; });
    const saveGate = new Promise<void>(resolve => { release = resolve; });
    await p.route('**/api/ops/project_update', async route => {
      const response = await route.fetch(); assert.equal(response.status(), 200);
      written(); await saveGate; await route.fulfill({ response });
    });
    await p.getByLabel('Name', { exact: true }).fill(id + '-submitted');
    await p.getByRole('button', { name: 'Save project', exact: true }).click(); await writing;
    assert.equal(await p.getByRole('button', { name: 'Save project', exact: true }).getAttribute('aria-disabled'), 'true');
    await p.getByRole('button', { name: 'Reload project', exact: true }).click();
    await p.getByLabel('Name', { exact: true }).fill(id + '-newer');
    const refreshed = p.waitForResponse(response => /\/api\/ops\/project_(detail|context)$/.test(response.url()));
    release!(); await (await refreshed).finished();
    await p.locator('.kv-row').filter({ hasText: /Saved version4$/ }).waitFor(); await settle();
    assert.equal(await p.getByLabel('Name', { exact: true }).inputValue(), id + '-newer');
    assert.equal(await p.getByText(/definition changed while you were editing/).count(), 0, 'own verified save cannot create an external-source conflict');
    const retained = await call('project_context', { projectId: id });
    assert.equal(retained.revision, 4); assert.equal(retained.name, id + '-submitted');
    await p.unroute('**/api/ops/project_update');
    assert.deepEqual(errors, []);
    assert.deepEqual(failures, []);
    console.log('ok Project Editor real save revision/hash readback, delayed old response, and retained host readiness');
  } finally { release?.(); await context.close(); }
}

if (process.argv[1]?.endsWith('/project-editor.check.ts')) {
  const server = await checkServer(process.env.FLEET_EDITOR_PREPARED ? { env: { FLEET_RESOURCE_READS: 'on', FLEET_RESOURCE_PROCESSOR: 'on' } } : {});
  const browser = await chromium.launch(process.env.FLEET_CHROMIUM ? { executablePath: process.env.FLEET_CHROMIUM } : {});
  try { await checkProjectEditor(browser, server.origin, server.host); }
  finally { await browser.close(); await server.close(); }
}
