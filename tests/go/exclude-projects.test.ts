// Requirement: OR-34 ignore is a server-side exclusion: the app passes the person's ignored projects as excludeProjectId(s)
// to every list it reads, so work outside projects and in projects missing from the project list stays visible.
// Risk: ignore is turned into an allow-list of known projects and work of an undefined project disappears, or an ignored
// project's rows stay in a list, its counts or its live membership.
import {test} from 'node:test';
import assert from 'node:assert/strict';
import {startCoordinator} from '../harness.ts';

// R2, OR-34; risk: an ignored project's rows stay in a list or its live membership, or work outside projects and in an undefined project is left out too.
test('excludeProjectId(s) leave out only the named projects in task_page, sessions_list, list_membership, questions_list, executions_list and artifact_list', {timeout: 30_000}, async t => {
  const c = await startCoordinator({name: 'exclude'}); t.after(() => c.stop());
  const call = async (name: string, body: object) => {
    const r = await fetch(c.base + '/api/ops/' + name, {method: 'POST', headers: {authorization: 'Bearer ' + c.secret, 'x-fleet-person': 'owner@trial.test', 'content-type': 'application/json'}, body: JSON.stringify(body)});
    return {status: r.status, d: await r.json() as any};
  };
  const op = async (name: string, body: object) => { const r = await call(name, body); assert.equal(r.status, 200, name + ' ' + JSON.stringify(r.d)); return r.d; };
  // keep and muted; 'stray' has rows but no project definition (the case an allow-list of known projects lost).
  c.sql(`INSERT INTO task(id,project_id,title) VALUES ('t-keep','keep','A'),('t-muted','muted','B'),('t-stray','stray','C'),('t-free',NULL,'D');
    INSERT INTO document(kind,id,project_id,data) VALUES ('session','s-keep','keep','{}'),('session','s-muted','muted','{}'),('session','s-stray','stray','{}'),('session','s-free',NULL,'{}');
    INSERT INTO question(id,kind,project_id,text) VALUES ('q-keep','operator_decision','keep','a'),('q-muted','operator_decision','muted','b'),('q-stray','operator_decision','stray','c'),('q-free','operator_decision',NULL,'d');
    INSERT INTO execution(id,kind,state,project_id,data) VALUES ('e-keep','agent_run','succeeded','keep','{}'),('e-muted','agent_run','succeeded','muted','{}'),('e-stray','agent_run','succeeded','stray','{}'),('e-free','agent_run','succeeded',NULL,'{}');
    INSERT INTO artifact(id,project_id,scope,content_hash,size,media_type,name,state) VALUES ('a-keep','keep','prod','k',1,'text/plain','k.txt','stored'),('a-muted','muted','prod','m',1,'text/plain','m.txt','stored'),('a-stray','stray','prod','s',1,'text/plain','s.txt','stored'),('a-free',NULL,'prod','f',1,'text/plain','f.txt','stored');`);
  const sorted = (xs: any[], prefix: string) => xs.map(x => x.id).filter((id: string) => id.startsWith(prefix)).sort();
  const of = (prefix: string, ...names: string[]) => names.map(n => prefix + n).sort();

  // task_page: rows, counts and project groups.
  const tasks = await op('task_page', {group: 'project', groupCounts: true, filter: {excludeProjectId: ['muted']}});
  assert.deepEqual(sorted(tasks.tasks, 't-'), of('t-', 'keep', 'stray', 'free'), 'work outside projects and an undefined project stay');
  assert.equal(tasks.total, 3); assert.ok(!tasks.groups.some((g: any) => g.label === 'muted'), 'no group of the ignored project');
  assert.deepEqual(sorted((await op('task_page', {filter: {excludeProjectId: ['muted', null]}})).tasks, 't-'), of('t-', 'keep', 'stray'), 'null leaves out work outside projects');
  assert.deepEqual(sorted((await op('task_page', {filter: {excludeProjectId: []}})).tasks, 't-'), of('t-', 'keep', 'muted', 'stray', 'free'), 'an empty exclusion leaves nothing out');
  assert.deepEqual(sorted((await op('task_page', {filter: {projectId: ['keep', 'muted'], excludeProjectId: ['muted']}})).tasks, 't-'), ['t-keep'], 'the exclusion applies after projectId');
  assert.deepEqual(sorted((await op('task_page', {})).tasks, 't-'), of('t-', 'keep', 'muted', 'stray', 'free'), 'without the filter (agents) every project shows');
  assert.equal((await call('task_page', {filter: {excludeProjectId: 'muted'}})).status, 400, 'not a list: refused, never all rows');
  assert.equal((await call('task_page', {filter: {excludeProjectId: [1]}})).status, 400);

  // sessions_list and live membership, with the same filter the list was loaded with.
  assert.deepEqual(sorted((await op('sessions_list', {filter: {excludeProjectId: ['muted']}})).sessions, 's-'), of('s-', 'keep', 'stray', 'free'));
  assert.deepEqual(sorted((await op('sessions_list', {filter: {excludeProjectId: ['muted', null]}})).sessions, 's-'), of('s-', 'keep', 'stray'));
  const m = await op('list_membership', {ids: ['t-keep', 't-muted', 't-free', 's-keep', 's-muted', 's-free'], lists: [{name: 'tasks', filter: {excludeProjectId: ['muted']}}, {name: 'sessions', filter: {excludeProjectId: ['muted']}}]});
  assert.deepEqual(sorted(m.lists[0].rows, 't-'), of('t-', 'keep', 'free'), 'a changed task of an ignored project does not join the list');
  assert.deepEqual(sorted(m.lists[1].rows, 's-'), of('s-', 'keep', 'free'));

  // questions_list and executions_list ('-' is no project), artifact_list (null is no project).
  for (const [name, key, prefix] of [['questions_list', 'questions', 'q-'], ['executions_list', 'executions', 'e-']] as const) {
    assert.deepEqual(sorted((await op(name, {excludeProjectIds: ['muted']}))[key], prefix), of(prefix, 'keep', 'stray', 'free'), name);
    assert.deepEqual(sorted((await op(name, {excludeProjectIds: ['muted', '-']}))[key], prefix), of(prefix, 'keep', 'stray'), name + ' with -');
    assert.deepEqual(sorted((await op(name, {projectIds: ['keep', 'muted'], excludeProjectIds: ['muted']}))[key], prefix), [prefix + 'keep'], name + ' after projectIds');
    assert.deepEqual(sorted((await op(name, {}))[key], prefix), of(prefix, 'keep', 'muted', 'stray', 'free'), name + ' unfiltered');
  }
  const page = await op('executions_list', {excludeProjectIds: ['muted'], limit: 1});
  const rest = await op('executions_list', {excludeProjectIds: ['muted'], limit: 1000, after: page.nextAfter});
  assert.ok(![...page.executions, ...rest.executions].some((e: any) => e.id === 'e-muted'), 'paging keeps the exclusion');
  assert.deepEqual(sorted((await op('artifact_list', {excludeProjectIds: ['muted']})).artifacts, 'a-'), of('a-', 'keep', 'stray', 'free'));
  assert.deepEqual(sorted((await op('artifact_list', {excludeProjectIds: ['muted', null]})).artifacts, 'a-'), of('a-', 'keep', 'stray'));
  assert.deepEqual(sorted((await op('artifact_list', {projectIds: ['keep', 'muted'], excludeProjectIds: ['muted'], limit: 10})).artifacts, 'a-'), ['a-keep']);
});
