// Requirement: entity_card returns a card summary for every entity kind a reference names, by D17 code or kind and id
// (docs/design-system/controls/entity-card.md, fleet.md R7: agents get the same read as a tool).
// Risk: a card leaks an entity outside the caller's grants, an old code stops resolving, or a kind answers a wrong shape.
import {test} from 'node:test';
import assert from 'node:assert/strict';
import {startCoordinator} from '../harness.ts';

const OWNER = 'owner@trial.test', OUTSIDER = 'outsider@trial.test', MEMBER = 'member@trial.test';

// R2, R7, OR-38; risk: grants bypassed by the card, a kind missing its state or path, a UUID shown where it must only be copied.
test('entity_card: every kind by code or kind and id, within the caller\'s grants', {timeout: 60_000}, async t => {
  const c = await startCoordinator({name: 'entitycard'}); t.after(() => c.stop());
  const raw = async (name: string, body: object, who = OWNER, auth = c.secret) => {
    const r = await fetch(c.base + name, {method: 'POST', headers: {authorization: 'Bearer ' + auth, ...(who ? {'x-fleet-person': who} : {}), 'content-type': 'application/json'}, body: JSON.stringify(body)});
    return {status: r.status, body: await r.json() as any};
  };
  const op = async (name: string, body: object, who = OWNER) => { const r = await raw('/api/ops/' + name, body, who); assert.equal(r.status, 200, `${name} ${JSON.stringify(r.body)}`); return r.body; };
  const card = (body: object, who = OWNER) => op('entity_card', body, who);
  const status = async (body: object, who = OWNER) => (await raw('/api/ops/entity_card', body, who)).status;

  const task = await op('task_create', {projectId: 'hello-world', title: 'Card the deploy'});
  const own = await op('task_create', {projectId: 'desiign', title: 'Member task'});
  c.sql(`INSERT INTO question(id,kind,project_id,task_id,text,asked_by) VALUES('q-card','operator_decision','hello-world','${task.id}','Ship the card today?','agent');
    INSERT INTO execution(id,kind,state,project_id,task_id,host_id,harness,model,started_at,ended_at,data) VALUES('e-card','agent_run','succeeded','hello-world','${task.id}','mac-mini','claude','opus',now()-interval '90 seconds',now(),'{}');
    INSERT INTO artifact(id,project_id,scope,content_hash,size,media_type,name,execution_id,task_id,state) VALUES('a-card','hello-world','prod','${'ab'.repeat(32)}',2048,'text/plain','report.txt','e-card','${task.id}','stored');
    INSERT INTO experiment(id,project_id,kind,pipeline_id,goal,state) VALUES('x-card','hello-world','single','search-basic','Is the card useful?','draft');`);

  // Task by kind and id, and by its code (any case): state, title, project, harness, facts, path; the UUID only to copy.
  const t1 = await card({kind: 'task', id: task.id});
  assert.equal(t1.kind, 'task'); assert.equal(t1.code, task.code); assert.equal(t1.uuid, task.id); assert.equal(t1.title, 'Card the deploy');
  assert.equal(t1.state, 'queued'); assert.equal(t1.projectId, 'hello-world'); assert.equal(t1.path, '/tasks/' + task.id); assert.equal(t1.atLabel, 'changed');
  assert.ok(t1.facts.some((f: any) => f.label === 'Runs' && f.type === 'count' && f.value === '1'), JSON.stringify(t1.facts));
  assert.ok(!t1.facts.some((f: any) => f.value === '0'), 'no zero facts (OR-29)');
  assert.deepEqual(await card({code: task.code.toLowerCase()}), t1);
  // Question, execution, result, experiment.
  const q = await card({kind: 'question', id: 'q-card'});
  assert.equal(q.state, 'open'); assert.match(q.code, /^Q-/); assert.equal(q.path, '/tasks/' + task.id);
  assert.ok(q.facts.some((f: any) => f.label === 'Task' && f.value === task.code));
  assert.equal((await card({code: q.code})).id, 'q-card');
  const e = await card({kind: 'execution', id: 'e-card'});
  assert.equal(e.state, 'succeeded'); assert.equal(e.harness, 'claude'); assert.equal(e.title, 'Card the deploy'); assert.equal(e.path, '/tasks/' + task.id);
  assert.ok(e.facts.some((f: any) => f.label === 'Duration' && f.type === 'duration' && Number(f.value) >= 89));
  const a = await card({kind: 'result', id: 'a-card'});
  assert.equal(a.title, 'report.txt'); assert.equal(a.hash, 'ab'.repeat(32)); assert.equal(a.path, '/results/a-card');
  assert.ok(a.facts.some((f: any) => f.label === 'Size' && f.type === 'bytes' && f.value === '2048'));
  const x = await card({kind: 'experiment', id: 'x-card'});
  assert.equal(x.state, 'draft'); assert.equal(x.title, 'Is the card useful?'); assert.equal(x.path, '/lab/x-card');
  // Conversation by code, with its first message as the title.
  const sid = (await op('session_create', {})).sessionId;
  c.sql(`INSERT INTO document(kind,id,data) VALUES('message','m-card','{"sessionId":"${sid}","text":"Plan   the entity card"}')`);
  const sc = (await op('sessions_list', {})).sessions.find((s: any) => s.id === sid).code;
  const conv = await card({code: sc});
  assert.equal(conv.kind, 'conversation'); assert.equal(conv.id, sid); assert.equal(conv.title, 'Plan the entity card'); assert.equal(conv.state, 'idle'); assert.equal(conv.atLabel, 'active');
  // Project, host, agent, pipeline, release.
  const p = await card({kind: 'project', id: 'hello-world'});
  assert.equal(p.path, '/projects/hello-world'); assert.ok(p.code); assert.equal(p.uuid ?? null, null);
  assert.ok(p.facts.some((f: any) => f.label === 'Open tasks' && f.value === '1'));
  const {secret} = await op('hosts_register', {id: 'card-host', holder: OWNER});
  await raw('/api/hosts/poll', {platform: 'darwin', capabilities: [], stores: [], problems: [], version: '1.2.3'}, '', secret);
  const h = await card({kind: 'host', id: 'card-host'});
  assert.equal(h.state, 'online'); assert.equal(h.title, 'card-host'); assert.equal(h.atLabel, 'seen'); assert.equal(h.path, '/hosts/card-host');
  const ag = await card({kind: 'agent', id: 'claude-worker'});
  assert.equal(ag.harness, 'claude'); assert.equal(ag.path, '/agents/claude-worker'); assert.ok(ag.facts.some((f: any) => f.label === 'Model' && f.value === 'sonnet'));
  const pl = await card({kind: 'pipeline', id: 'search-basic'});
  assert.ok(pl.facts.some((f: any) => f.label === 'Steps' && f.value === '1')); assert.equal(pl.path, '/pipelines/search-basic');
  const rel = await card({kind: 'release', id: 'v1.2.3'});
  assert.equal(rel.state, 'installed'); assert.ok(rel.facts.some((f: any) => f.label === 'Hosts' && f.value === '1 of 1'), JSON.stringify(rel.facts));
  assert.equal((await card({kind: 'release', id: 'v9.0.0'})).state, 'installing');
  // An old code after a project rename still resolves, to the current code.
  await op('project_code_set', {projectId: 'hello-world', code: 'HWC'});
  const moved = await card({code: task.code});
  assert.equal(moved.id, task.id); assert.match(moved.code, /^T-HWC-/);

  // Bad requests: neither or both of code and kind/id, a malformed code, a spec rule code, a bad pull request or tag.
  for (const b of [{}, {kind: 'task'}, {code: task.code, kind: 'task', id: task.id}, {code: 'nope'}, {code: 'S-FLT-D3'}, {kind: 'pull_request', id: 'x'}, {kind: 'release', id: 'latest'}]) assert.equal(await status(b), 400, JSON.stringify(b));
  assert.equal(await status({kind: 'task', id: 'missing'}), 404);
  assert.equal(await status({kind: 'pull_request', id: 'taskqorg/fleet#1'}), 404, 'a pull request the delivery pass has not read');

  // Grants: an outsider sees none of them; a member sees its own project's task, not another's; definitions and Lab
  // stay the architect's.
  for (const b of [{kind: 'task', id: task.id}, {code: moved.code}, {kind: 'question', id: 'q-card'}, {kind: 'execution', id: 'e-card'}, {kind: 'result', id: 'a-card'},
    {kind: 'experiment', id: 'x-card'}, {kind: 'project', id: 'hello-world'}, {kind: 'conversation', id: sid}, {kind: 'agent', id: 'claude-worker'}, {kind: 'pipeline', id: 'search-basic'}])
    assert.equal(await status(b, OUTSIDER), 404, 'outsider ' + JSON.stringify(b));
  assert.equal((await card({code: own.code}, MEMBER)).title, 'Member task');
  assert.equal(await status({kind: 'task', id: task.id}, MEMBER), 404);
  assert.equal(await status({kind: 'project', id: 'hello-world'}, MEMBER), 404);
  assert.equal((await card({kind: 'project', id: 'desiign'}, MEMBER)).path, '/projects/desiign');
  for (const b of [{kind: 'agent', id: 'claude-worker'}, {kind: 'pipeline', id: 'search-basic'}]) assert.equal(await status(b, MEMBER), 404, 'member ' + JSON.stringify(b));

  // Agents get the same read as an MCP tool, with a description.
  const list = await raw('/api/tools', {jsonrpc: '2.0', id: 1, method: 'tools/list'});
  const tool = list.body.result.tools.find((x: any) => x.name === 'entity_card');
  assert.ok(tool, 'entity_card is an agent tool'); assert.match(tool.description, /card/);
  const called = await raw('/api/tools', {jsonrpc: '2.0', id: 2, method: 'tools/call', params: {name: 'entity_card', arguments: {code: moved.code}}});
  assert.equal(JSON.parse(called.body.result.content[0].text).id, task.id);
});
