// Requirement: installed versions, offered updates and unknown/stale observations remain distinct.
// Risk: missing observations look healthy, old actor data leaks, or an activation timeout reloads an old app.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { build } from 'esbuild';
const bundle = await build({ stdin: { contents: "export * from './app/src/version.ts';export * from './app/src/access.ts';", resolveDir: process.cwd(), loader: 'ts' }, bundle: true, write: false, platform: 'node', format: 'esm', jsx: 'automatic', jsxImportSource: 'preact' });
const v = await import('data:text/javascript;base64,' + Buffer.from(bundle.outputFiles[0].text).toString('base64'));
v.registerAccessValidator(async () => ({ identityKey: 'fixture@trial.test', grantsVersion: 'one' }));
v.registerAccessBaseline(async () => ({ cursor: 0, activate() {}, dispose() {} }));
await v.ensureAccessGeneration();
const snapshot = () => ({ coordinator: { version: '1.261115.5', started: new Date().toISOString() }, ui: { kind: 'bundled', version: '1.261115.5', backendVersion: '1.261115.5' }, hosts: [], serverNow: Date.now(), staleAfterMs: 60000 });
const html = '<script src="/assets/main-fixture.js"></script>';
const tick = () => new Promise<void>(resolve => setImmediate(resolve));

test('unknown comparisons preserve failed and pending updates', () => {
  assert.equal(v.versionInfo().rows[0].state, 'unknown');
  v.updateReady.value = true; v.updateChecked.value = false;
  assert.equal(v.versionInfo().updateReady, false, 'unverified site availability exposed Update');
  assert.equal(v.olderThan('dev', '1.261115.5'), null);
  const host = { id: 'fixture', version: null, lastSeenAt: 10, autoUpdate: 'on', update: null, behind: null };
  assert.equal(v.hostState(host, 10, 100).state, 'unknown');
  assert.equal(v.hostState({ ...host, lastSeenAt: null, update: { state: 'failed', version: '1.261115.5', error: 'disk full' } }, 10, 100).tone, 'danger');
  assert.equal(v.hostState({ ...host, version: 'dev', update: { state: 'error', version: '1.261115.5' } }, 10, 100).state, 'update error');
  assert.equal(v.hostState({ ...host, update: { state: 'pending' } }, 10, 100).state, 'update pending');
  assert.equal(v.hostState({ ...host, version: '1.261115.6', behind: false, update: { state: 'error', version: '1.261115.5' } }, 10, 100).state, 'current');
});

test('failed refresh retains a timestamped snapshot with unknown status', () => {
  const data = snapshot(); (data.hosts as any[]).push({ id: 'fixture', version: null, lastSeenAt: null, autoUpdate: 'on', update: { state: 'failed', error: 'disk full' }, behind: null });
  v.versions.value = data; v.versionObservedAt.value = Date.now(); v.versionError.value = 'unreachable';
  const coordinator = v.versionInfo().rows.find((r: any) => r.key === 'coordinator');
  assert.match(coordinator.state, /unknown/); assert.match(coordinator.note, /unreachable/);
  assert.ok(coordinator.times.some((t: any) => t.label === 'observed' && typeof t.at === 'number'), 'the observed moment is a time, shown compact with the exact time on hover (OR-2)');
  assert.equal(coordinator.tone, 'attention');
  assert.equal(v.versionInfo().rows.find((r: any) => r.key === 'host:fixture').tone, 'danger', 'stale snapshot erased a known failure alert');
});

test('the open overview recounts ages and states from one later present', () => {
  const now = Date.now(), data = snapshot();
  data.serverNow = now; (data as any).gateway = { version: '1.261115.5', state: 'ok', observedAt: now - 40_000 };
  v.versions.value = data; v.versionObservedAt.value = now; v.versionError.value = null;
  const info = v.versionInfo(now), gateway = (rows: any[]) => rows.find((r: any) => r.key === 'gateway');
  assert.equal(gateway(info.rows).state, 'ok'); assert.equal(gateway(info.rows).times[0].now - gateway(info.rows).times[0].at, 40_000);
  const later = info.at!(now + 30_000).rows;
  assert.equal(gateway(later).times[0].now - gateway(later).times[0].at, 70_000, 'the age moves on with the present');
  assert.match(gateway(later).state, /unknown/, 'and the state next to it reflects the same present');
});

test('access invalidation clears private version data and rejects a late read', async () => {
  const originalFetch = globalThis.fetch;
  Object.defineProperty(globalThis, 'document', { configurable: true, value: { head: { innerHTML: html } } });
  let release!: (value: Response) => void;
  globalThis.fetch = async (url: any) => url === '/overview' ? new Response(html, { headers: { 'content-type': 'text/html' } }) : new Promise<Response>(resolve => { release = resolve; });
  try {
    const read = v.loadVersions(); await tick();
    v.invalidateAccess('actor changed'); assert.equal(v.versions.value, null);
    await v.ensureAccessGeneration(); release(Response.json(snapshot())); await read;
    assert.equal(v.versions.value, null, 'old actor snapshot crossed the fence');
    assert.equal(v.tabVersion.value, null);
  } finally { globalThis.fetch = originalFetch; }
});

