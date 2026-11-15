import { test } from 'node:test';
import assert from 'node:assert/strict';
import { expected, fixture, manifest, seedSQL } from '../performance/seed-interface.ts';
import { sourceRate, scheduleSourcePhases, stats, workload, type Fact, type PhaseWindow, type Workload } from '../performance/interface.ts';

const completedWindows = (spec: Workload): PhaseWindow[] => [
  ['warmup', 0, spec.warmup, spec.warmup * spec.rate, 'paced'],
  ['sustained', spec.warmup, spec.duration, spec.duration * spec.rate, 'paced'],
  ['burst', spec.warmup + spec.duration, 1, spec.burst, 'early'],
].map(([phase, offset, seconds, count, offering]) => ({ phase, plannedStart: Number(offset) * 1000, plannedEnd: (Number(offset) + Number(seconds)) * 1000, actualStart: Number(offset) * 1000, actualEnd: (Number(offset) + Number(seconds)) * 1000, startOvershootMs: 0, endOvershootMs: 0, offered: count, expected: count, offering })) as PhaseWindow[];

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
    const phaseWindows = completedWindows(spec);
    const sustained = make('sustained', spec.duration * spec.rate, 10, 60000);
    const burst = make('burst', 1000, .99903, 660000);
    assert.equal(sourceRate([...sustained, ...burst], spec, 0, phaseWindows).status, 'qualified');
    const slowed = burst.map((f, i) => ({ ...f, lower: 660000 + i * 2475.49 / 999, upper: 660000 + i * 2475.49 / 999 + .1 }));
    assert.equal(sourceRate([...sustained, ...slowed], spec, 0, phaseWindows).status, 'invalid-source-rate');
    assert.equal(stats([851.66]).violations, 0, 'UI success cannot qualify the source');
    assert.equal(sourceRate([...sustained, ...burst], spec, 1, phaseWindows).status, 'invalid-source-rate', 'uncertain one-second boundary cannot pass');
    assert.equal(sourceRate([...sustained, ...burst.slice(1)], spec, 0, phaseWindows).status, 'invalid-source-rate');
    assert.equal(sourceRate([...sustained, ...burst.map(f => ({ ...f, transaction: 'one-counter-jump' }))], spec, 0, phaseWindows).status, 'invalid-source-rate');
    assert.equal(sourceRate([...sustained.map(f => ({ ...f, upper: f.upper! + 1200 })), ...burst], spec, 0, phaseWindows).status, 'invalid-source-rate', 'sustained actual windows must qualify too');
    assert.equal(sourceRate([...sustained, ...burst.map(f => ({ ...f, upper: null }))], spec, 0, phaseWindows).status, 'invalid-source-rate');
  }
  assert.equal(workload({}).sourceDistribution, 'hot');
  assert.throws(() => workload({ sourceDistribution: 'prefix' as any }));
});

// Requirement: full absolute phase horizons and an early-offered independent burst.
// Risk: rate1's last tick ends3s at2s, or a late phase silently resets the original clock.
test('scheduler waits full short and600-second phases, retains cadence and offers burst at original start', async () => {
  for (const spec of [workload({ warmup: 1, duration: 3, burst: 1, recovery: 5, rate: 1 }, true), workload({ rate: 1 })]) {
    let now = 0;
    const offered: { phase: string; due: number; at: number }[] = [], events: { event: string; window: PhaseWindow }[] = [];
    const windows = await scheduleSourcePhases(spec, (phase, due) => offered.push({ phase, due, at: now }), (event, window) => events.push({ event, window }), { now: () => now, sleep: async ms => { now += ms; } });
    assert.equal(now, (spec.warmup + spec.duration + 1) * 1000);
    assert.deepEqual(windows.map(w => [w.plannedStart, w.plannedEnd, w.actualStart, w.actualEnd]), completedWindows(spec).map(w => [w.plannedStart, w.plannedEnd, w.actualStart, w.actualEnd]));
    assert.equal(events.filter(e => e.event === 'start').length, 3); assert.equal(events.filter(e => e.event === 'end').length, 3);
    const sustained = offered.filter(f => f.phase === 'sustained'), burst = offered.filter(f => f.phase === 'burst');
    assert.deepEqual(sustained.map(f => f.at), Array.from({ length: spec.duration }, (_, i) => (spec.warmup + i) * 1000));
    assert.equal(burst.length, spec.burst); assert.ok(burst.every(f => f.at === (spec.warmup + spec.duration) * 1000 && f.due === f.at));
  }
  let now = 0;
  const windows = await scheduleSourcePhases(workload({ warmup: 1, duration: 3, burst: 1, recovery: 5 }, true), () => {}, () => {}, { now: () => now, sleep: async ms => { now += ms + 25; } });
  assert.deepEqual(windows.map(w => w.plannedStart), [0, 1000, 4000]);
  assert.deepEqual(windows.map(w => w.actualEnd), [1025, 4025, 5025]);
  assert.deepEqual(windows.map(w => w.endOvershootMs), [25, 25, 25]);
  assert.equal(windows[2].actualStart, windows[1].actualEnd, 'late phase does not move planned burst start');
  now = 0; let early = true;
  const retried = await scheduleSourcePhases(workload({ warmup: 1, duration: 3, burst: 1, recovery: 5 }, true), () => {}, () => {}, { now: () => now, sleep: async ms => { now += early ? ms - 1 : ms; early = false; } });
  assert.equal(retried[0].actualEnd, 1000, 'early timer wake cannot shorten the phase');
});

