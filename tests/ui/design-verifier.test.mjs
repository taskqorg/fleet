import assert from 'node:assert/strict';
import { execFileSync } from 'node:child_process';
import { mkdtempSync, mkdirSync, readFileSync, rmSync, writeFileSync } from 'node:fs';
import { createRequire } from 'node:module';
import { tmpdir } from 'node:os';
import { dirname, join } from 'node:path';
import { test } from 'node:test';
import { runInNewContext } from 'node:vm';
import { designCredentialIsPrivate, fleetDesignChecks, fleetDesignGitHub } from '../../packages/ui/scripts/design-github-checks.mjs';
import { appId, DesignBlocked, githubCandidates, repo, repositoryId } from '../../deploy/design-verifier/provider.mjs';
import { openRepository } from '../../deploy/design-verifier/git.mjs';
import { openAsyncRepository, runGitProcess } from '../../deploy/design-verifier/git-isolated.mjs';
import { receiptStore } from '../../deploy/design-verifier/receipts.mjs';
import { evaluate } from '../../deploy/design-verifier/verifier.mjs';
import { openSite } from '../../deploy/design-verifier/worker/site.mjs';
import { registerImageOpenSelector } from '../../deploy/design-verifier/worker/image-journey.mjs';
import { loadPolicy } from '../../deploy/design-verifier/parity.mjs';

test('the pinned Playwright handle query runs the opener engine beyond main-world interception', async () => {
  const require = createRequire(import.meta.url);
  const requirePlaywright = createRequire(require.resolve('playwright/package.json'));
  const core = dirname(requirePlaywright.resolve('playwright-core/package.json'));
  const { Selectors: ClientSelectors } = require(join(core, 'lib/client/selectors.js'));
  const { Selectors: ServerSelectors } = require(join(core, 'lib/server/selectors.js'));
  const { ElementHandle: ClientHandle } = require(join(core, 'lib/client/elementHandle.js'));
  const { ElementHandle: ServerHandle } = require(join(core, 'lib/server/dom.js'));
  const { FrameSelectors } = require(join(core, 'lib/server/frameSelectors.js'));
  const clientSelectors = new ClientSelectors({});
  await registerImageOpenSelector(clientSelectors);
  const { selectorEngines } = clientSelectors._withSelectorOptions({});
  const [definition] = selectorEngines;
  const serverSelectors = new ServerSelectors(selectorEngines);
  const signal = '/_fleet_image_open/' + 'a'.repeat(32);
  const sent = [];
  let click;
  class HTMLElement {}
  const engine = runInNewContext(definition.source, {
    HTMLElement,
    window: { addEventListener: (_type, fn) => { click = fn; }, removeEventListener: () => { click = undefined; } },
    XMLHttpRequest: class {
      open(method, path, async) { assert.deepEqual([method, path, async], ['POST', signal, false]); }
      send() { sent.push(signal); }
    },
  });
  const main = { injectedScript() { assert.fail('candidate main world intercepted the opener binding'); } };
  const delivered = { _context: main };
  const utility = {
    injectedScript: async () => ({
      evaluateHandle: async (query, args) => ({
        asElement: () => query({ querySelector: (parsed, root) => {
          assert.equal(root, opener);
          assert.equal(parsed.parts.length, 1);
          return engine.queryAll(root, parsed.parts[0].body)[0];
        } }, args) ? delivered : null,
      }),
    }),
  };
  const frame = {
    _page: { browserContext: { _options: {}, selectors: () => serverSelectors } },
    _context: async world => world === 'utility' ? utility : main,
    _mainContext: async () => main,
  };
  frame.selectors = new FrameSelectors(frame);
  const opener = Object.assign(new HTMLElement(), {
    _frame: frame, isConnected: true, dispatchEvent() { assert.fail('candidate intercepted an event'); },
  });
  const handle = { _elementChannel: { querySelector: async ({ selector }) => ({
    element: { _object: await ServerHandle.prototype.querySelector.call(opener, selector) },
  }) } };
  assert.equal(await ClientHandle.prototype.$.call(handle, definition.name + '=' + signal), delivered);
  assert.deepEqual(sent, []);
  click({ isTrusted: false, composedPath: () => [opener] });
  click({ isTrusted: true, composedPath: () => [new HTMLElement()] });
  assert.deepEqual(sent, []);
  click({ isTrusted: true, composedPath: () => [opener] });
  assert.deepEqual(sent, [signal]);
  assert.equal(click, undefined);
});

