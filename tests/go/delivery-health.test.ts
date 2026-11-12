// Outside test of delivery health (docs/designs/fleet-delivery-pipeline.md, Delivery health): history, baseline
// snapshots and the regression check with its self-clearing needs-you question.
//   FLEET_TRIAL_TESTS=tests/go/delivery-health.test.ts node tests/trial/run.ts
import {test} from 'node:test';
import assert from 'node:assert/strict';
import {startCoordinator} from '../harness.ts';

test('DH1 a stage slower over the last 24 h than its 7-day baseline opens one question naming the first pull request, and the question withdraws once the stage is back (requirement: Delivery health; risk: delivery gets slower unnoticed, or a stale question stays)', {timeout: 60_000}, async () => {
  const c = await startCoordinator({name: 'delhealth'});
  const call = async (name: string, body: unknown, person = 'owner@trial.test') => {
    const r = await fetch(c.base + '/api/ops/' + name, {method: 'POST', headers: {authorization: 'Bearer ' + c.secret, 'x-fleet-person': person, 'content-type': 'application/json'}, body: JSON.stringify(body)});
    const text = await r.text(); let parsed: any = text; try { parsed = JSON.parse(text); } catch {}
    return {status: r.status, body: parsed};
  };
  const ok = async (name: string, body: unknown) => { const r = await call(name, body); assert.equal(r.status, 200, `${name}: ${JSON.stringify(r.body)}`); return r.body; };
  const pr = (n: number, hoursAgo: number, queueMin: number) => {
    const merged = new Date(Date.now() - hoursAgo * 3600e3).toISOString().replace(/\.\d+Z$/, 'Z');
    const d = {number: n, title: `pr ${n}`, url: `https://github.com/taskqorg/fleet/pull/${n}`, headSha: 'h' + n, mergeSha: 'm' + n, opened: merged, merged,
      installed: {}, durations: {queue: queueMin * 60e3, lead: (queueMin + 5) * 60e3}, queueRuns: 1, queueRemovals: 0, flakes: 0};
    c.sql(`INSERT INTO document(kind,id,project_id,data) VALUES('delivery-pr','delivery-pr:${n}','fleet','${JSON.stringify(d)}') ON CONFLICT (id) DO UPDATE SET data=EXCLUDED.data`);
  };
  try {
    for (let i = 0; i < 20; i++) pr(i + 1, 30 + i * 6, 10);
    for (let i = 0; i < 6; i++) pr(100 + i, 20 - i * 3, 40);
    const hist = await ok('delivery_history', {since: new Date(Date.now() - 8 * 864e5).toISOString().slice(0, 10)});
    assert.equal(hist.prs.length, 26);
    assert.ok(hist.prs[0].merged < hist.prs[25].merged, 'oldest first');
    const one = await ok('delivery_history', {number: 103});
    assert.deepEqual(one.prs.map((p: any) => p.number), [103]);

    const snap = await ok('delivery_baseline_set', {snapshot: 'trial baseline'});
    assert.deepEqual(snap.snapshots.map((s: any) => s.name), ['trial baseline']);
    assert.equal(snap.settings.ratio, 1.5);
    const queue = snap.rolling.stats.find((s: any) => s.key === 'queue');
    assert.equal(queue.n, 26);
    assert.equal(snap.trend.length, 7);
    assert.equal((await call('delivery_baseline_set', {settings: {ratio: 1, minSamples: 5, minDeltaMs: 0, baseline: ''}})).status, 400);
    assert.equal((await call('delivery_baseline_set', {settings: {ratio: 2, minSamples: 5, minDeltaMs: 0, baseline: 'none such'}})).status, 404);

    const first = await ok('delivery_regressions', {check: true});
    const keys = first.regressions.map((r: any) => `${r.key}:${r.metric}`).sort();
    assert.deepEqual(keys, ['lead:p50', 'lead:p90', 'queue:p50', 'queue:p90']);
    const q50 = first.regressions.find((r: any) => r.key === 'queue' && r.metric === 'p50');
    assert.equal(q50.baselineMs, 10 * 60e3);
    assert.equal(q50.currentMs, 40 * 60e3);
    assert.equal(q50.firstPr, 100, 'the earliest merged of the slow ones');
    assert.equal(q50.lastGoodPr, 1);
    assert.ok(q50.compareUrl.endsWith('/compare/m1...m105'));
    const open = () => c.sql(`SELECT count(*) FROM question WHERE cause='problem:delivery:queue:p50' AND state='open'`).trim();
    assert.equal(open(), '1');
    await ok('delivery_regressions', {check: true});
    assert.equal(open(), '1', 'one question per cause, not one per check');
    assert.equal((await ok('delivery_regressions', {})).regressions.length, 4, 'the last check is served');

    for (let i = 0; i < 6; i++) pr(100 + i, 20 - i * 3, 10);
    assert.equal((await ok('delivery_regressions', {check: true})).regressions.length, 0);
    await ok('delivery_regressions', {check: true});
    assert.equal(open(), '0', 'withdrawn after the clean checks');
    assert.equal(c.sql(`SELECT count(*) FROM question WHERE cause IS NULL AND data->>'cause'='problem:delivery:queue:p50' AND state='withdrawn'`).trim(), '1');
  } finally { await c.stop(); }
});
