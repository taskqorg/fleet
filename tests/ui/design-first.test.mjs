// Regression checks for OR-62 scene discovery, local source rules, stored captures and recipe equality.
import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import { readFileSync, mkdirSync, mkdtempSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { test } from 'node:test';
import { API } from 'typescript/unstable/sync';
import { findScene, readScenes, sceneDirectory } from '../../packages/ui/scripts/scenes.ts';
import { buildScene, repository } from '../../packages/ui/scripts/scene-build.mjs';
import { captureMatrix, captureScene } from '../../packages/ui/scripts/capture-scene.mjs';
import { packetDigest, verifyDesignEvidence } from '../../packages/ui/scripts/design-evidence.mjs';
import { designImpact } from '../../packages/ui/scripts/design-candidate.mjs';
import { problemsInSource } from '../../packages/ui/scripts/design-sources.mjs';

test('scene discovery orders disjoint additions and rejects duplicate routes and missing backlinks', t => {
  const root = mkdtempSync(join(tmpdir(), 'fleet-scenes-test-'));
  t.after(() => rmSync(root, { recursive: true, force: true }));
  const spec = ['docs', 'design-system', 'pages', 'sample.md'].join('/');
  mkdirSync(join(root, 'docs/design-system/pages'), { recursive: true });
  writeFileSync(join(root, spec), `[scenes](../../../${sceneDirectory})\n`);
  const manifest = (route) => ({ version: 1, spec, route, states: { default: route }, landmark: 'main' });
  function makeScene(id, route) {
    const p = join(root, sceneDirectory, 'views', 'sample', id);
    mkdirSync(p, { recursive: true });
    for (const name of ['scene.tsx', 'fixture.ts']) writeFileSync(join(p, name), '');
    writeFileSync(join(p, 'scene.json'), JSON.stringify(manifest(route)));
    return p;
  }
  makeScene('wait', '/wait');
  makeScene('empty', '/empty');
  assert.deepEqual(readScenes(root).map(x => x.key), ['views/sample/empty', 'views/sample/wait']);
  const duplicate = makeScene('other', '/wait');
  assert.throws(() => readScenes(root), /duplicate state route/);
  rmSync(duplicate, { recursive: true });
  const escaped = makeScene('outside', '/outside');
  writeFileSync(join(escaped, 'scene.json'), JSON.stringify({ ...manifest('/outside'), spec: spec.replace('/pages/sample.md', '/../../AGENTS.md') }));
  assert.throws(() => readScenes(root), /owning spec/);
  rmSync(escaped, { recursive: true });
  writeFileSync(join(root, spec), '# Sample');
  assert.throws(() => readScenes(root), /must link the scene/);
});

test('source checks detect raw controls, dynamic data and non-UI imports in scene source', t => {
  const root = mkdtempSync(join(tmpdir(), 'fleet-source-test-'));
  t.after(() => rmSync(root, { recursive: true, force: true }));
  const config = join(root, 'tsconfig.json');
  const scene = { dir: 'scenes/sample' };
  const path = scene.dir + '/scene.tsx';
  mkdirSync(join(root, scene.dir), { recursive: true });
  writeFileSync(config, JSON.stringify({ compilerOptions: { jsx: 'preserve' }, include: ['**/*.tsx'] }));
  writeFileSync(join(root, path), `
    import type { Data } from '../../app/src/view.ts';
    const stamp = Date['now']() + Math.random();
    export default () => <div style={{ color: 'blue' }}><Button icon="missing" /><svg /><input /></div>;
  `);
  const api = new API({ cwd: root });
  try {
    const snap = api.updateSnapshot({ openProjects: [config] });
    try {
      const source = snap.getProject(config).program.getSourceFile(join(root, path));
      const checks = problemsInSource(path, source, scene, {}).join('\n');
      assert.match(checks, /outside the scene/);
      assert.match(checks, /fixed clock or seeded/);
      assert.match(checks, /shared styles/);
      assert.match(checks, /unknown icon missing/);
      assert.match(checks, /raw <svg>/);
      assert.match(checks, /raw <input>/);
    } finally { snap.dispose(); }
  } finally { api.close(); }
});

test('single-scene compilation is reproducible and isolated from other scenes', async () => {
  const scenes = readScenes(repository);
  const tasks = findScene(scenes, 'pages/tasks/list');
  const one = await buildScene(tasks, 'waiting');
  const two = await buildScene(findScene([...scenes].reverse(), tasks.key), 'waiting');
  const other = await buildScene(tasks, 'failed');
  assert.equal(one.recipe.digest, two.recipe.digest);
  assert.equal(one.html, two.html);
  assert.notEqual(one.recipe.digest, other.recipe.digest);
  assert.ok(one.recipe.inputs.some(({ path }) => path.endsWith('/tasks/list/fixture.ts')));
  assert.ok(!one.recipe.inputs.some(({ path }) => path.startsWith(sceneDirectory + '/pages/hosts/')));
  assert.ok(one.recipe.inputs.every(({ path }) => !path.includes(repository) && !path.includes('..')));
  await assert.rejects(buildScene(tasks, 'unknown'), /unknown state/);
});

const hashA = 'a'.repeat(64);
const hashB = 'b'.repeat(64);
const repositoryName = 'invented/project';
const project = 'DEMO';
const scopeKey = 'pages/tasks/list:waiting';
const scopeKey2 = 'pages/hosts/list:default';
const task = 'T-DEMO-1';

function packet(scopeKey, recipe, task) {
  return {
    project, repository: repositoryName, task, liveWalk: 'Invented live walk', scope: [scopeKey],
    options: ['A', 'B'].map(key => ({ key, label: key, cases: [{ scopeKey, recipe: key === 'A' ? recipe : hashB,
      frames: captureMatrix.map((frame, i) => ({ ...frame, sha256: hashA, artifactId: `${task}-${key}-${i}`, critique: 'Invented review' })),
    }] })),
  };
}

function selected(scopeKey, recipe, task) {
  const comparison = packet(scopeKey, recipe, task);
  return { packet: comparison, decision: {
    project, repository: repositoryName, task, state: 'selected', ownerActor: 'invented owner', executionId: task, question: 'Q-DEMO-1',
    packet: packetDigest(comparison), option: 'A',
  } };
}

test('current selected recipes survive unrelated heads and are recomputed on the combined tree', () => {
  const one = selected(scopeKey, hashA, task);
  const two = selected(scopeKey2, hashA, 'T-DEMO-2');
  const artifact = (id) => ({
    project, repository: repositoryName, task: id.split('-').slice(0, 3).join('-'), executionId: id.split('-').slice(0, 3).join('-'),
    state: 'stored', mediaType: 'image/png', sha256: hashA,
  });
  const candidate = { project, repository: repositoryName, unknown: [], cases: [{ scopeKey, recipe: hashA }], head: 'unrelated' };
  assert.equal(verifyDesignEvidence(candidate, [one], artifact).classification, 'selected-preview-matches');
  assert.throws(() => verifyDesignEvidence(candidate, [], artifact), /missing current selection/);
  assert.throws(() => verifyDesignEvidence({ ...candidate, unknown: ['app/src/new.tsx'] }, [one], artifact), /unverified/);
  const combined = { ...candidate, cases: [...candidate.cases, { scopeKey: scopeKey2, recipe: hashA }] };
  assert.equal(verifyDesignEvidence(combined, [one, two], artifact).classification, 'selected-preview-matches');
  assert.throws(() => verifyDesignEvidence({
    ...combined, cases: [{ scopeKey, recipe: hashB }, combined.cases[1]],
  }, [one, two], artifact), /missing current selection/);
  assert.throws(() => verifyDesignEvidence(candidate, [{ ...one, decision: { ...one.decision, option: 'B' } }], artifact), /missing current selection/);
  assert.throws(() => verifyDesignEvidence(candidate, [{ ...one, decision: { ...one.decision, state: 'revoked' } }], artifact), /stale owner decision/);
  assert.throws(() => verifyDesignEvidence(candidate, [one], id => ({ ...artifact(id), sha256: hashB })), /changed or foreign original/);
  assert.throws(() => verifyDesignEvidence(candidate, [one], id => ({ ...artifact(id), task: 'T-DEMO-9' })), /changed or foreign original/);
  one.packet.options[0].cases[0].frames[0].critique = 'Replaced after decision';
  assert.throws(() => verifyDesignEvidence(candidate, [one], artifact), /stale owner decision/);
  assert.equal(verifyDesignEvidence({ ...candidate, cases: [] }, [], artifact).classification, 'nonvisual');
});

test('impact spots shared dependencies, unmapped app edits and removed scenes', () => {
  const scenes = readScenes(repository);
  assert.deepEqual(designImpact(['tests/ui/design-first.test.ts', 'go/coordinator/example_test.go'], scenes), { affected: [], unknown: [] });
  const taskScene = 'packages/ui/book/scenes/pages/tasks/list/fixture.ts';
  assert.deepEqual(designImpact([taskScene], scenes).affected.map(({ key }) => key), ['pages/tasks/list']);
  const token = 'packages/ui/tokens.json';
  assert.equal(designImpact([token], scenes).affected.length, scenes.length);
  const deleted = taskScene.replace('tasks', 'removed');
  assert.deepEqual(designImpact([deleted, 'app/src/views/tasks.tsx'], scenes).unknown, ['app/src/views/tasks.tsx', deleted]);
});

test('managed capture stops on a missing frame or stored receipt and still closes the browser', async () => {
  const url = 'http://127.0.0.1:4173/preview/invented#/tasks';
  const scene = { name: 'test-scene', recipeDigest: hashA, htmlSha256: hashB };
  const recipe = { digest: hashA, clock: 123, scene: 'views/tasks/list', state: 'default', landmark: 'main', matrix: captureMatrix, timeZone: 'UTC', locale: 'en-US', dpr: 1 };
  const result = (value) => ({ content: [{ type: 'text', text: JSON.stringify(value) }] });
  for (const failure of ['none', 'scene', 'wrong-scene', 'wrong-size', 'wrong-state', 'not-stored']) {
    let index = -1, closed = 0;
    const artifacts = [];
    const tools = {
      mcp__project_browser__close: async () => { closed++; return result({}); },
      mcp__project_browser__open_scene: async args => {
        assert.deepEqual(args, scene);
        return result({ url, recipeDigest: failure === 'wrong-scene' ? hashB : hashA, htmlSha256: hashB });
      },
      mcp__project_browser__emulate: async () => { index++; return result({}); },
      mcp__project_browser__navigate: async args => { assert.deepEqual(args, { url }); return result({}); },
      mcp__project_browser__evaluate: async () => result({ result: {
        ...captureMatrix[index], ready: failure !== 'wrong-state', href: url, digest: recipe.digest, clock: 123, dpr: 1,
        dark: captureMatrix[index].theme === 'dark', scheme: 'light dark', reducedMotion: true, timeZone: 'UTC', locale: 'en-US',
      } }),
      mcp__project_browser__screenshot: async () => result({
        ...captureMatrix[index], width: failure === 'wrong-size' ? 200 : captureMatrix[index].width, path: String(index),
      }),
      mcp__fleet__artifact_save: async ({ path }) => {
        artifacts.push({ id: path, state: failure === 'not-stored' ? 'pending' : 'stored', sha256: hashA, size: 1 });
        return result({ artifactId: path });
      },
      mcp__fleet__artifact_list: async () => result({ artifacts }),
    };
    const target = ['scene', 'wrong-scene'].includes(failure) ? { scene, recipe } : { url, recipe };
    if (failure === 'none' || failure === 'scene') {
      const packet = await captureScene(tools, target);
      assert.equal(packet.frames.length, 4);
      assert.deepEqual(packet.frames.map((f) => f.sha256), Array(4).fill(hashA));
    } else await assert.rejects(captureScene(tools, target), /different scene|unexpected|not confirmed/);
    assert.equal(closed, 2);
    if (['wrong-size', 'wrong-state', 'wrong-scene'].includes(failure)) assert.equal(artifacts.length, 0);
  }
});

test('check:design propagates the book build failure', t => {
  const { scripts } = JSON.parse(readFileSync(join(repository, 'package.json'), 'utf8'));
  const root = mkdtempSync(join(tmpdir(), 'fleet-design-command-test-'));
  t.after(() => rmSync(root, { recursive: true, force: true }));
  const fake = join(root, 'packages/ui/scripts');
  mkdirSync(fake, { recursive: true });
  writeFileSync(join(fake, 'build-book.mjs'), 'process.exitCode = 7;');
  writeFileSync(join(fake, 'check-design.mjs'), 'console.log("unexpected report");');
  const result = spawnSync(scripts['check:design'], { cwd: root, shell: true, encoding: 'utf8' });
  assert.equal(result.status, 7, result.stderr);
  assert.equal(result.stdout, '');
});

test('protected verification fences provider members, derives actual impact and binds parity to the retained selection', async t => {
  const root = mkdtempSync(join(tmpdir(), 'fleet-protected-design-'));
  t.after(() => rmSync(root, { recursive: true, force: true }));
  const git = (...args) => {
    const run = spawnSync('git', args, { cwd: root, encoding: 'utf8' });
    assert.equal(run.status, 0, run.stderr);
    return run.stdout.trim();
  };
  git('init', '--quiet');
  git('config', 'user.name', 'Invented verifier');
  git('config', 'user.email', 'invented@example.invalid');
  git('commit', '--quiet', '--allow-empty', '-m', 'base');
  const base = git('rev-parse', 'HEAD');
  writeFileSync(join(root, 'README.md'), 'Maintenance notes');
  git('add', 'README.md');
  git('commit', '--quiet', '-m', 'documentation');
  const docs = git('rev-parse', 'HEAD');
  const uiPath = 'app/src/view.tsx';
  mkdirSync(join(root, 'app/src'), { recursive: true });
  writeFileSync(join(root, uiPath), 'visible change');
  git('add', uiPath);
  git('commit', '--quiet', '-m', 'visible change');
  const visual = git('rev-parse', 'HEAD');
  git('commit', '--quiet', '--allow-empty', '-m', 'combined tree');
  const groupHead = git('rev-parse', 'HEAD');

  const pr = { kind: 'pull_request', head: visual, base, target: 'main', repository: 'github.com/taskqorg/fleet', number: 1 };
  const group = { ...pr, kind: 'merge_group', head: groupHead, number: undefined };
  const members = [{ ...pr }];
  const design = packet(scopeKey, hashA, task);
  const review = { state: 'selected', projectId: 'fleet', taskId: task, executionId: 'design execution',
    repository: pr.repository, owner: 'invented owner', questionId: 'sealed question', digest: hashB,
    selected: 'A', walkArtifactId: design.liveWalk, options: design.options };
  const adapter = {
    repositoryDir: root,
    readCandidate: async event => ({ ...event, complete: true, members }),
    readSceneInventory: async () => [],
    measureImpact: async (event, paths) => {
      assert.deepEqual(paths, [uiPath]);
      return { ...event, paths: { [uiPath]: [scopeKey] } };
    },
    measureParity: async (event, scopes) => {
      assert.deepEqual(scopes, [scopeKey]);
      return { ...event, cases: [{ scopeKey, recipe: hashA, receipt: hashB, appMatches: true,
        originalSha256: captureMatrix.map(() => hashA) }] };
    },
    readAdmittedTasks: async () => [{ number: 1, head: visual, task }],
    readReview: async () => review,
    readOriginal: async () => ({ project: 'fleet', repository: pr.repository, task, executionId: review.executionId,
      state: 'stored', mediaType: 'image/png', sha256: hashA }),
  };
  const { verifyProtectedDesignCandidate: verify } = await import('../../packages/ui/scripts/design-protected.mjs');
  assert.equal((await verify({ ...pr, head: docs }, adapter)).classification, 'nonvisual');
  await assert.rejects(verify({ ...pr, visualImpact: 'nonvisual' }, {
    ...adapter, measureImpact: async event => ({ ...event, paths: {} }),
  }), /visual impact is unverified/);
  await assert.rejects(verify(pr, { ...adapter, readAdmittedTasks: async () => [{ number: 1, head: docs, task }] }), /member head/);
  await assert.rejects(verify(pr, { ...adapter, readReview: async () => ({ ...review, state: 'inactive' }) }), /no active owner selection/);
  await assert.rejects(verify(pr, { ...adapter, measureParity: async event => {
    const parity = await adapter.measureParity(event, [scopeKey]);
    parity.cases[0].originalSha256[0] = hashB;
    return parity;
  } }), /selected originals/);
  const receipt = await verify(pr, adapter);
  assert.equal(receipt.classification, 'selected-preview-matches');
  assert.equal(receipt.decisions[0].digest, review.digest);
  await assert.rejects(verify(group, {
    ...adapter, readCandidate: async event => ({ ...event, complete: false, members }),
  }), /membership is unknown/);
  await assert.rejects(verify(group, { ...adapter, readPRReceipt: async () => ({ ...receipt, head: docs }) }), /exact member head/);
  await assert.rejects(verify(group, { ...adapter, readPRReceipt: async () => receipt, measureParity: async event => {
    const parity = await adapter.measureParity(event, [scopeKey]);
    parity.cases[0].recipe = hashB;
    return parity;
  } }), /missing current selection/);
  const combined = await verify(group, { ...adapter, readPRReceipt: async () => receipt });
  assert.equal(combined.head, groupHead);
  assert.deepEqual(combined.members, [{ number: 1, head: visual }]);
});


test('the protected GitHub check signature is read back by App, receipt and exact head', async () => {
  const { designGitHubConfig, readDesignCheckReceipt } = await import('../../packages/ui/scripts/design-github-checks.mjs');
  const env = { FLEET_DESIGN_GITHUB_APP_ID: '123', FLEET_DESIGN_GITHUB_INSTALLATION_ID: '456',
    FLEET_DESIGN_GITHUB_APP_KEY_FILE: '/run/credentials/fleet-design-verifier.service/github-app.pem' };
  assert.deepEqual(designGitHubConfig(env), { appId: 123, installationId: 456, keyFile: env.FLEET_DESIGN_GITHUB_APP_KEY_FILE });
  assert.throws(() => designGitHubConfig({ ...env, FLEET_DESIGN_GITHUB_APP_KEY_FILE: 'candidate.pem' }), /absolute key file/);
  const expected = { appId: 123, head: 'a'.repeat(40), externalId: hashB };
  const row = { id: 7, app: { id: 123 }, name: 'design-approval', head_sha: expected.head,
    status: 'completed', conclusion: 'success', external_id: hashB };
  assert.deepEqual(readDesignCheckReceipt(row, expected), { ...expected, checkRunId: 7, conclusion: 'success' });
  for (const override of [{ app: { id: 456 } }, { head_sha: 'b'.repeat(40) }, { external_id: hashA }, { conclusion: 'neutral' }]) {
    assert.throws(() => readDesignCheckReceipt({ ...row, ...override }, expected), /did not match/);
  }
});