test('image policy registration preserves the legacy policy used by the rollback release', async t => {
  const fixture = { start: '/conversations/C-TST-1', target: '/conversations/C-TST-1',
    recipe: 'a'.repeat(64), sourceSha256: 'b'.repeat(64),
    responses: [{ op: 'session_view', request: { sessionId: 'invented' }, reply: { head: {} }, required: true }] };
  const scope = 'pages/conversations/example:default';
  const v1 = Buffer.from(JSON.stringify({ version: 1, paths: [], cases: { [scope]: { ...fixture, flow: 'direct', landmark: 'main' } } }));
  const files = new Map([['/etc/fleet-design-verifier/parity.json', v1]]);
  const read = path => {
    if (!files.has(path)) throw Object.assign(new Error('fixture absent'), { code: 'ENOENT' });
    return files.get(path);
  };
  const legacy = loadPolicy(read);
  const png = 'iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+aZ9sAAAAASUVORK5CYII=';
  const v2 = '/etc/fleet-design-verifier/parity-v2.json';
  const image = { ...fixture, flow: 'image-open', surface: 'conversation', entry: 'thumbnail',
    resultTitle: 'Invented', resultCode: 'R-TST-1', image: 'sample',
    responses: [...fixture.responses, { op: 'entity_refs', request: {}, reply: {}, required: true }] };
  const policy = { version: 2, paths: [], cases: { ...legacy.cases, 'pages/conversations/example:image-open': image },
    images: { sample: { path: '/api/results/invented', png, sha256: 'f86b5f3ad5f392fbfcd6915312c28ffeccd37adbd20bd5d02e4095c89711f26c' } } };
  files.set(v2, Buffer.from(JSON.stringify(policy)));
  const registered = loadPolicy(read);
  assert.notEqual(registered.hash, legacy.hash);
  assert.equal(registered.cases['pages/conversations/example:image-open'].flow, 'image-open');
  // The previous release only reads parity.json. Its input should be byte-for-byte identical.
  assert.deepEqual(read('/etc/fleet-design-verifier/parity.json'), v1);
  for (const cases of [registered.cases, JSON.parse(read('/etc/fleet-design-verifier/parity.json')).cases]) {
    const site = await openSite({ '/index.html': Buffer.from('<main>Legacy</main>').toString('base64') }, cases[scope]);
    t.after(() => site.close());
    assert.equal(await (await fetch(site.origin + fixture.start)).text(), '<main>Legacy</main>');
    assert.deepEqual(await (await fetch(site.origin + '/api/ops/session_view',
      { method: 'POST', body: JSON.stringify(fixture.responses[0].request) })).json(), { head: {} });
    assert.deepEqual(site.unmet(), []);
  }
  files.set(v2, v1);
  assert.throws(() => loadPolicy(read), /Invalid protected app route policy/);
  files.delete(v2);
  assert.equal(loadPolicy(read).hash, legacy.hash);
});

