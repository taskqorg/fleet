// Outside tests of dispatch (host side: poll and dispatch, receipts, host events, host and budget operations).
// HTTP only, against FLEET_TRIAL_URL:
//   FLEET_TRIAL_TESTS=tests/go/dispatch.test.ts FLEET_TRIAL_SERVER=go/bin/fleet-coordinator node tests/trial/run.ts
// Each test names its requirement and the risk it guards.
import {test} from 'node:test';
import assert from 'node:assert/strict';
import {randomUUID} from 'node:crypto';
import {readFileSync} from 'node:fs';

const BASE = process.env.FLEET_TRIAL_URL!, SECRET = process.env.FLEET_TRIAL_BROWSER_SECRET!;
const OWNER = 'owner@trial.test';
type Reply = {status: number, body: any};
async function call(method: string, path: string, body?: unknown, headers: Record<string, string> = {}): Promise<Reply> {
  const r = await fetch(BASE + path, {method, headers: {...(body === undefined ? {} : {'content-type': 'application/json'}), ...headers}, body: body === undefined ? undefined : JSON.stringify(body)});
  const text = await r.text(); let parsed: any = text; try { parsed = JSON.parse(text); } catch {}
  return {status: r.status, body: parsed};
}
const owner = {authorization: 'Bearer ' + SECRET, 'x-fleet-person': OWNER};
const ok = (r: Reply) => { assert.equal(r.status, 200, JSON.stringify(r.body)); return r.body; };
const op = (name: string, b: object = {}) => call('POST', '/api/ops/' + name, b, owner);
const secrets = new Map<string, string>();
async function host(id: string) {
  if (!secrets.has(id)) secrets.set(id, ok(await op('hosts_register', {id})).secret);
  return (path: string, body: object) => call('POST', path, body, {authorization: 'Bearer ' + secrets.get(id)});
}
const poll = {platform: 'darwin', capabilities: [], stores: [], problems: []};
async function queued() {
  const {sessionId} = ok(await call('POST', '/api/sessions', {}, owner));
  const e = ok(await call('POST', `/api/sessions/${sessionId}/messages`, {requestId: randomUUID(), text: 'hello'}, owner));
  return {sessionId, executionId: e.id as string};
}
const execution = async (sessionId: string, id: string) => ok(await call('GET', `/api/sessions/${sessionId}`, undefined, owner)).executions.find((x: any) => x.id === id);

test('A1 a paused host takes no work: the execution waits with the skip reason and one stall question, and starts once intake is open (requirement: docs/work-allocation.md holder settings; risk: dispatch ignores a holder setting or never releases its question)', {timeout: 30_000}, async () => {
  const h = await host('trial-host');
  ok(await h('/api/hosts/poll', poll));
  ok(await op('hosts_update', {id: 'trial-host', intake: 'paused'}));
  const {sessionId, executionId} = await queued();
  assert.ok(!ok(await h('/api/hosts/poll', poll)).commands.some((c: any) => c.executionId === executionId), 'no start command while paused');
  assert.deepEqual([(await execution(sessionId, executionId)).waitReason, (await execution(sessionId, executionId)).waitDetail], ['placement', 'trial-host is Paused']);
  const waiting = () => op('questions_list', {state: 'open'}).then(r => ok(r).questions.filter((q: any) => /Work is waiting: trial-host is Paused/.test(q.text)));
  assert.equal((await waiting()).length, 1, 'one open stall question');
  ok(await h('/api/hosts/poll', poll));
  assert.equal((await waiting()).length, 1, 'not asked again');
  ok(await op('hosts_update', {id: 'trial-host', intake: 'all'}));
  const {commands} = ok(await h('/api/hosts/poll', poll));
  assert.ok(commands.some((c: any) => c.executionId === executionId && c.kind === 'start'), 'claimed once open');
  assert.equal((await waiting()).length, 0, 'the question is withdrawn with its cause');
  const {decisions} = ok(await op('route_decisions', {executionId}));
  assert.ok(decisions.at(-1).winner?.host === 'trial-host');
});

test('A2 a reached stop budget stops queued work of its director at the next poll (requirement: fleet.md R3 resources, stop budgets; risk: budgets shown but never enforced)', {timeout: 30_000}, async () => {
  const h = await host('trial-host');
  const b = ok(await op('budget_set', {scope: 'person', scopeId: OWNER, unit: 'wall_ms', limit: 0, action: 'stop'}));
  try {
    const {sessionId, executionId} = await queued();
    assert.ok(!ok(await h('/api/hosts/poll', poll)).commands.some((c: any) => c.executionId === executionId));
    const e = await execution(sessionId, executionId);
    assert.equal(e.state, 'cancelled'); assert.match(e.stopDetail, /budget reached/);
  } finally { ok(await op('budget_delete', {id: b.id})); }
});

