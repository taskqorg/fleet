import assert from 'node:assert/strict';
import {createHash} from 'node:crypto';
import {test} from 'node:test';
import {startCoordinator} from '../harness.ts';

test('Pipeline MCP reply bounds and continuation (requirement: T-FLT-52; risk: escaped replies exceed the transport limit or pagination loses runs)', async t => {
  const c = await startCoordinator({name: 'pipeline-mcp'});
  t.after(() => c.stop());
  const projectId = 'hello-world';
  const cap = 16 * 1024 * 1024;
  const sha256 = 'a'.repeat(64);
  const signature = createHash('sha256').update(JSON.stringify({
    invocation: 'pipeline', methodId: null, projectId, query: 'matching query',
  })).digest('hex');

  // Backslashes double in both the stored JSON string and the outer MCP text.
  // Two medium runs overflow one frame. The large run overflows by itself.
  c.sql(`
    INSERT INTO execution(id, kind, state, project_id, data)
    SELECT id, 'pipeline_run', 'succeeded', project_id,
      jsonb_build_object('id', id, 'pipelineId', 'search-basic', 'projectId', project_id,
        'requestId', id, 'signature', '${signature}', 'planVersion', 1,
        'example', repeat(chr(92), payload_size),
        'canonicalResult', '{"artifact":{"artifactId":"fixture-result","sha256":"${sha256}"},
          "inputs":[],"mode":"finalizer","producerExecutionId":"fixture-step",
          "selectedSources":[],"stepId":"result"}'::jsonb)
    FROM (VALUES (1, 'medium-one', '${projectId}', 2200000),
                 (2, 'other-project', 'desiign', 0),
                 (3, 'medium-two', '${projectId}', 2200000),
                 (4, 'large-run', '${projectId}', 4500000),
                 (5, 'small-run', '${projectId}', 0)) AS seed(ord, id, project_id, payload_size)
    ORDER BY ord;
    INSERT INTO pipeline_run(run_id, definition_sha256, revision)
    SELECT id, '${sha256}', 1 FROM execution WHERE kind = 'pipeline_run';
  `);

  async function request(path: string, parameters: object, person = 'owner@trial.test') {
    const response = await fetch(c.base + path, {
      method: 'POST',
      headers: {authorization: `Bearer ${c.secret}`, 'x-fleet-person': person, 'content-type': 'application/json'},
      body: JSON.stringify(parameters),
    });
    return {status: response.status, text: await response.text()};
  }

  async function mcp(name: string, args: object, person?: string) {
    return request('/api/project-tools', {
      jsonrpc: '2.0', id: 1, method: 'tools/call', params: {name, arguments: {projectId, ...args}},
    }, person);
  }

  function bounded(reply: Awaited<ReturnType<typeof mcp>>) {
    assert.equal(reply.status, 200, reply.text.slice(0, 200));
    assert.ok(Buffer.byteLength(reply.text) < cap, 'complete MCP frame stays under 16 MiB');
    return JSON.parse(JSON.parse(reply.text).result.content[0].text);
  }

  const first = bounded(await mcp('pipeline_history', {limit: 4}));
  assert.deepEqual(first.runs.map((run: any) => run.id), ['medium-one']);
  assert.equal(first.runs[0].example.length, 2200000);
  assert.equal(typeof first.nextCursor, 'string');

  const second = bounded(await mcp('pipeline_history', {limit: 2, cursor: first.nextCursor}));
  assert.deepEqual(second.runs.map((run: any) => run.id), ['medium-two', 'large-run']);
  assert.equal(second.runs[1].truncated, true);
  assert.equal(second.runs[1].plan, undefined);
  const last = bounded(await mcp('pipeline_history', {cursor: second.nextCursor}));
  assert.deepEqual(last.runs.map((run: any) => run.id), ['small-run']);
  assert.equal(last.nextCursor, undefined);
  assert.deepEqual(bounded(await mcp('pipeline_history', {projectId: 'desiign', cursor: second.nextCursor})), {runs: []});
  assert.equal((await mcp('pipeline_history', {cursor: 'bad'})).status, 400);
  assert.equal((await mcp('pipeline_history', {limit: 101})).status, 400);
  assert.equal((await mcp('pipeline_history', {cursor: first.nextCursor}, 'outsider@trial.test')).status, 404);

  const invoke = (requestId: string) => mcp('pipeline_run', {requestId, query: 'matching query'});
  const summary = bounded(await invoke('large-run'));
  assert.equal(summary.id, 'large-run');
  assert.equal(summary.state, 'succeeded');
  assert.equal(summary.truncated, true);
  assert.deepEqual(summary, second.runs[1]);
  assert.deepEqual(summary.resultArtifact, {artifactId: 'fixture-result', sha256});
  assert.equal(summary.example, undefined);
  const small = bounded(await invoke('small-run'));
  assert.ok(small.plan);
  assert.equal(small.truncated, undefined);

  const detail = await request('/api/ops/pipeline_get', {runId: summary.id, projectId});
  assert.equal(detail.status, 200, detail.text.slice(0, 200));
  assert.equal(JSON.parse(detail.text).example.length, 4500000);
  assert.ok(Buffer.byteLength(JSON.stringify({result: {content: [{type: 'text', text: detail.text}]}})) >= cap);
});
