// Outside tests of session presets (docs/designs/fleet-conversations.md, "Start a conversation"): session_presets and
// session_start with a preset. HTTP only, against FLEET_TRIAL_URL:
//   FLEET_TRIAL_TESTS=tests/go/session-presets.test.ts FLEET_TRIAL_SERVER=go/bin/fleet-coordinator node tests/trial/run.ts
// Each test names its requirement and the risk it guards.
import {test} from 'node:test';
import assert from 'node:assert/strict';
import {randomUUID} from 'node:crypto';
import {hostCommand} from '../harness.ts';

const BASE = process.env.FLEET_TRIAL_URL!, SECRET = process.env.FLEET_TRIAL_BROWSER_SECRET!;
type Reply = {status: number, body: any};
async function call(path: string, body: unknown, headers: Record<string, string>): Promise<Reply> {
  const r = await fetch(BASE + path, {method: 'POST', headers: {'content-type': 'application/json', ...headers}, body: JSON.stringify(body)});
  const text = await r.text(); let parsed: any = text; try { parsed = JSON.parse(text); } catch {}
  return {status: r.status, body: parsed};
}
const owner = {authorization: 'Bearer ' + SECRET, 'x-fleet-person': 'owner@trial.test'};
const ok = (r: Reply) => { assert.equal(r.status, 200, JSON.stringify(r.body)); return r.body; };
const op = (name: string, b: object = {}) => call('/api/ops/' + name, b, owner);
const poll = {platform: 'darwin', capabilities: ['start-access-v1'], stores: [], problems: [], gitConnections: ['cs-gitlab']};
const byId = (presets: any[]) => Object.fromEntries(presets.map(p => [p.id, p]));

test('P1 a project without roles gets the system presets on system profiles, and a project with roles maps them to its own (requirement: fleet-conversations.md start a conversation, owner 2026-11-17 "when a project has no roles (cs), fall back to system profiles"; risk: nothing can start in cs, or a mapped role is ignored)', async () => {
  const cs = ok(await op('session_presets', {projectId: 'cs'}));
  assert.deepEqual(cs.presets.map((p: any) => p.id), ['discuss', 'design', 'fix', 'review', 'investigate']);
  const c = byId(cs.presets);
  assert.deepEqual([c.discuss.source, c.discuss.role ?? null, c.discuss.profileId, c.discuss.access, c.discuss.branch, c.discuss.unavailable ?? null], ['system', null, 'pm', 'read', false, null]);
  assert.deepEqual([c.fix.profileId, c.fix.access, c.fix.branch, c.fix.harness], ['coding-worker', 'write', true, 'codex']);
  assert.deepEqual([c.review.profileId, c.review.harness, c.review.model], ['claude-reviewer', 'claude', 'sonnet']);
  assert.match(c.design.starter, /owner-rules\.md/);
  assert.match(c.investigate.starter, /bugsink/);
  assert.deepEqual([cs.branchPrefix, cs.base], ['fleet-test', 'origin/main']);
  const d = byId(ok(await op('session_presets', {projectId: 'desiign'})).presets);
  assert.deepEqual([d.fix.role, d.fix.profileId, d.review.role, d.discuss.role], ['worker', 'claude-worker', 'reviewer', 'pm']);
  const outside = ok(await op('session_presets', {}));
  assert.deepEqual([outside.presets.length, outside.branchPrefix ?? null, outside.base ?? null], [5, null, null], 'outside projects: the system presets, no branch prefix or base');
  assert.equal((await op('session_presets', {projectId: 'no-such-project'})).status, 404);
});

