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

test('PM1 rights: an agent call reads and writes with its director\'s grants, never the coordinator\'s * (requirement: docs/designs/fleet-pm-agent.md; risk: a member\'s PM sees or changes other projects)', {timeout: 30_000}, async () => {
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

test('PM2 deny list: take and host secrets stay with people, even for the owner\'s PM (requirement: docs/designs/fleet-pm-agent.md; risk: an injected owner PM mints a host secret)', {timeout: 30_000}, async () => {
  const pm = await agent(OWNER, {projectId: PROJECT, role: 'pm'});
  const t = ok(await op('task_create', {projectId: PROJECT, title: 'PM2 ' + randomUUID()}));
  for (const [name, args] of [['task_take', {taskId: t.id}], ['hosts_register', {id: 'pm2-host'}], ['hosts_rotate_secret', {id: 'trial-host'}], ['connections_token_create', {name: 'pm2'}]] as const)
    assert.equal((await pm.tool(name, args)).status, 403, name);
  assert.equal(ok(await pm.tool('profiles_list')).profiles !== undefined, true, 'the owner\'s PM reaches owner operations');
});

test('PM3 focus: a focused PM starts workers in its focus projects only, and focus never exceeds the grants (requirement: docs/designs/fleet-pm-agent.md; risk: the one-project lock blocks a PM, or focus widens rights)', {timeout: 30_000}, async () => {
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

test('PM4 no project: the owner\'s PM without a project focuses on every project and starts workers where asked (requirement: docs/designs/fleet-pm-agent.md; risk: a projectless PM gets 403 from the worker lock)', {timeout: 30_000}, async () => {
  const pm = await agent(OWNER, {});
  assert.equal((await pm.tool('session_start', {requestId: randomUUID(), role: 'worker', projectId: PROJECT})).status, 403, 'no focus: the worker lock answers');
  ok(await pm.tool('focus_set', {projectIds: '*'}));
  assert.equal(ok(await pm.tool('session_start', {requestId: randomUUID(), role: 'worker', projectId: PROJECT})).projectId, PROJECT);
  const all = ok(await pm.tool('task_page', {limit: 500})).tasks.map((t: any) => t.projectId);
  assert.ok(all.includes(PROJECT) && all.includes(OTHER), 'focus "*" of the owner lists every project');
});

test('PM5 Held: an agent task starts Held with the execution as creator, and leaves Held only by release (requirement: docs/designs/fleet-pm-agent.md; risk: an agent task runs before the person agrees, or a person\'s pause is undone by an agent)', {timeout: 30_000}, async () => {
  const pm = await agent(MEMBER, {projectId: PROJECT, role: 'pm'});
  ok(await pm.tool('focus_set', {projectIds: [PROJECT]}));
  const t = ok(await pm.tool('task_create', {title: 'PM5 ' + randomUUID(), held: false, createdBy: 'operator'}));
  assert.deepEqual([t.state, t.pauseReason, t.createdBy, t.directedBy, t.projectId], ['paused', 'held', 'execution:' + pm.executionId, MEMBER, PROJECT]);
  // A typed operation reply reaches the agent too (risk: the tool path dropped every reply that is not a map).
  assert.equal(ok(await pm.tool('execution_status', {executionId: pm.executionId})).executionId, pm.executionId);
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

test('PM6 audit: an agent setting change records the execution as actor and its director, and people stay owner-only (requirement: docs/designs/fleet-pm-agent.md; risk: an agent change looks like the person did it, or a member edits people)', {timeout: 30_000}, async () => {
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

const sleep = (ms: number) => new Promise(r => setTimeout(r, ms));
const queuedTexts = async (sessionId: string) => ok(await call('GET', '/api/sessions/' + sessionId, undefined, as(OWNER))).queued.map((m: any) => m.text as string);

test('PM7 watch: foreign changes in focus wake the PM with one message, its own changes never do, and the timer wakes it (requirement: docs/designs/fleet-pm-agent.md; risk: a PM misses a question, or loops on its own edits)', {timeout: 30_000}, async () => {
  const pm = await agent(OWNER, {});
  ok(await pm.tool('focus_set', {projectIds: [PROJECT]}));
  ok(await pm.tool('watch_set', {kinds: ['task', 'question']}));
  assert.ok([403, 404].includes((await op('watch_stop', {sessionId: pm.sessionId}, MEMBER)).status), 'only its director changes a watch');
  ok(await pm.tool('task_create', {title: 'PM7 own ' + randomUUID()}));
  await sleep(800);
  assert.deepEqual(await queuedTexts(pm.sessionId), [], 'own changes do not wake');
  // Each wake is one batch per tick, so assert per change, never on two changes landing in one tick.
  const worker = await agent(OWNER, {projectId: PROJECT, role: 'pm'});
  const taskWatch = await agent(OWNER, {projectId: PROJECT, role: 'pm'});
  ok(await taskWatch.tool('focus_set', {projectIds: [PROJECT]}));
  ok(await taskWatch.tool('watch_set', {kinds: ['task']}));
  const asked = ok(await worker.tool('ask', {requestId: randomUUID(), question: 'Which README?'}));
  const foreign = ok(await op('task_create', {projectId: PROJECT, title: 'PM7 foreign ' + randomUUID()}));
  const outside = ok(await op('task_create', {projectId: OTHER, title: 'PM7 outside focus ' + randomUUID()}));
  const eventually = async (sid: string, re: RegExp) => { for (let i = 0; i < 50; i++) { const t = await queuedTexts(sid); if (t.some(x => re.test(x))) return t; await sleep(100); } return queuedTexts(sid); };
  const texts = await eventually(pm.sessionId, new RegExp(`question ${asked.questionId ?? asked.id}`));
  assert.equal(texts.length, 1, JSON.stringify(texts));
  assert.match(texts[0], new RegExp(`question ${asked.questionId ?? asked.id}`), 'the first foreign change wakes the PM');
  assert.ok(!texts[0].includes(outside.id), 'outside the focus');
  const tw = await eventually(taskWatch.sessionId, new RegExp(`task ${foreign.id} now queued`));
  assert.ok(tw.some(x => new RegExp(`task ${foreign.id} now queued`).test(x)), JSON.stringify(tw));
  assert.ok(tw.every(x => !x.includes(outside.id)), 'outside the focus');
  ok(await op('task_create', {projectId: PROJECT, title: 'PM7 while busy ' + randomUUID()}));
  await sleep(800);
  assert.equal((await queuedTexts(pm.sessionId)).length, 1, 'a queued wake covers later changes');
  const w = ok(await pm.tool('watch_get')).watch;
  assert.equal(w.wakes, 1);
  ok(await pm.tool('watch_set', {kinds: ['task'], timerMs: 300}));
  await sleep(1000);
  assert.equal(ok(await pm.tool('watch_get')).watch.wakes, 1, 'no timer wake while a wake is queued');
  ok(await worker.tool('watch_set', {kinds: ['execution'], timerMs: 300}));
  await sleep(1000);
  assert.deepEqual((await queuedTexts(worker.sessionId)).filter(t => /watch timer/.test(t)).length, 1, 'the stored timer wakes an idle watch once per queued wake');
});

test('PM8 profile: a PM session carries its skills and sees the task tools (requirement: docs/designs/fleet-pm-agent.md; risk: the PM profile loses its instructions outside pipelines)', {timeout: 30_000}, async () => {
  await hostOf(OWNER);
  const s = ok(await op('session_start', {requestId: randomUUID(), text: 'pm turn', projectId: PROJECT, role: 'pm'}));
  const {commands} = ok(await hostCall(OWNER, '/api/hosts/poll', poll));
  const c = commands.find((x: any) => x.executionId === s.executionId && x.kind === 'start');
  assert.deepEqual(JSON.stringify(c).match(/"id":"pm-(tasks|watch|fleet)"/g)?.length, 3, 'three skills in the start command');
  const offered = (c.payload.fleetOperations ?? []).map((x: any) => x.name);
  for (const n of ['task_create', 'task_release', 'people_set', 'focus_set', 'watch_set']) assert.ok(offered.includes(n), 'the start command offers ' + n);
  assert.ok(!offered.includes('task_take'), 'never a person-only tool');
  const pm = await agent(OWNER, {projectId: PROJECT, role: 'pm'});
  const names = await pm.list();
  for (const n of ['task_create', 'task_release', 'watch_set', 'focus_set', 'people_set', 'setting_changes']) assert.ok(names.includes(n), n);
});

test('PM9 definitions: an architect\'s PM saves a definition and the revision names its execution and director; an operator\'s PM may not (requirement: fleet-definition-editors.md Rights, who saved; risk: an agent change looks like the person did it, or an operator\'s agent edits definitions)', async () => {
  const pm = await agent(OWNER, {projectId: PROJECT, role: 'pm'});
  const g = ok(await pm.tool('definition_get', {kind: 'pipeline', id: 'hello-search'}));
  const c = JSON.parse(JSON.stringify(g.content)); c.pipeline.steps[0].arguments = {...c.pipeline.steps[0].arguments, pm9: true};
  const s = ok(await pm.tool('definition_save', {kind: 'pipeline', id: 'hello-search', baseRevision: g.head, content: c, message: 'PM9'}));
  assert.deepEqual([s.revision.source, s.revision.actor.kind, s.revision.actor.executionId, s.revision.actor.directedBy.email], ['agent', 'agent', pm.executionId, OWNER]);
  const own = ok(await op('executions_list', {})).executions.find((e: any) => e.id === pm.executionId);
  assert.deepEqual(own.definitions.map((d: any) => [d.kind, d.id]), [['agent', 'pm'], ['project', PROJECT]], 'an agent run pins its profile and effective project revision');
  const h = ok(await op('definition_history', {kind: 'pipeline', id: 'hello-search'}));
  assert.equal(h.revisions[0].actor.executionId, pm.executionId, 'the history shows the agent as author');
  const member = await agent(MEMBER, {projectId: PROJECT, role: 'pm'});
  assert.ok(!(await member.list()).includes('definition_save'), 'an operator\'s PM has no definition tools');
  assert.notEqual((await member.tool('definition_get', {kind: 'pipeline', id: 'hello-search'})).status, 200);
});

test('W5 project question: ask is idempotent by requestId, answer and history read the question columns (requirement: fleet-data-foundation.md §1 typed rows; risk: a question read from a stale data blob, or a lost answer)', async () => {
  const requestId = randomUUID();
  const asked = ok(await op('question_ask', {projectId: PROJECT, requestId, text: 'Ship it?'}));
  assert.equal(asked.state, 'waiting');
  assert.equal(ok(await op('question_ask', {projectId: PROJECT, requestId, text: 'Ship it?'})).id, asked.id, 'same request, same question');
  assert.equal((await op('question_ask', {projectId: PROJECT, requestId, text: 'Other'})).status, 409);
  const answered = ok(await op('question_answer', {projectId: PROJECT, questionId: asked.id, answer: 'yes'}));
  assert.deepEqual([answered.state, answered.answer, answered.answeredBy, typeof answered.answeredAt], ['answered', 'yes', OWNER, 'number']);
  const h = ok(await op('question_history', {projectId: PROJECT})).questions.find((x: any) => x.id === asked.id);
  assert.deepEqual([h.state, h.answer, h.requestId, h.text, h.createdAt], ['answered', 'yes', requestId, 'Ship it?', asked.createdAt]);
  assert.equal((await op('question_answer', {projectId: PROJECT, questionId: asked.id, answer: 'no'})).status, 409);
});

test('PM10 key request: an agent asks for a key and gets only its fingerprint; no answer path carries a value (requirement: docs/designs/fleet-connections.md key requests; risk: a key value passing through the agent, its transcript or a typed answer)', {timeout: 30_000}, async () => {
  const conn = 'kr-' + randomUUID().slice(0, 8), value = 'tvly-' + randomUUID();
  ok(await op('connections_create', {name: conn, provider: 'tavily'}));
  const pm = await agent(OWNER, {projectId: PROJECT, role: 'pm'});
  assert.ok((await pm.list()).includes('connections_key_request'));
  assert.ok(!(await pm.list()).includes('connections_key_add'), 'an agent never handles a key value');
  assert.equal((await pm.tool('connections_key_add', {connection: conn, value})).status, 403);
  const requestId = randomUUID();
  assert.equal((await pm.tool('connections_key_request', {projectId: PROJECT, requestId, connection: conn, value})).status, 400, 'a value argument is refused');
  const asked = ok(await pm.tool('connections_key_request', {projectId: PROJECT, requestId, connection: conn, reason: 'search'}));
  assert.equal(ok(await pm.tool('connections_key_request', {projectId: PROJECT, requestId, connection: conn, reason: 'search'})).id, asked.id, 'same request, same question');
  const open = ok(await op('questions_list', {state: 'open'})).questions.find((x: any) => x.id === asked.id);
  assert.deepEqual([open.kind, open.options, open.executionId, open.sessionId], ['secret', {connection: conn}, pm.executionId, pm.sessionId], 'a secret question in the PM conversation');
  assert.equal((await pm.tool('question_answer', {projectId: PROJECT, questionId: asked.id, answer: 'decline'})).status, 403, 'the agent never settles its own request');
  assert.equal(ok(await pm.tool('question_ask', {projectId: PROJECT, requestId, text: 'Same id?'})).text, 'Same id?', 'question_ask has its own requestId space');
  assert.equal((await op('questions_answer', {questionId: asked.id, answer: value})).status, 400, 'a typed answer cannot carry the value');
  assert.equal((await op('questions_answer', {questionId: asked.id, answer: 'decline'}, MEMBER)).status, 403);
  assert.equal((await op('connections_key_add', {connection: 'no-such', value, questionId: asked.id})).status, 404);
  const k = ok(await op('connections_key_add', {connection: conn, value, questionId: asked.id}));
  // A client retry after a cut reply (app writeOnce) sends the same add again: the held key answers it, no second key.
  assert.equal(ok(await op('connections_key_add', {connection: conn, value, questionId: asked.id})).fingerprint, k.fingerprint, 'a retried add is the same write');
  const h = ok(await pm.tool('question_history', {projectId: PROJECT})).questions.find((x: any) => x.id === asked.id);
  assert.deepEqual([h.state, JSON.parse(h.answer), h.answeredBy], ['answered', {connection: conn, fingerprint: k.fingerprint, state: 'active'}, OWNER]);
  assert.equal(k.addedBy, OWNER, 'who added it is recorded');
  for (const r of [await pm.tool('question_history', {projectId: PROJECT}), await op('questions_list', {}), await op('connections_list')]) assert.ok(!JSON.stringify(r.body).includes(value));
  // A second request answered with a key Fleet already holds; a retired key is never a valid answer.
  const again = ok(await pm.tool('connections_key_request', {projectId: PROJECT, requestId: randomUUID(), connection: conn}));
  ok(await op('connections_key_add', {connection: conn, value, questionId: again.id}));
  ok(await op('connections_key_state', {keyId: k.fingerprint, state: 'retired'}));
  const third = ok(await pm.tool('connections_key_request', {projectId: PROJECT, requestId: randomUUID(), connection: conn}));
  assert.equal((await op('questions_answer', {questionId: third.id, answer: JSON.stringify({connection: conn, fingerprint: k.fingerprint, state: 'active'})})).status, 400);
  ok(await op('questions_answer', {questionId: third.id, answer: 'decline'}));
  // A client retry of the same answer (app writeOnce) succeeds without a second answer; another answer is a conflict.
  assert.equal(ok(await op('questions_answer', {questionId: third.id, answer: 'decline'})).state, 'answered');
  assert.equal((await op('questions_answer', {questionId: third.id, answer: 'yes'})).status, 409);
  assert.equal((await op('questions_answer', {questionId: third.id, answer: 'decline'}, MEMBER)).status, 403, 'a replay keeps the owner-only rule');
});
