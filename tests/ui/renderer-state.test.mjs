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
