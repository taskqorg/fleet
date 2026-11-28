import assert from 'node:assert/strict';
import { test } from 'node:test';
import { refreshRenderer } from '../../deploy/design-verifier/renderer-state.mjs';

test('renderer recovers on a later poll after an unavailable probe', async () => {
  const state = {};
  assert.equal(await refreshRenderer(state, () => ({ probe: async () => { throw Error('offline'); } })), undefined);
  assert.equal(state.visualParity, 'unavailable');
  const runner = { probe: async () => ({ node: 'fixture' }) };
  assert.equal(await refreshRenderer(state, () => runner), runner);
  assert.equal(state.visualParity, 'ready');
  assert.deepEqual(state.renderer, { node: 'fixture' });
  assert.equal(state.visualParityReason, undefined);
});

test('provider can proceed while one probe waits and execution failure invalidates readiness', async () => {
  const { rendererController } = await import('../../deploy/design-verifier/renderer-state.mjs');
  const state = {}, stop = new AbortController();
  let finish, unavailable, calls = 0;
  const manager = rendererController(state, failed => {
    calls++; unavailable = failed;
    return { probe: () => new Promise(resolve => { finish = resolve; }), measure: async () => { throw new Error('bad renderer reply'); } };
  }, stop.signal);
  manager.start(); manager.start();
  assert.equal(calls, 1);
  assert.equal(manager.current, undefined); // Provider polling need not await probe.
  finish({ node: 'fixture' }); await manager.wait();
  assert.equal(state.visualParity, 'ready'); assert.ok(manager.current);
  await assert.rejects(manager.current.measure(), /bad renderer reply/);
  assert.equal(manager.current, undefined); assert.equal(state.visualParity, 'unavailable');
  unavailable();
  manager.start(); assert.equal(calls, 2);
  stop.abort(); finish({ node: 'fixture' }); await manager.wait();
  assert.equal(manager.current, undefined); assert.equal(state.visualParity, 'unavailable');
});

test('one-shot waits for local or VM probe and never succeeds with an unavailable renderer', async () => {
  const { rendererController, rendererPoll } = await import('../../deploy/design-verifier/renderer-state.mjs');
  for (const ready of [true, false]) {
    let finish, polled = false;
    const state = {}, stop = new AbortController();
    const controller = rendererController(state, () => ({ probe: () => new Promise((resolve, reject) => {
      finish = () => ready ? resolve({ node: 'fixture' }) : reject(Error('unavailable'));
    }) }), stop.signal);
    const result = rendererPoll(controller, async () => { polled = true; return true; }, true);
    await Promise.resolve(); assert.equal(polled, false);
    finish(); assert.equal(await result, ready); assert.equal(polled, true);
  }
});

test('ordinary polling continues while a local probe is pending', async () => {
  const { rendererController, rendererPoll } = await import('../../deploy/design-verifier/renderer-state.mjs');
  let finish;
  const controller = rendererController({}, () => ({ probe: () => new Promise(resolve => { finish = resolve; }) }));
  assert.equal(await rendererPoll(controller, async () => true, false), true);
  assert.equal(controller.current, undefined);
  finish({ node: 'fixture' }); await controller.wait();
});

test('one-shot status cannot succeed with pending results or unavailable evidence', async () => {
  const { pollComplete } = await import('../../deploy/design-verifier/renderer-state.mjs');
  assert.equal(pollComplete({ provider: 'ready' }, 'ready', [{ conclusion: 'success' }]), true);
  assert.equal(pollComplete({ provider: 'ready' }, 'ready', [{ conclusion: 'failure' }]), true);
  assert.equal(pollComplete({ provider: 'ready' }, 'ready', [{ conclusion: null }]), false);
  assert.equal(pollComplete({ provider: 'ready' }, 'ready', [{ state: 'unknown' }]), false);
  assert.equal(pollComplete({ provider: 'ready' }, 'unknown', []), false);
});
