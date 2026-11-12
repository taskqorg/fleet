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

test('A5 stopping work no host has taken settles it as stopped with its stop command (requirement: fleet.md task states; risk: a stop before dispatch leaves the execution open forever)', {timeout: 30_000}, async () => {
  const {sessionId, executionId} = await queued();
  const c = ok(await call('POST', `/api/executions/${executionId}/stop`, {}, owner));
  assert.equal(c.kind, 'stop'); assert.equal(c.status, 'stopped');
  const e = await execution(sessionId, executionId);
  assert.equal(e.state, 'cancelled'); assert.equal(e.stopDetail, 'stopped before dispatch');
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
  const s = ok(await op('session_start', {requestId: randomUUID(), text: 'hi', projectId: 'fleet-hello-world', profileId: 'pm', hostId: 'owner-mac'}));
  assert.ok(!ok(await h('/api/hosts/poll', {...poll, gitConnections: []})).commands.some((c: any) => c.executionId === s.executionId), 'no start without the connection');
  assert.equal((await execution(s.sessionId, s.executionId)).waitDetail, 'owner-mac has no git connection fleet-hello-world');
  const {commands} = ok(await h('/api/hosts/poll', {...poll, gitConnections: ['fleet-hello-world']}));
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
    const other = await queued();
    assert.equal((await receipt(stop.id, 'stopped', other.executionId)).status, 403, 'another execution cannot use this command');
    assert.equal((await foreign('/api/hosts/receipts', {commandId: stop.id, executionId, status: 'stopped'})).status, 403, 'another host cannot confirm cleanup');
    ok(await receipt(stop.id, 'stopped'));
    const ended = await execution(sessionId, executionId);
    assert.deepEqual([ended.state, ended.observation], ['cancelled', 'confirmed']);
    assert.equal(ok(await op('execution_status', {executionId})).stop.outcome, 'proven');
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
