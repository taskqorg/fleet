// Requirement: installed versions, offered updates and unknown/stale observations remain distinct; only a confirmed current
// problem tones the version mark (nav-rail.md, Version); Update runs once and stays busy until the page goes.
// Risk: a momentary read flashes a warning, old actor data leaks, or an activation timeout reloads an old app.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { build } from 'esbuild';
import { transform } from '@solidjs/compiler';
// @ts-expect-error ESM helper module
import { solidEsbuildPlugin } from '../../packages/ui/scripts/solid-esbuild.mjs';
const bundle = await build({ stdin: { contents: "export * from './app/src/version.ts';export * from './app/src/access.ts';", resolveDir: process.cwd(), loader: 'ts' }, bundle: true, write: false, platform: 'node', format: 'esm', conditions: ['solid'], plugins: [solidEsbuildPlugin()] });
const buildSolid = async (src: string) => {
  const compiled = transform(src, { filename: 'fixture.tsx', generate: 'dom', moduleName: '@solidjs/web' });
  return build({ stdin: { contents: compiled.code, resolveDir: process.cwd(), loader: 'ts' }, bundle: true, write: false, format: 'iife', conditions: ['solid'], plugins: [solidEsbuildPlugin()] });
};
const v = await import('data:text/javascript;base64,' + Buffer.from(bundle.outputFiles[0].text).toString('base64'));
v.registerAccessValidator(async () => ({ identityKey: 'fixture@trial.test', grantsVersion: 'one' }));
v.registerAccessBaseline(async () => ({ cursor: 0, activate() {}, dispose() {} }));
await v.ensureAccessGeneration();
const snapshot = () => ({ coordinator: { version: '1.261115.5', started: new Date().toISOString() }, ui: { kind: 'bundled', version: '1.261115.5', backendVersion: '1.261115.5' }, hosts: [], serverNow: Date.now(), staleAfterMs: 60000 });
const html = '<script src="/assets/main-fixture.js"></script>';
const tick = () => new Promise<void>(resolve => setImmediate(resolve));

test('unknown comparisons preserve failed and pending updates', () => {
  assert.equal(v.versionInfo().rows[0].state, 'not checked yet');
  assert.equal(v.versionInfo().tone, 'neutral', 'a tab not yet checked is not a problem');
  v.updateReady.value = true; v.updateChecked.value = false;
  assert.equal(v.versionInfo().updateReady, false, 'unverified site availability exposed Update');
  assert.equal(v.olderThan('dev', '1.261115.5'), null);
  const host = { id: 'fixture', version: null, lastSeenAt: 10, autoUpdate: 'on', update: null, behind: null };
  assert.equal(v.hostState(host, 10, 100).state, 'unknown');
  assert.equal(v.hostState({ ...host, lastSeenAt: null, update: { state: 'failed', version: '1.261115.5', error: 'disk full' } }, 10, 100).tone, 'danger');
  assert.equal(v.hostState({ ...host, version: 'dev', update: { state: 'error', version: '1.261115.5' } }, 10, 100).state, 'update error');
  assert.equal(v.hostState({ ...host, update: { state: 'pending' } }, 10, 100).state, 'updating');
  assert.equal(v.hostState({ ...host, version: '1.261115.4', behind: true }, 10, 100).tone, 'neutral', 'behind with auto-update on updates itself');
  assert.equal(v.hostState({ ...host, version: '1.261115.4', behind: true, autoUpdate: 'off' }, 10, 100).tone, 'attention');
  assert.equal(v.hostState({ ...host, lastSeenAt: 1, version: '1.261115.5', behind: false }, 500, 100).tone, 'neutral', 'an offline host is not a version problem');
  assert.equal(v.hostState({ ...host, version: '1.261115.6', behind: false, update: { state: 'error', version: '1.261115.5' } }, 10, 100).state, 'current');
  const failing = v.hostState({ ...host, version: '1.261115.6', behind: false, pollError: { status: 409, reason: 'pipeline_format_unsupported', message: 'Unsupported pipeline run format', since: 4 } }, 10, 100);
  assert.deepEqual(failing, { state: 'polls failing', tone: 'neutral', failingSince: 4, note: 'pipeline_format_unsupported (409)' }, 'failing polls before the coordinator opens the problem are a fact, not a warning; the reason code, never the message');
  const confirmed = v.hostState({ ...host, version: '1.261115.6', behind: false, pollProblem: true, pollError: { status: 409, reason: 'pipeline_format_unsupported', message: 'x', since: 4 } }, 10, 100);
  assert.equal(confirmed.tone, 'danger', 'an open poll problem is danger');
  assert.equal(v.hostState({ ...host, lastSeenAt: 1, pollError: { status: 409, reason: 'pipeline_format_unsupported', message: 'x', since: 1 } }, 500, 100).state, 'offline', 'a snapshot aged past staleness shows the host offline, not its old failure');
});

