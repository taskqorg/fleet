// Requirement: R2 shared core and R7 agentic first: one search operation serves the Spotlight palette and agents.
// Risk: a field term is dropped and the palette lists everything, or a person finds tasks of a project outside their grants.
import {test} from 'node:test';
import assert from 'node:assert/strict';
import {startCoordinator} from '../harness.ts';

// R2, R7; risk: a field term is dropped and every task matches, or grants leak a hidden project's task.
test('search finds tasks by title and id prefix, applies field terms, keeps grants', {timeout: 30_000}, async t => {
  const c = await startCoordinator({name: 'search'}); t.after(() => c.stop());
  const op = (body: object, who?: string) => op0('search', body, who);
  const op0 = async (name: string, body: object, who = 'owner@trial.test') => {
    const r = await fetch(c.base + '/api/ops/' + name, {method: 'POST', headers: {authorization: 'Bearer ' + c.secret, 'x-fleet-person': who, 'content-type': 'application/json'}, body: JSON.stringify(body)});
    const d: any = await r.json(); return name === 'search' ? {status: r.status, d} : (assert.equal(r.status, 200, JSON.stringify(d)), {...d, status: r.status});
  };
  c.sql(`INSERT INTO task(id,project_id,title,updated_at) VALUES
      ('abc12345-flaky','hello-world','Fix flaky deploy', now() - interval '1 hour'),
      ('ffff0000-other','hello-world','Write abc12345 notes', now()),
      ('hidden-0001','hidden-project','Fix flaky hidden', now());
    INSERT INTO execution(id,kind,state,task_id,harness,data) VALUES('e-flaky','agent_run','succeeded','abc12345-flaky','claude','{}');
    UPDATE task_derived SET agent='claude',host='mac-mini' WHERE task_id='abc12345-flaky';`);
  const people = (await op0('people_list', {})).people;
  assert.equal((await op0('people_set', {people: [...people.filter((p: any) => p.email !== 'scoped@trial.test'), {email: 'scoped@trial.test', name: 'Scoped', roles: ['operator'], grants: [{kind: 'project', id: 'hello-world'}]}]})).status, 200);
  const ids = (r: any) => r.d.hits.filter((h: any) => h.kind === 'task').map((h: any) => h.id);
  const flaky = await op({query: 'flaky'});
  assert.equal(flaky.status, 200, JSON.stringify(flaky.d));
  assert.deepEqual(ids(flaky).sort(), ['abc12345-flaky', 'hidden-0001']);
  // An id that starts with the text comes before a newer title match (jump by code).
  assert.deepEqual(ids(await op({query: 'abc12345'})), ['abc12345-flaky', 'ffff0000-other']);
  assert.deepEqual(ids(await op({query: 'agent:claude host:mac'})), ['abc12345-flaky']);
  assert.deepEqual((await op({query: 'agent:codex flaky'})).d.hits, []);
  assert.deepEqual((await op({query: ''})).d.hits, []);
  assert.deepEqual(ids(await op({query: 'flaky'}, 'scoped@trial.test')), ['abc12345-flaky']);
  assert.ok((await op({query: 'hello'})).d.hits.some((h: any) => h.kind === 'project' && h.id === 'hello-world'));
  assert.ok((await op({query: 'hello agent:claude'})).d.hits.every((h: any) => h.kind === 'task'));
  assert.equal((await op({query: 'x'.repeat(501)})).status, 400);
  assert.equal((await op({query: 'flaky', limit: 0})).status, 400);
  assert.deepEqual(ids(await op({query: 'state:queued abc12345'})), ['abc12345-flaky', 'ffff0000-other']);
  assert.deepEqual(ids(await op({query: 'a_c'})), []);
  assert.deepEqual(ids(await op({query: 'deploy fix'})), ['abc12345-flaky']);
});

