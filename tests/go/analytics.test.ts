// Outside integration tests of T1-PR3 analytics_query, multi-currency (currency, basis) CostFigure aggregation,
// RLS hidden/unknown reporting, agent focus scoping, non-overlapping task/session/thread/definition/project rollups,
// and the 'projects' view_link route.
import {test} from 'node:test';
import assert from 'node:assert/strict';
import {randomUUID} from 'node:crypto';
import {startCoordinator, lit, type Coordinator} from '../harness.ts';

const owner = 'owner@trial.test';
const member = 'member@trial.test';
const outsider = 'outsider@trial.test';

async function op(c: Coordinator, name: string, body: object, person = owner, expectedStatus = 200) {
  const r = await fetch(`${c.base}/api/ops/${name}`, {
    method: 'POST',
    headers: {authorization: `Bearer ${c.secret}`, 'x-fleet-person': person, 'content-type': 'application/json'},
    body: JSON.stringify(body),
  });
  const text = await r.text();
  assert.equal(r.status, expectedStatus, `${name} (${person}) status=${r.status}: ${text}`);
  return text ? JSON.parse(text) : null;
}

async function seedProject(c: Coordinator, id: string, name: string) {
  const probe = await fetch(`${c.base}/api/ops/project_context`, {
    method: 'POST',
    headers: {authorization: `Bearer ${c.secret}`, 'x-fleet-person': owner, 'content-type': 'application/json'},
    body: JSON.stringify({projectId: id}),
  });
  if (probe.status === 200) return;
  await op(c, 'project_create', {
    id,
    requestId: randomUUID(),
    content: {
      settings: {id, name, contextFiles: ['rules.md'], roles: {pm: 'pm'}},
      contextFiles: [{path: 'rules.md', text: 'Trial rules.'}],
    },
  });
}

