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
  assert.match(coordinator.state, /unknown/); assert.match(coordinator.note, /observed .*unreachable/);
  assert.equal(coordinator.tone, 'attention');
  assert.equal(v.versionInfo().rows.find((r: any) => r.key === 'host:fixture').tone, 'danger', 'stale snapshot erased a known failure alert');
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
