// Requirement: fleet.md Verification; risk: an incomplete candidate diff silently omits behavioral protection.
import {test} from 'node:test';
import assert from 'node:assert/strict';
import {execFileSync} from 'node:child_process';
import {mkdtempSync, mkdirSync, writeFileSync, rmSync} from 'node:fs';
import {tmpdir} from 'node:os';
import {join} from 'node:path';
import {fileURLToPath} from 'node:url';
import {comparison, changedPaths, select, makePlan, families, verifyEvidence, execution, previousResult} from '../../scripts/test-selection.ts';
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
test('unqualified history executes full suites even when impact selection proposes docs only', () => {
  const plan = makePlan('push', {before: base, after: head}, head, args => args[0] === 'rev-parse' ? head : args[0] === 'diff' ? 'M\0docs/example.md\0' : '');
  assert.deepEqual(plan.selected, []);
  assert.deepEqual(plan.executed, [...families]);
});

test('release rejects missing, failed or mismatched candidate evidence', () => {
  const plan = makePlan('push', {before: base, after: head}, head, args => args[0] === 'rev-parse' ? head : args[0] === 'diff' ? 'M\0docs/example.md\0' : '');
  const jobs: Record<string, {result: string}> = {...Object.fromEntries(['selection', ...families].map(f => [f, {result: 'success'}])), thin: {result: 'skipped'}};
  assert.doesNotThrow(() => verifyEvidence(plan, head, jobs));
  assert.throws(() => verifyEvidence(plan, base, jobs));
  assert.throws(() => verifyEvidence({...plan, policy: 'unknown'}, head, jobs));
  assert.throws(() => verifyEvidence({...plan, executed: []}, head, jobs));
  assert.throws(() => verifyEvidence(plan, head, {...jobs, go: {result: 'failure'}}));
  const {browser, ...missing} = jobs;
  assert.throws(() => verifyEvidence(plan, head, missing));
});

test('qualified module unions preserve archive gates and prior failure or rollback restores full', () => {
  assert.deepEqual(execution(['docs/example.md'], true, false), []);
  assert.deepEqual(execution(['app/src/views/lab-create.tsx'], true, false), ['app', 'browser']);
  for (const paths of [['go/obs/obs.go'], ['packages/view-url/view-url.ts'], ['app/src/views/lab-create.tsx', 'go/coordinator/access.go']])
    assert.deepEqual(execution(paths, true, false), [...families]);
  assert.deepEqual(execution(['docs/example.md'], false, false), [...families]);
  assert.deepEqual(execution(['docs/example.md'], true, true), [...families]);
  assert.equal(previousResult([{databaseId: 1, status: 'completed', conclusion: 'failure'}], 2), false);
  assert.equal(previousResult([{databaseId: 1, status: 'in_progress', conclusion: ''}], 2), false);
  assert.equal(previousResult([{databaseId: 1, status: 'completed', conclusion: 'success'}], 2), true);
  assert.equal(previousResult([{databaseId: 2, status: 'completed', conclusion: 'success'}], 2), null);
});
test('qualified omitted families require actual skipped results and cannot hide selected failure', () => {
  const plan = makePlan('push', {before: base, after: head}, head, args => args[0] === 'rev-parse' ? head : args[0] === 'diff' ? 'M\0docs/example.md\0' : '', true);
  const jobs = {selection: {result: 'success'}, thin: {result: 'skipped'}, ...Object.fromEntries(families.map(f => [f, {result: 'skipped'}]))};
  assert.doesNotThrow(() => verifyEvidence(plan, head, jobs));
  assert.throws(() => verifyEvidence({...plan, previousGreen: false}, head, jobs));
  assert.throws(() => verifyEvidence({...plan, paths: ['go/coordinator/access.go']}, head, jobs));
  assert.throws(() => verifyEvidence(plan, head, {...jobs, go: {result: 'failure'}}));
});