test('R1 analytics_query enforces leaf-only accounting, per-leaf effective_from pricing, multi-currency & subscription non-mixing, percentiles, and "unknown" dimensions (requirement: fleet-analytics.md CA-01/CA-04; risk: double-counting containers, wrong historical price, or subscription mixing with metered spend)', async () => {
  const c = await startCoordinator({name: 'analytics-core'});
  try {
    await seedProject(c, 'desiign', 'Desiign');

    // Two USD prices on the same UTC day (2026-11-10) so a naive bucket-level price join would misprice early runs.
    c.sql(`
      INSERT INTO price_entry(id, subject, unit, price, currency, source, observed_at, effective_from, created_by) VALUES
        ('pr-in-old', 'claude-sonnet', 'input_tokens', 0.003, 'USD', 'owner', '2026-11-10T00:00:00Z', '2026-11-10T00:00:00Z', 'owner@trial.test'),
        ('pr-in-new', 'claude-sonnet', 'input_tokens', 0.006, 'USD', 'owner', '2026-11-10T12:00:00Z', '2026-11-10T12:00:00Z', 'owner@trial.test'),
        ('pr-eur-tool', 'conn-a/search', 'request', 0.25, 'EUR', 'owner', '2026-11-10T00:00:00Z', '2026-11-10T00:00:00Z', 'owner@trial.test');

      -- Containers + placeholder child_agent (no execution_totals) MUST NOT be counted as primary leaves.
      INSERT INTO execution(id, kind, state, project_id, harness, model, started_at, ended_at, directed_by) VALUES
        ('pipe-run', 'pipeline_run', 'succeeded', 'desiign', NULL, NULL, '2026-11-10T01:00:00Z', '2026-11-10T16:00:00Z', 'owner@trial.test'),
        ('pipe-step', 'pipeline_step', 'succeeded', 'desiign', NULL, NULL, '2026-11-10T01:00:00Z', '2026-11-10T15:00:00Z', 'owner@trial.test'),
        ('child-empty', 'child_agent', 'succeeded', 'desiign', 'claude', 'claude-sonnet', '2026-11-10T01:05:00Z', '2026-11-10T01:06:00Z', 'owner@trial.test'),
        -- Metered leaf before 12:00 price transition ($0.003 * 1000 = $3.00 USD estimated)
        ('leaf-early', 'agent_run', 'succeeded', 'desiign', 'claude', 'claude-sonnet', '2026-11-10T02:00:00Z', '2026-11-10T02:00:01Z', 'owner@trial.test'),
        -- Metered leaf after 12:00 price transition ($0.006 * 1000 = $6.00 USD estimated), NULL profile_id -> "unknown"
        ('leaf-late', 'agent_run', 'failed', 'desiign', 'claude', 'claude-sonnet', '2026-11-10T14:00:00Z', '2026-11-10T14:00:03Z', 'owner@trial.test'),
        -- Subscription leaf after 12:00 ($0.006 * 1000 = $6.00 USD subscription), MUST NOT mix with metered USD ($9.00)!
        ('leaf-sub', 'agent_run', 'succeeded', 'desiign', 'claude', 'claude-sonnet', '2026-11-10T15:00:00Z', '2026-11-10T15:00:05Z', 'owner@trial.test'),
        -- Direct measured connection_cost leaf ($1.50 USD measured)
        ('tool-usd', 'tool_run', 'succeeded', 'desiign', NULL, NULL, '2026-11-10T03:00:00Z', '2026-11-10T03:00:01Z', 'owner@trial.test'),
        -- Estimated EUR tool leaf (2 * €0.25 = €0.50 EUR estimated)
        ('tool-eur', 'tool_run', 'succeeded', 'desiign', NULL, NULL, '2026-11-10T04:00:00Z', '2026-11-10T04:00:01Z', 'owner@trial.test'),
        -- Unpriced tool leaf -> unavailable figure (never 0)
        ('tool-unpriced', 'tool_run', 'succeeded', 'desiign', NULL, NULL, '2026-11-10T05:00:00Z', '2026-11-10T05:00:01Z', 'owner@trial.test');

      UPDATE execution SET account_id = 'sub-acc' WHERE id = 'leaf-sub';
      UPDATE execution SET data = '{"connection":"conn-a","tool":"search"}'::jsonb WHERE id = 'tool-eur';
      UPDATE execution SET data = '{"connection":"conn-b","tool":"mystery"}'::jsonb WHERE id = 'tool-unpriced';

      INSERT INTO execution_totals(execution_id, project_id, tokens_input, tokens_input_basis, wall_ms, wall_ms_basis, cpu_ms, cpu_ms_basis, peak_memory_bytes, peak_memory_bytes_basis, connection_cost, connection_cost_basis) VALUES
        ('pipe-run', 'desiign', 99999, 'measured', 99999, 'measured', NULL, 'unavailable', NULL, 'unavailable', NULL, 'unavailable'),
        ('leaf-early', 'desiign', 1000, 'measured', 1000, 'measured', 200, 'measured', 5000000, 'measured', NULL, 'unavailable'),
        ('leaf-late', 'desiign', 1000, 'measured', 3000, 'measured', 400, 'measured', 9000000, 'measured', NULL, 'unavailable'),
        ('leaf-sub', 'desiign', 1000, 'subscription', 5000, 'measured', 600, 'measured', 7000000, 'measured', NULL, 'unavailable'),
        ('tool-usd', 'desiign', NULL, 'unavailable', 100, 'measured', NULL, 'unavailable', NULL, 'unavailable', '{"currency":"USD","cost":1.5}'::jsonb, 'measured'),
        ('tool-eur', 'desiign', NULL, 'unavailable', 200, 'measured', NULL, 'unavailable', NULL, 'unavailable', '{"unit":"request","amount":2}'::jsonb, 'measured'),
        ('tool-unpriced', 'desiign', NULL, 'unavailable', 300, 'measured', NULL, 'unavailable', NULL, 'unavailable', '{"unit":"request","amount":1}'::jsonb, 'measured');
    `);

    const q = await op(c, 'analytics_query', {
      window: {from: '2026-11-10T00:00:00Z', to: '2026-11-11T00:00:00Z'},
      dimensions: ['day', 'profile_id'],
      measures: ['executions', 'tokens_input', 'wall_ms', 'wall_ms_p50', 'wall_ms_p95', 'cpu_ms', 'peak_memory_bytes', 'cost'],
    });

    assert.equal(q.hidden, 0);
    assert.equal(q.unknown, 6, 'all 6 leaves lack profile_id');
    assert.equal(q.rows.length, 1);
    assert.deepEqual(q.rows[0].dimensions, {day: '2026-11-10', profile_id: 'unknown'});

    // 6 real leaves (3 agent_run + 3 tool_run); containers & empty child_agent excluded.
    assert.equal(q.totals.measures.executions.value, 6);
    assert.equal(q.totals.measures.tokens_input.value, 3000, 'pipe-run 99999 must not enter sums');
    assert.equal(q.totals.measures.tokens_input.basis, 'subscription', 'includes subscription agent tokens');
    assert.equal(q.totals.measures.wall_ms.value, 9600);
    assert.equal(q.totals.measures.cpu_ms.value, 1200);
    assert.equal(q.totals.measures.cpu_ms.basis, 'measured', 'tool_run rows lack CPU but must not downgrade measured agent_runs');
    assert.equal(q.totals.measures.peak_memory_bytes.value, 9000000);
    assert.equal(q.totals.measures.peak_memory_bytes.basis, 'measured');

    // Sort order of wall_ms over [100, 200, 300, 1000, 3000, 5000]:
    // p50 = (300 + 1000)/2 = 650; p95 = 3000 + 0.75 * 2000 = 4500.
    assert.equal(q.totals.measures.wall_ms_p50.value, 650);
    assert.equal(q.totals.measures.wall_ms_p95.value, 4500);

    // Multi-currency & (currency, basis) cost separation.
    const costByKey = new Map<string, any>(
      q.totals.cost.map((f: any) => [`${f.currency ?? 'null'}:${f.basis}`, f]),
    );
    assert.equal(costByKey.get('USD:measured')?.value, 1.5);
    assert.equal(costByKey.get('USD:estimated')?.value, 9, 'early $3 + late $6 = $9 (never $6 sub)');
    assert.deepEqual(costByKey.get('USD:estimated')?.prices.map((p: any) => p.id), ['pr-in-new', 'pr-in-old']);
    assert.equal(costByKey.get('USD:subscription')?.value, 6);
    assert.equal(costByKey.get('USD:subscription')?.note, 'subscription (list-price equivalent)');
    assert.equal(costByKey.get('EUR:estimated')?.value, 0.5);
    assert.equal(costByKey.get('null:unavailable')?.value, null);
    assert.equal(costByKey.get('null:unavailable')?.usages, 1);
  } finally { await c.stop(); }
});

