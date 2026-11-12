// Outside tests of the Runners screen query (executions_runners): runs per host with resources, spend today and this month.
//   FLEET_TRIAL_TESTS=tests/go/runners.test.ts node tests/trial/run.ts
import {test} from 'node:test';
import assert from 'node:assert/strict';
import {startCoordinator} from '../harness.ts';

test('R1 executions_runners lists active and recent runs with their totals, leaves out pipeline containers and old runs, and sums spend per host, kind and UTC period (requirement: Runners screen, fleet.md R2; risk: the screen shows stale or double-counted work)', async () => {
  const c = await startCoordinator({name: 'runners1'});
  try {
    c.sql(`INSERT INTO execution(id,kind,state,host_id,harness,model,started_at,ended_at) VALUES
      ('r-live','agent_run','running','h1','claude','m1',now()-interval '30 seconds',NULL),
      ('r-done','agent_run','succeeded','h1','claude','m1',now()-interval '2 minutes',now()-interval '1 minute'),
      ('r-old','agent_run','succeeded','h1','claude','m1',now()-interval '40 days',now()-interval '40 days'),
      ('r-pipe','pipeline_run','running',NULL,NULL,NULL,now(),NULL);
      INSERT INTO execution_totals(execution_id,tokens_input,tokens_output,wall_ms,cpu_ms) VALUES ('r-done',100,50,3600000,1200),('r-live',10,0,NULL,NULL);
      INSERT INTO execution(id,kind,state,host_id,started_at,ended_at) VALUES ('r-call','tool_run','succeeded','h1',now()-interval '1 minute',now());
      INSERT INTO execution_totals(execution_id,wall_ms) VALUES ('r-call',40);`);
    const r = await fetch(c.base + '/api/ops/executions_runners', {method: 'POST', headers: {authorization: 'Bearer ' + c.secret, 'x-fleet-person': 'owner@trial.test', 'content-type': 'application/json'}, body: '{}'});
    const b: any = await r.json();
    assert.equal(r.status, 200, JSON.stringify(b));
    assert.deepEqual(b.runs.map((x: any) => x.id).sort(), ['r-call', 'r-done', 'r-live']);
    assert.equal(b.runs.find((x: any) => x.id === 'r-call').tokens, null, 'no token measurement: unknown, never 0');
    const done = b.runs.find((x: any) => x.id === 'r-done');
    assert.deepEqual([done.hostId, done.tokens, done.wallMs, done.cpuMs], ['h1', 150, 3600000, 1200]);
    assert.ok(done.endedAt > done.startedAt);
    const month = b.spend.find((s: any) => s.hostId === 'h1' && s.kind === 'agent_run' && s.period === 'month');
    assert.equal(month.tokens, 160);
    assert.deepEqual([month.runs, month.measured], [2, 2], JSON.stringify(b.spend));
    assert.deepEqual(month.cost, [{value: null, currency: null, basis: 'unavailable', reason: 'no usage amount or price entry', usages: 3}], 'no price entry: unknown, never 0');
  } finally { await c.stop(); }
});

test('R2 executions_runners shows a person only the runs and spend of projects they may access (requirement: fleet-people-access.md project grants; risk: Runners leaks other projects\' tasks, hosts work or spend)', async () => {
  const c = await startCoordinator({name: 'runners2'});
  try {
    c.sql(`INSERT INTO execution(id,kind,state,host_id,project_id,started_at,ended_at) VALUES
      ('s-mine','agent_run','succeeded','h1','desiign',now()-interval '2 minutes',now()-interval '1 minute'),
      ('s-other','agent_run','succeeded','h1','other',now()-interval '2 minutes',now()-interval '1 minute'),
      ('s-none','tool_run','succeeded',NULL,NULL,now()-interval '2 minutes',now()-interval '1 minute');
      INSERT INTO execution_totals(execution_id,project_id,tokens_input) VALUES ('s-mine','desiign',5),('s-other','other',7);`);
    const as = async (person: string) => (await (await fetch(c.base + '/api/ops/executions_runners', {method: 'POST', headers: {authorization: 'Bearer ' + c.secret, 'x-fleet-person': person, 'content-type': 'application/json'}, body: '{}'})).json()) as any;
    const member = await as('member@trial.test');
    assert.deepEqual(member.runs.map((r: any) => r.id), ['s-mine'], JSON.stringify(member.runs));
    assert.deepEqual(member.spend.filter((s: any) => s.period === 'month').map((s: any) => [s.hostId, s.runs, s.tokens]), [['h1', 1, 5]]);
    assert.deepEqual((await as('outsider@trial.test')).runs, []);
    assert.deepEqual((await as('owner@trial.test')).runs.map((r: any) => r.id).sort(), ['s-mine', 's-none', 's-other']);
  } finally { await c.stop(); }
});
