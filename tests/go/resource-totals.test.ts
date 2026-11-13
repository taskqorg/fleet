// Outside tests for leaf execution resource totals persistence, token fallback precedence,
// tool_run change suppression, lineage propagation, and directed_totals RLS.
//   FLEET_TRIAL_TESTS=tests/go/resource-totals.test.ts node tests/trial/run.ts
import {test} from 'node:test';
import assert from 'node:assert/strict';
import {createHash, randomUUID} from 'node:crypto';
import {appendFileSync, mkdirSync, mkdtempSync, rmSync, writeFileSync} from 'node:fs';
import {tmpdir} from 'node:os';
import {join} from 'node:path';
import {startCoordinator} from '../harness.ts';

const OWNER = 'owner@trial.test';
const MEMBER = 'member@trial.test';
const OUTSIDER = 'outsider@trial.test';

async function postJSON(url: string, headers: Record<string, string>, body: unknown) {
  const res = await fetch(url, {
    method: 'POST',
    headers: {'content-type': 'application/json', ...headers},
    body: JSON.stringify(body),
  });
  const text = await res.text();
  let parsed: any = text;
  try {
    parsed = JSON.parse(text);
  } catch {}
  return {status: res.status, body: parsed};
}

test('Out-of-order process.closed and terminal receipt converge on execution_totals, and pre-PR1 events record executor_version missing reason (requirement: leaf execution resource summary persistence; risk: reordered ingest drops cpu/memory or overwrites measured metrics)', async () => {
  const c = await startCoordinator({name: 'tot-order'});
  try {
    const call = (path: string, body: unknown, auth = c.secret) =>
      postJSON(c.base + path, {authorization: 'Bearer ' + auth, 'x-fleet-person': OWNER}, body);
    const reg = await call('/api/ops/hosts_register', {id: 'h-tot'});
    assert.equal(reg.status, 200, JSON.stringify(reg.body));
    const hostSecret = reg.body.secret as string;

    const sessRes = await call('/api/ops/session_create', {});
    assert.equal(sessRes.status, 200, JSON.stringify(sessRes.body));
    const sessionId = sessRes.body.sessionId as string;

    // Seed three running executions with queued start commands so receipts can settle them.
    const startedMs = Date.now() - 5000;
    const finishedMs = startedMs + 2500;
    c.sql(`INSERT INTO execution(id,kind,state,observation,project_id,session_id,host_id,harness,started_at,directed_by,data) VALUES
      ('ex-pc-first','agent_run','running','confirmed',NULL,'${sessionId}','h-tot','claude',to_timestamp(${startedMs}/1000.0),'${OWNER}',jsonb_build_object('id','ex-pc-first','sessionId','${sessionId}','hostId','h-tot','startedAt',${startedMs})),
      ('ex-rc-first','agent_run','running','confirmed',NULL,'${sessionId}','h-tot','claude',to_timestamp(${startedMs}/1000.0),'${OWNER}',jsonb_build_object('id','ex-rc-first','sessionId','${sessionId}','hostId','h-tot','startedAt',${startedMs})),
      ('ex-pre-pr1','agent_run','running','confirmed',NULL,'${sessionId}','h-tot','claude',to_timestamp(${startedMs}/1000.0),'${OWNER}',jsonb_build_object('id','ex-pre-pr1','sessionId','${sessionId}','hostId','h-tot','startedAt',${startedMs}));
      INSERT INTO document(kind,id,project_id,data) VALUES
      ('command','cmd-pc-first',NULL,jsonb_build_object('id','cmd-pc-first','kind','start','executionId','ex-pc-first','sessionId','${sessionId}','hostId','h-tot','status','started')),
      ('command','cmd-rc-first',NULL,jsonb_build_object('id','cmd-rc-first','kind','start','executionId','ex-rc-first','sessionId','${sessionId}','hostId','h-tot','status','started')),
      ('command','cmd-pre-pr1',NULL,jsonb_build_object('id','cmd-pre-pr1','kind','start','executionId','ex-pre-pr1','sessionId','${sessionId}','hostId','h-tot','status','started'));`);

    const summary = {
      cpuMs: 420,
      peakMemoryBytes: 104857600,
      activeMs: 900,
      turnCount: 3,
      toolCalls: {Bash: {calls: 2, errors: 1}},
      toolCallsOther: {calls: 1, errors: 0},
    };
    const sendClosed = (exId: string, resources?: unknown) =>
      postJSON(
        c.base + '/api/hosts/events',
        {authorization: 'Bearer ' + hostSecret},
        {
          hostId: 'h-tot',
          executionId: exId,
          events: [
            {
              id: 'ev-pc-' + exId,
              seq: 1,
              type: 'process.closed',
              timestamp: finishedMs,
              data: resources ? {exitCode: 0, resources} : {exitCode: 0},
            },
          ],
        },
      );
    const sendReceipt = (cmdId: string, exId: string) =>
      postJSON(
        c.base + '/api/hosts/receipts',
        {authorization: 'Bearer ' + hostSecret},
        {
          commandId: cmdId,
          hostId: 'h-tot',
          executionId: exId,
          status: 'completed',
        },
      );

    // Order A: process.closed BEFORE terminal receipt.
    assert.equal((await sendClosed('ex-pc-first', summary)).status, 200);
    const rA = await sendReceipt('cmd-pc-first', 'ex-pc-first');
    assert.equal(rA.status, 200, JSON.stringify(rA.body));

    // Order B: terminal receipt BEFORE process.closed.
    const rB = await sendReceipt('cmd-rc-first', 'ex-rc-first');
    assert.equal(rB.status, 200, JSON.stringify(rB.body));
    assert.equal((await sendClosed('ex-rc-first', summary)).status, 200);

    // Pre-PR1 process.closed without resources.
    assert.equal((await sendClosed('ex-pre-pr1')).status, 200);
    const rPre = await sendReceipt('cmd-pre-pr1', 'ex-pre-pr1');
    assert.equal(rPre.status, 200, JSON.stringify(rPre.body));

    const readTotals = (exId: string) =>
      JSON.parse(
        c.sql(`SELECT row_to_json(r)::text FROM (
          SELECT wall_ms, wall_ms_basis, active_ms, active_ms_basis, cpu_ms, cpu_ms_basis,
                 peak_memory_bytes, peak_memory_bytes_basis, turn_count, turn_count_basis,
                 tool_calls, tool_calls_basis, cpu_missing_reason, memory_missing_reason, billing_basis
          FROM execution_totals WHERE execution_id='${exId}'
        ) r`).trim(),
      );

    const a = readTotals('ex-pc-first');
    const b = readTotals('ex-rc-first');
    assert.ok(a.wall_ms >= 4500 && a.wall_ms < 30000, `unexpected wall_ms ${a.wall_ms}`);
    assert.ok(b.wall_ms >= 4500 && b.wall_ms < 30000, `unexpected wall_ms ${b.wall_ms}`);
    assert.deepEqual({...a, wall_ms: 0}, {...b, wall_ms: 0}, 'process.closed before vs after receipt must converge to identical execution_totals');
    assert.deepEqual(
      [a.wall_ms_basis, a.active_ms, a.active_ms_basis, a.cpu_ms, a.cpu_ms_basis, a.peak_memory_bytes, a.turn_count, a.tool_calls_basis, a.billing_basis],
      ['measured', 900, 'measured', 420, 'measured', 104857600, 3, 'measured', 'metered'],
    );
    assert.deepEqual(a.tool_calls, {byTool: {Bash: {calls: 2, errors: 1}}, other: {calls: 1, errors: 0}});
    assert.equal(a.cpu_missing_reason, null);
    assert.equal(a.memory_missing_reason, null);

    const pre = readTotals('ex-pre-pr1');
    assert.equal(pre.cpu_ms, null);
    assert.equal(pre.cpu_ms_basis, 'unavailable');
    assert.equal(pre.cpu_missing_reason, 'executor_version');
    assert.equal(pre.memory_missing_reason, 'executor_version');
  } finally {
    await c.stop();
  }
});