test('A3 a start receipt binds the native session; another native id for it is 409; a terminal receipt sets finishedAt once (requirement: docs/execution-contract.md native session; risk: a resumed run attaches to the wrong conversation)', {timeout: 30_000}, async () => {
  const h = await host('trial-host');
  const {sessionId, executionId} = await queued();
  const command = ok(await h('/api/hosts/poll', poll)).commands.find((c: any) => c.executionId === executionId);
  const receipt = (status: string, extra = {}) => h('/api/hosts/receipts', {commandId: command.id, executionId, status, ...extra});
  ok(await receipt('started', {nativeSessionId: 'native-1'}));
  assert.equal(ok(await call('GET', `/api/sessions/${sessionId}`, undefined, owner)).nativeSessionId, 'native-1');
  assert.equal((await receipt('completed', {nativeSessionId: 'native-2'})).status, 409);
  ok(await receipt('completed', {nativeSessionId: 'native-1'}));
  const e = await execution(sessionId, executionId);
  assert.equal(e.state, 'succeeded'); assert.equal(typeof e.finishedAt, 'number');
});

test('A4 an event whose seq another event holds is retained as a blocked transport conflict, visible to transport_inspect (requirement: docs/execution-contract.md event transport; risk: a host resend overwrites or silently drops an event)', {timeout: 30_000}, async () => {
  const h = await host('trial-host');
  const {executionId} = await queued();
  ok(await h('/api/hosts/poll', poll));
  const ev = (id: string, type: string) => ({id, seq: 0, type, timestamp: 1, data: {}});
  ok(await h('/api/hosts/events', {executionId, events: [ev('a', 'resource.observation')]}));
  assert.equal((await h('/api/hosts/events', {executionId, events: [ev('b', 'process.closed')]})).status, 409);
  const r = ok(await op('transport_inspect', {executionId}));
  assert.equal(r.conflicts.length, 1); assert.equal(r.conflicts[0].state, 'blocked'); assert.equal(r.conflicts[0].supported, true);
  assert.equal(r.eventCount, 1);
});

test('A5 stopping work no host has taken settles it as stopped with its stop command (requirement: fleet.md task states; risk: a stop before dispatch leaves the execution open forever)', {timeout: 30_000}, async (t) => {
  // No host may take it: trial-host, the only host registered so far in this file, is polled with intake all, and the
  // dispatch pass woken by the message would otherwise claim it first (the stop then waits for that host). t.after
  // reopens it when an assertion fails before the reopen below.
  const h = await host('trial-host');
  ok(await op('hosts_update', {id: 'trial-host', intake: 'paused'}));
  t.after(async () => { ok(await op('hosts_update', {id: 'trial-host', intake: 'all'})); });
  const {sessionId, executionId} = await queued();
  assert.equal((await execution(sessionId, executionId)).hostId ?? null, null, 'no host took it');
  const c = ok(await call('POST', `/api/executions/${executionId}/stop`, {}, owner));
  assert.equal(c.kind, 'stop'); assert.equal(c.status, 'stopped');
  const e = await execution(sessionId, executionId);
  assert.equal(e.state, 'cancelled'); assert.equal(e.stopDetail, 'stopped before dispatch');
  // The stop is final: once the host is open again, its poll (which waits for the dispatch pass) leaves it unclaimed.
  ok(await op('hosts_update', {id: 'trial-host', intake: 'all'}));
  assert.ok(!ok(await h('/api/hosts/poll', poll)).commands.some((x: any) => x.executionId === executionId), 'no start command for stopped work');
  const after = await execution(sessionId, executionId);
  assert.deepEqual([after.state, after.hostId ?? null], ['cancelled', null]);
});

test('A5b a stop racing the dispatch claim is never overwritten: a stop that settled stays cancelled with no host, otherwise the host gets it (requirement: fleet.md task states; risk: a claim committed beside the stop overwrites the cancelled execution, which then runs)', {timeout: 60_000}, async (t) => {
  const h = await host('trial-host');
  ok(await h('/api/hosts/poll', poll));
  let settled = 0, handed = 0;
  // Batches of stops 0-30 ms after their message, while the woken dispatch pass may claim each for trial-host.
  for (let i = 0; i < 6; i++) {
    const raced = await Promise.all(Array.from({length: 10}, async () => {
      const q = await queued();
      await new Promise(r => setTimeout(r, Math.random() * 30));
      return {...q, stop: ok(await call('POST', `/api/executions/${q.executionId}/stop`, {}, owner))};
    }));
    // A poll returns once the dispatch pass of these messages has ended: a claim that lost the race has committed.
    ok(await h('/api/hosts/poll', poll));
    for (const {sessionId, executionId, stop} of raced) {
      const e = await execution(sessionId, executionId);
      if (stop.status === 'stopped') {
        settled++;
        assert.deepEqual([e.state, e.hostId ?? null], ['cancelled', null], 'a settled stop stays settled');
      } else {
        handed++;
        assert.equal(stop.status, 'pending', 'an unsettled stop waits for the host that has the work');
        assert.equal(stop.hostId, e.hostId); assert.ok(secrets.has(e.hostId), 'a host of this file has it');
        // That host confirms it: no stray claimed work for the later tests.
        ok(await (await host(e.hostId))('/api/hosts/receipts', {commandId: stop.id, executionId, status: 'stopped'}));
        assert.equal((await execution(sessionId, executionId)).state, 'cancelled');
      }
    }
  }
  // Both outcomes depend on timing; the split is reported, not required.
  t.diagnostic(`settled ${settled}, handed to the host ${handed}`);
});