test('actual Git comparison accepts a qualified plan and rejects a corrupted omission manifest', () => {
  const root = mkdtempSync(join(tmpdir(), 'fleet-selection-'));
  const git = (args: string[]) => execFileSync('git', args, {cwd: root, encoding: 'utf8', stdio: ['ignore', 'pipe', 'pipe']});
  const commit = () => { git(['add', '.']); git(['-c', 'user.name=Fixture', '-c', 'user.email=fixture@example.invalid', 'commit', '-m', 'fixture']); return git(['rev-parse', 'HEAD']).trim(); };
  try {
    git(['init', '--quiet']); writeFileSync(join(root, 'README.md'), 'fixture'); const before = commit();
    mkdirSync(join(root, 'docs')); writeFileSync(join(root, 'docs/example.md'), 'prose'); const after = commit();
    const plan = makePlan('push', {before, after}, after, git, true);
    const path = join(root, 'plan.json'), results = join(root, 'results.json');
    writeFileSync(results, JSON.stringify({selection: {result: 'success'}, thin: {result: 'skipped'}, ...Object.fromEntries(families.map(f => [f, {result: 'skipped'}]))}));
    const verify = () => execFileSync(process.execPath, [fileURLToPath(new URL('../../scripts/test-selection.ts', import.meta.url)), '--verify', path, results],
      {cwd: root, env: {...process.env, GITHUB_SHA: after}, stdio: 'pipe'});
    writeFileSync(path, JSON.stringify(plan)); assert.match(verify().toString(), /PASS/);
    const tools = join(root, 'tools'); mkdirSync(tools);
    writeFileSync(join(tools, 'gh'), `#!${process.execPath}\nconsole.log(JSON.stringify([{databaseId:1,status:process.argv.includes('--commit')?'in_progress':'completed',conclusion:process.argv.includes('--commit')?'':'success'}]));\n`, {mode: 0o755});
    const eventPath = join(root, 'event.json'); writeFileSync(eventPath, JSON.stringify({before, after}));
    const observed = JSON.parse(execFileSync(process.execPath, [fileURLToPath(new URL('../../scripts/test-selection.ts', import.meta.url))],
      {cwd: root, encoding: 'utf8', env: {...process.env, PATH: tools + ':' + process.env.PATH, GITHUB_SHA: after,
        GITHUB_EVENT_NAME: 'push', GITHUB_EVENT_PATH: eventPath, GITHUB_REF_NAME: 'fixture', GITHUB_HEAD_REF: '', GITHUB_RUN_ID: '2', FLEET_TEST_FULL: ''}}));
    assert.equal(observed.previousGreen, false, 'green branch cannot bypass unfinished exact-base CI');
    assert.deepEqual(observed.executed, [...families]);

    writeFileSync(path, JSON.stringify({...plan, paths: ['docs/unrelated.md']})); assert.throws(verify);
  } finally { rmSync(root, {recursive: true, force: true}); }
});

test('known module execution retains native archives, HTTP consumers and shared fallback', () => {
  for (const module of ['executor','gateway','update']) assert.deepEqual(execution([`go/${module}/main.go`],true,false),['go','app']);
  assert.deepEqual(execution(['go/coordinator/main.go'],true,false),[...families]);
  for (const path of ['app/src/views/hosts.tsx','packages/ui/src/button.tsx']) assert.deepEqual(execution([path],true,false),['app','browser']);
  for (const path of ['go/obs/obs.go','go/contract/base.go','go/go.mod','tests/ui/new.test.ts']) assert.deepEqual(execution([path],true,false),[...families]);
  assert.deepEqual(execution(['go/executor/main.go','app/src/views/hosts.tsx'],true,false),[...families]);
});

test('pull request runs the thin check only and defers its heavy families to the merge queue', () => {
  const git = (args: string[]) => args[0] === 'rev-parse' ? head : args[0] === 'diff' ? 'M\0go/coordinator/access.go\0' : '';
  const plan = makePlan('pull_request', {pull_request: {base: {sha: base}}}, head, git);
  assert.deepEqual(plan.executed, []);
  assert.deepEqual(plan.deferred, [...families]);
  const jobs = {selection: {result: 'success'}, thin: {result: 'success'}, ...Object.fromEntries(families.map(f => [f, {result: 'skipped'}]))};
  assert.doesNotThrow(() => verifyEvidence(plan, head, jobs));
  assert.throws(() => verifyEvidence(plan, head, {...jobs, thin: {result: 'failure'}}));
  assert.throws(() => verifyEvidence({...plan, deferred: []}, head, jobs));
  const group = makePlan('merge_group', {merge_group: {base_sha: base, head_sha: head}}, head, git);
  assert.deepEqual(group.executed, [...families]);
  assert.deepEqual(group.deferred, []);
  assert.throws(() => verifyEvidence(group, head, {...jobs, thin: {result: 'skipped'}}));
});