// T-FLT-181: exercise the real Git diff, complete queue membership, disk receipts and Check Run adapter together.
// Fixture GitHub replies cannot qualify a live provider installation.
test('the verifier gives each original head its own receipt and fences the cumulative merge group', async t => {
  const path = mkdtempSync(join(tmpdir(), 'fleet-design-verifier-'));
  t.after(() => rmSync(path, { recursive: true, force: true }));
  const gitCommand = (dir, ...args) => execFileSync('git', args, { cwd: dir, encoding: 'utf8', stdio: ['ignore', 'pipe', 'pipe'] }).trim();
  const work = (...args) => gitCommand(path, ...args);
  const commit = (file, body) => {
    writeFileSync(join(path, file), body);
    work('add', file);
    work('commit', '-qm', body);
    return work('rev-parse', 'HEAD');
  };
  work('init', '-q', '-b', 'main');
  work('config', 'user.email', 'verifier-fixture@example.invalid');
  work('config', 'user.name', 'Verifier fixture');
  const base = commit('README.md', 'base');
  work('switch', '-qc', 'first');
  const first = commit('README.md', 'first');
  work('switch', '-qc', 'unknown', base);
  const second = commit('standalone.txt', 'unknown change');
  work('switch', '-qc', 'second', base);
  mkdirSync(join(path, 'tests'));
  const safeSecond = commit('tests/standalone.test.mjs', 'only test evidence');
  work('switch', '-qc', 'queue', base);
  work('merge', '-q', '--no-ff', 'first', '-m', 'Fleet first merge');
  const queuedFirst = work('rev-parse', 'HEAD');
  work('merge', '-q', '--no-ff', 'second', '-m', 'Fleet second merge');
  const head = work('rev-parse', 'HEAD');
  const ref = 'refs/heads/gh-readonly-queue/main/pr-2-' + queuedFirst;
  work('switch', 'first');
  const fresh = commit('README.md', 'first newer');

  const git = openRepository(path);
  gitCommand(git.dir, 'fetch', '-q', '--no-tags', path, '+refs/heads/*:refs/heads/*');
  const refRow = (ref, sha) => ({ ref, object: { type: 'commit', sha } });
  const pr = (number, oid) => ({ number, state: 'open', head: { sha: oid, ref: 'lane-' + number, repo: { id: repositoryId } },
    base: { sha: base, ref: 'main', repo: { id: repositoryId } } });
  const prs = [pr(1, first), pr(2, safeSecond), pr(3, second)];
  const merge = (sha, parent, original) => ({ sha, parents: [{ sha: parent }, { sha: original }],
    committer: { id: 19864447 }, commit: { verification: { verified: true } } });
  const runs = new Map();
  let runId = 0;
  let groupTrusted = true;
  let queueMemberLimit = 2;
  const mergeRule = { type: 'merge_queue', parameters: { merge_method: 'MERGE' } };
  let effectiveRules = () => [mergeRule];
  let mutateQueuePage = result => result;
  const api = async (endpoint, options) => {
    if (endpoint === '/graphql') {
      const restricted = options.body.query.includes('configuration');
      const cursor = options.body.variables.cursor;
      const members = prs.slice(0, queueMemberLimit);
      const hasNextPage = !cursor && members.length > 1;
      return mutateQueuePage({ ...(restricted && { errors: [{ type: 'FORBIDDEN', path: ['repository', 'mergeQueue', 'configuration'] }] }),
        data: { repository: { databaseId: repositoryId, nameWithOwner: 'taskqorg/fleet',
          mergeQueue: { entries: { totalCount: members.length, pageInfo: { hasNextPage, endCursor: 'page-two' },
            nodes: (cursor ? members.slice(1) : members.slice(0, 1)).map(({ number, head: { sha } }) => ({
              id: 'queued-' + number, pullRequest: { number, headRefOid: sha, baseRefName: 'main', state: 'OPEN' },
            })) } } } } }, cursor);
    }
    const request = new URL(endpoint, 'https://api.github.com');
    const pathname = request.pathname.slice(repo.length);
    if (pathname === '/rules/branches/main') return effectiveRules(request.searchParams.get('page'));
    if (pathname === '/pulls') return prs;
    if (pathname === '/pulls/1') return prs[0];
    if (pathname === '/pulls/2') return prs[1];
    if (pathname === '/pulls/3') return prs[2];
    if (pathname === '/git/matching-refs/heads/gh-readonly-queue/main/') return [refRow(ref, head)];
    if (pathname === '/git/ref/heads/main') return refRow('refs/heads/main', base);
    if (pathname === '/git/ref/' + ref.slice(5)) return refRow(ref, head);
    if (pathname === '/commits/' + queuedFirst) return merge(queuedFirst, base, first);
    if (pathname === '/commits/' + head) {
      const record = merge(head, queuedFirst, safeSecond);
      record.commit.verification.verified = groupTrusted;
      return record;
    }
    if (pathname === '/check-runs' || pathname.startsWith('/check-runs/')) {
      if (!options) return runs.get(Number(pathname.split('/').at(-1)));
      const current = options.method === 'PATCH' ? runs.get(Number(pathname.split('/').at(-1))) :
        { id: ++runId, app: { id: appId }, conclusion: null };
      Object.assign(current, options.body);
      runs.set(current.id, current);
      return current;
    }
    const sha = /^\/commits\/([a-f0-9]{40})\/check-runs$/.exec(pathname)?.[1];
    assert(sha, 'unhandled mock endpoint ' + endpoint);
    const current = [...runs.values()].findLast(run => run.head_sha === sha);
    return { total_count: current ? 1 : 0, check_runs: current ? [current] : [] };
  };
  const provider = githubCandidates(api);
  const checks = fleetDesignChecks(api, appId);
  const receipts = await receiptStore(path, 'd'.repeat(40), checks);
  const deps = { provider, git: openAsyncRepository(path), checks, receipts };
  const [firstPR, secondPR, unknown, group] = await provider.snapshot();
  assert.equal((await evaluate(group, deps)).conclusion, null);
  assert.equal((await evaluate(firstPR, deps)).conclusion, 'success');
  assert.equal((await evaluate(group, deps)).conclusion, null);
  assert.equal((await evaluate(secondPR, deps)).conclusion, 'success');
  const grouped = await evaluate(group, deps);
  assert.equal(grouped.conclusion, 'success');
  assert.equal(grouped.head, head);
  assert.equal(grouped.appId, appId);
  const retained = JSON.parse(readFileSync(join(path, 'receipts', grouped.externalId + '.json'), 'utf8'));
  assert.equal(retained.evidence.queue.mergeMethod, 'MERGE');
  assert.equal((await evaluate(group, deps)).checkRunId, grouped.checkRunId);
  const candidate = await provider.readCandidate(group, git);
  const conflictingRule = { type: 'merge_queue', parameters: { merge_method: 'SQUASH' } };
  let ruleReads = 0;
  effectiveRules = () => [ruleReads++ === 0 ? mergeRule : conflictingRule];
  const drifted = await evaluate(group, deps);
  assert.equal(ruleReads, 2);
  assert.equal(drifted.conclusion, null);
  assert.notEqual(drifted.checkRunId, grouped.checkRunId);
  assert.match(drifted.reason, /one effective MERGE queue rule/);

  for (const rules of [[], [{ type: 'merge_queue' }], [conflictingRule], [mergeRule, mergeRule]]) {
    effectiveRules = () => rules;
    const pending = await evaluate(group, deps);
    assert.equal(pending.conclusion, null);
    assert.match(pending.reason, /one effective MERGE queue rule/);
  }
  effectiveRules = page => page === '1'
    ? [mergeRule, ...Array.from({ length: 99 }, () => ({ type: 'deletion' }))]
    : [conflictingRule];
  assert.equal((await evaluate(group, deps)).conclusion, null);
  effectiveRules = () => { throw Object.assign(new Error('Forbidden'), { status: 403 }); };
  assert.equal((await evaluate(group, deps)).conclusion, null);

  effectiveRules = () => [mergeRule];

  mutateQueuePage = (result, cursor) => cursor ? { ...result, errors: [{ type: 'FORBIDDEN' }] } : result;
  assert.match((await evaluate(group, deps)).reason, /did not confirm the live merge queue/);
  mutateQueuePage = (result, cursor) => {
    if (cursor) result.data.repository.mergeQueue.entries.totalCount++;
    return result;
  };
  assert.match((await evaluate(group, deps)).reason, /inconsistent merge queue count/);
  mutateQueuePage = result => result;

  queueMemberLimit = 1;
  await assert.rejects(provider.assertCurrent(group, candidate), /left the queue/);
  queueMemberLimit = 2;
  groupTrusted = false;
  assert.equal((await evaluate(group, deps)).conclusion, null);
  assert.equal((await evaluate(unknown, deps)).conclusion, 'failure');
  const stopped = new AbortController(); stopped.abort();
  let stoppedWrites = 0;
  await assert.rejects(evaluate(unknown, { ...deps, signal: stopped.signal,
    checks: { publish: async () => { stoppedWrites++; } } }), /Verifier stopped/);
  assert.equal(stoppedWrites, 0);
  const gitStop = new AbortController();
  const stalled = { ...deps.git, ensure: () => runGitProcess(process.execPath,
    ['-e', 'setTimeout(() => {}, 10000)'], { signal: gitStop.signal }) };
  const pendingGit = evaluate(unknown, { ...deps, git: stalled, signal: gitStop.signal,
    checks: { publish: async () => { stoppedWrites++; } } });
  setTimeout(() => gitStop.abort(), 150);
  await assert.rejects(pendingGit, /Verifier stopped/);
  assert.equal(stoppedWrites, 0);
  const during = new AbortController();
  await assert.rejects(evaluate(unknown, { ...deps, signal: during.signal,
    checks: { publish: async (_head, _id, _conclusion, _summary, assertCurrent) => {
      during.abort(); await assertCurrent(); stoppedWrites++;
    } } }), /Verifier stopped/);
  assert.equal(stoppedWrites, 0);


  const pendingRenderer = await evaluate(unknown, { ...deps, rendererPending: true });
  assert.equal(pendingRenderer.conclusion, null);
  assert.match(pendingRenderer.reason, /temporarily unavailable/);
  const runner = {
    impact: (event, paths) => ({ ...event, policy: {}, paths: { [paths[0]]: ['pages/conversations/detail:default'] } }),
  };
  const absent = await evaluate(unknown, { ...deps, runner, fleet: { evidence: async () => null } });
  assert.equal(absent.conclusion, 'failure');
  assert.equal(absent.evidenceState, 'absent');
  const stale = await evaluate(unknown, { ...deps, runner, fleet: { evidence: async () => {
    throw new DesignBlocked('Incompatible selected decision');
  } } });
  assert.equal(stale.conclusion, 'failure');
  assert.equal(stale.evidenceState, 'blocked');
  assert.notEqual(stale.externalId, absent.externalId);
  prs[0] = pr(1, fresh);
  await assert.rejects(evaluate(firstPR, deps), /source, head or base changed/);
  const [newEvent] = await provider.snapshot();
  const newResult = await evaluate(newEvent, deps);
  assert.equal(newResult.head, fresh);
  assert.equal(newResult.conclusion, 'success');
});

