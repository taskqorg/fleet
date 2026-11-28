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
