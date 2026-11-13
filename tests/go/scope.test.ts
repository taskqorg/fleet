// Outside tests of project scope for people who are not the owner (docs/designs/fleet-people-access.md): a member granted
// one project (desiign) and an outsider with no grant, against the real binary and Postgres. The owner returns
// from unscoped() and row-level security before any check, so owner-only tests never exercise these paths.
//   FLEET_TRIAL_TESTS=tests/go/scope.test.ts node tests/trial/run.ts   (or tests/go/run.sh, which sets the store env)
// Each test names its requirement and the risk it guards. Allowing a member more is not a defect; reading another
// project's rows or a secret is.
import {after, test as nodeTest} from 'node:test';
import assert from 'node:assert/strict';
import {createHash, randomUUID} from 'node:crypto';
import {fakeS3} from '../s3-fake.ts';

const BASE = process.env.FLEET_TRIAL_URL!, SECRET = process.env.FLEET_TRIAL_BROWSER_SECRET!;
const OWNER = 'owner@trial.test', MEMBER = 'member@trial.test', OUTSIDER = 'outsider@trial.test', ARCHITECT = 'architect@trial.test';
const MINE = 'desiign', OTHER = 'hello-world';
const test = (name: string, fn: () => Promise<void>, o: {todo?: string} = {}) => nodeTest(name, {timeout: 30_000, ...o}, fn);
type Reply = {status: number, body: any, headers: Headers};
async function call(method: string, path: string, body?: unknown, headers: Record<string, string> = {}): Promise<Reply> {
  const r = await fetch(BASE + path, {method, headers: {...(body === undefined ? {} : {'content-type': 'application/json'}), ...headers}, body: body === undefined ? undefined : JSON.stringify(body)});
  const text = await r.text(); let parsed: any = text; try { parsed = JSON.parse(text); } catch {}
  return {status: r.status, body: parsed, headers: r.headers};
}
const as = (person: string) => ({authorization: 'Bearer ' + SECRET, 'x-fleet-person': person});
const ok = (r: Reply) => { assert.equal(r.status, 200, JSON.stringify(r.body)); return r.body; };
const op = (name: string, b: object = {}, person = OWNER) => call('POST', '/api/ops/' + name, b, as(person));
const poll = {platform: 'darwin', capabilities: [], stores: [], problems: []};
const start = (person: string, projectId: string, extra: object = {}) => op('session_start', {requestId: randomUUID(), text: 'scope turn', projectId, profileId: 'pm', ...extra}, person);

// One owner-held host (the default host id), so it may report account observations and stores.
let hostSecret = '';
async function host() {
  if (!hostSecret) hostSecret = ok(await op('hosts_register', {id: 'trial-host'})).secret;
  const h = (path: string, body: object) => call('POST', path, body, {authorization: 'Bearer ' + hostSecret});
  ok(await h('/api/hosts/poll', poll));
  return h;
}

const {port: s3port, close: closeS3} = await fakeS3();
after(closeS3);

test('S1 lists: a member lists only granted projects and their sessions; an outsider lists none (requirement: docs/designs/fleet-people-access.md grants; risk: row-level security or project settings leak another project)', async () => {
  const theirs = ok(await start(OWNER, OTHER)).sessionId, mine = ok(await start(MEMBER, MINE)).sessionId;
  assert.deepEqual(ok(await call('GET', '/api/projects', undefined, as(MEMBER))).projects.map((p: any) => p.id), [MINE]);
  assert.equal(ok(await call('GET', '/api/projects', undefined, as(OUTSIDER))).projects.length, 0);
  const listed = (person: string) => op('sessions_list', {}, person).then(r => ok(r).sessions.map((s: any) => s.id ?? s.sessionId) as string[]);
  const m = await listed(MEMBER);
  assert.ok(m.includes(mine) && !m.includes(theirs), JSON.stringify(m));
  assert.ok(!(await listed(OUTSIDER)).some(id => id === mine || id === theirs));
  assert.equal((await call('GET', '/api/sessions/' + theirs, undefined, as(MEMBER))).status, 404);
});

