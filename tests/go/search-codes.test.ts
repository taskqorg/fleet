// Requirement: OR-32, OR-36, D17: Spotlight jumps by code (old codes too), finds questions, and every list search uses one syntax.
// Risk: a renamed project's old code finds nothing, a field term is ignored by a list, or a question leaks across scopes.
import {test} from 'node:test';
import assert from 'node:assert/strict';
import {startCoordinator} from '../harness.ts';

// R2, R7 (OR-32, OR-36, D17); risk: code jump broken by a rename, list search ignores agent:/host:, a question leaks to an outsider.
test('search resolves codes and aliases, finds questions; task_page and sessions_list take the search syntax', {timeout: 30_000}, async t => {
  const c = await startCoordinator({name: 'searchcodes'}); t.after(() => c.stop());
  const raw = async (name: string, body: object, who = 'owner@trial.test') => {
    const r = await fetch(c.base + '/api/ops/' + name, {method: 'POST', headers: {authorization: 'Bearer ' + c.secret, 'x-fleet-person': who, 'content-type': 'application/json'}, body: JSON.stringify(body)});
    return {status: r.status, body: await r.json() as any};
  };
  const op = async (name: string, body: object, who?: string) => { const r = await raw(name, body, who); assert.equal(r.status, 200, JSON.stringify(r.body)); return r.body; };
  const a = await op('task_create', {projectId: 'hello-world', title: 'Deploy pipeline flaky'});
  const b = await op('task_create', {projectId: 'hello-world', title: 'Deploy docs'});
  c.sql(`INSERT INTO question(id,kind,project_id,task_id,text) VALUES('q-search','operator_decision','hello-world','${a.id}','Which colour for the deploy badge?');
    INSERT INTO execution(id,kind,state,task_id,harness,data) VALUES('e-a','agent_run','succeeded','${a.id}','claude','{}');
    UPDATE task_derived SET agent='claude',host='mac-mini' WHERE task_id='${a.id}';`);
  const hits = async (query: string, who?: string) => (await op('search', {query}, who)).hits;
  // Code jump: the task first, with its code; the task provider does not repeat it.
  const byCode = await hits(a.code.toLowerCase());
  assert.equal(byCode[0].id, a.id); assert.equal(byCode[0].code, a.code);
  assert.equal(byCode.filter((h: any) => h.id === a.id).length, 1);
  assert.equal(byCode[0].agent, 'claude', 'a code hit carries the task row facts');
  assert.deepEqual(await hits('agent:nobody ' + a.code), [], 'terms apply to a code too');
  const q = (await hits('colour badge')).find((h: any) => h.kind === 'question');
  assert.equal(q?.id, 'q-search'); assert.equal(q.taskId, a.id); assert.match(q.code, /^Q-/);
  assert.equal((await hits(q.code))[0].id, 'q-search');
  const sess = (await op('session_create', {})).sessionId;
  const sc = (await op('sessions_list', {})).sessions.find((x: any) => x.id === sess).code;
  const conv = await hits(sc);
  assert.equal(conv[0]?.kind, 'conversation'); assert.equal(conv[0].id, sess);
  // An old code after a project rename resolves to the entity with its current code.
  await op('project_code_set', {projectId: 'hello-world', code: 'HWX'});
  const old = await hits(a.code);
  assert.equal(old[0].id, a.id); assert.match(old[0].code, /^T-HWX-/);
  // One syntax: task_page and sessions_list apply field terms and every word.
  const page = async (search: string) => (await op('task_page', {filter: {search}})).tasks.map((x: any) => x.id).sort();
  c.sql(`INSERT INTO execution(id,kind,state,task_id,harness,data) VALUES('e-done','agent_run','succeeded','${b.id}','codex','{}')`);
  assert.deepEqual(await page('agent:codex'), [b.id], 'agent: is the latest execution harness, finished runs too');
  assert.equal((await hits('agent:codex')).find((h: any) => h.id === b.id)?.agent, 'codex', 'a hit shows the agent it matched on');
  assert.deepEqual(await page('deploy agent:claude'), [a.id]);
  assert.deepEqual(await page('deploy host:mac'), [a.id]);
  assert.deepEqual(await page('docs deploy'), [b.id]);
  const counted = await op('task_page', {filter: {search: 'deploy state:completed'}});
  assert.equal(counted.tasks.length, 0); assert.equal(counted.counts.queued, 2, 'a state term does not zero the other states\' counts');
  assert.deepEqual(await page('deploy kind:x'), [], 'an unknown field stays free text, which no title holds');
  assert.equal((await op('sessions_list', {filter: {search: 'agent:nobody'}})).total, 0);
  // An outsider finds neither the code nor the question.
  assert.deepEqual(await hits(old[0].code, 'outsider@trial.test'), []);
  assert.deepEqual(await hits('colour', 'outsider@trial.test'), []);
});
