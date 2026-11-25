// Isolated two-project HAL0 qualification tests (fleet.md Persistent bots, T-FLT-42).
// Exercises HAL0 across two isolated projects (hello-world and hello-world-alt): checking tasks, executions,
// and telemetry; raising Fleet questions for genuine owner decisions; continuing existing vs creating
// deduplicated tasks for confirmed technical causes; verifying fixes on the affected surface; retaining
// unconfirmed observations without premature dismissal; and audited dismissal/withdrawal of obsolete/duplicate entries.
import assert from 'node:assert/strict';
import { randomUUID } from 'node:crypto';
import { createServer } from 'node:http';
import { test } from 'node:test';
import { hostCommand, lit, noHostCommand, startCoordinator, type Coordinator } from '../harness.ts';

const OWNER = 'owner@trial.test';
const PROJ_A = 'hello-world';
const PROJ_B = 'hello-world-alt';

type ClickHouseReq = { query: string; body: string };

async function startFakeClickHouse() {
  const seen: ClickHouseReq[] = [];
  const srv = createServer((req, res) => {
    let body = '';
    req.on('data', (c) => { body += c; });
    req.on('end', () => {
      const params = new URL(req.url!, 'http://x').searchParams;
      const query = params.get('query') ?? body;
      seen.push({ query, body });
      if (query.includes('FROM span WHERE trace_id')) {
        const tid = params.get('param_t') || 'a'.repeat(32);
        res.writeHead(200, { 'content-type': 'application/x-ndjson' });
        res.end(JSON.stringify({
          TraceID: tid,
          SpanID: '1'.repeat(16),
          Parent: '',
          Service: 'executor',
          HostID: 'h-hal0',
          Name: 'agent.run',
          Kind: 'server',
          Start: '2026-11-24 08:00:00.000000',
          Outcome: 'error',
          ErrorClass: 'exit_2',
          ExecutionID: '',
          DurationMs: 1200,
          CPU: 0.4,
          RSS: 1024,
          Peak: 2048,
          Attrs: { 'fleet.project': PROJ_A },
          Visible: 1,
        }) + '\n');
        return;
      }
      if (query.includes('FROM span') && query.includes('GROUP BY')) {
        res.writeHead(200, { 'content-type': 'application/x-ndjson' });
        res.end('');
        return;
      }
      res.writeHead(200);
      res.end('');
    });
  });
  await new Promise<void>((r) => srv.listen(0, '127.0.0.1', r));
  const port = (srv.address() as { port: number }).port;
  return { url: `http://127.0.0.1:${port}`, seen, close: () => new Promise<void>((r) => srv.close(() => r())) };
}