test('S2 dispatch: a member\'s message picks the owner-held host and a take-rule subscription, and the host claims it; readiness runs too (requirement: docs/designs/fleet-people-access.md grants; risk: knownHost, eligible account placement or readiness rows run in the person\'s scope: 400 Unknown host, no account, 403)', async () => {
  const h = await host();
  const a = ok(await op('account_create', {name: 'scope-sub', harness: 'codex'}));
  ok(await h('/api/hosts/accounts', {accountId: a.id, hostId: 'trial-host', binding: 'scope-bind', platform: 'darwin', readiness: 'ready', nativeLoginObserved: null, quota: 'unknown', reason: 'ready'}));
  ok(await op('account_update', {accountId: a.id, take: {projects: [MINE]}}));
  const s = ok(await start(MEMBER, MINE, {hostId: 'trial-host'}));
  assert.equal(s.account, null, 'automatic account is selected at eligible placement');
  const {commands} = ok(await h('/api/hosts/poll', poll));
  const claimed = commands.find((c: any) => c.executionId === s.executionId && c.kind === 'start');
  assert.ok(claimed, 'the host claims the member\'s start');
  assert.equal(claimed.payload.account, 'scope-bind', 'the eligible take-rule account reaches the native start');
  // Readiness runs on the project's configured hosts (owner-mac in config/projects/desiign).
  const mac = ok(await op('hosts_register', {id: 'owner-mac'})).secret;
  ok(await call('POST', '/api/hosts/poll', poll, {authorization: 'Bearer ' + mac}));
  const r = ok(await op('project_readiness', {projectId: MINE}, MEMBER));
  assert.ok(r.hosts.find((x: any) => x.hostId === 'owner-mac')?.executionId, JSON.stringify(r));
  assert.equal((await op('project_readiness', {projectId: OTHER}, MEMBER)).status, 404);
});

test('S2b questions_withdraw, executions_abandon and executions_dismiss: owner only for every caller kind; a member (granted another project), an outsider and an agent acting for a member get 403 and change nothing (requirement: owner-only bulk clean-up ops; risk: a non-owner or its agent withdraws questions or ends runs)', async () => {
  const h = await host();
  const s = ok(await start(MEMBER, MINE, {hostId: 'trial-host'}));
  const {commands} = ok(await h('/api/hosts/poll', poll));
  const command = commands.find((c: any) => c.executionId === s.executionId && c.kind === 'start');
  assert.ok(command, 'the host claims the member run');
  // Taken work: the run takes the owner's subscription, so dispatch makes the owner its director (takenFrom: the member).
  const taken = ok(await call('GET', '/api/sessions/' + s.sessionId, undefined, as(OWNER))).executions.find((x: any) => x.id === s.executionId);
  assert.deepEqual([taken.directedBy, taken.takenFrom], [OWNER, MEMBER]);
  const calls: [string, object][] = [
    ['questions_withdraw', {ids: [randomUUID()], reason: 'x'}],
    ['executions_abandon', {ids: [s.executionId], reason: 'x'}],
    ['executions_dismiss', {ids: [s.executionId], reason: 'x'}]];
  for (const [name, args] of calls) {
    for (const who of [MEMBER, OUTSIDER]) assert.equal((await op(name, args, who)).status, 403, `${name} as ${who}`);
    const agent = await h('/api/hosts/agent-tools', {executionId: s.executionId, commandId: command.id, request: {jsonrpc: '2.0', id: 1, method: 'tools/call', params: {name, arguments: args}}});
    assert.equal(agent.status, 403, `${name} as the member's agent: ${JSON.stringify(agent.body)}`);
    assert.match(agent.body.message, /Only the owner/);
  }
  const e = ok(await call('GET', '/api/sessions/' + s.sessionId, undefined, as(OWNER))).executions.find((x: any) => x.id === s.executionId);
  assert.notEqual(e.state, 'failed', 'the run was not abandoned');
  assert.deepEqual(ok(await op('executions_abandon', {ids: [s.executionId], reason: 'owner'})).abandoned, [s.executionId]);
});

test('S3a questions: a person cannot answer an agent question of a project they are not granted, and changes nothing (requirement: docs/designs/fleet-people-access.md grants; risk: questions_answer replies to another project\'s session)', async () => {
  const s = ok(await start(MEMBER, MINE));
  const q = ok(await op('ask', {executionId: s.executionId, requestId: 'scope-qa', question: 'Which colour?'}));
  assert.equal((await op('questions_answer', {questionId: q.id, answer: 'red'}, OUTSIDER)).status, 404);
  const got = ok(await call('GET', '/api/sessions/' + s.sessionId, undefined, as(MEMBER)));
  assert.equal(got.questions.find((x: any) => x.id === q.id).state, 'open', 'the outsider changed nothing');
  // A member of another project: the agent question row has no project of its own, only its session has one.
  const t = ok(await start(OWNER, OTHER));
  const tq = ok(await op('ask', {executionId: t.executionId, requestId: 'scope-qa2', question: 'Which colour?'}));
  assert.equal((await op('questions_answer', {questionId: tq.id, answer: 'red'}, MEMBER)).status, 404);
  assert.equal(ok(await call('GET', '/api/sessions/' + t.sessionId, undefined, as(OWNER))).questions.find((x: any) => x.id === tq.id).state, 'open');
});

