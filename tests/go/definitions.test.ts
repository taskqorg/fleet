// Outside tests of definitions in the store (docs/designs/fleet-definition-editors.md). HTTP only, against a coordinator on a
// fresh database. Run: FLEET_TRIAL_TESTS=tests/go/definitions.test.ts node tests/trial/run.ts
// Each test names its requirement and the risk it guards.
import {test} from 'node:test';
import assert from 'node:assert/strict';
import {readFileSync} from 'node:fs';

const BASE = process.env.FLEET_TRIAL_URL!, SECRET = process.env.FLEET_TRIAL_BROWSER_SECRET!;
if (!BASE || !SECRET) throw Error('Run through tests/trial/run.ts');
const OWNER = 'owner@trial.test', MEMBER = 'member@trial.test';

async function op(name: string, body: unknown, person = OWNER) {
  const r = await fetch(BASE + '/api/ops/' + name, {method: 'POST', headers: {authorization: 'Bearer ' + SECRET, 'x-fleet-person': person, 'content-type': 'application/json'}, body: JSON.stringify(body)});
  return {status: r.status, body: await r.json()};
}
const ok = (r: {status: number, body: any}) => { assert.equal(r.status, 200, JSON.stringify(r.body)); return r.body; };

test('D1 a fresh store imports every shipped definition as revision 1 from the repository (requirement: Where definitions live, Reconcile rule 1; risk: a fresh database starts without the seed, or a head differs from the file)', async () => {
  const agents = ok(await op('definition_list', {kind: 'agent'})).items;
  const pm = agents.find((x: any) => x.id === 'pm');
  assert.ok(pm, JSON.stringify(agents.map((x: any) => x.id)));
  assert.deepEqual([pm.revision, pm.state, pm.path], [1, 'synced', 'config/agents/pm.json']);
  assert.ok(pm.agent.model, 'agent facts');
  const g = ok(await op('definition_get', {kind: 'agent', id: 'pm'}));
  assert.equal(g.text, readFileSync('config/agents/pm.json', 'utf8'), 'the stored text is the shipped canonical file');
  assert.deepEqual([g.meta.source, g.meta.actor.kind, g.head], ['repo', 'system', 1]);
  assert.equal(g.content.agent.id, 'pm');
  const pipes = ok(await op('definition_list', {kind: 'pipeline'})).items;
  assert.ok(pipes.some((x: any) => x.id === 'hello-search'));
  assert.equal(ok(await op('definition_get', {kind: 'catalogue', id: 'catalogue'})).content.catalogue.capability.length > 0, true);
});

test('D2 definition reads are the architect\'s; unknown ones are 404 (requirement: Rights; risk: an operator reads or a typo reads another definition)', async () => {
  assert.equal((await op('definition_list', {kind: 'agent'}, MEMBER)).status, 403);
  assert.equal((await op('definition_get', {kind: 'agent', id: 'no-such'})).status, 404);
  assert.equal((await op('definition_get', {kind: 'agent', id: 'pm', revision: 9})).status, 404);
  assert.equal((await op('definition_list', {kind: 'skill'})).status, 400);
});

const content = (g: any) => JSON.parse(JSON.stringify(g.content));

test('D3 a save from the head is revision n+1; a second save from the same base is 409 base-moved with the head (requirement: Save and conflicts; risk: a silent overwrite of another person\'s save)', async () => {
  const g = ok(await op('definition_get', {kind: 'pipeline', id: 'hello-search'}));
  const c = content(g); c.pipeline.steps[0].arguments = {...c.pipeline.steps[0].arguments, extra: 'd3'};
  const s = ok(await op('definition_save', {kind: 'pipeline', id: 'hello-search', baseRevision: g.head, content: c, message: 'D3 edit'}));
  assert.deepEqual([s.revision.revision, s.revision.source, s.revision.actor.person.email, s.state], [g.head + 1, 'editor', OWNER, 'ahead']);
  const again = await op('definition_save', {kind: 'pipeline', id: 'hello-search', baseRevision: g.head, content: c, message: 'D3 again'});
  assert.deepEqual([again.status, again.body.reason, again.body.head], [409, 'base-moved', g.head + 1]);
  assert.equal((await op('definition_save', {kind: 'pipeline', id: 'hello-search', baseRevision: g.head + 1, content: c, message: 'same'})).body.reason, 'no-change');
  const h = ok(await op('definition_history', {kind: 'pipeline', id: 'hello-search'}));
  assert.deepEqual(h.revisions.map((r: any) => r.revision), [g.head + 1, g.head]);
  const d = ok(await op('definition_diff', {kind: 'pipeline', id: 'hello-search', from: {repository: true}, to: {revision: g.head + 1}}));
  assert.ok(d.unified.includes('+') && d.fields.some((f: any) => f.path.endsWith('/arguments/extra') && f.op === 'add'), JSON.stringify(d.fields));
});