test('task cancellation stops active work and fences later start commands (requirement: Fleet task cancellation and UC3; risk: cancelled tasks keep running or start again)', {timeout: 30_000}, async () => {
  const h = await host('trial-host');
  const projectId = 'hello-world';
  const createTask = async () => ok(await op('task_create', {projectId, title: 'cancel ' + randomUUID(), held: false})).id;
  const start = (taskId?: string, requestId = randomUUID()) =>
    op('session_start', {projectId, profileId: 'pm', taskId, requestId, text: 'work', hostId: 'trial-host'});
  const commands = async () => ok(await h('/api/hosts/poll', poll)).commands;
  const taskId = await createTask();
  const requestId = randomUUID();
  const active = ok(await start(taskId, requestId));
  const startCmd = (await commands()).find((c: any) => c.kind === 'start' && c.executionId === active.executionId);
  assert.ok(startCmd);
  const receipt = (command: any, status: string) =>
    h('/api/hosts/receipts', {commandId: command.id, executionId: command.executionId, status});
  ok(await receipt(startCmd, 'started'));
  const queued = ok(await op('message_send', {sessionId: active.sessionId, requestId: randomUUID(), text: 'queued next turn'}));
  assert.equal(queued.queued, true);
  const pending = ok(await start(taskId));
  const pendingStart = (await commands()).find((c: any) => c.kind === 'start' && c.executionId === pending.executionId);
  assert.ok(pendingStart);
  assert.equal(ok(await op('task_cancel', {taskId})).state, 'cancelled');
  const after = await commands();
  const activeStop = after.find((c: any) => c.kind === 'stop' && c.executionId === active.executionId);
  const pendingStop = after.find((c: any) => c.kind === 'stop' && c.executionId === pending.executionId);
  assert.ok(activeStop, 'cancelling the task requests stop for its running execution');
  assert.ok(pendingStop, 'a previously handed out command could still be running');
  assert.ok(!after.some((c: any) => c.kind === 'start' && c.executionId === pending.executionId));
  assert.equal((await execution(active.sessionId, active.executionId)).state, 'running', 'a stop request is not process proof');
  assert.equal((await start(taskId)).status, 409, 'fresh task-bound start is refused');
  assert.equal(ok(await start(taskId, requestId)).sessionId, active.sessionId, 'idempotent start returns its original session');
  assert.equal((await op('message_send', {sessionId: active.sessionId, requestId: randomUUID(), text: 'late'})).status, 409);
  ok(await receipt(activeStop, 'stopped'));
  ok(await receipt(activeStop, 'stopped'));
  assert.equal((await execution(active.sessionId, active.executionId)).state, 'cancelled');
  const activeSession = ok(await op('session_get', {sessionId: active.sessionId}));
  assert.equal(activeSession.executions.length, 1, 'the queued turn never becomes a second execution');
  ok(await receipt(pendingStart, 'started'));
  const late = await commands();
  assert.ok(late.some((c: any) => c.id === pendingStop.id), 'a late start receipt leaves the stop outstanding');
  ok(await receipt(pendingStop, 'stopped'));
  ok(await receipt(pendingStart, 'completed'));
  assert.equal((await execution(pending.sessionId, pending.executionId)).state, 'cancelled', 'late completion preserves the terminal result');
  const attachedTask = await createTask();
  const attached = ok(await start());
  const attachedStart = (await commands()).find((c: any) => c.kind === 'start' && c.executionId === attached.executionId);
  assert.ok(attachedStart);
  ok(await receipt(attachedStart, 'started'));
  const attachedQueue = ok(await op('message_send', {sessionId: attached.sessionId, requestId: randomUUID(), text: 'before attachment'}));
  ok(await op('session_task_attach', {sessionId: attached.sessionId, taskId: attachedTask}));
  ok(await op('task_cancel', {taskId: attachedTask}));
  const attachedStop = (await commands()).find((c: any) => c.kind === 'stop' && c.executionId === attached.executionId);
  assert.ok(attachedStop, 'cancellation reaches active work created before attachment');
  ok(await receipt(attachedStop, 'stopped'));
  assert.equal(ok(await op('queue_list', {sessionId: attached.sessionId})).queued.find((m: any) => m.id === attachedQueue.id)?.state, 'cancelled');

  const neverDeliveredTask = await createTask();
  const neverDelivered = ok(await start(neverDeliveredTask));
  for (let i = 0; i < 100; i++) {
    if ((await execution(neverDelivered.sessionId, neverDelivered.executionId)).hostId === 'trial-host') break;
    await new Promise(resolve => setTimeout(resolve, 30));
  }
  assert.equal((await execution(neverDelivered.sessionId, neverDelivered.executionId)).hostId, 'trial-host');
  ok(await op('task_cancel', {taskId: neverDeliveredTask}));
  assert.equal((await execution(neverDelivered.sessionId, neverDelivered.executionId)).state, 'cancelled');
  assert.ok(!(await commands()).some((c: any) => c.executionId === neverDelivered.executionId));

  const receiptRaceTask = await createTask();
  const receiptRace = ok(await start(receiptRaceTask));
  const racingStart = (await commands()).find((c: any) => c.kind === 'start' && c.executionId === receiptRace.executionId);
  assert.ok(racingStart);
  const [started, cancelled] = await Promise.all([receipt(racingStart, 'started'), op('task_cancel', {taskId: receiptRaceTask})]);
  ok(started);
  assert.equal(ok(cancelled).state, 'cancelled');
  const racingStop = (await commands()).find((c: any) => c.kind === 'stop' && c.executionId === receiptRace.executionId);
  assert.ok(racingStop, 'first receipt and cancellation retain one stop');
  ok(await receipt(racingStop, 'stopped'));

  for (let i = 0; i < 6; i++) {
    const racing = await createTask();
    const [launch, cancel] = await Promise.all([start(racing), op('task_cancel', {taskId: racing})]);
    assert.equal(ok(cancel).state, 'cancelled');
    assert.ok([200, 409].includes(launch.status), JSON.stringify(launch.body));
    if (launch.status === 200) {
      assert.ok(!(await commands()).some((c: any) => c.kind === 'start' && c.executionId === launch.body.executionId));
    }
  }
});