test('a failed refresh keeps the last confirmed rows and says when it last checked, without a warning', () => {
  const data = snapshot(); (data.hosts as any[]).push({ id: 'fixture', version: null, lastSeenAt: null, autoUpdate: 'on', update: { state: 'failed', error: 'disk full' }, behind: null });
  const observed = Date.now(); v.versions.value = data; v.versionObservedAt.value = observed; v.versionError.value = 'unreachable';
  const info = v.versionInfo(), coordinator = info.rows.find((r: any) => r.key === 'coordinator');
  assert.equal(coordinator.state, 'running'); assert.equal(coordinator.tone, 'success', 'a failed read is not a problem of the coordinator');
  assert.equal(info.checkError, 'unreachable'); assert.equal(info.checkedAt, observed);
  assert.equal(info.rows.find((r: any) => r.key === 'host:fixture').tone, 'danger', 'a confirmed failure stays shown');
  assert.equal(info.tone, 'danger');
  v.versions.value = snapshot(); v.versionError.value = null;
  assert.equal(v.versionInfo().tone, 'neutral');
});

test('the open overview recounts ages and states from one later present', () => {
  const now = Date.now(), data = snapshot();
  data.serverNow = now; (data as any).gateway = { version: '1.261115.5', state: 'ok', observedAt: now - 40_000 };
  v.versions.value = data; v.versionObservedAt.value = now; v.versionError.value = null;
  const info = v.versionInfo(now), gateway = (rows: any[]) => rows.find((r: any) => r.key === 'gateway');
  assert.equal(gateway(info.rows).state, 'ok'); assert.equal(gateway(info.rows).times[0].now - gateway(info.rows).times[0].at, 40_000);
  const later = info.at!(now + 30_000).rows;
  assert.equal(gateway(later).times[0].now - gateway(later).times[0].at, 70_000, 'the age moves on with the present');
  assert.equal(gateway(later).state, 'checking…', 'and the state next to it reflects the same present');
  assert.equal(gateway(later).tone, 'neutral', 'a probe not yet repeated is not a warning');
  (data as any).gateway = { state: 'unreachable', observedAt: now - 5_000 }; v.versions.value = { ...data };
  assert.equal(v.versionInfo(now).tone, 'danger', 'a fresh unreachable (probe and re-probe failed) is danger');
  (data as any).gateway = { state: 'unreachable', observedAt: now - 120_000 }; v.versions.value = { ...data };
  assert.equal(v.versionInfo(now).tone, 'danger', 'a confirmed outage stays shown while the coordinator repeats the probe');
});