test('D4 two concurrent saves from one base: one revision, one 409, no 500 (requirement: Save and conflicts, the row lock; risk: two heads or a server error)', async () => {
  const g = ok(await op('definition_get', {kind: 'agent', id: 'claude-reviewer'}));
  const edit = (n: string) => { const c = content(g); c.agent.instructions = (c.agent.instructions ?? '') + '\n' + n; return op('definition_save', {kind: 'agent', id: 'claude-reviewer', baseRevision: g.head, content: c, message: n}); };
  // The bytes sent are what the validator sees (risk: a misspelled key dropped by a typed decode and saved as missing).
  const typo = content(g); typo.agent.intructions = 'x';
  const p = ok(await op('definition_draft_put', {kind: 'agent', id: 'claude-reviewer', baseRevision: g.head, content: typo}));
  assert.ok(p.problems.length === 1 && p.problems[0].message.includes('intructions'), JSON.stringify(p.problems));
  ok(await op('definition_draft_discard', {kind: 'agent', id: 'claude-reviewer'}));
  const rs = await Promise.all([edit('one'), edit('two')]);
  assert.deepEqual(rs.map(r => r.status).sort(), [200, 409], JSON.stringify(rs.map(r => r.body)));
  assert.equal(ok(await op('definition_get', {kind: 'agent', id: 'claude-reviewer'})).head, g.head + 1);
});

test('D5 a draft is the caller\'s and is dropped by the save; create makes a store-only definition that export patches in (requirement: drafts, definition_create, definition_export; risk: a lost draft, or an export that does not apply)', async () => {
  const g = ok(await op('definition_get', {kind: 'pipeline', id: 'search-basic'}));
  const c = content(g); c.pipeline.steps[0].arguments = {...c.pipeline.steps[0].arguments, extra: 'd5'};
  const p = ok(await op('definition_draft_put', {kind: 'pipeline', id: 'search-basic', baseRevision: g.head, content: c}));
  assert.deepEqual([p.draft.baseRevision, p.draft.stale, p.draft.changes, p.problems], [g.head, false, 1, []]);
  assert.equal(ok(await op('definition_draft_get', {kind: 'pipeline', id: 'search-basic'})).content.pipeline.steps[0].arguments.extra, 'd5');
  assert.equal((await op('definition_draft_get', {kind: 'pipeline', id: 'search-basic'}, MEMBER)).status, 403);
  ok(await op('definition_save', {kind: 'pipeline', id: 'search-basic', baseRevision: g.head, content: c, message: 'D5'}));
  assert.equal((await op('definition_draft_get', {kind: 'pipeline', id: 'search-basic'})).status, 404, 'the save dropped the draft');
  const n = content(g); n.pipeline.id = 'd5-new';
  const made = ok(await op('definition_create', {kind: 'pipeline', id: 'd5-new', content: n, message: 'D5 new'}));
  assert.equal(made.revision.revision, 1);
  assert.equal((await op('definition_create', {kind: 'pipeline', id: 'd5-new', content: n, message: 'again'})).body.reason, 'wrong-state');
  assert.equal(ok(await op('definition_list', {kind: 'pipeline'})).items.find((x: any) => x.id === 'd5-new').state, 'store-only');
  const e = ok(await op('definition_export', {}));
  assert.ok(e.files.some((f: any) => f.path === 'config/pipelines/d5-new.json') && e.files.some((f: any) => f.path === 'config/pipelines/search-basic.json'));
  const {execFileSync} = await import('node:child_process');
  execFileSync('git', ['apply', '--check', '-'], {input: e.patch}); // applies to this checkout's config/
});

