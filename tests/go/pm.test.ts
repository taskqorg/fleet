// Outside tests of the PM agent (docs/designs/fleet-pm-agent.md): agent calls run with the director's rights, focus,
// task tools, audit and watch. HTTP only: an agent call is driven as its host, through /api/hosts/agent-tools with the
// start command of the calling execution.
//   FLEET_TRIAL_TESTS=tests/go/pm.test.ts node tests/trial/run.ts
// Each test names its requirement and the risk it guards.
import {test} from 'node:test';
import assert from 'node:assert/strict';
import {randomUUID} from 'node:crypto';

const BASE = process.env.FLEET_TRIAL_URL!, SECRET = process.env.FLEET_TRIAL_BROWSER_SECRET!;
const OWNER = 'owner@trial.test', MEMBER = 'member@trial.test';
const PROJECT = 'fleet-hello-world', OTHER = 'hello-world';
type Reply = {status: number, body: any};
async function call(method: string, path: string, body?: unknown, headers: Record<string, string> = {}): Promise<Reply> {
  const r = await fetch(BASE + path, {method, headers: {...(body === undefined ? {} : {'content-type': 'application/json'}), ...headers}, body: body === undefined ? undefined : JSON.stringify(body)});
  const text = await r.text(); let parsed: any = text; try { parsed = JSON.parse(text); } catch {}
  return {status: r.status, body: parsed};
}
const as = (person: string) => ({authorization: 'Bearer ' + SECRET, 'x-fleet-person': person});
const ok = (r: Reply) => { assert.equal(r.status, 200, JSON.stringify(r.body)); return r.body; };
const op = (name: string, b: object = {}, person = OWNER) => call('POST', '/api/ops/' + name, b, as(person));
const poll = {platform: 'darwin', capabilities: [], stores: [], problems: []};
// One host per director, held by that person: work directed by a person runs on a login they hold (people-access §6).
const hosts = new Map<string, {id: string, secret: string}>();
const hostCall = (person: string, path: string, body: object) => call('POST', path, body, {authorization: 'Bearer ' + hosts.get(person)!.secret});
async function hostOf(person: string) {
  if (!hosts.has(person)) {
    const id = person === OWNER ? 'trial-host' : 'host-' + person.split('@')[0];
    hosts.set(person, {id, secret: ok(await op('hosts_register', {id, holder: person})).secret});
    ok(await hostCall(person, '/api/hosts/poll', poll));
  }
  return hosts.get(person)!.id;
}

// agent: a running execution of a new session directed by person, and a caller of its agent tools.
async function agent(person: string, start: object) {
  const hostId = await hostOf(person);
  let s: any;
  if ('projectId' in start) s = ok(await op('session_start', {requestId: randomUUID(), text: 'pm turn', ...start}, person));
  else { // a session with no project: the person's own create, the PM profile, a first message
    const sessionId = ok(await call('POST', '/api/sessions', {}, as(person))).sessionId;
    ok(await call('POST', `/api/sessions/${sessionId}/profile`, {profileId: 'pm'}, as(person)));
    s = {sessionId, executionId: ok(await call('POST', `/api/sessions/${sessionId}/messages`, {requestId: randomUUID(), text: 'pm turn'}, as(person))).id};
  }
  const {commands} = ok(await hostCall(person, '/api/hosts/poll', poll));
  const c = commands.find((x: any) => x.executionId === s.executionId && x.kind === 'start');
  assert.ok(c, 'the host claims the start command');
  const rpc = (request: object) => hostCall(person, '/api/hosts/agent-tools', {hostId, executionId: s.executionId, commandId: c.id, request: {jsonrpc: '2.0', id: 1, ...request}});
  const tool = async (name: string, args: object = {}): Promise<Reply> => {
    const r = await rpc({method: 'tools/call', params: {name, arguments: args}});
    return r.status === 200 ? {status: 200, body: JSON.parse(r.body.result.content[0].text)} : r;
  };
  const list = async () => ok(await rpc({method: 'tools/list'})).result.tools.map((t: any) => t.name) as string[];
  return {sessionId: s.sessionId as string, executionId: s.executionId as string, tool, list};
}