test('P2 session_start with a preset starts its profile with its access, branch and starter, on a project without roles; explicit fields override it (requirement: fleet-conversations.md start a conversation; risk: a Discuss conversation can write, or a preset cannot start in cs)', {timeout: 30_000}, async () => {
  const secret = ok(await op('hosts_register', {id: 'owner-mac'})).secret;
  const host = (body: object, q = '') => call('/api/hosts/poll' + q, body, {authorization: 'Bearer ' + secret});
  ok(await host(poll));
  const discuss = ok(await op('session_start', {projectId: 'cs', preset: 'discuss', requestId: randomUUID(), text: 'What does the hero say?', hostId: 'owner-mac'}));
  assert.deepEqual([discuss.profileId, discuss.preset, discuss.access, discuss.branch, discuss.role ?? null], ['pm', 'discuss', 'read', false, null]);
  const design = ok(await op('session_start', {projectId: 'cs', preset: 'design', requestId: randomUUID(), text: 'Redesign the hero', hostId: 'owner-mac'}));
  assert.deepEqual([design.profileId, design.access, design.branch], ['coding-worker', 'write', true]);
  const startOf = async (sessionId: string) => (await hostCommand(async q => ok(await host(poll, q)), (c: any) => c.sessionId === sessionId && c.kind === 'start'))?.payload;
  const starts: Record<string, any> = {[discuss.sessionId]: await startOf(discuss.sessionId), [design.sessionId]: await startOf(design.sessionId)};
  const start = (sessionId: string) => starts[sessionId];
  assert.deepEqual([start(discuss.sessionId).readOnly, start(discuss.sessionId).branch], [true, false], 'a read-only preset without a branch reaches the host');
  assert.deepEqual([start(design.sessionId).readOnly ?? null, start(design.sessionId).branch ?? null], [null, null], 'write with a branch is the default payload');
  assert.match(start(design.sessionId).instructions, /Design: Design work\. .*packages\/ui/s, 'the starter instruction is in the turn');
  const override = ok(await op('session_start', {projectId: 'cs', preset: 'discuss', access: 'write', branch: true, requestId: randomUUID()}));
  assert.deepEqual([override.profileId, override.access, override.branch], ['pm', 'write', true]);
  assert.deepEqual(ok(await op('session_start', {projectId: 'cs', preset: 'review', access: 'write', requestId: randomUUID()})).branch, true, 'write without a branch field gets a branch');
  assert.equal(ok(await op('session_start', {projectId: 'cs', preset: 'fix', access: 'read', requestId: randomUUID()})).branch, false, 'read without a branch field gets none');
  const explicit = ok(await op('session_start', {projectId: 'desiign', preset: 'fix', profileId: 'pm', requestId: randomUUID()}));
  assert.deepEqual([explicit.profileId, explicit.access], ['pm', 'write'], 'an explicit profile wins over the preset');
  const id = randomUUID(), again = [await op('session_start', {projectId: 'cs', preset: 'review', requestId: id}), await op('session_start', {projectId: 'cs', preset: 'review', requestId: id})].map(ok);
  assert.deepEqual([again[1].sessionId, again[1].existing, again[1].preset, again[1].access], [again[0].sessionId, true, 'review', 'read']);
  assert.equal((await op('session_start', {projectId: 'cs', preset: 'review', access: 'write', requestId: id})).status, 409, 'the same requestId with other settings is a conflict');
  assert.equal((await op('session_start', {projectId: 'cs', preset: 'nope', requestId: randomUUID()})).status, 400);
  assert.equal((await op('session_start', {projectId: 'cs', preset: 'discuss', access: 'admin', requestId: randomUUID()})).status, 400);
  assert.equal((await op('session_start', {projectId: 'cs', preset: 'fix', branch: false, requestId: randomUUID()})).status, 400, 'write work needs its own branch');
  assert.equal((await op('session_start', {projectId: 'cs', role: 'worker', requestId: randomUUID()})).status, 400, 'an unmapped role without a preset still fails');
});

test('P3 a person starts outside projects with a preset; a project defines its own presets in its definition and a broken one is refused (requirement: fleet-conversations.md start a conversation, "No project is one project choice", presets stored in project settings; risk: a projectless conversation has no profile, or a saved preset cannot start)', {timeout: 30_000}, async () => {
  const rid = randomUUID(), s = ok(await op('session_start', {preset: 'investigate', requestId: rid}));
  assert.deepEqual([s.projectId ?? null, s.profileId, s.access, s.branch], [null, 'pm', 'read', false]);
  assert.equal(ok(await op('session_start', {preset: 'investigate', requestId: rid})).sessionId, s.sessionId, 'the same request is the same start');
  const member = {authorization: 'Bearer ' + SECRET, 'x-fleet-person': 'member@trial.test'};
  assert.equal((await call('/api/ops/session_start', {preset: 'investigate', requestId: rid}, member)).status, 403, 'outside projects needs full access; a request id never reaches another caller\'s start');
  const theirs = await call('/api/ops/session_presets', {}, member);
  assert.ok(theirs.status === 200 && theirs.body.presets.every((p: any) => p.unavailable === 'Starting outside projects needs full access'), 'the listing says so: ' + JSON.stringify(theirs.body));
  assert.equal((await op('session_start', {preset: 'investigate', taskId: 'x', requestId: randomUUID()})).status, 400, 'a task needs its project');
  const content = (presets: unknown) => ({settings: {id: 'preset-p', name: 'Preset P', contextFiles: ['rules.md'], roles: {pm: 'pm'}, presets}, contextFiles: [{path: 'rules.md', text: 'x'}]});
  assert.equal((await op('project_create', {id: 'preset-p', requestId: randomUUID(), content: content([{id: 'x', name: 'X', role: 'worker'}])})).status, 400, 'an unmapped role without a profile');
  assert.equal((await op('project_create', {id: 'preset-p', requestId: randomUUID(), content: content([{id: 'x', name: 'X', profile: 'no-such-profile'}])})).status, 400, 'an unknown profile');
  assert.equal((await op('project_create', {id: 'preset-p', requestId: randomUUID(), content: content([{id: 'x', name: 'X', role: 'pm', access: 'all'}])})).status, 400, 'a bad access');
  ok(await op('project_create', {id: 'preset-p', requestId: randomUUID(), content: content([{id: 'triage', name: 'Triage', description: 'Sort new issues', role: 'pm', access: 'read', starter: 'Sort the issues.'}])}));
  const own = ok(await op('session_presets', {projectId: 'preset-p'}));
  assert.deepEqual(own.presets.map((p: any) => [p.id, p.source, p.role, p.profileId, p.access, p.branch]), [['triage', 'project', 'pm', 'pm', 'read', false]]);
  assert.equal((await op('session_start', {projectId: 'preset-p', preset: 'discuss', requestId: randomUUID()})).status, 400, 'project presets replace the system ones');
  assert.equal(ok(await op('session_start', {projectId: 'preset-p', preset: 'triage', requestId: randomUUID()})).access, 'read');
});