// Requirement: OR-34 ignore; risk: an ignored project's tasks or project hit still show in the person's Spotlight, or one person's
// ignore hides hits from another or from a query that names the project.
test('search leaves out the projects the person ignores, unless the query names the project', {timeout: 30_000}, async t => {
  const c = await startCoordinator({name: 'search-ignore'}); t.after(() => c.stop());
  const call = async (name: string, body: object, who = 'owner@trial.test') => {
    const r = await fetch(c.base + '/api/ops/' + name, {method: 'POST', headers: {authorization: 'Bearer ' + c.secret, 'x-fleet-person': who, 'content-type': 'application/json'}, body: JSON.stringify(body)});
    const d: any = await r.json(); assert.equal(r.status, 200, JSON.stringify(d)); return d;
  };
  for (const id of ['keep', 'muted'])
    await call('project_create', {id, requestId: 'ignore-' + id, content: {settings: {id, name: id === 'keep' ? 'Keep' : 'Muted', contextFiles: ['rules.md'], roles: {pm: 'pm'}}, contextFiles: [{path: 'rules.md', text: 'x'}]}});
  c.sql(`INSERT INTO task(id,project_id,title,updated_at) VALUES ('t-keep','keep','Ship zebra',now()),('t-muted','muted','Ship zebra too',now()),('t-free',NULL,'Ship zebra free',now());`);
  const hits = async (query: string, who?: string) => (await call('search', {query}, who)).hits.map((h: any) => h.kind + ':' + h.id).sort();
  assert.deepEqual(await hits('zebra'), ['task:t-free', 'task:t-keep', 'task:t-muted']);
  await call('person_settings_set', {ignore: {project: 'muted', ignored: true}});
  assert.deepEqual(await hits('zebra'), ['task:t-free', 'task:t-keep']);
  assert.deepEqual(await hits('muted'), [], 'the ignored project is not a project hit either');
  assert.deepEqual(await hits('zebra project:muted'), ['task:t-muted'], 'a query naming the project still finds it');
  assert.deepEqual(await hits('zebra project:mut'), [], 'only a term that names the ignored project exactly lifts the ignore');
  const people = (await call('people_list', {})).people;
  await call('people_set', {people: [...people.filter((p: any) => p.email !== 'other@trial.test'), {email: 'other@trial.test', name: 'Other', roles: ['operator'], grants: [{kind: 'project', id: 'keep'}, {kind: 'project', id: 'muted'}]}]});
  assert.ok((await hits('zebra', 'other@trial.test')).includes('task:t-muted'), "one person's ignore does not hide hits from another");
  await call('person_settings_set', {ignored: ['muted', '-']});
  assert.deepEqual(await hits('zebra'), ['task:t-keep'], "'-' ignores work outside projects");
  await call('person_settings_set', {ignored: []});
  assert.deepEqual(await hits('zebra'), ['task:t-free', 'task:t-keep', 'task:t-muted']);
});