test('task cancellation stays scoped to its project (requirement: Fleet task cancellation and project grants; risk: a project operator cannot cancel work or can cancel outside their project)', {timeout: 30_000}, async () => {
  const h = await host('trial-host');
  const member = {...owner, 'x-fleet-person': 'member@trial.test'};
  const createTask = async (projectId: string) =>
    ok(await op('task_create', {projectId, title: 'scoped cancel ' + randomUUID(), held: false})).id;
  const start = async (projectId: string, taskId?: string) =>
    ok(await op('session_start', {projectId, profileId: 'pm', taskId, requestId: randomUUID(), text: 'work', hostId: 'trial-host'}));
  const commands = async () => ok(await h('/api/hosts/poll', poll)).commands;
  const receipt = (c: any, status: string) =>
    h('/api/hosts/receipts', {commandId: c.id, executionId: c.executionId, status});
  const cancelAsMember = (taskId: string) => call('POST', '/api/ops/task_update', {taskId, action: 'cancel'}, member);

  const taskId = await createTask('desiign');
  const active = await start('desiign', taskId);
  const c = (await commands()).find((x: any) => x.kind === 'start' && x.executionId === active.executionId);
  assert.ok(c);
  ok(await receipt(c, 'started'));
  const watch = {sessionId: active.sessionId, kinds: ['task'], timerMs: 60_000};
  ok(await op('watch_set', watch));
  const foreign = await createTask('hello-world');
  assert.ok([403, 404].includes((await cancelAsMember(foreign)).status));
  assert.equal(ok(await cancelAsMember(taskId)).state, 'cancelled');
  assert.equal(ok(await op('watch_get', {sessionId: active.sessionId})).watch ?? null, null);
  assert.equal((await op('watch_set', watch)).status, 409);
  const stop = (await commands()).find((x: any) => x.kind === 'stop' && x.executionId === active.executionId);
  assert.ok(stop);
  ok(await receipt(stop, 'stopped'));

  const detached = await start('desiign');
  const pending = (await commands()).find((x: any) => x.kind === 'start' && x.executionId === detached.executionId);
  assert.ok(pending);
  ok(await receipt(pending, 'started'));
  const attach = () => op('session_task_attach', {sessionId: detached.sessionId, taskId});
  assert.equal((await attach()).status, 409, 'active work cannot join a cancelled task');
  ok(await receipt(pending, 'completed'));
  assert.equal(ok(await attach()).taskId, taskId, 'finished history may still join the cancelled task');
});