test('R2 analytics_query honors PostgreSQL RLS and reports hidden & unknown counts cleanly (requirement: fleet-people-access.md CA-05; risk: unprivileged members leak hidden project rows or omit hidden indicator)', async () => {
  const c = await startCoordinator({name: 'analytics-rls'});
  try {
    await seedProject(c, 'desiign', 'Desiign');
    await seedProject(c, 'secret-proj', 'Secret');
    c.sql(`
      INSERT INTO execution(id, kind, state, project_id, harness, model, started_at, ended_at) VALUES
        ('vis-1', 'agent_run', 'succeeded', 'desiign', 'claude', 'claude-sonnet', '2026-11-10T02:00:00Z', '2026-11-10T02:01:00Z'),
        ('vis-2', 'agent_run', 'succeeded', 'desiign', 'claude', NULL, '2026-11-10T03:00:00Z', '2026-11-10T03:01:00Z'),
        ('hid-1', 'agent_run', 'succeeded', 'secret-proj', 'claude', 'claude-sonnet', '2026-11-10T04:00:00Z', '2026-11-10T04:01:00Z'),
        ('hid-2', 'agent_run', 'succeeded', 'secret-proj', 'claude', 'claude-sonnet', '2026-11-10T05:00:00Z', '2026-11-10T05:01:00Z');
      INSERT INTO execution_totals(execution_id, project_id, tokens_input, wall_ms) VALUES
        ('vis-1', 'desiign', 100, 100),
        ('vis-2', 'desiign', 50, 200),
        ('hid-1', 'secret-proj', 5000, 900),
        ('hid-2', 'secret-proj', 7000, 900);
    `);

    const body = {
      window: {from: '2026-11-10T00:00:00Z', to: '2026-11-11T00:00:00Z'},
      dimensions: ['project', 'model'],
      measures: ['executions', 'tokens_input'],
    };

    const asOwner = await op(c, 'analytics_query', body, owner);
    assert.equal(asOwner.hidden, 0);
    assert.equal(asOwner.unknown, 1);
    assert.equal(asOwner.totals.measures.executions.value, 4);

    // member@trial.test has access only to 'desiign' (tests/trial/people.json).
    const asMember = await op(c, 'analytics_query', body, member);
    assert.equal(asMember.totals.measures.executions.value, 2);
    assert.equal(asMember.totals.measures.tokens_input.value, 150);
    assert.equal(asMember.hidden, 2, 'reports exact count of hidden leaves outside caller project scope');
    assert.equal(asMember.unknown, 1, 'vis-2 has NULL model');
    for (const row of asMember.rows) {
      assert.equal(row.dimensions.project, 'desiign');
    }

    // Filter targeting secret-proj returns empty rows under RLS without leaking its data.
    const filtered = await op(c, 'analytics_query', {...body, filters: {project: ['secret-proj']}}, member);
    assert.equal(filtered.rows.length, 0);
    assert.equal(filtered.totals.measures.executions.value, 0);
    assert.equal(filtered.hidden, 2);

    const asOutsider = await op(c, 'analytics_query', body, outsider);
    assert.equal(asOutsider.totals.measures.executions.value, 0);
    assert.equal(asOutsider.hidden, 4);
  } finally { await c.stop(); }
});