test('PM1 rights: an agent call reads and writes with its director\'s grants, never the coordinator\'s * (risk: a member\'s PM sees or changes other projects)', {timeout: 30_000}, async () => {
  const mine = ok(await op('task_create', {projectId: PROJECT, title: 'PM1 mine ' + randomUUID()}));
  const other = ok(await op('task_create', {projectId: OTHER, title: 'PM1 other ' + randomUUID()}));
  const pm = await agent(MEMBER, {projectId: PROJECT, role: 'pm'});
  const ids = ok(await pm.tool('task_page', {limit: 500})).tasks.map((t: any) => t.id);
  assert.ok(ids.includes(mine.id) && !ids.includes(other.id), 'row-level security of the director');
  assert.equal((await pm.tool('task_get', {taskId: other.id})).status, 404);
  assert.equal((await pm.tool('task_create', {projectId: OTHER, title: 'x'})).status, 404, 'project outside the grants');
  assert.equal((await pm.tool('profiles_list')).status, 400, 'architect operations are not offered to an operator director');
  const names = await pm.list();
  assert.ok(names.includes('task_page') && names.includes('focus_set') && !names.includes('profiles_list') && !names.includes('task_take'));
});

test('PM2 deny list: take and host secrets stay with people, even for the owner\'s PM (risk: an injected owner PM mints a host secret)', {timeout: 30_000}, async () => {
  const pm = await agent(OWNER, {projectId: PROJECT, role: 'pm'});
  const t = ok(await op('task_create', {projectId: PROJECT, title: 'PM2 ' + randomUUID()}));
  for (const [name, args] of [['task_take', {taskId: t.id}], ['hosts_register', {id: 'pm2-host'}], ['hosts_rotate_secret', {id: 'trial-host'}], ['connections_token_create', {name: 'pm2'}]] as const)
    assert.equal((await pm.tool(name, args)).status, 403, name);
  assert.equal(ok(await pm.tool('profiles_list')).profiles !== undefined, true, 'the owner\'s PM reaches owner operations');
});

test('PM3 focus: a focused PM starts workers in its focus projects only, and focus never exceeds the grants (risk: the one-project lock blocks a PM, or focus widens rights)', {timeout: 30_000}, async () => {
  const pm = await agent(MEMBER, {projectId: PROJECT, role: 'pm'});
  assert.equal((await pm.tool('focus_set', {projectIds: [OTHER]})).status, 404, 'outside the grants');
  ok(await pm.tool('focus_set', {projectIds: [PROJECT]}));
  const child = ok(await pm.tool('session_start', {requestId: randomUUID(), role: 'worker'}));
  assert.equal(child.projectId, PROJECT, 'one focus project is the default');
  const s = ok(await call('GET', '/api/sessions/' + child.sessionId, undefined, as(OWNER)));
  assert.equal(s.directedBy, MEMBER, 'the child keeps the PM\'s director');
  ok(await pm.tool('focus_set', {projectIds: '*'}));
  assert.equal((await pm.tool('session_start', {requestId: randomUUID(), role: 'worker'})).status, 400, 'several projects: projectId required');
  assert.equal((await pm.tool('session_start', {requestId: randomUUID(), role: 'worker', projectId: OTHER})).status, 404, '"*" is the grants, not every project');
});

test('PM4 no project: the owner\'s PM without a project focuses on every project and starts workers where asked (risk: a projectless PM gets 403 from the worker lock)', {timeout: 30_000}, async () => {
  const pm = await agent(OWNER, {});
  assert.equal((await pm.tool('session_start', {requestId: randomUUID(), role: 'worker', projectId: PROJECT})).status, 403, 'no focus: the worker lock answers');
  ok(await pm.tool('focus_set', {projectIds: '*'}));
  assert.equal(ok(await pm.tool('session_start', {requestId: randomUUID(), role: 'worker', projectId: PROJECT})).projectId, PROJECT);
  const all = ok(await pm.tool('task_page', {limit: 500})).tasks.map((t: any) => t.projectId);
  assert.ok(all.includes(PROJECT) && all.includes(OTHER), 'focus "*" of the owner lists every project');
});