test('task cancellation fences delegated work after the parent ended (requirement: task cancellation prevents new steps; risk: detached child launches under the cancelled task)', {timeout: 30_000}, async () => {
  const h = await host('trial-host');
  const projectId = 'desiign';
  const profileId = 'cancel-parent-fixture';
  const pm = ok(await op('definition_get', {kind: 'agent', id: 'pm'})).content.agent;
  ok(await op('definition_create', {kind: 'agent', id: profileId, message: 'fixture',
    content: {agent: {...pm, id: profileId, delegationProfiles: ['baseline']}}}));
  const {id: taskId} = ok(await op('task_create', {projectId, title: 'cancel delegated work', held: false}));
  const commands = async () => ok(await h('/api/hosts/poll', poll)).commands;
  const delegate = async (taskId?: string) => {
    const parent = ok(await op('session_start', {projectId, profileId, taskId, requestId: randomUUID(), text: 'work', hostId: 'trial-host'}));
    const cmd = (await commands()).find((c: any) => c.kind === 'start' && c.executionId === parent.executionId);
    assert.ok(cmd);
    const receipt = (status: string) => h('/api/hosts/receipts', {commandId: cmd.id, executionId: cmd.executionId, status});
    ok(await receipt('started'));
    const child = ok(await h('/api/hosts/delegations', {executionId: parent.executionId, commandId: cmd.id,
      delegation: {profileId: 'baseline', requestId: randomUUID(), input: 'fixture'}}));
    assert.ok(child.childExecutionId, JSON.stringify(child));
    ok(await receipt('completed'));
    return {parent, child};
  };
  const {child} = await delegate(taskId);
  assert.equal(ok(await op('task_cancel', {taskId})).state, 'cancelled');
  assert.equal(ok(await op('execution_status', {executionId: child.childExecutionId})).state, 'cancelled');
  assert.ok(!(await commands()).some((c: any) => c.executionId === child.childExecutionId));
  const detached = await delegate();
  const attach = await call('POST', '/api/ops/session_task_attach',
    {sessionId: detached.parent.sessionId, taskId}, {...owner, 'x-fleet-person': 'member@trial.test'});
  assert.equal(attach.status, 409, 'project-scoped attachment sees active coordinator-owned delegates');
  const stop = ok(await op('execution_stop', {executionId: detached.child.childExecutionId}));
  if (stop.status !== 'stopped') {
    ok(await h('/api/hosts/receipts', {commandId: stop.id, executionId: stop.executionId, status: 'stopped'}));
  }
});

test('task cancellation fences an agent-owned pipeline (requirement: Fleet task cancellation; risk: a cancelled task starts a later pipeline step)', {timeout: 30_000}, async () => {
  const h = await host('trial-host');
  const projectId = 'hello-world';
  const pipelineId = 'task-cancellation-fixture';
  const steps = ['first', 'later'].map(id => ({id, kind: 'script', host: 'trial-host', command: 'true'}));
  ok(await op('definition_create', {kind: 'pipeline', id: pipelineId, content: {pipeline: {id: pipelineId, version: 2, steps}}, message: 'test fixture'}));
  const {id: taskId} = ok(await op('task_create', {projectId, title: 'pipeline cancellation', held: false}));
  const active = ok(await op('session_start', {projectId, profileId: 'pm', taskId, requestId: randomUUID(), text: 'work', hostId: 'trial-host'}));
  const commands = async () => ok(await h('/api/hosts/poll', {...poll, capabilities: ['script']})).commands;
  const parent = (await commands()).find((c: any) => c.kind === 'start' && c.executionId === active.executionId);
  assert.ok(parent);
  const receipt = (c: any, status: string) =>
    h('/api/hosts/receipts', {commandId: c.id, executionId: c.executionId, status});
  ok(await receipt(parent, 'started'));
  const reply = ok(await h('/api/hosts/agent-tools', {
    executionId: active.executionId, commandId: parent.id,
    request: {jsonrpc: '2.0', id: 1, method: 'tools/call', params: {name: 'pipeline_run_create',
      arguments: {pipelineId, projectId, requestId: randomUUID()}}}
  }));
  assert.ok(reply.result?.content?.[0]?.text, JSON.stringify(reply));
  const {runId} = JSON.parse(reply.result.content[0].text);
  assert.ok(runId, reply.result.content[0].text);
  let script: any;
  for (let i = 0; i < 80 && !script; i++) {
    script = (await commands()).find((c: any) => c.kind === 'script');
    if (!script) await new Promise(resolve => setTimeout(resolve, 25));
  }
  assert.ok(script, 'the first step was handed out');
  ok(await receipt(script, 'started'));
  ok(await op('task_cancel', {taskId}));
  const cancelled = ok(await op('pipeline_get', {runId}));
  assert.equal(cancelled.state, 'cancelled');
  assert.equal(cancelled.plan.entries[1].activation, 'blocked');
  const after = await commands();
  const stop = after.find((c: any) => c.kind === 'stop' && c.executionId === script.executionId);
  assert.ok(stop, 'the handed-off script receives a stop');
  ok(await receipt(stop, 'stopped'));
  ok(await receipt(script, 'completed'));
  assert.equal(ok(await op('pipeline_get', {runId})).state, 'cancelled');
  assert.ok(!(await commands()).some((c: any) => c.kind === 'script'), 'no later step was handed out');
  const parentStop = after.find((c: any) => c.kind === 'stop' && c.executionId === active.executionId);
  assert.ok(parentStop);
  ok(await receipt(parentStop, 'stopped'));
});

test('A6 hosts: a duplicate id is 409, a host secret never shows, the list carries the last poll (requirement: docs/designs/fleet-people-access.md host secrets; risk: a second host shadows the first or a secret leaks)', {timeout: 30_000}, async () => {
  const h = await host('trial-host');
  assert.equal((await op('hosts_register', {id: 'trial-host'})).status, 409);
  ok(await h('/api/hosts/poll', {...poll, platform: 'linux', capabilities: ['x']}));
  const l = ok(await op('hosts_list'));
  const x = l.hosts.find((y: any) => y.id === 'trial-host');
  assert.equal(x.platform, 'linux'); assert.deepEqual(x.capabilities, ['x']); assert.equal(typeof x.lastSeenAt, 'number');
  assert.ok(!JSON.stringify(l).includes('secretHash'));
});

