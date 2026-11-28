import assert from 'node:assert/strict';
import { execFileSync } from 'node:child_process';
import { chmodSync, lstatSync, mkdirSync, mkdtempSync, readFileSync, readdirSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { test } from 'node:test';
import { designerSource, packOverlay, hash, verifyRequestedSource } from '../../deploy/design-verifier/designer-source.mjs';
import { openSite } from '../../deploy/design-verifier/worker/site.mjs';
import { appOriginalsFixture } from '../../scripts/app-originals-fixture.ts';

test('private import captures detached worktree without writing its Git metadata and replays exactly', t => {
  const root = mkdtempSync(join(tmpdir(), 'fleet-originals-'));
  const repo = join(root, 'repo');
  const checkout = join(root, 'checkout');
  mkdirSync(repo);
  let gitCwd = repo;
  const git = (args, input) => execFileSync('/usr/bin/git', args, { cwd: gitCwd, input, stdio: ['pipe', 'pipe', 'pipe'] })
    .toString().trim();
  git(['init', '-q']);
  mkdirSync(join(repo, 'app/src'), { recursive: true });
  const path = 'app/src/draft.tsx';
  writeFileSync(join(repo, path), 'export const title = "Base";\n');
  git(['add', path]);
  git(['-c', 'user.name=Fixture', '-c', 'user.email=fixture@example.invalid', 'commit', '-qm', 'Parent']);
  writeFileSync(join(repo, path), 'export const title = "Base"; // with parent\n');
  git(['add', path]);
  git(['-c', 'user.name=Fixture', '-c', 'user.email=fixture@example.invalid', 'commit', '-qm', 'Base']);
  const base = git(['rev-parse', 'HEAD']);
  const baseTree = git(['rev-parse', 'HEAD^{tree}']);
  const bare = join(root, 'depth-one.git');
  git(['init', '--bare', '-q', bare]);
  execFileSync('/usr/bin/git', ['--git-dir', bare, 'fetch', '--depth=1', '--no-tags', 'file://' + repo,
    base + ':refs/heads/release'], { stdio: 'pipe' });
  const bundle = join(root, 'base.bundle');
  execFileSync('/usr/bin/git', ['--git-dir', bare, 'bundle', 'create', bundle, 'refs/heads/release'], { stdio: 'ignore' });
  git(['worktree', 'add', '--detach', checkout, base]);
  gitCwd = checkout;
  writeFileSync(join(checkout, path), 'export const title = "Draft";\n');
  // Simulate the Design preset: the linked Git directory is not writable.
  const protect = dir => { for (const name of readdirSync(dir)) { const p = join(dir, name); const st = lstatSync(p);
    if (st.isDirectory()) protect(p); else if (st.isFile()) chmodSync(p, 0o400); } chmodSync(dir, 0o500); };
  const unprotect = dir => { chmodSync(dir, 0o700); for (const name of readdirSync(dir)) { const p = join(dir, name); const st = lstatSync(p);
    if (st.isDirectory()) unprotect(p); else if (st.isFile()) chmodSync(p, 0o600); } };
  assert.ok(lstatSync(join(checkout, '.git')).isFile());
  const dotgit = join(repo, '.git');
  protect(dotgit);
  t.after(() => { unprotect(dotgit); rmSync(root, { recursive: true, force: true }); });
  const before = git(['status', '--porcelain']);
  const overlay = packOverlay(checkout, base, [path]);
  const draft = designerSource(readFileSync(bundle), overlay, {});
  assert.equal(draft.baseTree, baseTree);
  assert.deepEqual(draft.paths, [path]);
  assert.equal(git(['status', '--porcelain']), before);
  const replay = designerSource(readFileSync(bundle), { version: 1, base, files: [] }, {}, draft);
  assert.equal(replay.tree, draft.tree);
  assert.equal(hash(replay.archive), hash(draft.archive));
  assert.equal(draft.fingerprints[path], hash('export const title = "Draft";\n'));
  const overlayBytes = Buffer.from(JSON.stringify(overlay) + '\n');
  const bound = { baseBundle: readFileSync(bundle), overlayBytes, patch: draft.patch,
    replay: { base, baseTree, tree: draft.tree, paths: draft.paths, overlaySha256: hash(overlayBytes), archiveSha256: hash(draft.archive) } };
  verifyRequestedSource(bound, {}, root);
  assert.throws(() => verifyRequestedSource({ ...bound, replay: { ...bound.replay, paths: [] } }, {}, root), /reconstruct/);
  assert.throws(() => verifyRequestedSource({ ...bound, patch: Buffer.from('unrelated patch') }, {}, root), /reconstruct/);
  const otherOverlay = Buffer.from(JSON.stringify({ ...overlay, files: [] }) + '\n');
  assert.throws(() => verifyRequestedSource({ ...bound, overlayBytes: otherOverlay }, {}, root), /digest/);
  assert.throws(() => verifyRequestedSource({ ...bound, overlayBytes: otherOverlay,
    replay: { ...bound.replay, overlaySha256: hash(otherOverlay) } }, {}, root), /reconstruct/);
  assert.throws(() => designerSource(readFileSync(bundle), overlay, { [path]: '100644:' + '0'.repeat(64) }), /protected path/);
  assert.throws(() => designerSource(readFileSync(bundle), { version: 1, base: '1'.repeat(40), files: [] }, {}));
  const changed = Buffer.from(draft.patch.toString().replace('title = "Draft"', 'title = "Changed"'));
  assert.throws(() => designerSource(readFileSync(bundle), { version: 1, base, files: [] }, {}, { ...draft, patch: changed }), /different Git tree/);
  assert.throws(() => packOverlay(checkout, base, ['../outside']), /Unsupported draft path/);
});

test('the designer fixture uses exact synthetic requests on real paths', async () => {
  for (const { scenario } of appOriginalsFixture.cases) {
    const files = { '/index.html': Buffer.from('<script type="module" src="/assets/main-test.js"></script>').toString('base64'),
      '/assets/main-test.js': Buffer.from('export const fixture = true').toString('base64') };
    const site = await openSite(files, scenario, true);
    try {
      const html = await fetch(site.origin + scenario.start);
      assert.equal(html.status, 200);
      const home = await fetch(site.origin + '/home');
      assert.equal(home.status, 200);
      assert.match(home.headers.get('content-type'), /^text\/html/);
      const js = await fetch(site.origin + '/assets/main-test.js');
      assert.equal(js.status, 200);
      assert.match(js.headers.get('content-type'), /^text\/javascript/);
      const call = (op, request) => fetch(site.origin + '/api/ops/' + op, { method: 'POST', body: JSON.stringify(request) });
      for (const { op, request, reply, required } of scenario.responses) {
        if (!required) continue;
        const result = await call(op, request);
        assert.equal(result.status, 200);
        assert.deepEqual(await result.json(), reply);
      }
      assert.deepEqual(site.unmet(), []);
      assert.equal((await call('boot', { unexpected: true })).status, 404);
      assert.deepEqual([...site.missing], ['boot']);
      assert.deepEqual([...site.unmatched], ['boot:{"unexpected":true}']);
    } finally {
      await site.close();
    }
  }
});