test('R3 agent MCP calls to analytics_query strictly enforce session focus and reject cross-project reads (requirement: fleet-pm-agent.md agent scoping; risk: owner-directed agent in a focused session reads hidden projects)', async () => {
  const c = await startCoordinator({name: 'analytics-agent'});
  try {
    await seedProject(c, 'desiign', 'Desiign');
    await seedProject(c, 'secret-proj', 'Secret');
    c.sql(`
      INSERT INTO execution(id, kind, state, project_id, harness, model, started_at, ended_at) VALUES
        ('d1', 'agent_run', 'succeeded', 'desiign', 'claude', 'claude-sonnet', '2026-11-10T02:00:00Z', '2026-11-10T02:01:00Z'),
        ('s1', 'agent_run', 'succeeded', 'secret-proj', 'claude', 'claude-sonnet', '2026-11-10T03:00:00Z', '2026-11-10T03:01:00Z');
      INSERT INTO execution_totals(execution_id, project_id, tokens_input, wall_ms) VALUES
        ('d1', 'desiign', 111, 100),
        ('s1', 'secret-proj', 999, 200);
    `);

    const host = await op(c, 'hosts_register', {id: 'h-analytics', holder: owner}, owner);
    const poll = async () => {
      const r = await fetch(`${c.base}/api/hosts/poll`, {
        method: 'POST',
        headers: {authorization: `Bearer ${host.secret}`, 'content-type': 'application/json'},
        body: JSON.stringify({platform: 'darwin', capabilities: [], stores: [], problems: []}),
      });
      assert.equal(r.status, 200);
      return r.json() as Promise<any>;
    };
    await poll();

    const sess = await op(c, 'session_start', {projectId: 'desiign', role: 'pm', requestId: randomUUID(), text: 'Audit desiign metrics'}, owner);
    const cmd = (await poll()).commands.find((x: any) => x.sessionId === sess.sessionId && x.kind === 'start');
    assert.ok(cmd, 'start command issued for agent session');
    await op(c, 'focus_set', {sessionId: sess.sessionId, projectIds: ['desiign']}, owner);

    const mcpCall = async (args: object) => {
      const r = await fetch(`${c.base}/api/hosts/agent-tools`, {
        method: 'POST',
        headers: {authorization: `Bearer ${host.secret}`, 'content-type': 'application/json'},
        body: JSON.stringify({
          hostId: 'h-analytics',
          commandId: cmd.id,
          executionId: cmd.executionId,
          agentToolKey: cmd.payload.agentToolKey,
          request: {jsonrpc: '2.0', id: 1, method: 'tools/call', params: {name: 'analytics_query', arguments: args}},
        }),
      });
      const body: any = await r.json();
      return {status: r.status, body};
    };

    const win = {from: '2026-11-10T00:00:00Z', to: '2026-11-11T00:00:00Z'};
    // Unfiltered call auto-scopes strictly to session focus ('desiign').
    const scoped = await mcpCall({window: win, dimensions: ['project'], measures: ['executions', 'tokens_input']});
    assert.equal(scoped.status, 200);
    const scopedPayload = JSON.parse(scoped.body.result.content[0].text);
    assert.equal(scopedPayload.totals.measures.executions.value, 1);
    assert.equal(scopedPayload.totals.measures.tokens_input.value, 111);
    assert.equal(scopedPayload.rows[0].dimensions.project, 'desiign');

    // Explicitly querying an out-of-focus project via projectId or filters fails with 403 / outside focus.
    for (const badArgs of [
      {window: win, projectId: 'secret-proj'},
      {window: win, filters: {project: ['secret-proj']}},
      {window: win, filters: {projectId: ['desiign', 'secret-proj']}},
    ]) {
      const denied = await mcpCall(badArgs);
      assert.ok(denied.status === 403 || denied.body.error || denied.body.result?.isError, `expected denial for ${JSON.stringify(badArgs)}: ${JSON.stringify(denied)}`);
    }

    // Wildcard focus allows owner-directed agent to read both granted projects.
    await op(c, 'focus_set', {sessionId: sess.sessionId, projectIds: '*'}, owner);
    const wildcard = await mcpCall({window: win, dimensions: ['project'], measures: ['executions', 'tokens_input']});
    assert.equal(wildcard.status, 200);
    const wildPayload = JSON.parse(wildcard.body.result.content[0].text);
    assert.equal(wildPayload.totals.measures.executions.value, 2);
    assert.equal(wildPayload.totals.measures.tokens_input.value, 1110);
  } finally { await c.stop(); }
});