test('A9 an execution record has one state vocabulary and its real kind and harness: a claude run stores harness claude, a receipt "unknown" keeps the state and marks it unconfirmed (requirement: docs/designs/fleet-data-foundation.md §7 one execution record, C9, C10; risk: every run reported as codex, two state vocabularies disagree)', {timeout: 30_000}, async () => {
  const claude = ok(await call('POST', '/api/sessions', {}, owner)).sessionId;
  ok(await call('POST', `/api/sessions/${claude}/profile`, {profileId: 'claude-worker'}, owner));
  ok(await call('POST', `/api/sessions/${claude}/messages`, {requestId: randomUUID(), text: 'hello'}, owner));
  const [row] = ok(await op('executions_list', {sessionId: claude})).executions;
  assert.deepEqual([row.kind, row.harness, row.state], ['agent_run', 'claude', 'preparing']);
  const h = await host('trial-host');
  const {sessionId, executionId} = await queued();
  const command = ok(await h('/api/hosts/poll', poll)).commands.find((c: any) => c.executionId === executionId);
  ok(await h('/api/hosts/receipts', {commandId: command.id, executionId, status: 'started'}));
  ok(await h('/api/hosts/receipts', {commandId: command.id, executionId, status: 'unknown'}));
  const e = await execution(sessionId, executionId);
  assert.deepEqual([e.state, e.observation], ['running', 'unconfirmed']);
});

test('A9b a run no host will finish ends: executions_abandon fails it with one audit row, and the lost sweep ends an unconfirmed run after FLEET_LOST_AFTER_MS (requirement: Needs you shows only what still needs the owner; risk: zombie runs pile up and keep stuck questions open)', {timeout: 30_000}, async () => {
  const h = await host('trial-host');
  const lost = async () => {
    const {sessionId, executionId} = await queued();
    const command = ok(await h('/api/hosts/poll', poll)).commands.find((c: any) => c.executionId === executionId);
    ok(await h('/api/hosts/receipts', {commandId: command.id, executionId, status: 'started'}));
    ok(await h('/api/hosts/receipts', {commandId: command.id, executionId, status: 'unknown'}));
    return {sessionId, executionId};
  };
  const a = await lost();
  assert.equal((await op('executions_abandon', {ids: [a.executionId], reason: ' '})).status, 400);
  const r = ok(await op('executions_abandon', {ids: [a.executionId, randomUUID()], reason: 'test: host lost track'}));
  assert.deepEqual(r.abandoned, [a.executionId]); assert.equal(r.skipped[0].state, 'unknown');
  const e = await execution(a.sessionId, a.executionId);
  assert.deepEqual([e.state, e.stopDetail], ['failed', 'abandoned']);
  assert.deepEqual(ok(await op('executions_abandon', {ids: [a.executionId], reason: 'again'})).skipped, [{id: a.executionId, state: 'failed'}]);
  const audit = ok(await op('setting_changes', {target: 'execution:' + a.executionId})).changes;
  assert.deepEqual([audit.length, audit[0].previous, audit[0].reason], [1, 'running', 'test: host lost track']);
  // Needs you: an abandoned run needs no one; executions_dismiss takes a finished problem off and keeps it on the record.
  assert.ok(!ok(await op('executions_list', {needsYou: true})).executions.some((x: any) => x.id === a.executionId));
  assert.equal((await op('executions_dismiss', {ids: [a.executionId], reason: ' '})).status, 400);
  const d = ok(await op('executions_dismiss', {ids: [a.executionId, randomUUID()], reason: 'test: seen'}));
  assert.deepEqual(d.dismissed, [a.executionId]); assert.equal(d.skipped[0].state, 'unknown');
  assert.ok(ok(await op('executions_list', {problem: true})).executions.some((x: any) => x.id === a.executionId), 'the problem stays on the record');
  assert.equal(ok(await op('setting_changes', {target: 'execution:' + a.executionId})).changes[0].value, 'dismissed');
  const b = await lost();
  for (let i = 0; i < 100 && (await execution(b.sessionId, b.executionId)).state !== 'failed'; i++) await new Promise(r => setTimeout(r, 200));
  assert.equal((await execution(b.sessionId, b.executionId)).stopDetail, 'abandoned');
});

test('A10 host poll and events decode into the host-protocol types: a field of the wrong type is 400, not read as empty (requirement: contracts/host-protocol.schema.json PollRequest and HostEvents, C1; risk: a renamed or retyped field reaches the coordinator as a missing value)', {timeout: 30_000}, async () => {
  const h = await host('trial-host');
  ok(await h('/api/hosts/poll', poll));
  { const r = await h('/api/hosts/poll', {...poll, capabilities: 'codex'}); assert.equal(r.status, 400, JSON.stringify(r)); }
  assert.equal((await h('/api/hosts/poll', {...poll, version: 7})).status, 400);
  const {executionId} = await queued();
  ok(await h('/api/hosts/poll', poll));
  assert.equal((await h('/api/hosts/events', {executionId, events: [{id: 'a', seq: 1.5, type: 'x', timestamp: 1, data: {}}]})).status, 400);
});

