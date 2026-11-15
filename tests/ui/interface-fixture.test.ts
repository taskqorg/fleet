import { test } from 'node:test';
import assert from 'node:assert/strict';
import { expected, fixture, manifest, seedSQL } from '../performance/seed-interface.ts';
import { stats, workload } from '../performance/interface.ts';

// Requirement: reproducible synthetic v1/seed1 with independently enumerated facts.
// Risk: the benchmark silently samples private distributions or blesses its own accounting output.
test('fixture has exact small independent aggregates, separated currencies, missing and measured zero', () => {
  const f = fixture(6, 1), p = expected(f)['interface-seed1-1'];
  assert.equal(p.executions, 4); assert.equal(p.metrics.input.sum, 6); assert.equal(p.metrics.output.sum, 12);
  assert.equal(p.metrics.reasoning.sum, 6); assert.equal(p.metrics.toolCalls.sum, 2);
  assert.deepEqual(p.directCostsCents, { 'USD/measured': 0, 'GBP/subscription': 7 });
  assert.equal(p.estimatedEURMicro, 5);
  const larger = expected(fixture(618, 3));
  assert.ok(Object.values(larger).some(x => x.missingTotals > 0));
  assert.ok(Object.values(larger).some(x => x.measuredZeroTotals > 0));
  assert.deepEqual(manifest(f), manifest(f));
  assert.notEqual(manifest(f).sha256, manifest(fixture(7)).sha256);
  assert.match(seedSQL(f, 0, 5), /generate_series\(0,5\)/);
  assert.throws(() => seedSQL(f, 0, 6)); assert.throws(() => fixture(0));
});

// Requirement: five seconds is an all-sample maximum and full run dimensions are not shortened.
// Risk: p95 hides a breach or a development smoke is accepted as capacity qualification.
test('workload and statistics retain the maximum breach and reject a shortened qualification', () => {
  assert.deepEqual(stats([10, 20, 30, 5001]), { count: 4, min: 10, p50: 20, p95: 5001, p99: 5001, max: 5001, violations: 1 });
  assert.equal(stats([]).max, null);
  assert.throws(() => workload({ duration: 3 }));
  assert.throws(() => workload({ readers: 50, duration: 3 }, true));
  assert.equal(workload({ readers: 50, rate: 100 }).duration, 600);
  assert.equal(workload({ readers: 1, rate: 1, warmup: 0, duration: 3, burst: 1, recovery: 5 }, true).duration, 3);
});