test('P4 an agent starts only profiles its project maps, also through a preset, and a read-only conversation starts read-only sessions only; a read-only start waits for a host that honours it (requirement: security review 2026-11-18 of this change; risk: an agent escalates to a system profile through a preset, a Discuss conversation gets a writer one hop later, or an old host runs a read-only start with write tools)', {timeout: 30_000}, async () => {
  ok(await op('project_create', {id: 'preset-agent', requestId: randomUUID(), content: {settings: {id: 'preset-agent', name: 'Preset agent', contextFiles: ['rules.md'], roles: {pm: 'pm'}}, contextFiles: [{path: 'rules.md', text: 'x'}]}}));
  const secret = ok(await op('hosts_register', {id: 'trial-host'})).secret;
  const host = (path: string, body: object) => call(path, body, {authorization: 'Bearer ' + secret});
  ok(await host('/api/hosts/poll', {...poll, capabilities: []}));
  const agent = async (start: object) => {
    const s = ok(await op('session_start', {projectId: 'preset-agent', hostId: 'trial-host', requestId: randomUUID(), text: 'turn', ...start}));
    const c = await hostCommand(async q => ok(await host('/api/hosts/poll' + q, poll)), (x: any) => x.executionId === s.executionId && x.kind === 'start');
    assert.ok(c, 'the host claims the start command');
    const tool = async (name: string, args: object): Promise<Reply> => {
      const r = await host('/api/hosts/agent-tools', {hostId: 'trial-host', executionId: s.executionId, commandId: c.id, request: {jsonrpc: '2.0', id: 1, method: 'tools/call', params: {name, arguments: args}}});
      return r.status === 200 && r.body.result ? {status: 200, body: JSON.parse(r.body.result.content[0].text)} : r;
    };
    return {...s, tool};
  };
  // An old host (no start-access-v1) takes no read-only start; it waits with the reason.
  const waiting = ok(await op('session_start', {projectId: 'preset-agent', preset: 'discuss', hostId: 'trial-host', requestId: randomUUID(), text: 'turn'}));
  assert.ok(!ok(await host('/api/hosts/poll', {...poll, capabilities: []})).commands.some((c: any) => c.executionId === waiting.executionId), 'no read-only start on a host that would ignore it');
  const writer = await agent({role: 'pm'});
  const listed = ok(await writer.tool('session_presets', {}));
  const review = listed.presets.find((p: any) => p.id === 'review');
  assert.deepEqual([review.profileId, review.unavailable], ['claude-reviewer', 'Your project maps no role to profile claude-reviewer'], 'the agent sees which presets it cannot start');
  assert.equal((await writer.tool('session_start', {preset: 'review', requestId: randomUUID()})).status, 403, 'no system profile the project does not map');
  assert.equal((await writer.tool('session_start', {preset: 'fix', requestId: randomUUID()})).status, 403, 'no fallback worker either');
  assert.equal(ok(await writer.tool('session_start', {preset: 'discuss', requestId: randomUUID()})).profileId, 'pm', 'a mapped role starts');
  const reader = await agent({preset: 'discuss'});
  assert.equal((await reader.tool('session_start', {role: 'pm', access: 'write', requestId: randomUUID()})).status, 403, 'a read-only conversation cannot start a writer');
  assert.equal(ok(await reader.tool('session_start', {role: 'pm', requestId: randomUUID()})).access, 'read', 'its starts are read by default');
  const created = ok(await reader.tool('session_create', {}));
  assert.equal(ok(await op('session_get', {sessionId: created.sessionId})).access, 'read', 'a session it creates is read-only too');
});
