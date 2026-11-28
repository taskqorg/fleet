import assert from 'node:assert/strict';
import { execFileSync } from 'node:child_process';
import { mkdtempSync, mkdirSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { test } from 'node:test';
import { designCredentialIsPrivate, fleetDesignChecks, fleetDesignGitHub } from '../../packages/ui/scripts/design-github-checks.mjs';
import { appId, githubCandidates, repo, repositoryId } from '../../deploy/design-verifier/provider.mjs';
import { openRepository } from '../../deploy/design-verifier/git.mjs';
import { receiptStore } from '../../deploy/design-verifier/receipts.mjs';
import { evaluate } from '../../deploy/design-verifier/verifier.mjs';

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
  const api = async (endpoint, options) => {
    if (endpoint === '/graphql') return { data: { repository: { databaseId: repositoryId, nameWithOwner: 'taskqorg/fleet',
      mergeQueue: { configuration: { mergeMethod: 'MERGE' }, entries: { totalCount: queueMemberLimit, pageInfo: { hasNextPage: false },
        nodes: prs.slice(0, queueMemberLimit).map(({ number, head: { sha } }) => ({ id: 'queued-' + number,
          pullRequest: { number, headRefOid: sha, baseRefName: 'main', state: 'OPEN' } })) } } } } };
    const request = new URL(endpoint, 'https://api.github.com');
    const pathname = request.pathname.slice(repo.length);
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
  const deps = { provider, git, checks, receipts };
  const [firstPR, secondPR, unknown, group] = await provider.snapshot();
  assert.equal((await evaluate(group, deps)).conclusion, null);
  assert.equal((await evaluate(firstPR, deps)).conclusion, 'success');
  assert.equal((await evaluate(group, deps)).conclusion, null);
  assert.equal((await evaluate(secondPR, deps)).conclusion, 'success');
  const grouped = await evaluate(group, deps);
  assert.equal(grouped.conclusion, 'success');
  assert.equal(grouped.head, head);
  assert.equal(grouped.appId, appId);
  assert.equal((await evaluate(group, deps)).checkRunId, grouped.checkRunId);
  const candidate = await provider.readCandidate(group, git);
  queueMemberLimit = 1;
  await assert.rejects(provider.assertCurrent(group, candidate), /left the queue/);
  queueMemberLimit = 2;
  groupTrusted = false;
  assert.equal((await evaluate(group, deps)).conclusion, null);
  assert.equal((await evaluate(unknown, deps)).conclusion, 'failure');
  prs[0] = pr(1, fresh);
  await assert.rejects(evaluate(firstPR, deps), /source, head or base changed/);
  const [newEvent] = await provider.snapshot();
  const newResult = await evaluate(newEvent, deps);
  assert.equal(newResult.head, fresh);
  assert.equal(newResult.conclusion, 'success');
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