test('A13 every artifact, store, account and agent-tools host route decodes into its host-protocol type: a field of the wrong type is 400 (requirement: contracts/host-protocol.schema.json Artifact*Request, StoresRequest, AgentToolsRequest, C3; risk: the artifact transfer seam has no contract and a retyped field reads as empty)', {timeout: 30_000}, async () => {
  const h = await host('trial-host');
  ok(await h('/api/hosts/poll', poll));
  const {executionId} = await queued();
  ok(await h('/api/hosts/poll', poll));
  // Each of these was read as empty before: the store size, the reserve media type, a list filter, an agent-tools id.
  for (const [route, body] of [['/api/hosts/stores', {store: {id: 's', capacityBytes: 'big'}}],
    ['/api/hosts/artifacts/reserve', {executionId, name: 'a.txt', mediaType: 5, size: 1, sha256: 'x'}],
    ['/api/hosts/artifacts/list', {executionId, filter: {taskId: 5}}],
    ['/api/hosts/agent-tools', {executionId: 7, commandId: 'c', request: {}}]] as const)
    assert.equal((await h(route, body)).status, 400, route);
});

test('A12 receipts have one route per envelope: a project operation on /api/hosts/project-operations, a status on /api/hosts/receipts; a body that is not its envelope is 400 on the split routes, and /api/hosts/receipts takes only a status (requirement: contracts/host-protocol.schema.json ProjectReceipt and Receipt, C4; risk: a stray field picks another handler; a host on the previous release loses its receipts)', {timeout: 30_000}, async () => {
  const h = await host('trial-host');
  ok(await h('/api/hosts/poll', poll));
  const {sessionId} = ok(await op('session_start', {requestId: randomUUID(), text: 'hi', projectId: 'hello-world', profileId: 'pm'}));
  const command = ok(await h('/api/hosts/poll', poll)).commands.find((c: any) => c.sessionId === sessionId && c.kind === 'start');
  const body = (requestId: unknown) => ({commandId: command.id, executionId: command.executionId, delegation: {operation: 'project_context', projectId: 'hello-world', arguments: {}, requestId}});
  assert.equal(ok(await h('/api/hosts/project-operations', body('r1'))).id, 'hello-world');
  assert.equal((await h('/api/hosts/project-operations', body(5))).status, 400, 'requestId must be a string');
  assert.equal((await h('/api/hosts/receipts', body('r2'))).status, 400, 'a project operation is not a status receipt');
  const status = {commandId: command.id, executionId: command.executionId, status: 'started'};
  assert.equal((await h('/api/hosts/project-operations', status)).status, 400, 'a status receipt is not a project operation');
  assert.equal((await h('/api/hosts/delegations', body('r3'))).status, 400, 'a project operation is not a child request');
  assert.equal((await h('/api/hosts/accounts/report', status)).status, 400, 'a status receipt is not an account report');
  ok(await h('/api/hosts/receipts', status));
});

test('A11 the start command payload is the StartPayload contract: named fields only, no blind merge (requirement: contracts/host-protocol.schema.json StartPayload, C7 coordinator side; risk: a dead or overriding payload field reaches the executor)', {timeout: 30_000}, async () => {
  const h = await host('trial-host');
  ok(await h('/api/hosts/poll', poll));
  const {executionId} = await queued();
  const c = ok(await h('/api/hosts/poll', poll)).commands.find((x: any) => x.executionId === executionId);
  const schema = JSON.parse(readFileSync(new URL('../../contracts/host-protocol.schema.json', import.meta.url), 'utf8'));
  const allowed = Object.keys(schema.$defs.StartPayload.properties);
  assert.deepEqual(Object.keys(c.payload).filter(k => !allowed.includes(k)), []);
  assert.deepEqual([c.kind, c.payload.harness, c.payload.text], ['start', 'codex', 'hello']);
});

test('A9 a host without the project repository\'s git connection takes no run of it: the execution waits with the reason, and starts once the host reports the connection (requirement: git connections in each poll, 2026-11-11; risk: a run fails after setup with connection-missing)', async () => {
  const h = await host('owner-mac');
  ok(await h('/api/hosts/poll', {...poll, gitConnections: []}));
  const s = ok(await op('session_start', {requestId: randomUUID(), text: 'hi', projectId: 'cs', profileId: 'pm', hostId: 'owner-mac'}));
  assert.ok(!ok(await h('/api/hosts/poll', {...poll, gitConnections: []})).commands.some((c: any) => c.executionId === s.executionId), 'no start without the connection');
  assert.equal((await execution(s.sessionId, s.executionId)).waitDetail, 'owner-mac has no git connection cs-gitlab');
  const {commands} = ok(await h('/api/hosts/poll', {...poll, gitConnections: ['cs-gitlab']}));
  assert.ok(commands.some((c: any) => c.executionId === s.executionId && c.kind === 'start'), 'starts once the host holds the connection');
});


