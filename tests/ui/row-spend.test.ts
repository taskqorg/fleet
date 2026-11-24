// Requirement: fleet.md Tasks, Conversations, and Projects row spend keeps currencies, bases, partial coverage, and unknown states distinct.
// Risk: multi-currency or subscription spend collapses into one figure, missing prices render fake zero, or links share ambiguous labels.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { formatRowSpend } from '../../packages/ui/src/format.ts';
import type { CostFigure } from '../../contracts/generated/ops.ts';

test('formatRowSpend separates measured, estimated, subscription, multi-currency, partial, unknown, and stale states', () => {
  const unknown = formatRowSpend(undefined, false, 'T-FLT-1');
  assert.equal(unknown.hasFigures, false);
  assert.equal(unknown.primaryText, 'Spend —');
  assert.equal(unknown.suffix, '');
  assert.equal(unknown.subscription, false);
  assert.match(unknown.tip, /unknown/);
  assert.match(unknown.ariaLabel, /^T-FLT-1: Spend —\./);

  const onlyUnavailable = formatRowSpend([{ value: null, currency: null, basis: 'unavailable', usages: 3 }], false, 'T-FLT-2');
  assert.equal(onlyUnavailable.hasFigures, false);
  assert.equal(onlyUnavailable.primaryText, 'Spend —');

  const unpricedSub = formatRowSpend([{ value: null, currency: 'USD', basis: 'subscription', note: 'Claude Max' }], false, 'T-FLT-3');
  assert.equal(unpricedSub.hasFigures, false);
  assert.equal(unpricedSub.subscription, true);
  assert.match(unpricedSub.tip, /included in subscription/);

  const measuredSingle = formatRowSpend([{ value: 1.25, currency: 'USD', basis: 'measured' }], false, 'T-FLT-4');
  assert.equal(measuredSingle.hasFigures, true);
  assert.equal(measuredSingle.primaryText, '$1.25');
  assert.equal(measuredSingle.suffix, '');
  assert.equal(measuredSingle.subscription, false);
  assert.equal(measuredSingle.tip, 'Cumulative spend: 1.25 dollars. Open resource breakdown.');

  const estimated = formatRowSpend([{ value: 0.38, currency: 'USD', basis: 'estimated' }], false, 'T-FLT-5');
  assert.equal(estimated.primaryText, '≈ $0.38');
  assert.match(estimated.tip, /estimated/);

  const subscription = formatRowSpend([{ value: 2.5, currency: 'USD', basis: 'subscription', note: 'subscription (list-price equivalent)' }], false, 'T-FLT-6');
  assert.equal(subscription.primaryText, '≈ $2.50');
  assert.equal(subscription.subscription, true);
  assert.match(subscription.tip, /subscription list-price equivalent/);

  const multiAndPartial: CostFigure[] = [
    { value: 3.75, currency: 'EUR', basis: 'measured' },
    { value: 5.0, currency: 'USD', basis: 'measured' },
    { value: 1.25, currency: 'USD', basis: 'subscription', note: 'subscription (list-price equivalent)' },
  ];
  const multi = formatRowSpend(multiAndPartial, false, 'T-FLT-7');
  assert.equal(multi.primaryText, '3.75 EUR');
  assert.equal(multi.suffix, '+2');
  assert.equal(multi.subscription, false);
  assert.match(multi.tip, /3\.75 EUR, 5\.00 dollars, about 1\.25 dollars, subscription list-price equivalent/);

  const partial = formatRowSpend([
    { value: 2.5, currency: 'EUR', basis: 'measured' },
    { value: null, currency: null, basis: 'unavailable', usages: 4 },
  ], true, 'T-FLT-8');
  assert.equal(partial.primaryText, '2.50 EUR');
  assert.equal(partial.suffix, 'partial');
  assert.match(partial.tip, /partial, last confirmed/);
  assert.match(partial.ariaLabel, /^T-FLT-8: 2\.50 EUR partial\./);
});
