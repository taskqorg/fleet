import { test } from 'node:test';
import assert from 'node:assert/strict';
import { expected, fixture, manifest, seedSQL } from '../performance/seed-interface.ts';
import { bindProof, canAcknowledge, sourceRate, stats, workload, type Fact, type Publication } from '../performance/interface.ts';

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

// Requirement: BOTH source distributions independently qualify actual confirmed sustained/burst rate.
// Risk: offered1000/999.03ms and UImax851.66ms conceal a 2475.49ms confirmed burst.
test('source rate rejects slow or ambiguous confirmations, missing facts and counter jumps in both modes', () => {
  for (const sourceDistribution of ['hot', 'distinct'] as const) {
    const spec = workload({ sourceDistribution, rate: 100 });
    const make = (phase: string, count: number, interval: number, offset: number): Fact[] => Array.from({ length: count }, (_, i) => ({ id: phase + i, execution: sourceDistribution === 'hot' ? 'marker' : phase + i, project: 'p', n: i + 1, calls: sourceDistribution === 'hot' ? i + 1 : 1, errors: 0, phase, due: offset + i * interval, lower: offset + i * interval, upper: offset + i * interval + .1, transaction: phase + i }));
    const sustained = make('sustained', spec.duration * spec.rate, 10, 0);
    const burst = make('burst', 1000, .99903, 600000);
    assert.equal(sourceRate([...sustained, ...burst], spec, 0).status, 'qualified');
    const slowed = burst.map((f, i) => ({ ...f, lower: 600000 + i * 2475.49 / 999, upper: 600000 + i * 2475.49 / 999 + .1 }));
    assert.equal(sourceRate([...sustained, ...slowed], spec, 0).status, 'invalid-source-rate');
    assert.equal(stats([851.66]).violations, 0, 'UI success cannot qualify the source');
    assert.equal(sourceRate([...sustained, ...burst], spec, 1).status, 'invalid-source-rate', 'uncertain one-second boundary cannot pass');
    assert.equal(sourceRate([...sustained, ...burst.slice(1)], spec, 0).status, 'invalid-source-rate');
    assert.equal(sourceRate([...sustained, ...burst.map(f => ({ ...f, transaction: 'one-counter-jump' }))], spec, 0).status, 'invalid-source-rate');
    assert.equal(sourceRate([...sustained.map(f => ({ ...f, upper: f.upper! + 1200 })), ...burst], spec, 0).status, 'invalid-source-rate', 'sustained actual windows must qualify too');
    assert.equal(sourceRate([...sustained, ...burst.map(f => ({ ...f, upper: null }))], spec, 0).status, 'invalid-source-rate');
  }
  assert.equal(workload({}).sourceDistribution, 'hot');
  assert.throws(() => workload({ sourceDistribution: 'prefix' as any }));
});

// Requirement: a fact is acknowledged only after exact leaf inclusion and painted same-epoch publication.
// Risk: a later unrelated leaf/summary crosses the missing fact's prefix; replay drops or double-counts it.
test('distinct inclusion waits for its leaf, exact errors, epoch and revision; hot replacement remains supported', () => {
  const make = (id: string, errors = 0): Fact => ({ id, execution: id, project: 'p', n: 1, calls: 1, errors, lower: 0, upper: 1, phase: 'burst', due: 0, transaction: id });
  const a = make('pending', 1), b = make('published'), facts = [a, b];
  const early: Publication = { epoch: 'epoch-a', revision: '9007199254740993', calls: 1, errors: 0 };
  bindProof(facts, early, { published: { calls: 1, errors: 0 } }, 'distinct');
  assert.equal(canAcknowledge(a, early, '1 · 0 failed'), false, 'equal aggregate is not pending leaf inclusion');
  assert.equal(canAcknowledge(b, early, '1 · 0 failed'), true);
  const unrelated = { ...early, revision: '9007199254740994', calls: 5 };
  bindProof(facts, unrelated, { published: { calls: 1, errors: 0 } }, 'distinct');
  assert.equal(canAcknowledge(a, unrelated, '5 · 0 failed'), false, 'higher count/newer revision is not inclusion');
  const included = { ...early, revision: '9007199254740995', calls: 2, errors: 1 };
  bindProof(facts, included, { pending: { calls: 1, errors: 0 } }, 'distinct');
  assert.equal(a.proof, undefined, 'wrong leaf errors cannot bind');
  a.upper = null;
  bindProof(facts, included, { pending: { calls: 1, errors: 1 } }, 'distinct');
  assert.equal(a.proof, undefined, 'publication may arrive before commit receipt');
  a.upper = 2;
  bindProof(facts, included, { pending: { calls: 1, errors: 1 } }, 'distinct');
  assert.equal(canAcknowledge(a, included, '2 · 1 failed'), true);
  assert.equal(canAcknowledge(a, early, '1 · 0 failed'), false, 'old revision');
  assert.equal(canAcknowledge(a, { ...included, epoch: 'epoch-b' }, '2 · 1 failed'), false, 'wrong epoch');
  assert.equal(canAcknowledge(a, included, '2 · 0 failed'), false, 'wrong numeric representation');
  assert.equal(canAcknowledge(a, { ...included, calls: 1 }, '1 · 1 failed'), false);
  const seen = new Set<string>();
  for (let replay = 0; replay < 2; replay++) for (const fact of facts) if (canAcknowledge(fact, included, '2 · 1 failed')) seen.add(fact.id);
  assert.deepEqual([...seen].sort(), ['pending', 'published'], 'no missing fact or duplicate acknowledgement');
  assert.equal(facts.reduce((s, f) => s + f.calls, 0), 2);
  assert.equal(facts.reduce((s, f) => s + f.errors, 0), 1);
  const hot = [make('marker'), { ...make('marker'), id: 'next', n: 2, calls: 2 }];
  bindProof(hot, { ...included, errors: 0 }, { marker: { calls: 2, errors: 0 } }, 'hot');
  assert.ok(hot.every(f => canAcknowledge(f, { ...included, errors: 0 }, '2 · 0 failed')));
});