test('3-tier fleet_write_tokens precedence deduplicates interrupted Claude assistant usage and yields to result and usage.observation (requirement: PR 2 token precedence; risk: interrupted Claude runs lose tokens or finished runs double-count assistant and result)', async () => {
  const c = await startCoordinator({name: 'tot-tok3'});
  try {
    c.sql(`INSERT INTO execution(id,kind,state) VALUES ('t-tier3','agent_run','failed'),('t-tier2','agent_run','succeeded');
      INSERT INTO execution_totals(execution_id) VALUES ('t-tier3'),('t-tier2');
      INSERT INTO document(kind,id,data) VALUES
      -- t-tier3: no result/usage.observation, two partial frames of msg-1 (latest wins) + one frame of msg-2
      ('event','ev-m1a',jsonb_build_object('id','ev-m1a','executionId','t-tier3','seq',1,'data',jsonb_build_object('type','assistant','message',jsonb_build_object('id','msg-1','usage',jsonb_build_object('input_tokens',10,'output_tokens',5))))),
      ('event','ev-m1b',jsonb_build_object('id','ev-m1b','executionId','t-tier3','seq',2,'data',jsonb_build_object('type','assistant','message',jsonb_build_object('id','msg-1','usage',jsonb_build_object('input_tokens',10,'output_tokens',25,'cache_read_input_tokens',40))))),
      ('event','ev-m2',jsonb_build_object('id','ev-m2','executionId','t-tier3','seq',3,'data',jsonb_build_object('type','assistant','message',jsonb_build_object('id','msg-2','usage',jsonb_build_object('input_tokens',7,'output_tokens',12,'cache_creation_input_tokens',100))))),
      -- t-tier2: has assistant events AND result event; result must win without summing assistant messages
      ('event','ev-2a',jsonb_build_object('id','ev-2a','executionId','t-tier2','seq',1,'data',jsonb_build_object('type','assistant','message',jsonb_build_object('id','msg-9','usage',jsonb_build_object('input_tokens',500,'output_tokens',500))))),
      ('event','ev-2r',jsonb_build_object('id','ev-2r','executionId','t-tier2','seq',2,'data',jsonb_build_object('fleetNormalized',jsonb_build_object('lifecycle','result','nativeUsage',jsonb_build_object('input_tokens',11,'output_tokens',22,'cache_read_input_tokens',33,'cache_creation_input_tokens',44)))));
      SELECT fleet_write_tokens(id) FROM execution;`);

    const row = (id: string) =>
      c.sql(`SELECT concat_ws(',',tokens_input,tokens_output,tokens_cache_read,tokens_cache_write,tokens_input_basis) FROM execution_totals WHERE execution_id='${id}'`).trim();
    assert.equal(row('t-tier3'), '17,37,40,100,measured', 'deduplicated msg-1 (10,25,40) + msg-2 (7,12,0,100)');
    assert.equal(row('t-tier2'), '11,22,33,44,measured', 'Tier 2 result overrides Tier 3 assistant frames');

    // Now add a Tier 1 usage.observation event to t-tier2: Tier 1 must override Tier 2.
    c.sql(`INSERT INTO document(kind,id,data) VALUES
      ('event','ev-2obs',jsonb_build_object('id','ev-2obs','executionId','t-tier2','seq',3,'data',jsonb_build_object('fleetNormalized',jsonb_build_object('lifecycle','usage.observation','nativeUsage',jsonb_build_object('last',jsonb_build_object('inputTokens',90,'cachedInputTokens',20,'outputTokens',9),'total',jsonb_build_object('inputTokens',9999))))));
      SELECT fleet_write_tokens('t-tier2');`);
    assert.equal(row('t-tier2'), '70,9,20,measured', 'Tier 1 usage.observation overrides Tier 2 result');
  } finally {
    await c.stop();
  }
});