// Expected to fail: defect found by this file. agentReply (agent.go) writes the answered question back with put(), whose
// row carries no project, so row-level security refuses the member with 403 "Not in your scope". Owner-only B6b passes.
test('S3b questions: a member answers an agent question of their project and the answer reaches the asking session (requirement: docs/designs/fleet-people-access.md grants; risk: the question project is read from a null column, or the reply write runs outside the member\'s scope)', async () => {
  const s = ok(await start(MEMBER, MINE));
  const q = ok(await op('ask', {executionId: s.executionId, requestId: 'scope-qb', question: 'Which colour?'}));
  const r = await op('questions_answer', {questionId: q.id, answer: 'blue'}, MEMBER);
  assert.notEqual(r.status, 404, 'the member has the session\'s project (1cc53b7)');
  ok(r);
  const got = ok(await call('GET', '/api/sessions/' + s.sessionId, undefined, as(MEMBER)));
  assert.equal(got.questions.find((x: any) => x.id === q.id).state, 'answered');
  assert.ok(got.queued.some((m: any) => /blue/.test(m.text)), 'the answer reaches the asking session');
});

test('S4 task take: a member takes a task of a granted project; not one of another project; an outsider takes none (requirement: docs/designs/fleet-people-access.md grants; risk: take moves direction across projects)', async () => {
  const mine = ok(await op('task_create', {projectId: MINE, title: 'S4 mine ' + randomUUID()}));
  const other = ok(await op('task_create', {projectId: OTHER, title: 'S4 other ' + randomUUID()}));
  assert.equal(ok(await op('task_take', {taskId: mine.id}, MEMBER)).directedBy, MEMBER);
  assert.notEqual((await op('task_take', {taskId: other.id}, MEMBER)).status, 200);
  assert.notEqual((await op('task_take', {taskId: mine.id}, OUTSIDER)).status, 200);
});

test('S5 artifacts: a member lists and reads a result of a granted project; an outsider neither lists nor reads it (requirement: docs/designs/fleet-people-access.md grants; risk: result bytes leak across projects)', async () => {
  const h = await host();
  ok(await h('/api/hosts/stores', {store: {id: 'scope-store', bucket: 'fleet', endpoint: `http://127.0.0.1:${s3port}`, accessKeyId: 'k', secretAccessKey: 's'}}));
  const s = ok(await start(MEMBER, MINE, {hostId: 'trial-host'}));
  ok(await h('/api/hosts/poll', poll));
  const bytes = Buffer.from('scope bytes\n'), sha256 = createHash('sha256').update(bytes).digest('hex');
  const r = ok(await h('/api/hosts/artifacts/reserve', {executionId: s.executionId, name: 'out/scope.txt', mediaType: 'text/plain', size: bytes.length, sha256}));
  const {parts} = ok(await h('/api/hosts/artifacts/parts', {artifactId: r.artifactId, partNumbers: [1]}));
  const put = await fetch(parts[0].url, {method: 'PUT', body: bytes});
  const c = ok(await h('/api/hosts/artifacts/commit', {artifactId: r.artifactId, parts: [{partNumber: 1, etag: put.headers.get('etag')}]}));
  const staged = Buffer.from(await (await fetch(c.verify.url)).arrayBuffer());
  ok(await h('/api/hosts/artifacts/verified', {artifactId: r.artifactId, storeId: c.verify.storeId, sha256: createHash('sha256').update(staged).digest('hex')}));
  assert.deepEqual(ok(await op('artifact_list', {executionId: s.executionId, limit: 10}, MEMBER)).artifacts.map((a: any) => a.id ?? a.artifactId), [r.artifactId]);
  assert.equal((await call('GET', `/api/results/${r.artifactId}`, undefined, as(MEMBER))).body, bytes.toString());
  const outside = await op('artifact_list', {executionId: s.executionId, limit: 10}, OUTSIDER);
  assert.ok(outside.status !== 200 || outside.body.artifacts.length === 0, JSON.stringify(outside.body));
  assert.notEqual((await call('GET', `/api/results/${r.artifactId}`, undefined, as(OUTSIDER))).status, 200);
});

test('S6 stream: a member\'s /api/stream carries changes of granted projects only (requirement: docs/designs/fleet-people-access.md grants; risk: the change feed skips row scope)', async () => {
  const ac = new AbortController();
  const r = await fetch(BASE + '/api/stream', {headers: as(MEMBER), signal: ac.signal});
  let text = '';
  const reading = (async () => { const dec = new TextDecoder(); try { for await (const chunk of r.body as any) text += dec.decode(chunk, {stream: true}); } catch {} })();
  const mine = ok(await start(MEMBER, MINE)).sessionId, theirs = ok(await start(OWNER, OTHER)).sessionId;
  for (let i = 0; i < 50 && !text.includes(mine); i++) await new Promise(res => setTimeout(res, 100));
  await new Promise(res => setTimeout(res, 300));
  ac.abort(); await reading;
  assert.ok(text.includes(mine), 'the member sees a change of their project');
  assert.ok(!text.includes(theirs), 'the member sees no change of another project');
});

