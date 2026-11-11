// Requirement: fleet.md Verification; risk: an incomplete candidate diff silently omits behavioral protection.
import {test} from 'node:test';
import assert from 'node:assert/strict';
import {comparison, changedPaths, select, makePlan, families, verifyEvidence} from '../../scripts/test-selection.ts';
const base = 'a'.repeat(40), head = 'b'.repeat(40);
test('complete group uses event base and unions UI and coordinator consumers', () => {
  assert.equal(comparison('merge_group', {merge_group: {base_sha: base, head_sha: head}}, head), base);
  assert.deepEqual(select(['app/src/views/Lab.tsx', 'go/coordinator/routes.go']).selected, [...families]);
  assert.throws(() => comparison('merge_group', {merge_group: {base_sha: base, head_sha: base}}, head));
});
test('renamed/deleted paths retain old consumers and unknown/config/test changes broaden', () => {
  const paths = changedPaths('R100\0go/coordinator/access.go\0docs/access.md\0D\0packages/view-url/view-url.ts\0');
  assert.deepEqual(paths, ['go/coordinator/access.go', 'docs/access.md', 'packages/view-url/view-url.ts']);
  assert.deepEqual(select(paths).selected, [...families]);
  for (const path of ['config/agents/a.md', 'contracts/a.json', 'packages/view-url/view-url.ts', 'tests/ui/new.test.ts', '.github/workflows/ci.yml', 'unmapped'])
    assert.deepEqual(select([path]).selected, [...families]);
});
test('docs-only proposes no behavioral family; shared UI retains browser and app', () => {
  assert.deepEqual(select(['docs/example.md']).selected, []);
  assert.deepEqual(select(['packages/ui/src/Button.tsx']).selected, ['app', 'browser']);
});
test('missing base, malformed diff, unavailable history and checkout mismatch cannot skip', () => {
  for (const diff of ['', 'A\0', 'X\0file\0', 'A\0../outside\0', 'A\0file']) assert.throws(() => changedPaths(diff));
  assert.throws(() => comparison('push', {before: '0'.repeat(40), after: head}, head));
  for (const git of [() => {throw Error('missing history')}, () => base]) {
    const plan = makePlan('push', {before: base, after: head}, head, git);
    assert.deepEqual(plan.selected, [...families]);
    assert.deepEqual(plan.executed, [...families]);
  }
});
test('shadow always executes full suites even when impact selection proposes docs only', () => {
  const plan = makePlan('push', {before: base, after: head}, head, args => args[0] === 'rev-parse' ? head : args[0] === 'diff' ? 'M\0docs/example.md\0' : '');
  assert.deepEqual(plan.selected, []);
  assert.deepEqual(plan.executed, [...families]);
});

test('release rejects missing, failed or mismatched candidate evidence', () => {
  const plan = makePlan('push', {before: base, after: head}, head, args => args[0] === 'rev-parse' ? head : args[0] === 'diff' ? 'M\0docs/example.md\0' : '');
  const jobs = Object.fromEntries(['selection', ...families].map(f => [f, {result: 'success'}]));
  assert.doesNotThrow(() => verifyEvidence(plan, head, jobs));
  assert.throws(() => verifyEvidence(plan, base, jobs));
  assert.throws(() => verifyEvidence({...plan, policy: 'unknown'}, head, jobs));
  assert.throws(() => verifyEvidence({...plan, executed: []}, head, jobs));
  assert.throws(() => verifyEvidence(plan, head, {...jobs, go: {result: 'failure'}}));
  const {browser, ...missing} = jobs;
  assert.throws(() => verifyEvidence(plan, head, missing));
});
