// Outside tests of the TypeScript routes and operations the Go rewrite had dropped (go/coordinator/b_restored.go).
//   FLEET_TRIAL_TESTS=tests/go/restored.test.ts FLEET_TRIAL_SERVER=go/bin/fleet-coordinator node tests/trial/run.ts
import {test} from 'node:test';
import assert from 'node:assert/strict';
import {randomUUID} from 'node:crypto';

const BASE = process.env.FLEET_TRIAL_URL!, SECRET = process.env.FLEET_TRIAL_BROWSER_SECRET!;
const owner = {authorization: 'Bearer ' + SECRET, 'x-fleet-person': 'owner@trial.test'};
async function call(method: string, path: string, body?: unknown) {
  const r = await fetch(BASE + path, {method, headers: {...(body === undefined ? {} : {'content-type': 'application/json'}), ...owner}, body: body === undefined ? undefined : JSON.stringify(body)});
  const text = await r.text(); let parsed: any = text; try { parsed = JSON.parse(text); } catch {}
  return {status: r.status, body: parsed};
}
async function queued() {
  const sessionId = (await call('POST', '/api/sessions', {})).body.sessionId as string;
  const e = await call('POST', `/api/sessions/${sessionId}/messages`, {requestId: randomUUID(), text: 'hello'});
  assert.equal(e.status, 200, JSON.stringify(e.body));
  return {sessionId, executionId: e.body.id as string};
}

test('R1 execution differences: route and operation compare two executions of one session and refuse another session (risk: the Go rewrite drops a route the TypeScript coordinator served)', async () => {
  // A second message queues behind the first and is not an execution yet: compare the execution with itself.
  const a = await queued(), b = a, other = await queued();
  const r = await call('GET', `/api/executions/${b.executionId}/differences?previous=${a.executionId}`);
  assert.equal(r.status, 200, JSON.stringify(r.body));
  assert.deepEqual(r.body, {sessionId: a.sessionId, previousExecutionId: a.executionId, executionId: b.executionId, files: []});
  assert.equal((await call('POST', '/api/ops/execution_differences', {executionId: b.executionId, previous: a.executionId})).status, 200);
  assert.equal((await call('GET', `/api/executions/${b.executionId}/differences?previous=${other.executionId}`)).status, 403);
});

test('R2 GET /api/reconciliation returns the latest reconcile report (risk: the report is written but no route reads it)', async () => {
  assert.equal((await call('POST', '/api/ops/reconcile', {})).status, 200);
  const r = await call('GET', '/api/reconciliation');
  assert.equal(r.status, 200, JSON.stringify(r.body));
  assert.ok(r.body.reconciliation && typeof r.body.reconciliation === 'object', JSON.stringify(r.body));
});

test('R3 /test-sites refuses unknown sites and file names (risk: the sandboxed example-site route is missing or serves arbitrary paths)', async () => {
  assert.equal((await call('GET', '/test-sites/other/index.html')).status, 404);
  assert.equal((await call('GET', '/test-sites/hello-world/..%2Fx.html')).status, 404);
});