test('Container exclusions, task_view.totals, tool_run change suppression, connRecord lineage, task created_by_* columns, and directed_totals RLS with revocation (requirement: PR 2 leaf accounting & RLS; risk: double counting, change flooding, or revoked director leak)', async () => {
  const dir = mkdtempSync(join(tmpdir(), 'fleet-pr2-gw-'));
  const gwDir = join(dir, 'gateway');
  const secDir = join(dir, 'secret');
  mkdirSync(join(gwDir, 'state', 'spool'), {recursive: true});
  mkdirSync(secDir, {recursive: true});

  const c = await startCoordinator({
    name: 'tot-rls',
    env: {
      FLEET_GATEWAY_DIR: gwDir,
      FLEET_SECRET_DIR: secDir,
      FLEET_CONNECTION_TICK_MS: '120',
    },
  });
  try {
    const op = (name: string, body: unknown, person = OWNER) =>
      postJSON(c.base + '/api/ops/' + name, {authorization: 'Bearer ' + c.secret, 'x-fleet-person': person}, body);

    // 1. Seed a projectless session + parent agent_run owned by MEMBER so we test created_by_execution_id and connRecord lineage.
    const memSess = await op('session_create', {}, MEMBER);
    assert.equal(memSess.status, 200, JSON.stringify(memSess.body));
    const memSessId = memSess.body.sessionId as string;

    const taskRes = await op(
      'task_create',
      {projectId: 'desiign', title: 'Leaf accounting task'},
      OWNER,
    );
    assert.equal(taskRes.status, 200, JSON.stringify(taskRes.body));
    const taskId = taskRes.body.id as string;

    c.sql(`INSERT INTO execution(id,kind,state,project_id,parent_execution_id,session_id,task_id,host_id,pipeline_id,pipeline_revision,step_id,profile_id,profile_revision,directed_by,data) VALUES
      ('p-run-1','pipeline_run','succeeded','desiign',NULL,'${memSessId}','${taskId}',NULL,'release-pipe',4,NULL,NULL,NULL,'${OWNER}','{}'::jsonb),
      ('p-step-1','pipeline_step','succeeded','desiign','p-run-1','${memSessId}','${taskId}',NULL,'release-pipe',4,'gen',NULL,NULL,'${OWNER}','{}'::jsonb),
      ('leaf-agent-1','agent_run','running','desiign','p-step-1','${memSessId}','${taskId}','h-task','release-pipe',4,'gen','coder',9,'${OWNER}',jsonb_build_object('id','leaf-agent-1','hostId','h-task','sessionId','${memSessId}','projectId','desiign','directedBy','${OWNER}')),
      ('projless-agent','agent_run','succeeded',NULL,NULL,'${memSessId}',NULL,NULL,'release-pipe',2,'plan','pm',3,'${MEMBER}','{}'::jsonb);
      INSERT INTO document(kind,id,project_id,data) VALUES
      ('command','cmd-leaf-1','desiign',jsonb_build_object('id','cmd-leaf-1','kind','start','executionId','leaf-agent-1','sessionId','${memSessId}','hostId','h-task','status','started'));
      INSERT INTO execution_totals(execution_id,project_id,directed_by,billing_basis,tokens_input,wall_ms,wall_ms_basis,cpu_ms,cpu_ms_basis,peak_memory_bytes,peak_memory_bytes_basis,turn_count,turn_count_basis) VALUES
      ('leaf-agent-1','desiign','${OWNER}','metered',50,1200,'measured',300,'measured',2048,'measured',2,'measured'),
      ('projless-agent',NULL,'${MEMBER}','metered',88,600,'measured',100,'measured',1024,'measured',1,'measured');`);

    // Task created by execution:'leaf-agent-1' via /api/hosts/agent-tools populates created_by_execution_id and created_by_session_id.
    const reg = await op('hosts_register', {id: 'h-task'});
    assert.equal(reg.status, 200);
    const hostSecret = reg.body.secret as string;
    const hTaskRes = await postJSON(
      c.base + '/api/hosts/agent-tools',
      {authorization: 'Bearer ' + hostSecret},
      {
        hostId: 'h-task',
        commandId: 'cmd-leaf-1',
        executionId: 'leaf-agent-1',
        request: {jsonrpc: '2.0', id: 1, method: 'tools/call', params: {name: 'task_create', arguments: {projectId: 'desiign', title: 'Subtask from agent'}}},
      },
    );
    assert.equal(hTaskRes.status, 200, JSON.stringify(hTaskRes.body));
    const subTaskId = JSON.parse(hTaskRes.body.result.content[0].text).id as string;
    assert.ok(subTaskId, JSON.stringify(hTaskRes.body));
    assert.equal(
      c.sql(`SELECT created_by || '|' || created_by_execution_id || '|' || created_by_session_id FROM task WHERE id='${subTaskId}'`).trim(),
      `execution:leaf-agent-1|leaf-agent-1|${memSessId}`,
    );

    // Spool a gateway tool_run under leaf-agent-1 (linked to taskId) and check task_view.totals excludes container & tool_run rows.
    const spoolFile = join(gwDir, 'state', 'spool', new Date().toISOString().slice(0, 10) + '.jsonl');
    const taskTokSha = createHash('sha256').update('tok-task-' + randomUUID()).digest('hex');
    const taskCallId = 'call-task-' + randomUUID().slice(0, 8);
    const taskCallStart = Date.now() - 250;
    c.sql(`INSERT INTO document(kind,id,project_id,data) VALUES
      ('run-token','run-token:${taskTokSha}','desiign',jsonb_build_object('id','run-token:${taskTokSha}','tokenSha256','${taskTokSha}','executionId','leaf-agent-1','directedBy','${OWNER}','projectId','desiign'));`);
    writeFileSync(
      spoolFile,
      JSON.stringify({
        callId: taskCallId,
        tokenSha256: taskTokSha,
        executionId: 'leaf-agent-1',
        connection: 'openai',
        tool: 'responses',
        status: 200,
        outcome: 'ok',
        keyFingerprint: 'fp-task',
        usage: {unit: 'credits', amount: 1},
        startedAt: taskCallStart,
        ms: 210,
      }) + '\n',
    );
    for (let i = 0; i < 40; i++) {
      if (c.sql(`SELECT id FROM execution WHERE id='call:${taskCallId}'`).trim()) break;
      await new Promise(r => setTimeout(r, 80));
    }
    assert.equal(c.sql(`SELECT wall_ms||','||active_ms FROM execution_totals WHERE execution_id='call:${taskCallId}'`).trim(), '210,210');

    assert.equal(c.sql(`SELECT count(*) FROM execution_totals WHERE execution_id IN ('p-run-1','p-step-1')`).trim(), '0');
    const tv = JSON.parse(c.sql(`SELECT totals::text FROM task_view WHERE task_id='${taskId}'`).trim());
    assert.equal(tv.tokens_input, 50);
    assert.equal(tv.wall_ms, 1200);
    assert.equal(tv.turn_count, 2);
    assert.equal(tv.peak_memory_bytes, 2048);
    assert.equal(tv.measured_wall, 1);
    assert.equal(tv.measured_cpu, 1);
    assert.equal(tv.executions_with_totals, 1);

    // 2. Spool a gateway call for MEMBER's projless-agent and verify connIngest creates tool_run with inherited lineage + 0 change rows.
    const rawToken = 'tok-member-' + randomUUID();
    const tokenSha = createHash('sha256').update(rawToken).digest('hex');
    const startedMs = Date.now() - 300;
    c.sql(`INSERT INTO document(kind,id,project_id,data) VALUES
      ('run-token','run-token:${tokenSha}',NULL,jsonb_build_object('id','run-token:${tokenSha}','tokenSha256','${tokenSha}','executionId','projless-agent','directedBy','${MEMBER}','projectId',NULL));`);
    const callId = 'call-' + randomUUID().slice(0, 8);
    appendFileSync(
      spoolFile,
      JSON.stringify({
        callId,
        tokenSha256: tokenSha,
        executionId: 'projless-agent',
        connection: 'openai',
        tool: 'responses',
        status: 200,
        outcome: 'ok',
        keyFingerprint: 'fp-1',
        usage: {unit: 'credits', amount: 2},
        startedAt: startedMs,
        ms: 140,
      }) + '\n',
    );
    // Personal token (session_id IS NULL) also tested via SQL row + RLS below.
    let toolRow = '';
    for (let i = 0; i < 40; i++) {
      toolRow = c.sql(`SELECT coalesce(id,'')||'|'||coalesce(task_id,'')||'|'||coalesce(session_id,'')||'|'||coalesce(directed_by,'')||'|'||coalesce(pipeline_id,'')||'|'||coalesce(pipeline_revision::text,'')||'|'||coalesce(step_id,'')||'|'||coalesce(profile_id,'')||'|'||coalesce(profile_revision::text,'') FROM execution WHERE id='call:${callId}'`).trim();
      if (toolRow) break;
      await new Promise(r => setTimeout(r, 80));
    }
    assert.equal(
      toolRow,
      `call:${callId}||${memSessId}|${MEMBER}|release-pipe|2|plan|pm|3`,
      'connRecord must inherit session_id, directed_by, pipeline/step/profile scalars from parent execution',
    );
    const toolTotals = c.sql(`SELECT billing_basis||','||wall_ms||','||active_ms||','||connection_cost_basis FROM execution_totals WHERE execution_id='call:${callId}'`).trim();
    assert.equal(toolTotals, 'metered,140,140,measured');
    const changeRows = c.sql(`SELECT count(*) FROM change WHERE id='call:${callId}'`).trim();
    assert.equal(changeRows, '0', 'fleet_change() must suppress change rows for tool_run on execution and execution_totals');

    // 3. Verify directed_totals RLS as MEMBER vs OUTSIDER, plus revocation via people_set removal & re-add (including personal run-token).
    const personalSha = createHash('sha256').update('tok-personal-' + randomUUID()).digest('hex');
    c.sql(`INSERT INTO document(kind,id,project_id,data) VALUES
      ('run-token','run-token:${personalSha}',NULL,jsonb_build_object('id','run-token:${personalSha}','tokenSha256','${personalSha}','personal','cli','executionId','personal-holder','directedBy','${MEMBER}','projectId',NULL));
      INSERT INTO execution(id,kind,state,project_id,session_id,directed_by) VALUES ('personal-tr','tool_run','succeeded',NULL,NULL,'${MEMBER}');
      INSERT INTO execution_totals(execution_id,project_id,directed_by,billing_basis,wall_ms,wall_ms_basis) VALUES ('personal-tr',NULL,'${MEMBER}','metered',50,'measured');`);

    const postRevokeCallId = 'call-post-revoke-' + randomUUID().slice(0, 8);
    const countAsApp = (person: string) => {
      const out = c.sql(`BEGIN; SELECT set_config('fleet.project_ids','',true); SELECT set_config('fleet.person','${person}',true); SELECT count(*) AS n FROM execution_totals WHERE execution_id IN ('projless-agent','call:${callId}','personal-tr','call:${postRevokeCallId}'); COMMIT;`).trim();
      const lines = out.split('\n').filter(Boolean);
      return Number(lines[lines.length - 1]);
    };
    assert.equal(countAsApp(MEMBER), 3, 'member sees session-backed and personal-token projectless execution_totals');
    assert.equal(countAsApp(OUTSIDER), 0, 'outsider must see 0 projectless execution_totals');

    // Remove MEMBER then re-add via people_set -> revokes run-token and projectless executions/totals.
    const people = (await op('people_list', {}, OWNER)).body.people;
    assert.equal((await op('people_set', {people: people.filter((p: any) => p.email !== MEMBER)}, OWNER)).status, 200);
    assert.equal(
      c.sql(`SELECT ((data ? 'revokedAt') AND (data ? 'directorAccessRevokedAt'))::text FROM document WHERE id='run-token:${personalSha}'`).trim(),
      'true',
      'people_set removal must set revokedAt and directorAccessRevokedAt on personal run-token',
    );
    assert.equal((await op('people_set', {people}, OWNER)).status, 200);
    appendFileSync(
      spoolFile,
      JSON.stringify({
        callId: postRevokeCallId,
        tokenSha256: personalSha,
        executionId: 'personal-holder',
        connection: 'openai',
        tool: 'responses',
        status: 200,
        outcome: 'ok',
        keyFingerprint: 'fp-personal',
        usage: {unit: 'credits', amount: 1},
        startedAt: Date.now() - 100,
        ms: 60,
      }) + '\n',
    );
    let postRevokeRow = '';
    for (let i = 0; i < 40; i++) {
      postRevokeRow = c.sql(`SELECT coalesce(data->>'directorAccessRevokedAt','') FROM execution WHERE id='call:${postRevokeCallId}'`).trim();
      if (postRevokeRow) break;
      await new Promise(r => setTimeout(r, 80));
    }
    assert.ok(postRevokeRow, 'post-removal spooled tool_run must be ingested with directorAccessRevokedAt');
    assert.equal(countAsApp(MEMBER), 0, 're-added member must not regain revoked prior or post-removal personal execution_totals');
  } finally {
    await c.stop();
    rmSync(dir, {recursive: true, force: true});
  }
});
