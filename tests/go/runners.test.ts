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
      INSERT INTO execution(id,kind,state,host_id,parent_execution_id,started_at,ended_at) VALUES ('r-call','tool_run','succeeded','h1','github-read:1',now()-interval '1 minute',now()),
       ('r-call2','tool_run','succeeded','h1','github-read:2',now()-interval '1 minute',now());
      INSERT INTO execution_totals(execution_id,wall_ms) VALUES ('r-call',40);`);
    const r = await fetch(c.base + '/api/ops/executions_runners', {method: 'POST', headers: {authorization: 'Bearer ' + c.secret, 'x-fleet-person': 'owner@trial.test', 'content-type': 'application/json'}, body: '{}'});
    const b: any = await r.json();
    assert.equal(r.status, 200, JSON.stringify(b));
    assert.deepEqual(b.runs.map((x: any) => x.id).sort(), ['calls:h1/conn:', 'r-done', 'r-live']);
    const calls = b.runs.find((x: any) => x.id === 'calls:h1/conn:');
    assert.deepEqual([calls.calls, calls.failedCalls, calls.state, calls.wallMs], [2, 0, 'succeeded', 40], 'a parent id that is no run is no caller; tool calls summed per caller, not a row each');
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
    assert.deepEqual((await as('owner@trial.test')).runs.map((r: any) => r.id).sort(), ['calls:/conn:', 's-mine', 's-other']);
  } finally { await c.stop(); }
});

test('R3 the lost sweep ends a host job its host stopped reporting while the host keeps polling, and leaves one on a silent host (requirement: Runners shows only live work; risk: an executor restart leaves host jobs running forever)', {timeout: 30_000}, async () => {
  const c = await startCoordinator({name: 'runners3', env: {FLEET_LOST_AFTER_MS: '1200'}});
  try {
    const call = async (path: string, body: unknown, bearer = c.secret) => (await fetch(c.base + path, {method: 'POST', headers: {authorization: 'Bearer ' + bearer, 'x-fleet-person': 'owner@trial.test', 'content-type': 'application/json'}, body: JSON.stringify(body)})).json() as any;
    const secret = (await call('/api/ops/hosts_register', {id: 'h-live'})).secret;
    c.sql(`INSERT INTO execution(id,kind,state,host_id,started_at,last_seen_at,data) VALUES
      ('j-lost','host_job','running','h-live',now()-interval '1 hour',now()-interval '1 hour','{"id":"j-lost","state":"running","actor":"program-worker"}'),
      ('j-silent','host_job','running','h-gone',now()-interval '1 hour',now()-interval '1 hour','{"id":"j-silent","state":"running","actor":"program-worker"}')`);
    const poll = () => fetch(c.base + '/api/hosts/poll', {method: 'POST', headers: {authorization: 'Bearer ' + secret, 'content-type': 'application/json'}, body: JSON.stringify({platform: 'linux', capabilities: [], stores: [], problems: [], gitConnections: [], projectRoots: []})});
    const state = (id: string) => c.sql(`SELECT state||' '||coalesce(problem,'') FROM execution WHERE id='${id}'`).trim();
    for (let i = 0; i < 50 && state('j-lost') !== 'failed unknown:abandoned'; i++) { await poll(); await new Promise(r => setTimeout(r, 200)); }
    assert.equal(state('j-lost'), 'failed unknown:abandoned', c.sql(`SELECT state,observation,data::text FROM execution WHERE id='j-lost'`));
    assert.equal(c.sql(`SELECT wall_ms_basis FROM execution_totals WHERE execution_id='j-lost'`).trim(), 'unavailable', 'abandoned: its time is unknown, not start-to-abandon');
    assert.match(state('j-silent'), /^running/, 'a host that stopped polling may still finish its job');
  } finally { await c.stop(); }
});

test('R4 agent-run tokens come from the harness usage report: the last result of a Claude run, a Codex turn without its cached input counted twice (requirement: Runners shows agent tokens; risk: token totals stay empty or double-count cached input)', async () => {
  const c = await startCoordinator({name: 'runners4'});
  try {
    const ev = (id: string, eid: string, seq: number, lifecycle: string, u: unknown) => `('event','${id}',jsonb_build_object('id','${id}','executionId','${eid}','seq',${seq},'data',jsonb_build_object('fleetNormalized',jsonb_build_object('lifecycle','${lifecycle}','nativeUsage','${JSON.stringify(u)}'::jsonb))))`;
    c.sql(`INSERT INTO execution(id,kind,state) VALUES ('t-claude','agent_run','succeeded'),('t-codex','agent_run','succeeded'),('t-none','agent_run','succeeded'),('t-bad','agent_run','succeeded'),('t-app','agent_run','succeeded');
      INSERT INTO execution_totals(execution_id) VALUES ('t-claude'),('t-codex'),('t-none'),('t-bad'),('t-app');
      INSERT INTO document(kind,id,data) VALUES
      ${ev('e1', 't-claude', 1, 'result', {input_tokens: 1, output_tokens: 1})},
      ${ev('e2', 't-claude', 2, 'result', {input_tokens: 4, output_tokens: 489, cache_read_input_tokens: 17492, cache_creation_input_tokens: 2015})},
      ${ev('e3', 't-codex', 1, 'turn.completed', {input_tokens: 1000, cached_input_tokens: 800, output_tokens: 50})},
      ${ev('e4', 't-bad', 1, 'result', {input_tokens: 'x', output_tokens: 4.5})},
      ${ev('e5', 't-app', 1, 'usage.observation', {last: {inputTokens: 300, cachedInputTokens: 200, outputTokens: 5, cacheWriteInputTokens: 0}, total: {inputTokens: 9000}})},
      ${ev('e6', 't-app', 2, 'usage.observation', {last: {inputTokens: 400, cachedInputTokens: 300, outputTokens: 7, cacheWriteInputTokens: 0}, total: {inputTokens: 9400}})};
      SELECT fleet_write_tokens(id) FROM execution;`);
    const row = (id: string) => c.sql(`SELECT concat_ws(',',tokens_input,tokens_output,tokens_cache_read,tokens_cache_write,tokens_input_basis) FROM execution_totals WHERE execution_id='${id}'`).trim();
    assert.equal(row('t-claude'), '4,489,17492,2015,measured', 'the last report, not a sum');
    assert.equal(row('t-codex'), '200,50,800,measured', 'cached input counted once');
    assert.equal(row('t-none'), 'unavailable', 'no report: unknown, never 0');
    assert.equal(row('t-app'), '200,12,500,0,measured', 'app server: the sum of each call\'s last, never the thread total');
    assert.equal(row('t-bad'), 'unavailable', 'a malformed count is unknown and never blocks the end');
  } finally { await c.stop(); }
});

test('R5 runs are priced by the model their harness reports; a run on a subscription login shows its list-price equivalent as subscription, never as spend (requirement: Runners USD per host and month; risk: subscription work reads as money spent, or aliases stay unpriced)', async () => {
  const c = await startCoordinator({name: 'runners5'});
  try {
    const call = async (op: string, body: unknown) => (await fetch(c.base + '/api/ops/' + op, {method: 'POST', headers: {authorization: 'Bearer ' + c.secret, 'x-fleet-person': 'owner@trial.test', 'content-type': 'application/json'}, body: JSON.stringify(body)})).json() as any;
    const from = Date.now() - 864e5;
    for (const [unit, price] of [['input_tokens', 0.000004], ['output_tokens', 0.00002]] as const)
      await call('price_add', {subject: 'claude-opus-5-5', unit, price, currency: 'USD', source: 'vendor', sourceRef: 'https://platform.claude.com/docs/en/about-claude/pricing', effectiveFrom: from});
    c.sql(`INSERT INTO execution(id,kind,state,host_id,model,account_id,started_at,ended_at) VALUES
      ('p-api','agent_run','succeeded','h1','opus',NULL,now()-interval '2 minutes',now()-interval '1 minute'),
      ('p-sub','agent_run','succeeded','h1','opus','acct-1',now()-interval '2 minutes',now()-interval '1 minute');
      INSERT INTO execution_totals(execution_id,tokens_input,tokens_output) VALUES ('p-api',1000000,100000),('p-sub',1000000,100000);
      INSERT INTO document(kind,id,data) VALUES
      ('event','m1',jsonb_build_object('id','m1','executionId','p-api','seq',1,'data',jsonb_build_object('model','claude-opus-5-5'))),
      ('event','m2',jsonb_build_object('id','m2','executionId','p-sub','seq',1,'data',jsonb_build_object('model','claude-opus-5-5')));
      SELECT fleet_write_model(id) FROM execution WHERE id LIKE 'p-%';`);
    const b = await call('executions_runners', {});
    const cost = (id: string) => b.runs.find((r: any) => r.id === id).cost[0];
    assert.deepEqual([cost('p-api').value, cost('p-api').basis], [6, 'estimated'], 'opus resolves to claude-opus-5-5: 1M x $4 + 100k x $20');
    assert.deepEqual([cost('p-sub').value, cost('p-sub').basis, cost('p-sub').note], [6, 'subscription', 'subscription (list-price equivalent)']);
    const month = b.spend.find((s: any) => s.hostId === 'h1' && s.kind === 'agent_run' && s.period === 'month');
    assert.deepEqual(month.cost.map((x: any) => [x.value, x.basis]), [[6, 'estimated'], [6, 'subscription']], 'spend keeps money and subscription apart');
  } finally { await c.stop(); }
});