// Requirement: confirmations fit original windows including clock uncertainty, not first receipt.
// Risk: delayed submission shortens the measured burst span or resets a one-second source bucket.
test('source validity requires complete horizons and rejects late last commit at the original clock bound', () => {
  const spec = workload({ warmup: 0, duration: 3, burst: 1, recovery: 5, rate: 1, sourceDistribution: 'distinct' }, true), windows = completedWindows(spec);
  const fact = (id: string, phase: string, lower: number, upper: number): Fact => ({ id, execution: id, project: 'p', n: 1, calls: 1, errors: 0, phase, due: phase === 'burst' ? 3000 : lower, lower, upper, transaction: id });
  const sustained = [fact('s0', 'sustained', 0, 1), fact('s1', 'sustained', 1000, 1001), fact('s2', 'sustained', 2000, 2001)];
  const burst = fact('b', 'burst', 3900, 3998);
  assert.equal(sourceRate([...sustained, burst], spec, 1, windows).status, 'qualified');
  assert.equal(sourceRate([...sustained, { ...burst, upper: 3998.01 }], spec, 1, windows).status, 'invalid-source-rate', 'outward clock brackets exceed the fixed4000 deadline');
  assert.equal(sourceRate([...sustained, { ...burst, upper: 4001 }], spec, 0, windows).status, 'invalid-source-rate', '101ms from late submission is not within1000ms from burst start');
  assert.equal(sourceRate([{ ...sustained[0], lower: 1100, upper: 1101 }, ...sustained.slice(1), burst], spec, 0, windows).status, 'invalid-source-rate', 'late first one-second bucket cannot rebase its clock');
  assert.equal(sourceRate([...sustained, burst], spec, 0).status, 'invalid-source-rate', 'missing phase evidence never passes');
  const shortened = windows.map(w => w.phase === 'sustained' ? { ...w, actualEnd: 2000, endOvershootMs: -1000 } : w);
  assert.equal(sourceRate([...sustained, burst], spec, 0, shortened).status, 'invalid-source-rate', 'the old3s-to2s early exit cannot qualify');
  const reset = windows.map(w => w.phase === 'burst' ? { ...w, plannedStart: 3900, plannedEnd: 4900, actualStart: 3900, actualEnd: 4900 } : w);
  assert.equal(sourceRate([...sustained, { ...burst, upper: 4001 }], spec, 0, reset).status, 'invalid-source-rate', 'rewritten burst origin is rejected');
});

// Requirement: a fact is acknowledged only after exact leaf inclusion and painted same-epoch publication.
// Risk: a later unrelated leaf/summary crosses the missing fact's prefix; replay drops or double-counts it.

// Requirement: fixed100 facts in each original second, with planned commit headroom.
// Risk: late-tail fixtures waive a real deadline or silently rebase the next window.
test('paced100/s retains all600 original windows and rejects slow commits despite headroom', async () => {
 const spec=workload({rate:100});let now=0;const facts:Fact[]=[];
 const windows=await scheduleSourcePhases(spec,(phase,due)=>{
  const id=String(facts.length);facts.push({id,execution:id,project:'p',n:1,calls:1,errors:0,phase,due,lower:now,upper:now+100,transaction:id});
 },()=>{},{now:()=>now,sleep:async ms=>{now+=ms;}});
 const sustained=facts.filter(f=>f.phase==='sustained');assert.equal(sustained.length,60000);
 for(let second=0;second<600;second++){
  const bucket=sustained.slice(second*100,(second+1)*100),start=60000+second*1000;
  assert.equal(bucket[0].due,start);assert.equal(bucket[99].due,start+792);
 }
 const result=sourceRate(facts,spec,1,windows);assert.equal(result.status,'qualified');
 assert.equal(result.phases[0].windows.length,600);assert.equal(result.phases[0].windows[0].confirmationTailHeadroomMs,106);
 const slow=facts.map(f=>f===sustained[99]?{...f,upper:61000}:f);
 assert.equal(sourceRate(slow,spec,1,windows).status,'invalid-source-rate','all slow facts remain subject to original deadline');
 assert.equal(windows[2].plannedStart,660000);assert.equal(now,661000);
});