test('S7 owner-only and director-only: people_set stays the owner\'s, its audit is hidden from a non-owner architect, and focus is set only by a session\'s director (requirement: docs/designs/fleet-people-access.md grants; risk: people list audit or another person\'s session changed by a non-owner)', async () => {
  assert.equal((await op('people_set', {people: []}, MEMBER)).status, 403);
  const people = ok(await op('people_list')).people;
  ok(await op('people_set', {people: [...people, {email: ARCHITECT, name: 'Architect', roles: ['operator', 'architect'], grants: [{kind: 'project', id: MINE}]}]}));
  assert.ok(ok(await op('setting_changes', {target: 'people'})).changes.length > 0, 'the owner sees the people audit');
  assert.deepEqual(ok(await op('setting_changes', {target: 'people'}, ARCHITECT)).changes, []);
  const owners = ok(await start(OWNER, MINE)).sessionId, members = ok(await start(MEMBER, MINE)).sessionId;
  assert.equal((await op('focus_set', {sessionId: owners, projectIds: [MINE]}, MEMBER)).status, 403);
  ok(await op('focus_set', {sessionId: members, projectIds: [MINE]}, MEMBER));
});

test('S-SESS session routes: a member cannot message or re-profile a session of a project they cannot see (404), and may drive one in a granted project directed by someone else (requirement: fleet-people-access.md director and project scope; risk: cross-project session change; a director-only rule the spec does not set)', async () => {
  const theirs = ok(await start(OWNER, OTHER)).sessionId, shared = ok(await start(OWNER, MINE)).sessionId;
  assert.equal((await call('POST', `/api/sessions/${theirs}/messages`, {requestId: randomUUID(), text: 'x'}, as(MEMBER))).status, 404);
  assert.equal((await call('POST', `/api/sessions/${theirs}/profile`, {profileId: 'pm'}, as(MEMBER))).status, 404);
  ok(await call('POST', `/api/sessions/${shared}/messages`, {requestId: randomUUID(), text: 'x'}, as(MEMBER)));
});

test('S9 a director with grant * takes the owner\'s subscription without a take rule, and the execution keeps who directed it (requirement: development owner rule 2026-11-11, agents may do every owner-only operation; risk: site-agents cannot run Claude on owner-mac, or a take hides the real director)', async () => {
  const AGENTS = 'agents@trial.test';
  const h = await host();
  const a = ok(await op('account_create', {name: 'star-sub', harness: 'codex'}));
  ok(await h('/api/hosts/accounts', {accountId: a.id, hostId: 'trial-host', binding: 'star-bind', platform: 'darwin', readiness: 'ready', nativeLoginObserved: null, quota: 'unknown', reason: 'ready'}));
  const s = ok(await op('session_start', {requestId: randomUUID(), text: 'star turn', projectId: OTHER, profileId: 'pm', hostId: 'trial-host', account: 'star-sub'}, AGENTS));
  const {commands} = ok(await h('/api/hosts/poll', poll));
  assert.ok(commands.some((c: any) => c.executionId === s.executionId && c.kind === 'start'), 'the owner-held subscription runs work directed by grant *');
  const e = ok(await call('GET', '/api/sessions/' + s.sessionId, undefined, as(OWNER))).executions.find((x: any) => x.id === s.executionId);
  assert.equal(e.takenFrom, AGENTS, 'the real director stays on the execution');
});

test('S10 a director with grant * and no subscription or host of its own is placed on an owner-held subscription without naming one (requirement: development owner rule 2026-11-11 in dispatch placement; risk: a site-agents PM session stays preparing forever, live 2026-11-12)', async () => {
  const AGENTS = 'agents@trial.test';
  const h = await host();
  const a = ok(await op('account_create', {name: 'star-any', harness: 'codex'}));
  ok(await h('/api/hosts/accounts', {accountId: a.id, hostId: 'trial-host', binding: 'star-any-bind', platform: 'darwin', readiness: 'ready', nativeLoginObserved: null, quota: 'unknown', reason: 'ready'}));
  const s = ok(await op('session_start', {requestId: randomUUID(), text: 'star turn', projectId: OTHER, profileId: 'pm'}, AGENTS));
  const {commands} = ok(await h('/api/hosts/poll', poll));
  assert.ok(commands.some((c: any) => c.executionId === s.executionId && c.kind === 'start'), 'placement takes an owner subscription for grant *');
  const e = ok(await call('GET', '/api/sessions/' + s.sessionId, undefined, as(OWNER))).executions.find((x: any) => x.id === s.executionId);
  assert.equal(e.takenFrom, AGENTS);
});
