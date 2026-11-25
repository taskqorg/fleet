// Requirement: work facts (docs/design-system/controls/work-facts.md, OR-63): a row whose attempts or time in work is an
// order of magnitude above its peers stands out, and ordinary variation does not. Risk: every second attempt is marked
// (a spread statistic is zero when most rows have one attempt), or a fixed limit decides instead of the peers.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { peerFence } from '../../packages/ui/src/format.ts';

test('WF1 peerFence is ten times the peers\' median and needs three peers (requirement: work-facts.md outliers, OR-63; risk: ×2 among ×1 is marked, or 500 attempts look fine)', () => {
  const attempts = [1, 1, 2, 1, 3, 500];
  const fence = peerFence(attempts)!;
  assert.equal(fence, 15, 'median 1.5 (the middle two of six): the fence is 15');
  assert.deepEqual(attempts.filter(n => n > 1 && n >= fence), [500], 'only ×500 stands out; ×2 and ×3 are ordinary');
  assert.equal(peerFence([1, 40]), undefined, 'two peers give no fence');
  assert.equal(peerFence([3600, 7200, 10800, 144000]), 10 * 9000, 'times: the median of an even count is the mean of the middle two');
  assert.equal(peerFence([0, 0, 0, NaN]), undefined, 'zeros and unknown values are not peers');
});
