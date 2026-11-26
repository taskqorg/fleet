// Requirement: Setup › Apps and a pipeline's Used by lines (docs/designs/fleet-project-apps.md, Screens; board PA1, PA3)
// say an app's use in plain words: runs, failed runs and cost over 30 days, its pipelines and its limits in words. Risk:
// a zero shows as "0 runs · $0.00", an unpriced cost shows as money, or limits read as raw fields.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { appRowText, limitsText, usageLine } from '../../app/src/project-apps-model.ts';

const usd = (value: number) => ({ value, currency: 'USD', basis: 'estimated' as const });

test('use reads runs, failed and cost; nothing priced leaves the cost out', () => {
  assert.equal(usageLine({ runs: 412, failed: 3, cost: [usd(4.1)] }), '412 runs · 3 failed · ≈ $4.10');
  assert.equal(usageLine({ runs: 1, cost: [{ value: null, currency: null, basis: 'unavailable' }] }), '1 run');
  assert.equal(usageLine({ runs: 0, failed: 0, cost: [] }), 'no runs');
});

test('an app row: pipelines, use over 30 days and limits in words', () => {
  const base = { pipelines: ['doc-recognize-agent'], limits: {}, use30d: { runs: 2, failed: 1, cost: [usd(0.5)] } };
  assert.equal(appRowText({ ...base, state: 'active' }), 'doc-recognize-agent · 2 runs · ≈ $0.50 · 30 d · no limits');
  assert.equal(appRowText({ ...base, state: 'asked', pipelines: ['*'], use30d: { runs: 0, failed: 0, cost: [] } }), 'all pipelines · no limits');
  assert.equal(limitsText({ runsPerDay: 500, spendPerMonth: { amount: 20, currency: 'USD' } }), '500 runs a day, 20 USD a month');
});