test('the isolated app origin serves only the registered routes and exact fixture requests', async () => {
  const site = await openSite({ '/index.html': Buffer.from('<div id="app"></div>').toString('base64') }, {
    start: '/library/bots/B-FLT-1', target: '/conversations/C-FLT-1', forbiddenOps: ['sessions_list'],
    responses: [{ op: 'session_view', request: { sessionId: 'fixture', limit: 2 }, reply: { title: 'Fixture' }, required: true }],
  });
  try {
    assert.deepEqual(site.unmet(), ['session_view']);
    assert.equal((await fetch(site.origin + '/library/bots/B-FLT-1')).status, 200);
    assert.equal((await fetch(site.origin + '/conversations/C-FLT-1')).status, 200);
    assert.equal((await fetch(site.origin + '/conversations/another')).status, 404);
    const post = (op, request) => fetch(site.origin + '/api/ops/' + op, { method: 'POST', body: JSON.stringify(request) });
    assert.equal((await post('session_view', { sessionId: 'another', limit: 2 })).status, 404);
    assert.deepEqual(await (await post('session_view', { limit: 2, sessionId: 'fixture' })).json(), { title: 'Fixture' });
    assert.deepEqual(site.unmet(), []);
    assert.deepEqual([...site.missing], ['session_view']);
    const stream = await fetch(site.origin + '/api/stream?cursor=0');
    const reader = stream.body.getReader();
    assert.match(Buffer.from((await reader.read()).value).toString(), /event: ready/);
    await reader.cancel();
  } finally {
    await site.close();
  }
});