test('HAL0 qualifies across two isolated projects: checks tasks/executions/telemetry, raises owner questions, deduplicates/continues fixes, verifies fixes, retains unconfirmed observations, and audits dismissals (requirement: fleet.md Persistent bots; risk: cross-project leakage, duplicate fix tasks, premature dismissal of unconfirmed runs, or missed question_ask/audit history)', async () => {
  const ch = await startFakeClickHouse();
  const coordinator: Coordinator = await startCoordinator({
    name: 'hal0-qual',
    env: {
      FLEET_TRIGGER_TICK_MS: '200',
      FLEET_LOST_AFTER_MS: '120000',
      FLEET_CLICKHOUSE_URL: ch.url,
    },
  });

  async function op(name: string, body: object, actor = OWNER, status = 200) {
    const response = await fetch(`${coordinator.base}/api/ops/${name}`, {
      method: 'POST',
      headers: {
        'content-type': 'application/json',
        authorization: 'Bearer ' + coordinator.secret,
        'x-fleet-person': actor,
      },
      body: JSON.stringify(body),
    });
    const text = await response.text();
    let parsed: any = text;
    try { parsed = JSON.parse(text); } catch {}
    assert.equal(response.status, status, `${name} (${response.status}): ${text}`);
    return parsed;
  }

  async function hostCall(hostSecret: string, path: string, body: object) {
    const response = await fetch(coordinator.base + path, {
      method: 'POST',
      headers: {
        'content-type': 'application/json',
        authorization: 'Bearer ' + hostSecret,
      },
      body: JSON.stringify(body),
    });
    const text = await response.text();
    let parsed: any = text;
    try { parsed = JSON.parse(text); } catch {}
    assert.equal(response.status, 200, `${path} (${response.status}): ${text}`);
    return parsed;
  }

  const pollHost = (hostId: string, hostSecret: string) => async (query: string) =>
    hostCall(hostSecret, '/api/hosts/poll' + query, {
      hostId,
      platform: 'darwin',
      capabilities: ['codex', 'claude', 'start-access-v1', 'workspace-key-v1'],
      stores: [],
      problems: [],
    });

  async function agentTool(hostId: string, hostSecret: string, commandId: string, executionId: string, name: string, args: object) {
    const res = await hostCall(hostSecret, '/api/hosts/agent-tools', {
      hostId,
      commandId,
      executionId,
      request: {
        jsonrpc: '2.0',
        id: 1,
        method: 'tools/call',
        params: { name, arguments: args },
      },
    });
    const result = res.result ?? {};
    assert.ok(!result.isError, `agent tool ${name} failed: ${JSON.stringify(res)}`);
    if (result.structuredContent) return result.structuredContent;
    const txt = result.content?.[0]?.text;
    return txt ? JSON.parse(txt) : result;
  }

  try {
    const hostId = 'h-hal0';
    const hostSecret = (await op('hosts_register', { id: hostId })).secret as string;
    const poll = pollHost(hostId, hostSecret);
    // Warm up host observation.
    await poll('');

    // Ensure hello-world defines project roles so HAL0 can start worker sessions for fixes.
    const ctxA = await op('project_context', { projectId: PROJ_A });
    const contentA = structuredClone(ctxA.content);
    contentA.settings.roles = { pm: 'pm', worker: 'coding-worker' };
    await op('project_update', {
      projectId: PROJ_A,
      requestId: 'hal0-qual-roles-a',
      baseRevision: ctxA.revision,
      content: contentA,
    });

    // 1. Provision two isolated HAL0 bots (both named "HAL0") in hello-world and hello-world-alt.
    const halA = randomUUID();
    const halB = randomUUID();
    const baseSettings = (purpose: string) => ({
      name: 'HAL0',
      purpose,
      profileId: 'pm',
      instructions: 'Inspect Needs you entries, telemetry, and tasks; escalate owner decisions via question_ask; deduplicate technical fixes.',
      soul: 'Verify on the affected surface; retain unconfirmed observations; audit obsolete/duplicate dismissals.',
    });

    const createdA = (await op('bot_settings_save', { botId: halA, projectId: PROJ_A, baseRevision: 0, settings: baseSettings('Monitor Project A') })).bot;
    const createdB = (await op('bot_settings_save', { botId: halB, projectId: PROJ_B, baseRevision: 0, settings: baseSettings('Monitor Project B') })).bot;
    assert.equal(createdA.settings.name, 'HAL0');
    assert.equal(createdB.settings.name, 'HAL0');
    assert.notEqual(createdA.homeSessionId, createdB.homeSessionId);
    for (const botId of [halA, halB]) {
      await op('trigger_save', { id: randomUUID(), baseRevision: 0, botId, presetId: 'needs-you', settings: { everyMs: 200 } });
    }

    // 2. Seed Project A state BEFORE triggering Needs you:
    //    - An existing active fix task (tExistingA) + its earlier failed execution (eConfirmedA, with traceparent for telemetry_trace)
    //    - A duplicate failed execution (eDupA) for the same confirmed technical cause
    //    - An unconfirmed running execution (eUnconfA, observation='unconfirmed', problem='lost contact')
    const tExistingA = (await op('task_create', {
      projectId: PROJ_A,
      title: 'Fix parser panic on empty input',
      expectedOutcome: 'Parser handles empty input and passes verification',
    })).id as string;
    const traceA = 'a'.repeat(32);
    const eConfirmedA = 'exec-conf-a-' + randomUUID().slice(0, 8);
    const eDupA = 'exec-dup-a-' + randomUUID().slice(0, 8);
    const eUnconfA = 'exec-unconf-a-' + randomUUID().slice(0, 8);
    const foreignSessA = 'sess-foreign-a-' + randomUUID().slice(0, 8);
    const nowSec = Math.floor(Date.now() / 1000) - 60;

    coordinator.sql(`
      BEGIN;
      INSERT INTO session(id, project_id, payload) VALUES (${lit(foreignSessA)}, ${lit(PROJ_A)}, '{}'::jsonb);
      INSERT INTO execution(id, kind, state, observation, project_id, task_id, session_id, host_id, model, problem, started_at, ended_at, data) VALUES
        (${lit(eConfirmedA)}, 'agent_run', 'failed', 'confirmed', ${lit(PROJ_A)}, ${lit(tExistingA)}, ${lit(foreignSessA)}, ${lit(hostId)}, 'gpt-6.2-sol', 'exit 2: nil pointer in parser', to_timestamp(${nowSec - 30}), to_timestamp(${nowSec - 10}), ${lit({ id: eConfirmedA, traceparent: `00-${traceA}-${'2'.repeat(16)}-01` })}::jsonb),
        (${lit(eDupA)}, 'agent_run', 'failed', 'confirmed', ${lit(PROJ_A)}, ${lit(tExistingA)}, ${lit(foreignSessA)}, ${lit(hostId)}, 'gpt-6.2-sol', 'exit 2: nil pointer in parser', to_timestamp(${nowSec - 8}), to_timestamp(${nowSec - 2}), ${lit({ id: eDupA })}::jsonb),
        (${lit(eUnconfA)}, 'agent_run', 'running', 'unconfirmed', ${lit(PROJ_A)}, NULL, ${lit(foreignSessA)}, ${lit(hostId)}, 'gpt-6.2-sol', 'host contact lost mid-run', now(), NULL, ${lit({ id: eUnconfA })}::jsonb);
      COMMIT;
    `);

    // 3. Wait for HAL0-A to wake automatically on Needs you in Project A; HAL0-B in Project B must NOT wake!
    const startCmdA = await hostCommand(poll, (c) => c.kind === 'start' && c.sessionId === createdA.homeSessionId);
    assert.ok(startCmdA, 'HAL0-A should wake on Project A Needs you');
    const halExecA = startCmdA.executionId as string;
    const halCmdIdA = startCmdA.id as string;
    const halInstrA = startCmdA.payload?.instructions as string;
    assert.ok(halInstrA.includes(`id ${halA}`), `HAL0-A instructions should carry bot id ${halA}: ${halInstrA}`);
    assert.ok(halInstrA.includes(`project ${PROJ_A}`), `HAL0-A instructions should carry project ${PROJ_A}`);

    const runsBBefore = await op('bot_runs', { botId: halB });
    assert.equal(runsBBefore.runs.length, 0, 'HAL0-B in Project B must stay idle while only Project A has Needs you');

    await hostCall(hostSecret, '/api/hosts/receipts', {
      hostId, commandId: halCmdIdA, executionId: halExecA, status: 'started', nativeSessionId: 'ns-hal0-a-1',
    });

    // 4. Inside HAL0-A's active turn, perform full operational check:
    //    (a) Inspect tasks, Needs-you executions, and telemetry trace.
    const tasksA = await agentTool(hostId, hostSecret, halCmdIdA, halExecA, 'task_page', { filter: { projectId: [PROJ_A] } });
    assert.ok(tasksA.tasks.some((t: any) => t.id === tExistingA), 'HAL0-A sees existing task in Project A');

    const needsYouA = await agentTool(hostId, hostSecret, halCmdIdA, halExecA, 'executions_list', { projectId: PROJ_A, needsYou: true });
    const nyIdsA = needsYouA.executions.map((e: any) => e.id);
    assert.ok(nyIdsA.includes(eConfirmedA) && nyIdsA.includes(eDupA) && nyIdsA.includes(eUnconfA));

    const traceOut = await agentTool(hostId, hostSecret, halCmdIdA, halExecA, 'telemetry_trace', { executionId: eConfirmedA });
    assert.equal(traceOut.traceId, traceA);
    assert.equal(traceOut.spans.length, 1);

    const teleSum = await agentTool(hostId, hostSecret, halCmdIdA, halExecA, 'telemetry_summary', { window: '1h' });
    assert.equal(teleSum.window, '1h');

    //    (b) Raise a Fleet question for a genuine owner decision via question_ask.
    const ownerQ = await agentTool(hostId, hostSecret, halCmdIdA, halExecA, 'question_ask', {
      projectId: PROJ_A,
      requestId: 'hal0-a-owner-q1',
      text: 'Should parser return empty AST or error on zero-byte config?',
    });
    assert.ok(ownerQ.id);
    // Verify questions_list({ botId: halA }) returns this question_ask row (asked_by='execution:'+halExecA) and Project B never sees it.
    const botQsA = await op('questions_list', { botId: halA });
    assert.equal(botQsA.questions.length, 1);
    assert.equal(botQsA.questions[0].id, ownerQ.id);
    assert.equal(botQsA.questions[0].projectId, PROJ_A);
    assert.equal((await op('questions_list', { botId: halB })).questions.length, 0);

    //    (c) Continue the existing task tExistingA (deduplication instead of opening a duplicate task) and end Home turn 1 so worker runs.
    const contSess = await agentTool(hostId, hostSecret, halCmdIdA, halExecA, 'session_start', {
      projectId: PROJ_A,
      taskId: tExistingA,
      role: 'worker',
      requestId: 'hal0-a-cont-existing',
      text: 'Continue fix for exit 2 nil pointer in parser and run unit verification',
    });
    assert.ok(contSess.sessionId);

    // Complete HAL0-A Home turn 1 while worker runs; verify quiet=false and bot_tasks lists continued task.
    await hostCall(hostSecret, '/api/hosts/receipts', {
      hostId, commandId: halCmdIdA, executionId: halExecA, status: 'completed', nativeSessionId: 'ns-hal0-a-1',
    });

    // Worker session runs and completes -> parent wake brings HAL0-A back for Turn 2 to verify & audit dismissals!
    const fixCmd = await hostCommand(poll, (c) => c.kind === 'start' && c.sessionId === contSess.sessionId);
    assert.ok(fixCmd, 'continued task worker session should dispatch');
    await hostCall(hostSecret, '/api/hosts/receipts', {
      hostId, commandId: fixCmd.id, executionId: fixCmd.executionId, status: 'started',
    });
    await hostCall(hostSecret, '/api/hosts/receipts', {
      hostId, commandId: fixCmd.id, executionId: fixCmd.executionId, status: 'completed',
    });

    const wakeCmdA2 = await hostCommand(poll, (c) => c.kind === 'start' && c.sessionId === createdA.homeSessionId && c.executionId !== halExecA);
    assert.ok(wakeCmdA2, 'HAL0-A should wake on worker completion to verify fix');
    const halExecA2 = wakeCmdA2.executionId as string;
    const halCmdIdA2 = wakeCmdA2.id as string;
    await hostCall(hostSecret, '/api/hosts/receipts', {
      hostId, commandId: halCmdIdA2, executionId: halExecA2, status: 'started', nativeSessionId: 'ns-hal0-a-1',
    });

    // Confirm the continued task link and verification on the affected surface (task_get + bot_tasks).
    const verifiedTaskA = await agentTool(hostId, hostSecret, halCmdIdA2, halExecA2, 'task_get', { taskId: tExistingA });
    assert.equal(verifiedTaskA.botLink.botId, halA);
    assert.equal(verifiedTaskA.botLink.linkKind, 'continued');
    assert.ok(verifiedTaskA.executions.some((e: any) => e.id === fixCmd.executionId && e.state === 'succeeded' && e.observation === 'confirmed'));

    //    (d) Retain unconfirmed observation (eUnconfA) without dismissal, and audit dismissal of proved duplicate/verified entries (eDupA, eConfirmedA).
    const dismissRes = await agentTool(hostId, hostSecret, halCmdIdA2, halExecA2, 'executions_dismiss', {
      ids: [eUnconfA, eDupA, eConfirmedA],
      reason: 'Verified fix on task ' + tExistingA + '; duplicate/resolved confirmed problems dismissed while unconfirmed run retained',
    });
    assert.deepEqual(dismissRes.dismissed.sort(), [eConfirmedA, eDupA].sort());
    assert.deepEqual(dismissRes.skipped, [{ id: eUnconfA, state: 'running' }]);

    // Verify audit trail for dismissed entries and NO dismissal audit on unconfirmed entry.
    const auditDup = await op('setting_changes', { target: 'execution:' + eDupA });
    assert.equal(auditDup.changes.length, 1);
    assert.equal(auditDup.changes[0].field, 'problem');
    assert.equal(auditDup.changes[0].value, 'dismissed');
    assert.equal(auditDup.changes[0].actor, 'execution:' + halExecA2);
    assert.ok(String(auditDup.changes[0].reason).includes(tExistingA));
    assert.equal((await op('setting_changes', { target: 'execution:' + eUnconfA })).changes.length, 0);

    // Verify eDupA and eConfirmedA are off Needs you while retained in historical problem=true queries, and eUnconfA remains on Needs you.
    const nyAfterA = await op('executions_list', { projectId: PROJ_A, needsYou: true });
    assert.deepEqual(nyAfterA.executions.map((e: any) => e.id), [eUnconfA]);
    const histProbA = await op('executions_list', { projectId: PROJ_A, problem: true });
    assert.ok(histProbA.executions.some((e: any) => e.id === eDupA), 'dismissed problem remains on historical record');

    //    (e) Complete HAL0-A wake run 2 -> verify both runs succeeded (run 1 quiet=false) and bot_tasks lists continued task.
    await hostCall(hostSecret, '/api/hosts/receipts', {
      hostId, commandId: halCmdIdA2, executionId: halExecA2, status: 'completed', nativeSessionId: 'ns-hal0-a-1',
    });
    const botRunsA = await op('bot_runs', { botId: halA });
    assert.equal(botRunsA.runs.length, 2, `unexpected botRunsA: ${JSON.stringify(botRunsA.runs)}`);
    assert.ok(botRunsA.runs.every((r: any) => r.state === 'succeeded'));
    const nyRunA = botRunsA.runs.find((r: any) => r.cause === 'needs_you');
    assert.equal(nyRunA?.quiet, false);
    const botTasksA = await op('bot_tasks', { botId: halA });
    assert.equal(botTasksA.tasks.length, 1);
    assert.equal(botTasksA.tasks[0].taskId, tExistingA);

    // Verify HAL0-A does NOT self-wake on its own open question_ask!
    assert.equal(await noHostCommand(poll, (c) => c.kind === 'start' && c.sessionId === createdA.homeSessionId), true, 'HAL0-A must not self-wake on its own question_ask');

    // 5. Project B isolation & lifecycle: seed an obsolete open question and a confirmed execution failure in hello-world-alt.
    const qObsB = randomUUID();
    const eProbB = 'exec-prob-b-' + randomUUID().slice(0, 8);
    const foreignSessB = 'sess-foreign-b-' + randomUUID().slice(0, 8);
    coordinator.sql(`
      BEGIN;
      INSERT INTO session(id, project_id, payload) VALUES (${lit(foreignSessB)}, ${lit(PROJ_B)}, '{}'::jsonb);
      INSERT INTO question(id, kind, project_id, session_id, text, options, asked_by, state)
      VALUES (${lit(qObsB)}, 'operator_decision', ${lit(PROJ_B)}, ${lit(foreignSessB)}, 'Obsolete deploy gate from yesterday?', '["yes","no"]', ${lit(OWNER)}, 'open');
      INSERT INTO execution(id, kind, state, observation, project_id, session_id, host_id, model, problem, started_at, ended_at, data)
      VALUES (${lit(eProbB)}, 'agent_run', 'failed', 'confirmed', ${lit(PROJ_B)}, ${lit(foreignSessB)}, ${lit(hostId)}, 'gpt-6.2-sol', 'timeout fetching fixture index', to_timestamp(${nowSec - 15}), to_timestamp(${nowSec - 5}), ${lit({ id: eProbB })}::jsonb);
      COMMIT;
    `);

    const startCmdB = await hostCommand(poll, (c) => c.kind === 'start' && c.sessionId === createdB.homeSessionId);
    assert.ok(startCmdB, 'HAL0-B should wake on Project B Needs you');
    const halExecB = startCmdB.executionId as string;
    const halCmdIdB = startCmdB.id as string;
    await hostCall(hostSecret, '/api/hosts/receipts', {
      hostId, commandId: halCmdIdB, executionId: halExecB, status: 'started',
    });

    // HAL0-B creates a deduplicated fix task in Project B and audits withdrawal of obsolete question qObsB.
    const tNewB = await agentTool(hostId, hostSecret, halCmdIdB, halExecB, 'task_create', {
      projectId: PROJ_B,
      title: 'Add retry backoff for fixture index fetch',
      expectedOutcome: 'Retry succeeds on transient timeout',
      held: false,
    });
    assert.equal(tNewB.projectId, PROJ_B);
    assert.equal(tNewB.botLink.botId, halB);
    assert.equal(tNewB.botLink.linkKind, 'created');

    const withdrawRes = await agentTool(hostId, hostSecret, halCmdIdB, halExecB, 'questions_withdraw', {
      ids: [qObsB],
      reason: 'Obsolete: superseded by newer deployment',
    });
    assert.deepEqual(withdrawRes.withdrawn, [qObsB]);
    const qAuditB = await op('setting_changes', { target: 'question:' + qObsB });
    assert.equal(qAuditB.changes.length, 1);
    assert.equal(qAuditB.changes[0].field, 'state');
    assert.equal(qAuditB.changes[0].value, 'withdrawn');

    await hostCall(hostSecret, '/api/hosts/receipts', {
      hostId, commandId: halCmdIdB, executionId: halExecB, status: 'completed',
    });

    // 6. Verify strict two-project isolation across bot_tasks, analytics_query, and spend_summary.
    const tasksAOnly = await op('bot_tasks', { botId: halA });
    const tasksBOnly = await op('bot_tasks', { botId: halB });
    assert.deepEqual(tasksAOnly.tasks.map((t: any) => t.projectId), [PROJ_A]);
    assert.deepEqual(tasksBOnly.tasks.map((t: any) => t.projectId), [PROJ_B]);

    const win = { from: 0, to: Date.now() + 60_000 };
    const aq = await op('analytics_query', {
      window: win,
      dimensions: ['bot', 'project'],
      filters: { bot: [halA, halB] },
      measures: ['executions', 'failed'],
    });
    const buckets = aq.rows.map((r: any) => `${r.dimensions.bot}:${r.dimensions.project}=${r.measures.executions.value}`).sort();
    assert.deepEqual(buckets, [`${halA}:${PROJ_A}=3`, `${halB}:${PROJ_B}=1`].sort());

    const sp = await op('spend_summary', { by: 'bot', window: win });
    const rowA = sp.rows.find((r: any) => r.key === halA);
    const rowB = sp.rows.find((r: any) => r.key === halB);
    assert.equal(rowA?.label, 'HAL0');
    assert.equal(rowA?.runs, 3);
    assert.equal(rowB?.label, 'HAL0');
    assert.equal(rowB?.runs, 1);

    // Confirm HAL0-B recorded its single succeeded run (no split active_skip) and HAL0-A never woke during Project B activity.
    const botRunsB = await op('bot_runs', { botId: halB });
    assert.equal(botRunsB.runs.length, 1, `unexpected botRunsB: ${JSON.stringify(botRunsB.runs)}`);
    assert.equal(botRunsB.runs[0].state, 'succeeded');
    assert.equal((await op('bot_runs', { botId: halA })).runs.length, 2);
  } finally {
    await coordinator.stop();
    await ch.close();
  }
});