test('R4 task_get, session_get, threads, definition_usage, executions_runners, project_context, and view_link expose consistent non-overlapping rollups (requirement: fleet-analytics.md CA-01/CA-08; risk: pre-attachment leakage, tool_run leaks into task_get.executions, double counting across scopes, or missing projects route)', async () => {
  const c = await startCoordinator({name: 'analytics-rollups'});
  try {
    await seedProject(c, 'desiign', 'Desiign');

    const base = Date.now() - 3600_000;
    const iso = (ms: number) => new Date(ms).toISOString();

    c.sql(`
      INSERT INTO price_entry(id, subject, unit, price, currency, source, observed_at, effective_from, created_by) VALUES
        ('p-in', 'm1', 'input_tokens', 0.01, 'USD', 'owner', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z', 'owner@trial.test');

      -- Definition 'pipe-1' revision 1 (saved) vs draft run.
      INSERT INTO definition(kind, id, path, head) VALUES
        ('pipeline', 'pipe-1', 'pipelines/pipe-1.json', 1);
      INSERT INTO definition_revision(kind, id, revision, content, content_sha, source, actor_kind, by_email, message, at) VALUES
        ('pipeline', 'pipe-1', 1, '{"name":"pipe-1","steps":[]}', 'sha-pipe-1', 'editor', 'person', 'owner@trial.test', 'init', now());

      -- Primary task T1 and spawned task T2 (created by execution ex-own).
      INSERT INTO task(id, code, project_id, title, state, created_by_execution_id) VALUES
        ('task-1', 'TASK-1', 'desiign', 'Primary Task', 'in_progress', NULL),
        ('task-2', 'TASK-2', 'desiign', 'Spawned Task', 'completed', 'ex-own');

      INSERT INTO document(id, kind, project_id, data) VALUES
        ('sess-1', 'session', 'desiign', '{"id":"sess-1","projectId":"desiign","taskId":"task-1"}'::jsonb),
        ('sess-del', 'session', 'desiign', '{"id":"sess-del","projectId":"desiign","parentExecutionId":"ex-own"}'::jsonb),
        ('sess-spawn', 'session', 'desiign', '{"id":"sess-spawn","projectId":"desiign","taskId":"task-2"}'::jsonb);

      -- Pre-attachment turn in sess-1 (task_id IS NULL), overlapping own run + child tool_run, child_agent, delegated session run, spawned task run.
      -- Pipeline container ex-pipe (pinned pipe-1 rev 1) + leaf ex-own + draft run ex-draft (draft: true).
      INSERT INTO execution(id, kind, state, project_id, session_id, task_id, parent_execution_id, harness, model, started_at, ended_at, definitions) VALUES
        ('ex-pipe', 'pipeline_run', 'succeeded', 'desiign', 'sess-1', 'task-1', NULL, NULL, NULL, ${lit(iso(base))}, ${lit(iso(base + 250))}, '[{"kind":"pipeline","id":"pipe-1","revision":1}]'::jsonb),
        ('ex-pre', 'agent_run', 'succeeded', 'desiign', 'sess-1', NULL, NULL, 'claude', 'm1', ${lit(iso(base))}, ${lit(iso(base + 100))}, '[]'::jsonb),
        ('ex-own', 'agent_run', 'succeeded', 'desiign', 'sess-1', 'task-1', 'ex-pipe', 'claude', 'm1', ${lit(iso(base + 50))}, ${lit(iso(base + 250))}, '[]'::jsonb),
        ('ex-tool', 'tool_run', 'succeeded', 'desiign', 'sess-1', 'task-1', 'ex-own', NULL, NULL, ${lit(iso(base + 60))}, ${lit(iso(base + 90))}, '[]'::jsonb),
        ('ex-child', 'child_agent', 'succeeded', 'desiign', 'sess-1', 'task-1', 'ex-own', 'claude', 'm1', ${lit(iso(base + 100))}, ${lit(iso(base + 200))}, '[]'::jsonb),
        ('ex-del', 'agent_run', 'succeeded', 'desiign', 'sess-del', NULL, NULL, 'claude', 'm1', ${lit(iso(base + 110))}, ${lit(iso(base + 210))}, '[]'::jsonb),
        ('ex-spawn', 'agent_run', 'succeeded', 'desiign', 'sess-spawn', 'task-2', NULL, 'claude', 'm1', ${lit(iso(base + 300))}, ${lit(iso(base + 500))}, '[]'::jsonb),
        ('ex-draft', 'agent_run', 'succeeded', 'desiign', NULL, NULL, NULL, 'claude', 'm1', ${lit(iso(base + 600))}, ${lit(iso(base + 1600))}, '[{"kind":"pipeline","id":"pipe-1","revision":1,"draft":true}]'::jsonb);

      UPDATE execution SET data = '{"connection":"gh","tool":"pr"}'::jsonb WHERE id = 'ex-tool';
      UPDATE execution SET account_id = 'acc-sub' WHERE id = 'ex-del';

      INSERT INTO execution_totals(execution_id, project_id, tokens_input, tokens_input_basis, wall_ms, wall_ms_basis) VALUES
        ('ex-pipe', 'desiign', 9999, 'measured', 9999, 'measured'),
        ('ex-pre', 'desiign', 100, 'measured', 100, 'measured'),
        ('ex-own', 'desiign', 200, 'measured', 200, 'measured'),
        ('ex-tool', 'desiign', NULL, 'unavailable', 30, 'measured'),
        ('ex-child', 'desiign', 300, 'measured', 100, 'measured'),
        ('ex-del', 'desiign', 50, 'subscription', 100, 'measured'),
        ('ex-spawn', 'desiign', 400, 'measured', 200, 'measured'),
        ('ex-draft', 'desiign', 8000, 'measured', 1000, 'measured');
    `);

    // 1. task_get: own/delegated/spawned/total + tool_run filtered from executions (still in executions_list).
    const tGet = await op(c, 'task_get', {taskId: 'task-1'});
    assert.equal(tGet.resources.own.tokens.input, 300, 'own = ex-pre(100) + ex-own(200)');
    assert.equal(tGet.resources.own.durations.elapsedMs, 250, 'max(ended) - min(started) = 250ms across overlapping own leaves');
    assert.equal(tGet.resources.own.durations.executionWallMs, 330, 'sum(wall_ms) = 100 + 200 + 30');
    assert.equal(tGet.resources.own.tools.totalCalls, 1);
    assert.equal(tGet.resources.delegated.tokens.input, 350, 'delegated = ex-child(300) + ex-del(50); ex-pre did NOT leak!');
    assert.equal(tGet.resources.spawned.tokens.input, 400, 'spawned = ex-spawn(400)');
    assert.equal(tGet.resources.spawnedTasks.length, 1);
    assert.equal(tGet.resources.spawnedTasks[0].taskId, 'task-2');
    assert.equal(tGet.resources.total.tokens.input, 1050, 'own(300) + delegated(350) + spawned(400)');
    assert.ok(!tGet.executions.some((e: any) => e.kind === 'tool_run'), 'task_get filters tool_run rows out of task.executions');

    const execList = await op(c, 'executions_list', {taskId: 'task-1'});
    assert.ok(execList.executions.some((e: any) => e.kind === 'tool_run'), 'executions_list still includes tool_run rows');

    // Cost basis separation in task_get.
    const tCost = new Map<string, any>(tGet.cost.map((f: any) => [`${f.currency}:${f.basis}`, f]));
    assert.equal(tCost.get('USD:estimated')?.value, 10, '(100+200+300+400)*0.01 = 10 USD metered');
    assert.equal(tCost.get('USD:subscription')?.value, 0.5, 'ex-del 50*0.01 = 0.50 USD subscription kept separate');

    // 2. session_get matches non-overlapping lineage for sess-1.
    const sGet = await op(c, 'session_get', {sessionId: 'sess-1'});
    assert.equal(sGet.resources.own.tokens.input, 300);
    assert.equal(sGet.resources.delegated.tokens.input, 350);
    assert.equal(sGet.resources.spawned.tokens.input, 400);
    assert.equal(sGet.resources.total.tokens.input, 1050);

    // 3. thread_entries & thread_blocks boundary blocks carry .resources.
    const th = await op(c, 'thread_entries', {key: {session: 'sess-1'}, limit: 20});
    const ownBoundary = th.entries.find((e: any) => e.kind === 'boundary' && e.executionId === 'ex-own');
    assert.ok(ownBoundary?.resources, 'BoundaryBlock carries resources');
    assert.equal(ownBoundary.resources.tokens.input, 200);
    assert.equal(ownBoundary.resources.tools.totalCalls, 1, 'boundary includes direct tool_run child');

    const thBlocks = await op(c, 'thread_blocks', {key: {session: 'sess-1'}, rows: [{kind: 'execution', id: 'ex-own'}]});
    const ownBlock = thBlocks.entries.find((e: any) => e.kind === 'boundary' && e.executionId === 'ex-own');
    assert.equal(ownBlock?.resources?.tokens?.input, 200);

    // 4. definition_usage resolves pipe-1 container -> descendant leaves and excludes draft runs.
    const defUse = await op(c, 'definition_usage', {kind: 'pipeline', id: 'pipe-1', days: 30});
    assert.equal(defUse.total.runs, 1, 'ex-draft excluded');
    assert.ok(defUse.total.resources, 'Window.resources populated');
    assert.equal(defUse.total.resources.tokens.input, 550, 'ex-own(200) + ex-child(300) + ex-del(50); ignores container 9999 & draft 8000');

    // 5. executions_runners separates metered vs subscription spend cleanly.
    const runners = await op(c, 'executions_runners', {});
    const agentSpend = runners.spend.find((s: any) => s.kind === 'agent_run' && s.period === 'month');
    assert.ok(agentSpend && Array.isArray(agentSpend.cost));
    assert.ok(agentSpend.cost.some((f: any) => f.currency === 'USD' && f.basis === 'estimated' && f.value > 0), 'metered agent_run spend kept estimated');
    assert.ok(agentSpend.cost.some((f: any) => f.currency === 'USD' && f.basis === 'subscription' && f.value === 0.5), 'subscription spend kept separate');

    // 6. project_context includes ResourceSummary.
    const pCtx = await op(c, 'project_context', {projectId: 'desiign'});
    assert.ok(pCtx.resources, 'project_context includes resources');
    assert.equal(pCtx.resources.tokens.input, 9050, 'all visible leaves in desiign (ex-pipe excluded)');

    // 7. view_link supports 'projects' view parity.
    const vLink = await op(c, 'view_link', {view: {view: 'projects'}});
    assert.equal(vLink.path, '/projects');
    const vParsed = await op(c, 'view_link', {url: '/projects'});
    assert.deepEqual(vParsed.view, {view: 'projects'});
  } finally { await c.stop(); }
});