// R2, R7, OR-32, OR-34; risk: a conversation, result, agent or pipeline is not found, the focus does not lead, an "All N" count
// differs from the list it opens, or a person without the architect role finds definitions.
test('search finds conversations, results, agents and pipelines; the focus leads; counts match each list', {timeout: 30_000}, async t => {
  const c = await startCoordinator({name: 'searchkinds'}); t.after(() => c.stop());
  const op = async (name: string, body: object, who = 'owner@trial.test') => {
    const r = await fetch(c.base + '/api/ops/' + name, {method: 'POST', headers: {authorization: 'Bearer ' + c.secret, 'x-fleet-person': who, 'content-type': 'application/json'}, body: JSON.stringify(body)});
    const d: any = await r.json(); assert.equal(r.status, 200, name + ' ' + JSON.stringify(d)); return d;
  };
  const search = (body: object, who?: string) => op('search', body, who);
  // Ten tasks: four in hello-world, six in another project.
  const rows = Array.from({length: 10}, (_, i) => `('rocket-${i}',${i < 4 ? "'hello-world'" : "'other'"},'Rocket launch ${i}', now() - interval '${i} minutes')`).join(',');
  c.sql(`INSERT INTO task(id,project_id,title,updated_at) VALUES ${rows};
    INSERT INTO artifact(id,project_id,scope,content_hash,size,media_type,name,state) VALUES('art-rocket','hello-world','prod','x',2,'text/plain','rocket-report.txt','stored'),('art-gone','hello-world','prod','y',2,'text/plain','rocket-gone.txt','stored');
    SET fleet.deleted='on'; UPDATE artifact SET tombstoned_at=now() WHERE id='art-gone';`);
  const sess = (await op('session_create', {})).sessionId;
  c.sql(`UPDATE document SET project_id='hello-world' WHERE kind='session' AND id='${sess}';
    INSERT INTO document(kind,id,data) VALUES('message','m-rocket','{"sessionId":"${sess}","text":"Rocket launch checklist"}');
    INSERT INTO execution(id,kind,state,session_id,harness,data) VALUES('e-rocket','agent_run','running','${sess}','claude','{}');`);
  const kinds = (r: any, kind: string) => r.hits.filter((h: any) => h.kind === kind);
  // Without a focus: recently changed first; every task counts.
  const all = await search({query: 'rocket', limit: 3});
  assert.deepEqual(kinds(all, 'task').map((h: any) => h.id), ['rocket-0', 'rocket-1', 'rocket-2']);
  assert.equal(all.counts.task, 10);
  assert.ok(kinds(all, 'task').every((h: any) => typeof h.at === 'string' && h.state === 'queued'), 'a hit carries its state and last change');
  const conv = kinds(all, 'conversation')[0];
  assert.deepEqual([conv?.id, conv?.state, conv?.agent, conv?.projectId, conv?.title], [sess, 'running', 'claude', 'hello-world', 'Rocket launch checklist']);
  assert.match(conv.code, /^C-/); assert.equal(typeof conv.at, 'string');
  assert.deepEqual(kinds(all, 'result').map((h: any) => [h.id, h.state, h.projectId]), [['art-rocket', 'stored', 'hello-world']], 'a deleted result is not found');
  // The focus leads each kind, and the counts are what the lists show for the same query in the focus.
  const focused = await search({query: 'rocket', limit: 3, projectIds: ['other']});
  assert.deepEqual(kinds(focused, 'task').map((h: any) => h.id), ['rocket-4', 'rocket-5', 'rocket-6']);
  assert.equal(focused.counts.task, 6);
  assert.ok(kinds(focused, 'task').every((h: any) => !h.outside) && kinds(focused, 'conversation')[0]?.outside === true, 'a hit outside the focus is marked');
  const page = await op('task_page', {filter: {search: 'rocket', projectId: ['other']}});
  assert.equal(page.tasks.length, focused.counts.task, 'the Tasks list with the query and focus holds N rows');
  assert.equal(focused.counts.conversation, 0, 'the conversation is outside the focus');
  assert.equal((await op('sessions_list', {filter: {search: 'rocket', projectId: ['other']}})).total, 0);
  const few = await search({query: 'rocket', projectIds: ['hello-world']});
  assert.deepEqual(kinds(few, 'task').slice(0, 4).map((h: any) => h.id), ['rocket-0', 'rocket-1', 'rocket-2', 'rocket-3']);
  assert.deepEqual([few.counts.task, few.counts.conversation, few.counts.result], [4, 1, 1]);
  assert.equal((await op('artifact_list', {q: 'rocket', projectIds: ['hello-world']})).artifacts.length, few.counts.result);
  // '-' is work outside projects.
  c.sql(`INSERT INTO task(id,title) VALUES('rocket-none','Rocket without project')`);
  assert.equal(kinds(await search({query: 'rocket', limit: 1, projectIds: ['-']}), 'task')[0].id, 'rocket-none');
  // Field terms: agent: finds conversations too; state: applies to conversations; results have no agent.
  assert.deepEqual(kinds(await search({query: 'agent:claude rocket'}), 'conversation').map((h: any) => h.id), [sess]);
  assert.deepEqual(kinds(await search({query: 'state:failed rocket'}), 'conversation'), []);
  assert.deepEqual(kinds(await search({query: 'agent:claude rocket'}), 'result'), []);
  // The focus leads before a code prefix: other-project tasks whose id starts with the text never hide focused ones.
  assert.equal(kinds(await search({query: 'rocket', limit: 1, projectIds: ['hello-world']}), 'task')[0].id, 'rocket-0');
  c.sql(`INSERT INTO task(id,project_id,title) VALUES('rocketx-1','other','Prefix only')`);
  assert.equal(kinds(await search({query: 'rocket', limit: 1, projectIds: ['hello-world']}), 'task')[0].id, 'rocket-0');
  // A code jump finds an archived conversation, which its list leaves out: the count is the list's own (0).
  c.sql(`UPDATE document SET data=data||'{"archivedAt":1}' WHERE kind='session' AND id='${sess}'`);
  const archived = await search({query: conv.code, projectIds: ['hello-world']});
  assert.equal(kinds(archived, 'conversation')[0]?.id, sess);
  assert.equal(archived.counts.conversation, 0);
  // An exact code is what was asked for: its hit is never marked outside the focus.
  assert.equal((await search({query: conv.code, projectIds: ['other']})).hits[0].outside, undefined);
  // Agents and pipelines: by id or name, with their definition state; agent: is an agent's harness.
  const pm = kinds(await search({query: 'pm'}), 'agent').find((h: any) => h.id === 'pm');
  assert.ok(pm?.agent && pm.at && !pm.state, JSON.stringify(pm));
  assert.ok(kinds(await search({query: 'pm agent:' + pm.agent}), 'agent').some((h: any) => h.id === 'pm'));
  assert.ok(kinds(await search({query: 'search-basic'}), 'pipeline').some((h: any) => h.id === 'search-basic'));
  assert.deepEqual(kinds(await search({query: 'host:x search-basic'}), 'pipeline'), [], 'a pipeline takes no field term');
  // An ignored project leaves every kind and every count, so it is never among the results outside the focus either.
  await op('person_settings_set', {ignored: ['hello-world']});
  const quiet = await search({query: 'rocket', projectIds: ['other']});
  assert.ok(quiet.hits.every((h: any) => h.projectId !== 'hello-world'), JSON.stringify(quiet.hits.map((h: any) => h.kind + ':' + h.id)));
  assert.deepEqual(kinds(quiet, 'result'), []);
  assert.equal(quiet.counts.result, undefined);
  assert.equal((await search({query: 'rocket project:hello-world'})).hits.filter((h: any) => h.kind === 'result').length, 1, 'naming the project finds it');
  await op('person_settings_set', {ignored: []});
  // Definitions are the architect's: a person without the role finds none; grants still apply.
  const people = (await op('people_list', {})).people;
  await op('people_set', {people: [...people, {email: 'searcher@trial.test', name: 'Searcher', roles: ['operator'], grants: [{kind: 'project', id: 'hello-world'}]}]});
  assert.deepEqual((await search({query: 'pm'}, 'searcher@trial.test')).hits.filter((h: any) => h.kind === 'agent' || h.kind === 'pipeline'), []);
  assert.deepEqual(kinds(await search({query: 'rocket'}, 'searcher@trial.test'), 'task').map((h: any) => h.id).sort(), ['rocket-0', 'rocket-1', 'rocket-2', 'rocket-3']);
});