test('D6 runs read definitions from the store: a pipeline that exists only as a store revision runs (requirement: Resolve, readers read the head; risk: a saved definition has no effect until a release)', async () => {
  const g = ok(await op('definition_get', {kind: 'pipeline', id: 'hello-search'}));
  const n = content(g); n.pipeline.id = 'd6-store';
  ok(await op('definition_create', {kind: 'pipeline', id: 'd6-store', content: n, message: 'D6'}));
  const r = await fetch(BASE + '/api/pipelines/d6-store/runs', {method: 'POST', headers: {authorization: 'Bearer ' + SECRET, 'x-fleet-person': OWNER, 'content-type': 'application/json'}, body: JSON.stringify({requestId: 'd6', example: {}})});
  assert.equal(r.status, 200, await r.text());
  assert.equal((await fetch(BASE + '/api/pipelines/no-such/runs', {method: 'POST', headers: {authorization: 'Bearer ' + SECRET, 'x-fleet-person': OWNER, 'content-type': 'application/json'}, body: JSON.stringify({requestId: 'd6x', example: {}})})).status, 404);
});

test('D7 an execution pins the revision it resolved; a later save does not move it (requirement: failure mode 2, pinned revisions; risk: a run reports or uses a definition it did not start with)', async () => {
  const run = async (rid: string) => {
    const r = await fetch(BASE + '/api/pipelines/typed-decision/runs', {method: 'POST', headers: {authorization: 'Bearer ' + SECRET, 'x-fleet-person': OWNER, 'content-type': 'application/json'}, body: JSON.stringify({requestId: rid, example: {}})});
    assert.equal(r.status, 200); return (await r.json()).id as string;
  };
  const refOf = async (id: string) => ok(await op('executions_list', {})).executions.find((e: any) => e.id === id).definitions;
  const g = ok(await op('definition_get', {kind: 'pipeline', id: 'typed-decision'}));
  const a = await run('d7a');
  assert.deepEqual((await refOf(a)).map((d: any) => [d.kind, d.id, d.revision, d.contentSha]), [['pipeline', 'typed-decision', g.head, g.meta.contentSha]]);
  const c = content(g); c.pipeline.steps[0].arguments = {...c.pipeline.steps[0].arguments, d7: true};
  ok(await op('definition_save', {kind: 'pipeline', id: 'typed-decision', baseRevision: g.head, content: c, message: 'D7'}));
  const b = await run('d7b');
  assert.equal((await refOf(a))[0].revision, g.head, 'the earlier run keeps its revision');
  assert.equal((await refOf(b))[0].revision, g.head + 1);
});

test('D8 pipeline_run_create runs the head or the caller\'s draft and lists what it resolved (requirement: pipeline_run_create, draft runs; risk: a test from the editor runs the saved version, or an operator runs unsaved content)', async () => {
  const g = ok(await op('definition_get', {kind: 'pipeline', id: 'judge-scale'}));
  const head = ok(await op('pipeline_run_create', {pipelineId: 'judge-scale', requestId: 'd8-head'}));
  assert.deepEqual(head.definitions.map((d: any) => [d.id, d.revision, d.draft ?? false]), [['judge-scale', g.head, false]]);
  const c = content(g); c.pipeline.steps[0].prompt = 'd8 draft prompt';
  ok(await op('definition_draft_put', {kind: 'pipeline', id: 'judge-scale', baseRevision: g.head, content: c}));
  const draft = ok(await op('pipeline_run_create', {pipelineId: 'judge-scale', requestId: 'd8-draft', draft: true}));
  assert.deepEqual(draft.definitions.map((d: any) => [d.id, d.revision, d.draft]), [['judge-scale', g.head, true]]);
  // A client retry after a cut reply (app writeOnce) sends the same requestId: it answers the same runs, never new ones.
  assert.equal(ok(await op('pipeline_run_create', {pipelineId: 'judge-scale', requestId: 'd8-head'})).runId, head.runId, 'a repeated requestId is the same run');
  assert.equal(ok(await op('pipeline_run_create', {pipelineId: 'judge-scale', requestId: 'd8-draft', draft: true})).runId, draft.runId, 'a repeated draft requestId is the same run');
  assert.notEqual(draft.definitions[0].contentSha, g.meta.contentSha);
  assert.equal((await op('pipeline_run_create', {pipelineId: 'judge-scale', requestId: 'd8-m', draft: true}, MEMBER)).status, 403);
  assert.equal((await op('pipeline_run_create', {pipelineId: 'no-such', requestId: 'd8-x'})).status, 404);
});