test('access invalidation clears private version data and rejects a late read', async () => {
  const originalFetch = globalThis.fetch;
  Object.defineProperty(globalThis, 'document', { configurable: true, value: { head: { innerHTML: html } } });
  let release!: (value: Response) => void;
  globalThis.fetch = async (url: any) => url === '/home' ? new Response(html, { headers: { 'content-type': 'text/html' } }) : new Promise<Response>(resolve => { release = resolve; });
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

test('a started update stays busy until the page unloads and reloads once', async () => {
  const originalFetch = globalThis.fetch, originalNavigator = Object.getOwnPropertyDescriptor(globalThis, 'navigator');
  let reloads = 0;
  Object.defineProperty(globalThis, 'location', { configurable: true, value: { reload: () => { reloads++; } } });
  Object.defineProperty(globalThis, 'navigator', { configurable: true, value: {} });
  globalThis.fetch = async () => new Response(html, { headers: { 'content-type': 'text/html' } });
  try {
    v.updating.value = false; v.updateError.value = null;
    await v.applyUpdate(); await v.applyUpdate();
    assert.equal(reloads, 1, 'a second click while reloading does nothing');
    assert.equal(v.updating.value, true, 'the control does not turn back into Update before the page goes');
  } finally {
    v.updating.value = false; globalThis.fetch = originalFetch;
    if (originalNavigator) Object.defineProperty(globalThis, 'navigator', originalNavigator);
  }
});

test('the phone bottom bar exposes Versions under More and a long overview stays within the viewport', { skip: !process.env.FLEET_CONTROL_BROWSER }, async () => {
  const { chromium } = await import('playwright');
  const { uiCss } = await import(new URL('../../packages/ui/scripts/tokens.mjs', import.meta.url).href) as { uiCss: () => string };
  const fixture = await buildSolid(`
    import {render} from '@solidjs/web';import {AppShell} from './packages/ui/src/index.ts';
    const version={current:'1.261115.5',tone:'neutral',updateReady:false,onUpdate:()=>{},rows:Array.from({length:50},(_,i)=>({key:String(i),part:'host '+i,icon:'terminal',version:'1.261115.5',state:'unknown',tone:'attention'}))};
    render(()=><AppShell nav={[]} current="tasks" onGo={()=>{}} waiting={0} user={{name:'Fixture person',onLogout:()=>{},version}}><p>Fixture work</p></AppShell>,document.body);
  `);
  const browser = await chromium.launch({ headless: true });
  const page = await browser.newPage({ viewport: { width: 640, height: 480 } });
  try {
    await page.setContent('<body></body>'); await page.addStyleTag({ content: uiCss() });
    await page.addScriptTag({ content: fixture.outputFiles[0].text });
    await page.getByRole('button', { name: 'More', exact: true }).click();
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
  const fixture = await buildSolid(`
    import {render} from '@solidjs/web';import {AppShell} from './packages/ui/src/index.ts';
    const checked=Date.now()-40000, info=now=>({current:'1.261115.5',tone:'neutral',updateReady:false,onUpdate:()=>{},at:info,rows:[{key:'gateway',part:'Gateway',icon:'shield',version:'1.261115.5',state:now-checked>60000?'unknown':'ok',tone:'success',times:[{label:'checked',at:checked,now}]}]}), version=info(Date.now());
    render(()=><AppShell nav={[]} current="tasks" onGo={()=>{}} waiting={0} user={{name:'Fixture person',onLogout:()=>{},version}}><p>Fixture work</p></AppShell>,document.body);
  `);
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
    assert.equal(await page.locator('.ver-state').innerText(), 'unknown', 'the state counts from the same present as the age');
  } finally { await browser.close(); }
});

test('the user block keeps its overview open while versions change, keeps the avatar visible and shows the update strip', { skip: !process.env.FLEET_CONTROL_BROWSER }, async () => {
  const { chromium } = await import('playwright');
  const { uiCss } = await import(new URL('../../packages/ui/scripts/tokens.mjs', import.meta.url).href) as { uiCss: () => string };
  const fixture = await buildSolid(`
    import {render} from '@solidjs/web';import {createSignal} from 'solid-js';import {AppShell} from './packages/ui/src/index.ts';
    const row=(state,tone)=>({key:'host:a',part:'a',icon:'terminal',version:'1.261115.5',state,tone});
    const [info,setInfo]=createSignal({current:'1.261115.5',tone:'neutral',updateReady:false,onUpdate:()=>{window.updates=(window.updates||0)+1},rows:[row('current','success')]});
    window.setVersion=v=>setInfo({...info(),...v});
    render(()=><AppShell nav={[]} current="tasks" onGo={()=>{}} waiting={0} user={{name:'Fixture person',onLogout:()=>{},version:info()}}><p>Fixture work</p></AppShell>,document.body);
  `);
  const browser = await chromium.launch({ headless: true });
  const page = await browser.newPage({ viewport: { width: 1440, height: 900 }, colorScheme: 'dark' });
  try {
    await page.setContent('<body></body>'); await page.addStyleTag({ content: uiCss() });
    await page.addScriptTag({ content: fixture.outputFiles[0].text });
    const avatar = page.locator('[data-ui=nav-rail] .rail-av');
    const look = () => avatar.evaluate(e => { const s = getComputedStyle(e); return s.backgroundColor + ' ' + s.color; });
    const before = await look();
    assert.doesNotMatch(before, /rgba\(0, 0, 0, 0\)/, 'the avatar has its own surface on dark');
    await page.locator('[data-ui=nav-rail] .rail-ver').click();
    const panel = page.getByRole('group', { name: 'Versions', exact: true });
    await panel.waitFor();
    await page.evaluate(() => (window as any).setVersion({ tone: 'danger', summary: '1 part needs a look', rows: [{ key: 'host:a', part: 'a', icon: 'terminal', version: '1.261115.5', state: 'polls failing', tone: 'danger' }] }));
    assert.equal(await panel.isVisible(), true, 'the overview stays open while its values change');
    assert.equal(await panel.locator('.ver-state').innerText(), 'polls failing');
    assert.equal(await look(), before, 'the avatar never changes with the version state');
    await page.keyboard.press('Escape');
    await page.evaluate(() => (window as any).setVersion({ tone: 'neutral', summary: undefined, updateReady: true, newVersion: '1.261115.6', rows: [] }));
    const strip = page.locator('[data-ui=nav-rail] [data-ui=update-strip]');
    assert.match(await strip.innerText(), /New app ready\s+1\.261115\.6/);
    await strip.getByRole('button', { name: 'Reload' }).click();
    assert.equal(await page.evaluate(() => (window as any).updates), 1);
  } finally { await browser.close(); }
});