test('PM5 Held: an agent task starts Held with the execution as creator, and leaves Held only by release (risk: an agent task runs before the person agrees, or a person\'s pause is undone by an agent)', {timeout: 30_000}, async () => {
  const pm = await agent(MEMBER, {projectId: PROJECT, role: 'pm'});
  ok(await pm.tool('focus_set', {projectIds: [PROJECT]}));
  const t = ok(await pm.tool('task_create', {title: 'PM5 ' + randomUUID(), held: false, createdBy: 'operator'}));
  assert.deepEqual([t.state, t.pauseReason, t.createdBy, t.directedBy, t.projectId], ['paused', 'held', 'execution:' + pm.executionId, MEMBER, PROJECT]);
  const page = (state: string) => op('task_page', {filter: {state: [state], search: t.title}}).then(r => ok(r));
  const held = await page('held');
  assert.deepEqual([held.tasks.map((x: any) => x.id), held.counts.held, held.counts.paused], [[t.id], 1, 0]);
  assert.equal((await page('paused')).tasks.length, 0, 'a Held task is not an ordinary pause');
  assert.equal((await pm.tool('task_update', {taskId: t.id, action: 'resume'})).status, 400, 'agents change state only through the task tools');
  ok(await pm.tool('task_update', {taskId: t.id, expectedOutcome: 'README summary'}));
  assert.equal((await pm.tool('session_start', {requestId: randomUUID(), role: 'pm', taskId: t.id})).status, 409, 'no work on a Held task');
  assert.equal(ok(await pm.tool('task_release', {taskId: t.id})).state, 'queued');
  assert.equal(ok(await pm.tool('task_hold', {taskId: t.id})).pauseReason, 'held');
  assert.equal(ok(await pm.tool('task_release', {taskId: t.id})).state, 'queued');
  ok(await op('task_update', {taskId: t.id, action: 'pause'}));
  assert.equal((await pm.tool('task_release', {taskId: t.id})).status, 409, 'a person\'s pause is resumed by a person');
  assert.equal((await pm.tool('task_hold', {taskId: t.id})).status, 409);
  assert.equal(ok(await pm.tool('task_cancel', {taskId: t.id})).state, 'cancelled');
  const person = ok(await op('task_create', {projectId: PROJECT, title: 'PM5 person ' + randomUUID()}));
  assert.deepEqual([person.state, person.createdBy], ['queued', OWNER], 'the owner path is unchanged');
});

test('PM6 audit: an agent setting change records the execution as actor and its director, and people stay owner-only (risk: an agent change looks like the person did it, or a member edits people)', {timeout: 30_000}, async () => {
  const pm = await agent(OWNER, {projectId: PROJECT, role: 'pm'});
  const hostId = await hostOf(MEMBER);
  ok(await pm.tool('hosts_update', {id: hostId, intake: 'paused'}));
  const [c] = ok(await op('setting_changes', {target: 'host:' + hostId})).changes;
  assert.deepEqual([c.field, c.previous, c.value, c.actor, c.onBehalfOf], ['intake', 'all', 'paused', 'execution:' + pm.executionId, OWNER]);
  const people = ok(await pm.tool('people_list')).people;
  assert.ok(people.some((p: any) => p.email === MEMBER));
  assert.equal((await op('people_set', {people}, MEMBER)).status, 403);
  assert.equal((await op('people_set', {people: [null, ...people]})).status, 400, 'a broken list is refused and the old one stays');
  assert.equal(ok(await op('people_list')).people.length, people.length);
  assert.deepEqual(ok(await op('people_list', {}, MEMBER)).people.map((p: any) => p.email), [MEMBER]);
  ok(await pm.tool('people_set', {people}));
  const [p] = ok(await op('setting_changes', {target: 'people'})).changes;
  assert.deepEqual([p.actor, p.onBehalfOf], ['execution:' + pm.executionId, OWNER]);
  ok(await op('hosts_update', {id: hostId, intake: 'all'}));
});
