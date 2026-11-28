import assert from 'node:assert/strict';
import { test } from 'node:test';
import { execFileSync } from 'node:child_process';
import { createHash } from 'node:crypto';
import { mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { retain } from '../../deploy/design-verifier/designer-retain.mjs';
import { openSite, visualReady } from '../../deploy/design-verifier/worker/site.mjs';

const result = value => ({ content: [{ type: 'text', text: JSON.stringify(value) }] });

test('the protected build transport carries every unique pin from eight cases', async () => {
  const { dependencyWire, readDependencyWire } = await import('../../deploy/design-verifier/worker/dependencies.mjs');
  const pins = Array.from({ length: 723 }, (_, i) => ({
    path: `node_modules/.pnpm/fixture-package@1.0.0/node_modules/fixture-package/dist/module-${i}.mjs`,
    sha256: createHash('sha256').update(String(i)).digest('hex'),
  }));
  assert.ok(Buffer.byteLength(JSON.stringify(pins)) > 96 * 1024);
  const wire = dependencyWire(Array.from({ length: 8 }, () => pins).flat());
  assert.ok(Buffer.byteLength(wire) <= 96 * 1024);
  assert.deepEqual(readDependencyWire(wire), pins);
  assert.deepEqual(readDependencyWire(dependencyWire(pins.slice(0, 1))), pins.slice(0, 1));
  assert.throws(() => dependencyWire([...pins, { ...pins[0], sha256: pins[1].sha256 }]), /Conflicting/);
});

test('pending artifact receipts resume without new saves and every stored digest is checked', async () => {
  const execution = 'e1111111-1111-4111-8111-111111111111';
  const option = { execution, sourceSha256: 'source-sha', cases: [{ scopeKey: 'case', recipe: 'recipe',
    recipeFile: 'recipe.json', recipeSha256: 'recipe-sha', frames: [
      { file: 'image.png', viewport: 'desktop', theme: 'light', sha256: 'image-sha' },
    ] }] };
  const entries = new Map();
  let saves = 0, stored = false, listPending = true;
  const types = { 'source.tar': ['application/x-tar', 'source-sha'],
    'recipe.json': ['application/json', 'recipe-sha'], 'image.png': ['image/png', 'image-sha'] };
  const tools = {
    mcp__fleet__artifact_save: async ({ name }) => {
      const id = 'id-' + ++saves;
      entries.set(id, { id, executionId: execution, mediaType: types[name][0], sha256: types[name][1] });
      return result({ artifactId: id });
    },
    mcp__fleet__artifact_list: async ({ execution_id }) => {
      assert.equal(execution_id, execution);
      return listPending ? result({ state: 'pending', transferId: 'list-transfer' }) : result({ artifacts: [...entries.values()].map(item => ({
        ...item, state: stored ? 'stored' : 'pending',
      })) });
    },
  };
  const checkpoints = [];
  const first = await retain(tools, '/invented', option, {}, x => checkpoints.push(x));
  assert.equal(first.state, 'pending');
  assert.equal(saves, 3);
  assert.equal(checkpoints.length, 3);
  stored = true;
  listPending = false;
  const second = await retain(tools, '/invented', option, first.receipts);
  assert.equal(second.state, 'stored');
  assert.equal(saves, 3);
  entries.get(second.sourceArtifactId).sha256 = 'different';
  await assert.rejects(retain(tools, '/invented', option, first.receipts), /digest or media type differs/);
  entries.get(second.sourceArtifactId).executionId = 'e2222222-2222-4222-8222-222222222222';
  await assert.rejects(retain(tools, '/invented', option, first.receipts), /belongs to another execution/);
});

test('missing assets and unmatched operations block, with bounded diagnostics', async () => {
  const site = await openSite({ '/index.html': Buffer.from('ok').toString('base64') },
    { start: '/tasks/T-TST-1', target: '/tasks/T-TST-1', responses: [] }, true);
  try {
    assert.equal((await fetch(site.origin + '/missing.css')).status, 404);
    for (let i = 0; i < 60; i++) {
      await fetch(site.origin + '/api/ops/op_' + String.fromCharCode(97 + Math.floor(i / 26), 97 + i % 26), { method: 'POST', body: '{}' });
    }
    assert.ok(site.missing.has('asset:/missing.css'));
    assert.equal(site.missing.size, 48);
  } finally { await site.close(); }
});

test('Task fixture follows the app route translation and scoped read contracts', async () => {
  const { setProjectCodes, toInternal, toTarget } = await import('../../app/src/routes.ts');
  const { toPageParams } = await import('../../packages/view-url/view-url.ts');
  const { appOriginalsFixture } = await import('../../scripts/app-originals-fixture.ts');
  const item = appOriginalsFixture.cases.find(entry => entry.scenario.flow === 'task-details');
  setProjectCodes([{ id: 'fixture-project', code: 'TST' }]);
  assert.equal(toInternal(item.scenario.start), '/tasks/T-TST-1?project=fixture-project');
  assert.equal(toTarget('/tasks/T-TST-1?project=fixture-project'), item.scenario.target);
  const op = name => item.scenario.responses.find(row => row.op === name);
  assert.deepEqual(op('task_page').request, { ...toPageParams({ view: 'tasks', project: ['fixture-project'] }), limit: 100 });
  assert.deepEqual(op('entity_resolve').request, { code: 'T-TST-1' });
  assert.equal(op('entity_resolve').reply.id, op('task_get').request.taskId);
  assert.deepEqual(op('publishing_status').request, { projectId: 'fixture-project' });
  assert.ok(item.scenario.responses.some(row => row.op === 'entity_refs' && row.request.refs.some(ref => ref.kind === 'project' && ref.id === 'fixture-project')));
  assert.deepEqual(op('entity_card').request, { code: 'T-TST-1' });
  assert.equal(op('entity_card').reply.projectId, 'fixture-project');
  assert.equal(op('task_get').reply.projectId, 'fixture-project');
  assert.equal(op('entity_card').reply.id, op('task_get').request.taskId);
  assert.deepEqual(op('projects_view').request, {});
});

test('allocation requires real finite cgroup v2 memory, PID and CPU values', async () => {
  const { validateAllocation } = await import('../../deploy/design-verifier/designer-allocation.mjs');
  const values = { 'memory.max': '1048576', 'pids.max': '32', 'cpu.max': '50000 100000' };
  const read = (group, name) => { assert.equal(group, '/fixture.slice/job.scope'); return values[name]; };
  validateAllocation('0::/fixture.slice/job.scope\n', read);
  for (const key of Object.keys(values)) {
    const old = values[key]; values[key] = key === 'cpu.max' ? 'max 100000' : 'max';
    assert.throws(() => validateAllocation('0::/fixture.slice/job.scope\n', read), /finite/);
    values[key] = old;
  }
  assert.throws(() => validateAllocation('0::/../escape\n', read), /unified cgroup/);
  assert.throws(() => validateAllocation('2:cpu:/legacy\n', read), /unified cgroup/);
});

test('direct uiCss stylesheet reads are included in the recipe input inventory', async () => {
  const { observedInputs } = await import('../../deploy/design-verifier/worker/designer-inputs.mjs');
  const fs = await import('node:fs');
  const file = new URL('../../packages/ui/src/styles/base.css', import.meta.url);
  fs.readFileSync(file, 'utf8');
  assert.ok(observedInputs().includes('packages/ui/src/styles/base.css'));
});

test('visual admission rejects missing resources and an overridden theme', () => {
  const ready = { scheme: 'dark', routeScheme: 'dark', imagesReady: true, fontsReady: true };
  assert.equal(visualReady(ready, 'dark'), true);
  assert.equal(visualReady({ ...ready, scheme: 'light' }, 'dark'), false);
  assert.equal(visualReady({ ...ready, scheme: 'light dark' }, 'dark'), false);
  assert.equal(visualReady({ ...ready, routeScheme: 'light' }, 'dark'), false);
  assert.equal(visualReady({ ...ready, imagesReady: false }, 'dark'), false);
  assert.equal(visualReady({ ...ready, fontsReady: false }, 'dark'), false);
});


test('import scratch requires a private bounded tmpfs mount', async () => {
  const { validateScratch } = await import('../../deploy/design-verifier/designer-allocation.mjs');
  const info = { isDirectory: () => true, uid: 123, mode: 0o40700 };
  const fs = { type: 0x01021994, bsize: 4096, blocks: 65536 };
  const mounts = '50 1 0:35 / /job rw - tmpfs tmpfs rw,size=262144k\n';
  assert.equal(validateScratch('/job', '/job', info, fs, mounts, 123), '/job');
  assert.throws(() => validateScratch('/job', '/job', info, { ...fs, blocks: 65537 }, mounts, 123), /tmpfs/);
  assert.throws(() => validateScratch('/job', '/job', info, fs, mounts.replace('tmpfs', 'ext4'), 123), /tmpfs/);
  assert.throws(() => validateScratch('/job', '/job', { ...info, mode: 0o40777 }, fs, mounts, 123), /tmpfs/);
  assert.throws(() => validateScratch('/job', '/job/link', info, fs, mounts, 123), /tmpfs/);
  assert.throws(() => validateScratch('/job', '/job', info, fs, mounts.replace('0:35 / /job', '0:35 /shared /job'), 123), /tmpfs/);
  assert.throws(() => validateScratch('/job', '/job', info, fs, mounts + '51 1 0:35 / /other rw - tmpfs tmpfs rw\n', 123), /tmpfs/);
});

test('Conversation fixture binds its route, resolution and returned head', async () => {
  const { checkFixture } = await import('../../deploy/design-verifier/designer-fixture.mjs');
  const { appOriginalsFixture } = await import('../../scripts/app-originals-fixture.ts');
  checkFixture(appOriginalsFixture);
  const fixture = structuredClone(appOriginalsFixture);
  const conversation = fixture.cases.find(item => item.scenario.flow === 'direct').scenario;
  const session = conversation.responses.find(item => item.op === 'session_view');
  session.reply.head.id = 'wrong-session';
  assert.throws(() => checkFixture(fixture), /same conversation/);
  session.reply.head.id = session.request.sessionId;
  conversation.start = conversation.target = '/conversations/C-TST-1';
  assert.throws(() => checkFixture(fixture), /must resolve/);
  conversation.responses.push({ op: 'entity_resolve', request: { code: 'C-TST-1' },
    reply: { code: 'C-TST-1', type: 'C', id: session.request.sessionId }, required: true });
  checkFixture(fixture);
  conversation.start = conversation.target = '/conversations/c-tst-1';
  conversation.responses.at(-1).request.code = 'c-tst-1';
  checkFixture(fixture);
  conversation.responses.at(-1).reply.id = 'wrong-session';
  assert.throws(() => checkFixture(fixture), /same conversation/);
});

test('retention script asks for this execution’s staged directory', async () => {
  const { retainScript } = await import('../../deploy/design-verifier/designer-retain.mjs');
  const script = retainScript({ sourceSha256: 'abc', cases: [] });
  assert.match(script, /captureDirectory = null/);
  assert.match(script, /Stage the captured files into this execution/);
  assert.doesNotMatch(script, /trusted-vm-output/);
});

test('handoff matches this designer request and names only the expected capture files', async () => {
  const { handoffOption } = await import('../../deploy/design-verifier/designer-handoff.mjs');
  const { captureMatrix } = await import('../../packages/ui/scripts/capture-scene.mjs');
  const sha = c => c.repeat(64), base = 'b'.repeat(40);
  const request = { nonce: sha('a'), base, baseSha256: sha('c'), overlaySha256: sha('d'),
    session: 'c1111111-1111-4111-8111-111111111111', execution: 'e1111111-1111-4111-8111-111111111111' };
  const handoff = { version: 1, nonce: request.nonce, session: request.session, execution: request.execution, base, producer: { commit: base, version: 'v1', target: 'linux-amd64' },
    baseBundleSha256: request.baseSha256, overlaySha256: request.overlaySha256,
    source: 'source.tar', sourceSha256: sha('e'), cases: [{ scopeKey: 'pages/tasks/app-shell:details',
      recipe: sha('f'), recipeFile: 'case-1-recipe.json', recipeSha256: sha('e'),
      frames: captureMatrix.map(({ viewport, theme, width, height }) => ({ viewport, theme, width, height,
        file: `case-1-${viewport}-${theme}.png`, sha256: sha('d'), bytes: 123, routes: ['/tasks/T-TST-1'] })) }] };
  assert.equal(handoffOption(handoff, request).sourceSha256, sha('e'));
  assert.throws(() => handoffOption(handoff, { ...request, nonce: sha('b') }), /differs/);
  assert.throws(() => handoffOption(handoff, { ...request, overlaySha256: sha('a') }), /differs/);
  handoff.cases[0].frames[0].file = '../other.png';
  assert.throws(() => handoffOption(handoff, request), /frame/);
});

test('retained source and the guest job receipt bind the requested original bytes', async t => {
  const { validateSource } = await import('../../deploy/design-verifier/designer-handoff.mjs');
  const { captureRequest, checkCaptureInputs, writeCaptureResult } = await import('../../deploy/design-verifier/designer-job.mjs');
  const { hash } = await import('../../deploy/design-verifier/designer-source.mjs');
  const { appOriginalsFixture } = await import('../../scripts/app-originals-fixture.ts');
  const { captureMatrix } = await import('../../packages/ui/scripts/capture-scene.mjs');
  const root = mkdtempSync(join(tmpdir(), 'fleet-retain-source-'));
  t.after(() => rmSync(root, { recursive: true, force: true }));
  const base = 'b'.repeat(40), baseBytes = Buffer.from('fixture bundle'), patch = Buffer.from('fixture patch');
  const overlayBytes = Buffer.from(JSON.stringify({version:1,base,files:[]}) + '\n');
  const cases = appOriginalsFixture.cases.map((item, index) => ({ scopeKey: item.scopeKey, recipe: 'a'.repeat(64),
    recipeFile: `case-${index + 1}-recipe.json`, recipeSha256: 'b'.repeat(64),
    frames: captureMatrix.map(({ viewport, theme, width, height }) => ({ viewport, theme, width, height,
      file: `case-${index + 1}-${viewport}-${theme}.png`, sha256: 'c'.repeat(64), bytes: 200, routes: [item.scenario.target] })) }));
  const fixture = structuredClone(appOriginalsFixture);
  const manifest = { version: 1, kind: 'fleet-normal-app', base, baseTree: '1'.repeat(40), tree: '2'.repeat(40), paths: [], archiveSha256: '3'.repeat(64), appSha256: '4'.repeat(64), runtime: { imageSha256: '5'.repeat(64), fontSha256: '6'.repeat(64), lockSha256: '7'.repeat(64), node: 'v22', chromium: '140', playwright: '1.56' }, producer: { commit: base, version: 'v1', target: 'linux-amd64' },
    baseBundleSha256: hash(baseBytes), overlaySha256: hash(overlayBytes), patchSha256: hash(patch),
    fixtureSha256: hash(JSON.stringify(fixture) + '\n'), fixture, cases };
  const request = { base, baseSha256: hash(baseBytes), overlaySha256: manifest.overlaySha256,
    verifySource: async input => { assert.deepEqual(input.overlayBytes,overlayBytes); assert.deepEqual(input.patch,patch); } };
  const build = (value, names = ['base.bundle', 'overlay.json', 'overlay.patch', 'replay.json']) => {
    writeFileSync(join(root, 'base.bundle'), baseBytes); writeFileSync(join(root, 'overlay.json'), overlayBytes); writeFileSync(join(root, 'overlay.patch'), patch);
    writeFileSync(join(root, 'replay.json'), JSON.stringify(value));
    return execFileSync('/usr/bin/tar', ['--format=ustar', '-cf', '-', '-C', root, ...names]);
  };
  const check = (tar, expectedCases = cases) => validateSource(tar, { sourceSha256: hash(tar), producer: manifest.producer, cases: expectedCases }, request);
  const source = build(manifest);
  const measured = await check(source);
  const job = { version: 1, project: 'fleet', task: 'a1111111-1111-4111-8111-111111111111',
    session: 'c1111111-1111-4111-8111-111111111111', execution: 'e1111111-1111-4111-8111-111111111111',
    job: 'e2222222-2222-4222-8222-222222222222', nonce: 'd'.repeat(64), base, baseSha256: request.baseSha256,
    overlaySha256: request.overlaySha256, fixtureSha256: measured.fixtureSha256 };
  const requestBytes = Buffer.from(JSON.stringify(job) + '\n');
  const fixtureBytes = Buffer.from(JSON.stringify(fixture) + '\n');
  assert.deepEqual(captureRequest(requestBytes), job);
  assert.throws(() => captureRequest(JSON.stringify({ ...job, job: job.execution })), /Invalid Fleet designer/);
  assert.deepEqual(checkCaptureInputs(job, baseBytes, overlayBytes, fixtureBytes), { overlay: JSON.parse(overlayBytes), fixture });
  assert.throws(() => checkCaptureInputs(job, baseBytes, Buffer.from('{}'), fixtureBytes), /inputs differ/);

  const members = ['handoff.json', 'source.tar', ...cases.flatMap(item => [item.recipeFile, ...item.frames.map(frame => frame.file)])];
  for (const member of members) writeFileSync(join(root, member), member === 'source.tar' ? source : 'archive packaging fixture');
  await writeCaptureResult(root, root, job, requestBytes, { producer: manifest.producer, sourceSha256: hash(source), cases }, measured);
  const capture = readFileSync(join(root, 'capture.tar'));
  const receipt = JSON.parse(readFileSync(join(root, 'receipt.json')));
  assert.equal(receipt.requestSha256, hash(requestBytes));
  assert.deepEqual(receipt.request, job);
  assert.deepEqual(receipt.capture, { name: 'capture.tar', bytes: capture.length, sha256: hash(capture) });
  assert.deepEqual(receipt.source, { sha256: hash(source), baseTree: measured.baseTree, tree: measured.tree,
    patchSha256: measured.patchSha256, archiveSha256: measured.archiveSha256 });
  assert.deepEqual(execFileSync('/usr/bin/tar', ['-tf', join(root, 'capture.tar')], { encoding: 'utf8' }).trim().split('\n'), members);
  await assert.rejects(validateSource(build(manifest), { sourceSha256: hash(build(manifest)), producer: manifest.producer, cases }, { ...request, verifySource: undefined }), /Trusted isolated/);
  const extraFrame = structuredClone(manifest); extraFrame.cases[0].frames[0].extra = 'unexpected';
  await assert.rejects(check(build(extraFrame), extraFrame.cases), /fixture and cases/);
  for (const key of ['width', 'height', 'bytes', 'routes']) {
    const incomplete = structuredClone(manifest); delete incomplete.cases[0].frames[0][key];
    await assert.rejects(check(build(incomplete), incomplete.cases), /fixture and cases/);
  }
  for (const [key, value] of [['version', 'v2'], ['target', 'different-platform']]) {
    const mismatch = structuredClone(manifest); mismatch.producer[key] = value;
    await assert.rejects(check(build(mismatch)), /replay|differs/);
  }
  for (const field of ['tree', 'baseTree', 'runtime', 'archiveSha256', 'appSha256']) {
    const incomplete = { ...manifest }; delete incomplete[field];
    await assert.rejects(check(build(incomplete)), /replay manifest/);
  }
  await assert.rejects(check(build(manifest, ['replay.json'])), /complete replay/);
  const wrongPatch = { ...manifest, patchSha256: 'e'.repeat(64) };
  await assert.rejects(check(build(wrongPatch)), /differs/);
  const wrongFrame = structuredClone(cases); wrongFrame[0].frames[0].sha256 = 'f'.repeat(64);
  await assert.rejects(check(build(manifest), wrongFrame), /differs/);
  const wrongFixture = structuredClone(manifest); wrongFixture.fixture.cases.reverse();
  wrongFixture.fixtureSha256 = hash(JSON.stringify(wrongFixture.fixture) + '\n');
  await assert.rejects(check(build(wrongFixture)), /cases/);
});

test('conversation fixture cannot route context, questions or composer to unrelated sessions', async () => {
  const { checkFixture } = await import('../../deploy/design-verifier/designer-fixture.mjs');
  const { appOriginalsFixture } = await import('../../scripts/app-originals-fixture.ts');
  for (const mutate of [
    v => { v.context.sessionId = 'unrelated'; },
    v => { v.questions[0].sessionId = 'unrelated'; },
    v => { v.composer.sessionId = 'unrelated'; },
    v => { v.head.state = 'waiting'; },
    v => { v.composer.sessionId = null; },
    v => { v.head.state = 'running'; v.head.activeExecutionId = 'made-up'; },
  ]) {
    const f = structuredClone(appOriginalsFixture);
    mutate(f.cases.find(c => c.scenario.flow === 'direct').scenario.responses.find(r => r.op === 'session_view').reply);
    assert.throws(() => checkFixture(f));
  }
});

test('designer capture records malformed operation requests', async () => {
  const { openSite } = await import('../../deploy/design-verifier/worker/site.mjs');
  const site = await openSite({}, { start: '/home', target: '/home', responses: [] }, true);
  try {
    assert.equal((await fetch(site.origin + '/api/ops/not_registered?x=1', { method: 'POST' })).status, 405);
    assert.deepEqual([...site.missing], ['request:POST /api/ops/not_registered?x=1']);
  } finally { await site.close(); }
});

test('protected capture accepts Playwright text landmarks without running the designer color probe', async () => {
  const { runInNewContext } = await import('node:vm');
  const path = new URL('../../deploy/design-verifier/worker/render.mjs', import.meta.url);
  const source = readFileSync(path, 'utf8');
  const body = source.slice(source.indexOf('async function capture('), source.indexOf('\nawait registerImageOpenSelector('));
  assert.ok(body.startsWith('async function capture('));
  let target = '', designer = false, settle = 0; const steps = [];
  const site = { origin: 'http://fixture', used: new Set(['/assets/main.js']), missing: new Set(), unmatched: new Set(), unmatchedOmitted: () => 0,
    unmet: () => [], forbidden: () => [], close: async () => {} };
  const page = { setDefaultTimeout() {}, clock: { setFixedTime: async () => {} }, on() {},
    goto: async url => { target = url; return { ok: () => true }; }, waitForURL: async () => {}, url: () => target,
    locator: selector => ({ waitFor: async () => {}, evaluate: async () => { assert.equal(selector, 'text=Open conversation'); return { scheme: 'dark', routeScheme: 'dark', imagesReady: true, fontsReady: true }; } }),
    evaluate: async fn => { steps.push('settle'); await fn(); settle++; },
    screenshot: async () => { assert.equal(steps.at(-1), 'frame'); steps.push('screenshot'); return Buffer.from('fake screenshot'); } };
  const context = { newPage: async () => page, on() {}, route: async () => {}, close: async () => {} };
  const capture = runInNewContext('(' + body + ')', { openSite: async () => site, visualReady: () => true, isImageFlow: () => false, Buffer, URL,
    document: { fonts: { ready: Promise.resolve() } }, requestAnimationFrame: fn => { steps.push('frame'); fn(); } });
  const result = await capture({ newContext: async () => context }, { files: {}, main: '/assets/main.js',
    scenario: { start: '/conversations/x', target: '/conversations/x', flow: 'direct', landmark: 'text=Open conversation' }, clock: 0 },
  { width: 1440, height: 900, theme: 'dark' }, false);
  assert.equal(result.blocked, undefined);
  assert.ok(result.png); assert.equal(settle, 1);
  designer = true;
  const design = await capture({ newContext: async () => context }, { files: {}, main: '/assets/main.js',
    scenario: { start: '/conversations/x', target: '/conversations/x', flow: 'direct', landmark: 'text=Open conversation' }, clock: 0 },
  { width: 1440, height: 900, theme: 'dark' }, true);
  assert.equal(design.blocked, undefined);
  assert.ok(design.png); assert.equal(settle, 2);
});

test('a task conversation may target a visible same-project thread member', async () => {
  const { checkFixture } = await import('../../deploy/design-verifier/designer-fixture.mjs');
  const { appOriginalsFixture } = await import('../../scripts/app-originals-fixture.ts');
  const fixture = structuredClone(appOriginalsFixture);
  const scenario = fixture.cases.find(c => c.scenario.flow === 'direct').scenario;
  const read = scenario.responses.find(r => r.op === 'session_view');
  const target = 'c2222222-2222-4222-8222-222222222222';
  delete read.reply.head.subject; // Older task links resolve via the thread, not this optional field.
  read.reply.head.projectId = 'project-a'; read.reply.composer.sessionId = target;
  read.reply.questions[0].sessionId = target;
  const thread = scenario.responses.find(r => r.op === 'thread_entries');
  thread.reply.key = { task: 't1111111-1111-4111-8111-111111111111' };
  // Task-attached session has no executions and is absent from thread.sessionIds.
  scenario.proof = { sessions: [
    { id: read.reply.head.id, projectId: 'project-a', archivedAt: null, taskId: null, firstTaskId: 't1111111-1111-4111-8111-111111111111', createdAt: 1 },
    { id: target, projectId: 'project-a', archivedAt: null, taskId: 't1111111-1111-4111-8111-111111111111', firstTaskId: null, createdAt: 2 }], runs: [] };
  checkFixture(fixture);
  read.reply.composer.activeExecutionId = 'not-an-agent';
  const fake = { id:'not-an-agent', kind:'host_job', sessionId:target, projectId:'project-a', taskId:'other-task', state:'running', observation:'confirmed', seq:1 };
  scenario.proof.runs.push(fake);
  assert.throws(() => checkFixture(fixture), /Invalid modeled agent run/);
  fake.kind = 'agent_run'; assert.throws(() => checkFixture(fixture), /outside the thread/);
  fake.taskId = 't1111111-1111-4111-8111-111111111111';
  assert.throws(() => checkFixture(fixture), /membership/);
  thread.reply.sessionIds.push(target); checkFixture(fixture);
  scenario.proof.runs = []; thread.reply.sessionIds = []; read.reply.composer.activeExecutionId = null;
  scenario.proof.sessions[1].archivedAt = 123;
  assert.throws(() => checkFixture(fixture), /Composer target/);
  scenario.proof.sessions[1].archivedAt = null;
  scenario.proof.sessions[1].projectId = 'project-b';
  assert.throws(() => checkFixture(fixture), /Composer target/);
});


test('scope labels cannot claim another normal app surface', async () => {
  const { checkFixture } = await import('../../deploy/design-verifier/designer-fixture.mjs');
  const { appOriginalsFixture } = await import('../../scripts/app-originals-fixture.ts');
  const fixture = structuredClone(appOriginalsFixture);
  fixture.cases[0].scopeKey = 'pages/tasks/app-shell:details';
  fixture.cases[1].scopeKey = 'pages/tasks/app-shell:other';
  assert.throws(() => checkFixture(fixture), /scope/);
});

test('unmatched diagnostics report omitted exact keys and long requests', async () => {
  const site = await openSite({}, { start: '/home', target: '/home', responses: [] }, true);
  try {
    for (let i = 0; i < 14; i++) await fetch(site.origin + '/api/ops/fixture', { method: 'POST', body: JSON.stringify({ i }) });
    await fetch(site.origin + '/api/ops/fixture', { method: 'POST', body: JSON.stringify({ x: 'a'.repeat(1200) }) });
    assert.equal(site.unmatched.size, 12);
    assert.equal(site.unmatchedOmitted(), 3);
  } finally { await site.close(); }
});


test('head state handles a page full of newer host jobs without inventing a composer run', async () => {
  const { checkFixture } = await import('../../deploy/design-verifier/designer-fixture.mjs');
  const { appOriginalsFixture } = await import('../../scripts/app-originals-fixture.ts');
  const f = structuredClone(appOriginalsFixture), scenario = f.cases[0].scenario;
  const reply = scenario.responses.find(r => r.op === 'session_view').reply;
  reply.executions.rows = Array.from({length:20}, (_,i) => ({id:`host-${i}`, kind:'host_job', state:'succeeded', seq: 21-i}));
  reply.executions.total = 21; reply.head.state = 'succeeded'; reply.head.activeExecutionId = null;
  scenario.proof = { sessions: [{ id: reply.head.id, projectId: null, taskId: null, firstTaskId: null, archivedAt: null, createdAt: 1 }],
    runs: [{ id:'older', kind:'agent_run', sessionId:reply.head.id, projectId:null, taskId:null, state:'succeeded', observation:'confirmed', seq: 1 }] };
  scenario.responses.find(r => r.op === 'thread_entries').reply.sessionIds = [reply.head.id];
  checkFixture(f);
  scenario.proof.runs[0].state = 'invented'; reply.head.state = 'invented';
  assert.throws(() => checkFixture(f), /Invalid modeled agent run/);
  scenario.proof.runs[0].state = 'succeeded'; reply.head.state = 'succeeded';
  scenario.proof.runs[0].seq = 22; assert.throws(() => checkFixture(f), /page boundary/);
  scenario.proof.runs[0].seq = 1;
  reply.executions.rows = []; reply.executions.total = 0; reply.head.state = 'idle';
  scenario.proof.runs = []; scenario.responses.find(r => r.op === 'thread_entries').reply.sessionIds = [];
  reply.composer.activeExecutionId = 'invented';
  assert.throws(() => checkFixture(f), /Composer target/);
});

test('same-page labels must correspond to the exact gesture', async () => {
  const { checkFixture } = await import('../../deploy/design-verifier/designer-fixture.mjs');
  const { appOriginalsFixture } = await import('../../scripts/app-originals-fixture.ts');
  for (const [i, value] of [[0,'pages/conversations/app-shell:details'], [1,'pages/tasks/app-shell:history']]) {
    const f = structuredClone(appOriginalsFixture); f.cases[i].scopeKey = value;
    assert.throws(() => checkFixture(f), /scope/);
  }
});
