// Outside regression for typed publication definitions. HTTP only, fresh PostgreSQL.
// Run: FLEET_TRIAL_TESTS=tests/go/publication-definition.test.ts node tests/trial/run.ts
import {test} from 'node:test';
import assert from 'node:assert/strict';

const BASE = process.env.FLEET_TRIAL_URL!, SECRET = process.env.FLEET_TRIAL_BROWSER_SECRET!;
if (!BASE || !SECRET) throw Error('Run through tests/trial/run.ts');
async function op(name: string, body: unknown) {
  const r = await fetch(BASE + '/api/ops/' + name, {method: 'POST', headers: {authorization: 'Bearer ' + SECRET, 'x-fleet-person': 'owner@trial.test', 'content-type': 'application/json'}, body: JSON.stringify(body)});
  return {status: r.status, body: await r.json() as any};
}
const ok = (r: {status: number, body: any}) => {assert.equal(r.status, 200, JSON.stringify(r.body)); return r.body;};
const unchanged = [{id: 'search', kind: 'api', connection: 'tavily', tool: 'web_search', arguments: {}}, {id: 'build', kind: 'script', command: 'true'}];
const key = {kind: 'pipeline', id: 'publication-contract-roundtrip'};
const mapping = {'index.html': 'build/index.html', 'main.css': 'build/main.css'};
function check(g: any, files: Record<string, string>) {
  assert.deepEqual(g.content.pipeline, JSON.parse(g.text), 'typed and raw pipeline are identical');
  assert.deepEqual(g.content.pipeline.steps[2].files, files, 'exact publication names and source paths survive');
  assert.deepEqual(g.content.pipeline.steps.slice(0, 2), unchanged, 'old step shapes do not acquire files');
}

test('Publication definition create/get/draft/save/history/export preserves files; requirement: pipeline format v2 and typed definition contracts; risk: typed projection silently drops publication sources or a roundtrip changes earlier steps', async () => {
  const shipped = ok(await op('definition_get', {kind: 'pipeline', id: 'publish-static'}));
  assert.deepEqual(shipped.content.pipeline, JSON.parse(shipped.text), 'shipped typed projection includes files');
  const content = {pipeline: {id: key.id, version: 2, steps: [...unchanged, {id: 'publish', kind: 'publication', files: mapping}]}};
  assert.equal(ok(await op('definition_create', {...key, content, message: 'publication contract fixture'})).revision.revision, 1);
  check(ok(await op('definition_get', key)), mapping);
  const changed = structuredClone(content);
  changed.pipeline.steps[2] = {id: 'publish', kind: 'publication', files: {'index.html': 'build/updated.html', 'main.css': 'build/updated.css'}};
  assert.deepEqual(ok(await op('definition_draft_put', {...key, baseRevision: 1, content: changed})).problems, []);
  const draft = ok(await op('definition_draft_get', key));
  check(draft, changed.pipeline.steps[2].files!);
  assert.equal(ok(await op('definition_save', {...key, baseRevision: 1, content: draft.content, message: 'save typed draft'})).revision.revision, 2);
  check(ok(await op('definition_get', key)), changed.pipeline.steps[2].files!);
  check(ok(await op('definition_get', {...key, revision: 1})), mapping);
  assert.deepEqual(ok(await op('definition_history', key)).revisions.map((x: any) => x.revision), [2, 1]);
  const diff = ok(await op('definition_diff', {...key, from: {revision: 1}, to: {revision: 2}}));
  assert.ok(diff.fields.some((x: any) => x.path.endsWith('/files/index.html')), JSON.stringify(diff));
  const exported = ok(await op('definition_export', {keys: [key]})).files.find((x: any) => x.path === 'config/pipelines/' + key.id + '.json');
  assert.deepEqual(JSON.parse(exported.text), changed.pipeline, 'mirror/export uses the same retained mapping');
});

test('Publication definition rejects malformed maps and output traversal without moving its head; requirement: bounded publisher file contract; risk: bad typed data is stripped into a different valid definition or escapes the publication root', async () => {
  const good = ok(await op('definition_get', key));
  for (const [name, files] of Object.entries({number: {'index.html': 42}, array: ['index.html'], traversal: {'index.html': 'build/index.html', '../evil.css': 'build/main.css'}, missingIndex: {'main.css': 'build/main.css'}})) {
    const content = structuredClone(good.content);
    content.pipeline.steps[2].files = files;
    const status = name === 'number' || name === 'array' ? 400 : 409;
    const saved = await op('definition_save', {...key, baseRevision: good.head, content, message: name});
    assert.equal(saved.status, status, JSON.stringify(saved));
    if (status === 409) assert.equal(saved.body.reason, 'invalid-draft');
    const copy = structuredClone(content); copy.pipeline.id = 'publication-invalid-' + name.toLowerCase();
    const made = await op('definition_create', {kind: 'pipeline', id: copy.pipeline.id, content: copy, message: name});
    assert.equal(made.status, status, JSON.stringify(made));
    if (status === 409) assert.equal(made.body.reason, 'invalid-draft');
  }
  check(ok(await op('definition_get', key)), good.content.pipeline.steps[2].files);
  assert.equal(ok(await op('definition_get', key)).head, good.head);
});