test('only a root-owned systemd credential accepts the service ACL mode', () => {
  const systemdFile = '/run/credentials/fleet-design-verifier.service/github-app.pem';
  const credential = mode => ({ uid: 0, gid: 0, mode, isFile: () => true });
  assert.equal(designCredentialIsPrivate(systemdFile, credential(0o440)), true);
  assert.equal(designCredentialIsPrivate(systemdFile, { ...credential(0o440), uid: 1000 }), false);
  assert.equal(designCredentialIsPrivate('/somewhere/github-app.pem', credential(0o440)), false);
  assert.equal(designCredentialIsPrivate(systemdFile, credential(0o460)), false);
  assert.equal(designCredentialIsPrivate(systemdFile, credential(0o444)), false);
  assert.equal(designCredentialIsPrivate('/somewhere/github-app.pem', credential(0o600)), true);
});

test('a restarted issuer reacquires a short-lived installation token and closes only its own access', async () => {
  const auth = { issued: 0, revoked: [] };
  const options = {
    connect: async () => ({ token: ++auth.issued, expires: Date.now() + 90_000, authenticatedAt: 1, keyFingerprint: 'fixture' }),
    send: async (endpoint, token, request) => {
      if (request?.method === 'DELETE') auth.revoked.push(token);
      return endpoint === '/fixture' ? token : null;
    },
  };
  let client = fleetDesignGitHub({}, options);
  assert.equal(await client('/fixture'), 1);
  assert.equal(await client('/fixture'), 1);
  await client.close();
  assert.equal(client.authentication(), undefined);
  client = fleetDesignGitHub({}, options);
  assert.equal(await client('/fixture'), 2);
  await client.close();
  assert.deepEqual(auth, { issued: 2, revoked: [1, 2] });
});