test('D9 pipeline_rerun starts a run of a finished run on the current definition and is idempotent by requestId (requirement: pipeline_rerun; risk: a rerun of a running run, or one retried twice)', async () => {
  const first = ok(await op('pipeline_run_create', {pipelineId: 'hello-search', requestId: 'd9', example: {}}));
  let state = '';
  for (let i = 0; i < 50 && state !== 'failed'; i++) { state = ok(await op('pipeline_get', {runId: first.runId})).state; if (state !== 'failed') await new Promise(r => setTimeout(r, 100)); }
  assert.equal(state, 'failed', 'a missing example reference fails step one');
  const a = ok(await op('pipeline_rerun', {runId: first.runId, mode: 'failed', requestId: 'd9-r'}));
  assert.notEqual(a.runId, first.runId);
  assert.deepEqual(a.reused, [], 'the failed step reruns');
  assert.equal(ok(await op('pipeline_rerun', {runId: first.runId, mode: 'failed', requestId: 'd9-r'})).runId, a.runId);
  assert.equal(ok(await op('pipeline_get', {runId: a.runId})).rerunOf, first.runId);
  assert.equal((await op('pipeline_rerun', {runId: first.runId, mode: 'some'})).status, 400);
  assert.equal((await op('pipeline_rerun', {runId: 'no-such', mode: 'all'})).status, 404);
});

test('D10 definition_usage counts the runs that pinned a definition, per revision, and names the definitions that use it (requirement: definition_usage; risk: the Usage tab and the list show runs of another definition or revision)', async () => {
  const before = ok(await op('definition_usage', {kind: 'pipeline', id: 'web-research'}));
  ok(await op('pipeline_run_create', {pipelineId: 'web-research', requestId: 'd10', example: {}}));
  const u = ok(await op('definition_usage', {kind: 'pipeline', id: 'web-research'}));
  assert.equal(u.total.runs, before.total.runs + 1);
  assert.ok(u.byRevision.some((r: any) => r.revision === 1 && r.window.runs >= 1), JSON.stringify(u.byRevision));
  assert.ok(ok(await op('definition_list', {kind: 'pipeline'})).items.find((x: any) => x.id === 'web-research').last30.runs >= 1);
  // hello-search is a catalogue method's pipeline; design-rules a method's agentProfile.
  assert.deepEqual(ok(await op('definition_usage', {kind: 'pipeline', id: 'hello-search'})).usedBy, [{kind: 'catalogue', id: 'catalogue'}]);
  assert.ok(ok(await op('definition_usage', {kind: 'agent', id: 'design-rules'})).usedBy.some((k: any) => k.kind === 'catalogue'));
  // A draft run is not a run of its base revision.
  const g = ok(await op('definition_get', {kind: 'pipeline', id: 'web-research'}));
  const c = content(g); c.pipeline.steps[0].id = c.pipeline.steps[0].id + '-d10';
  ok(await op('definition_draft_put', {kind: 'pipeline', id: 'web-research', baseRevision: g.head, content: c}));
  ok(await op('pipeline_run_create', {pipelineId: 'web-research', requestId: 'd10-draft', draft: true, example: {}}));
  assert.equal(ok(await op('definition_usage', {kind: 'pipeline', id: 'web-research'})).total.runs, u.total.runs);
  assert.equal((await op('definition_usage', {kind: 'pipeline', id: 'no-such'})).status, 404);
});

test('D11 the Agents tabs: effective layers and result, compatibility rows, harness models; architect only (requirement: agent_effective, agent_compatibility, harness_models; risk: the Effective tab shows another profile than the start sends, or an incompatible label without a run)', async () => {
  const e = ok(await op('agent_effective', {id: 'pm', projectId: 'desiign'}));
  assert.deepEqual(e.layers.map((l: any) => l.name), ['role', 'definition', 'project']);
  assert.ok(e.layers[0].fields.includes('role:pm'));
  assert.equal(e.result.id, 'pm');
  const c = ok(await op('agent_compatibility', {id: 'pm'}));
  assert.ok(c.rows.length >= 1 && c.rows.every((r: any) => ['checked', 'failed', 'unchecked'].includes(r.state)));
  assert.ok(ok(await op('harness_models', {harness: 'codex'})).models.some((m: any) => m.id === 'gpt-6.1-sol' && m.source === 'shipped'));
  assert.equal((await op('agent_effective', {id: 'pm', projectId: 'desiign'}, MEMBER)).status, 403);
  assert.equal((await op('agent_effective', {id: 'pm', projectId: 'no-such'})).status, 404);
});
