// Outside tests of the Runners screen query (executions_runners): runs per host with resources, spend today and this month.
//   FLEET_TRIAL_TESTS=tests/go/runners.test.ts node tests/trial/run.ts
import {test} from 'node:test';
import assert from 'node:assert/strict';
import {startCoordinator} from '../harness.ts';

test('R1 executions_runners lists active and recent runs with their totals, leaves out pipeline containers and old runs, and sums spend per host, kind and UTC period (requirement: Runners screen, fleet.md R2; risk: the screen shows stale or double-counted work)', async () => {
  const c = await startCoordinator({name: 'runners1'});
  try {
    c.sql(`INSERT INTO execution(id,kind,state,host_id,harness,model,started_at,ended_at) VALUES
      ('r-live','agent_run','running','h1','claude','m1',now()-interval '5 minutes',NULL),
      ('r-done','agent_run','succeeded','h1','claude','m1',now()-interval '2 hours',now()-interval '1 hour'),
      ('r-old','agent_run','succeeded','h1','claude','m1',now()-interval '3 days',now()-interval '3 days'),
      ('r-pipe','pipeline_run','running',NULL,NULL,NULL,now(),NULL);
      INSERT INTO execution_totals(execution_id,tokens_input,tokens_output,wall_ms,cpu_ms) VALUES ('r-done',100,50,3600000,1200),('r-live',10,0,NULL,NULL);`);
    const r = await fetch(c.base + '/api/ops/executions_runners', {method: 'POST', headers: {authorization: 'Bearer ' + c.secret, 'x-fleet-person': 'owner@trial.test', 'content-type': 'application/json'}, body: '{}'});
    const b: any = await r.json();
    assert.equal(r.status, 200, JSON.stringify(b));
    assert.deepEqual(b.runs.map((x: any) => x.id).sort(), ['r-done', 'r-live']);
    const done = b.runs.find((x: any) => x.id === 'r-done');
    assert.deepEqual([done.hostId, done.tokens, done.wallMs, done.cpuMs], ['h1', 150, 3600000, 1200]);
    assert.ok(done.endedAt > done.startedAt);
    const month = b.spend.find((s: any) => s.hostId === 'h1' && s.kind === 'agent_run' && s.period === 'month');
    assert.equal(month.tokens, 160);
    assert.ok(month.runs >= 2, JSON.stringify(b.spend));
    assert.deepEqual(month.cost, [{value: null, currency: null, basis: 'unavailable', reason: 'no usage amount or price entry', usages: 3}], 'no price entry: unknown, never 0');
  } finally { await c.stop(); }
});