test('stopping during token renewal never starts the check write and revokes a late token', async () => {
  const stop = new AbortController(), calls = [];
  let started, finish;
  const entered = new Promise(resolve => { started = resolve; });
  const client = fleetDesignGitHub({}, { signal: stop.signal,
    connect: async () => { started(); return new Promise(resolve => { finish = resolve; }); },
    send: async (path, token, options) => { calls.push({ path, token, method: options.method }); },
  });
  const write = client('/repos/taskqorg/fleet/check-runs', { method: 'POST', body: {} });
  await entered; stop.abort();
  await assert.rejects(write);
  finish({ token: 'late-fixture', expires: Date.now() + 90_000 });
  await new Promise(resolve => setImmediate(resolve));
  assert.deepEqual(calls, [{ path: '/installation/token', token: 'late-fixture', method: 'DELETE' }]);
});

test('stopping a stalled provider request releases the poll and revokes its token', async () => {
  const stop = new AbortController(), calls = [];
  let entered;
  const started = new Promise(resolve => { entered = resolve; });
  const client = fleetDesignGitHub({}, { signal: stop.signal,
    connect: async () => ({ token: 'fixture', expires: Date.now() + 90_000 }),
    send: async (path, token, options) => {
      calls.push({ path, token, method: options.method });
      if (options.method !== 'DELETE') { entered(); return new Promise(() => {}); }
    },
  });
  const request = client('/fixture'); await started; stop.abort();
  await assert.rejects(request);
  await client.close();
  assert.deepEqual(calls.at(-1), { path: '/installation/token', token: 'fixture', method: 'DELETE' });
});