test('A14 control acknowledgement does not confirm an uncertain execution; only a proven stop settles it (requirement: execution observation is process evidence, not control delivery; risk: accepted then unknown falsely clears uncertainty)', {timeout: 30_000}, async () => {
  const h = await host('trial-host');
  const foreign = await host('foreign-stop-host');
  for (const initiallyUnknown of [true, false]) {
    const {sessionId, executionId} = await queued();
    const start = ok(await h('/api/hosts/poll', poll)).commands.find((c: any) => c.executionId === executionId);
    const receipt = (commandId: string, status: string, id = executionId) => h('/api/hosts/receipts', {commandId, executionId: id, status});
    ok(await receipt(start.id, 'started'));
    if (initiallyUnknown) ok(await receipt(start.id, 'unknown'));
    const stop = ok(await call('POST', `/api/executions/${executionId}/stop`, {}, owner));
    ok(await receipt(stop.id, 'accepted'));
    assert.deepEqual([(await execution(sessionId, executionId)).state, (await execution(sessionId, executionId)).observation], ['running', initiallyUnknown ? 'unconfirmed' : 'confirmed']);
    ok(await receipt(stop.id, 'unknown'));
    ok(await receipt(stop.id, 'unknown'));
    assert.equal((await receipt(stop.id, 'accepted')).status, 409, 'late acknowledgement cannot regress unknown');
    const e = await execution(sessionId, executionId);
    assert.deepEqual([e.state, e.observation, e.finishedAt ?? null], ['running', 'unconfirmed', null]);
    const status = ok(await op('execution_status', {executionId}));
    assert.equal(status.stop.outcome, 'unknown'); assert.equal(status.next, 'session_fork');
    assert.equal(status.observation, 'unconfirmed'); assert.ok(status.reason); assert.equal(status.state, 'running');
    const other = await queued();
    assert.equal((await receipt(stop.id, 'stopped', other.executionId)).status, 403, 'another execution cannot use this command');
    assert.equal((await foreign('/api/hosts/receipts', {commandId: stop.id, executionId, status: 'stopped'})).status, 403, 'another host cannot confirm cleanup');
    ok(await receipt(stop.id, 'stopped'));
    const ended = await execution(sessionId, executionId);
    assert.deepEqual([ended.state, ended.observation], ['cancelled', 'confirmed']);
    const confirmed = ok(await op('execution_status', {executionId}));
    assert.equal(confirmed.stop.outcome, 'proven'); assert.equal(confirmed.observation, 'confirmed'); assert.equal(confirmed.reason, null);
    ok(await receipt(stop.id, 'stopped'));
    assert.equal((await receipt(stop.id, 'unknown')).status, 409);
    assert.equal((await receipt(stop.id, 'accepted')).status, 409);
    assert.equal((await execution(sessionId, executionId)).finishedAt, ended.finishedAt, 'duplicate terminal receipt preserves history');
  }
});


test('A15 control receipts preserve completed execution history and do not confirm an uncertain steered run (requirement: command delivery and execution observation are distinct; risk: late control ACK clears a fence or rewrites completion)', {timeout: 30_000}, async () => {
  const h = await host('trial-host');
  ok(await h('/api/hosts/poll', {...poll, capabilities: ['steer']}));
  const {sessionId, executionId} = await queued();
  const start = ok(await h('/api/hosts/poll', poll)).commands.find((c: any) => c.executionId === executionId);
  const receipt = (id: string, status: string) => h('/api/hosts/receipts', {commandId: id, executionId, status});
  ok(await receipt(start.id, 'started'));
  ok(await h('/api/hosts/poll', {...poll, capabilities: ['steer']}));
  ok(await call('POST', `/api/sessions/${sessionId}/messages`, {requestId: randomUUID(), text: 'steer without proof of process health', delivery: 'steer'}, owner));
  const steer = ok(await h('/api/hosts/poll', poll)).commands.find((c: any) => c.executionId === executionId && c.kind === 'steer');
  assert.ok(steer);
  ok(await receipt(start.id, 'unknown'));
  for (const status of ['accepted', 'unknown', 'completed']) {
    ok(await receipt(steer.id, status));
    assert.deepEqual([(await execution(sessionId, executionId)).state, (await execution(sessionId, executionId)).observation], ['running', 'unconfirmed']);
  }
  const stop = ok(await call('POST', `/api/executions/${executionId}/stop`, {}, owner));
  ok(await receipt(start.id, 'completed'));
  const ended = await execution(sessionId, executionId);
  for (const status of ['accepted', 'unknown']) {
    ok(await receipt(stop.id, status));
    const current = await execution(sessionId, executionId);
    assert.deepEqual([current.state, current.observation, current.finishedAt], ['succeeded', 'confirmed', ended.finishedAt]);
    assert.equal(ok(await op('execution_status', {executionId})).stop.detail, 'already_finished');
  }
});