test('worker activation timeout remains retryable and does not reload', async () => {
  const originalFetch = globalThis.fetch, originalTimer = globalThis.setTimeout;
  const originalNavigator = Object.getOwnPropertyDescriptor(globalThis, 'navigator');
  let reloads = 0;
  Object.defineProperty(globalThis, 'location', { configurable: true, value: { reload: () => { reloads++; } } });
  Object.defineProperty(globalThis, 'navigator', { configurable: true, value: { serviceWorker: {
    getRegistration: async () => ({ update: async () => {}, waiting: { postMessage() {} } }),
    addEventListener() {}, removeEventListener() {}, controller: null,
  } } });
  globalThis.fetch = async () => new Response(html, { headers: { 'content-type': 'text/html' } });
  globalThis.setTimeout = ((fn: () => void) => { queueMicrotask(fn); return 0; }) as any;
  try {
    await v.applyUpdate(); assert.equal(reloads, 0); assert.equal(v.updating.value, false);
    assert.match(v.updateError.value, /activation is still pending/);
  } finally {
    globalThis.fetch = originalFetch; globalThis.setTimeout = originalTimer;
    if (originalNavigator) Object.defineProperty(globalThis, 'navigator', originalNavigator);
  }
});

test('narrow rail exposes Versions and a long overview stays within the viewport', { skip: !process.env.FLEET_CONTROL_BROWSER }, async () => {
  const { chromium } = await import('playwright');
  const { readFileSync } = await import('node:fs');
  const { tokensCss } = await import(new URL('../../packages/ui/scripts/tokens.mjs', import.meta.url).href) as { tokensCss: () => string };
  const fixture = await build({ stdin: { contents: `
    import {render} from 'preact';import {AppShell} from './packages/ui/src/index.ts';
    const version={current:'1.261115.5',tone:'neutral',updateReady:false,onUpdate:()=>{},rows:Array.from({length:50},(_,i)=>({key:String(i),part:'host '+i,icon:'terminal',version:'1.261115.5',state:'unknown',tone:'attention'}))};
    render(<AppShell nav={[]} current="tasks" onGo={()=>{}} waiting={0} user={{name:'Fixture person',onLogout:()=>{},version}}><p>Fixture work</p></AppShell>,document.body);
  `, resolveDir: process.cwd(), loader: 'tsx' }, bundle: true, write: false, format: 'iife', jsx: 'automatic', jsxImportSource: 'preact' });
  const browser = await chromium.launch({ headless: true });
  const page = await browser.newPage({ viewport: { width: 640, height: 480 } });
  try {
    await page.setContent('<body></body>'); await page.addStyleTag({ content: tokensCss() + readFileSync('packages/ui/src/styles.css', 'utf8') });
    await page.addScriptTag({ content: fixture.outputFiles[0].text });
    await page.getByRole('button', { name: 'Fixture person', exact: true }).click();
    await page.getByRole('menuitem', { name: 'Versions', exact: true }).click();
    const panel = page.getByRole('group', { name: 'Versions', exact: true });
    const box = await panel.boundingBox(); assert.ok(box && box.y >= 0 && box.y + box.height <= 480);
    assert.equal(await panel.evaluate(el => el.scrollHeight > el.clientHeight), true);
    await panel.getByText('host 49', { exact: true }).scrollIntoViewIfNeeded();
    assert.equal(await panel.getByText('host 49', { exact: true }).isVisible(), true);
  } finally { await browser.close(); }
});

test('the open version overview recounts its ages on a timer', { skip: !process.env.FLEET_CONTROL_BROWSER }, async () => {
  const { chromium } = await import('playwright');
  const fixture = await build({ stdin: { contents: `
    import {render} from 'preact';import {AppShell} from './packages/ui/src/index.ts';
    const version={current:'1.261115.5',tone:'neutral',updateReady:false,onUpdate:()=>{},rows:[{key:'gateway',part:'Gateway',icon:'shield',version:'1.261115.5',state:'ok',tone:'success',times:[{label:'checked',at:Date.now()-40000}]}]};
    render(<AppShell nav={[]} current="tasks" onGo={()=>{}} waiting={0} user={{name:'Fixture person',onLogout:()=>{},version}}><p>Fixture work</p></AppShell>,document.body);
  `, resolveDir: process.cwd(), loader: 'tsx' }, bundle: true, write: false, format: 'iife', jsx: 'automatic', jsxImportSource: 'preact' });
  const browser = await chromium.launch({ headless: true });
  const page = await browser.newPage();
  try {
    await page.clock.install(); await page.setContent('<body></body>');
    await page.addScriptTag({ content: fixture.outputFiles[0].text });
    await page.getByRole('button', { name: 'Fixture person', exact: true }).click();
    await page.getByRole('menuitem', { name: 'Versions', exact: true }).click();
    const age = page.getByRole('group', { name: 'Versions', exact: true }).locator('time');
    const first = await age.innerText();
    await page.clock.runFor(10 * 60_000);
    assert.notEqual(await age.innerText(), first, 'the age moved on while the overview stayed open');
    assert.equal(await age.innerText(), '10m');
  } finally { await browser.close(); }
});