test('stopping cancels an in-flight Fleet evidence request', async t => {
  const { fleetVerifierClient } = await import('../../deploy/design-verifier/fleet.mjs');
  const dir = mkdtempSync(join(tmpdir(), 'fleet-evidence-stop-'));
  t.after(() => rmSync(dir, { recursive: true, force: true }));
  writeFileSync(join(dir, 'fleet-access.json'), JSON.stringify({ clientId: 'invented-id', clientSecret: 'invented-secret' }), { mode: 0o600 });
  const stop = new AbortController();
  let entered;
  const started = new Promise(resolve => { entered = resolve; });
  const client = await fleetVerifierClient(dir, (_url, options) => new Promise((_, reject) => {
    entered(); options.signal.addEventListener('abort', () => reject(options.signal.reason), { once: true });
  }), { signal: stop.signal });
  const probe = client.probe(); await started; stop.abort();
  await assert.rejects(probe);
});

test('token revocation is bounded even when the provider transport stalls', async () => {
  const client = fleetDesignGitHub({}, {
    connect: async () => ({ token: 'fixture', expires: Date.now() + 90_000 }),
    send: async (_path, _token, options) => options.method === 'DELETE' ? new Promise(() => {}) : {},
  });
  await client('/fixture');
  const started = Date.now();
  await assert.rejects(client.close(), /revocation|timeout|abort/i);
  assert.ok(Date.now() - started < 5000);
});

test('final Fleet stop observation uses a fresh bounded signal', async t => {
  const { fleetVerifierClient } = await import('../../deploy/design-verifier/fleet.mjs');
  const dir = mkdtempSync(join(tmpdir(), 'fleet-stop-observation-'));
  t.after(() => rmSync(dir, { recursive: true, force: true }));
  writeFileSync(join(dir, 'fleet-access.json'), JSON.stringify({ clientId: 'invented', clientSecret: 'invented' }), { mode: 0o600 });
  const stop = new AbortController(); stop.abort(); const calls = [];
  const client = await fleetVerifierClient(dir, async (url, options) => {
    calls.push({ url, body: JSON.parse(options.body), aborted: options.signal.aborted });
    return { ok: true, json: async () => ({ ok: true }) };
  }, { signal: stop.signal });
  await assert.rejects(client.observe({ kind:'provider', state:'unknown', revision:'a'.repeat(40) }));
  await client.observe({ kind:'provider', state:'unknown', revision:'a'.repeat(40) }, { signal: AbortSignal.timeout(1000) });
  assert.equal(calls.length, 1); assert.equal(calls[0].aborted, false); assert.equal(calls[0].body.state, 'unknown');
});

test('lost mint response emits uncertainty without claiming token revocation', async () => {
  const { mintDesignToken } = await import('../../packages/ui/scripts/design-github-checks.mjs');
  const lines = [], original = console.error;
  console.error = line => lines.push(String(line));
  try {
    await assert.rejects(mintDesignToken(async () => { throw Error('connection lost'); }, '/fixture', 'redacted', {}));
    assert.match(lines[0], /issuance outcome unconfirmed/);
    assert.doesNotMatch(lines[0], /revoked/);
    lines.length = 0;
    await assert.rejects(mintDesignToken(async () => { throw Object.assign(Error('refused'), { status:403 }); }, '/fixture', 'redacted', {}));
    assert.equal(lines.length, 0);
    await assert.rejects(mintDesignToken(async () => { throw Object.assign(Error('server failed'), { status:503 }); }, '/fixture', 'redacted', {}));
    assert.match(lines[0], /issuance outcome unconfirmed/);
  } finally { console.error = original; }
});
