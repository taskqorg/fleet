import test from 'node:test';
import assert from 'node:assert/strict';
import { execFileSync } from 'node:child_process';
import { mkdtempSync, writeFileSync, rmSync, existsSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { openAsyncRepository, runGitProcess } from '../../deploy/design-verifier/git-isolated.mjs';
import { openRepository } from '../../deploy/design-verifier/git.mjs';
import { candidateChanges, ProtectedDesignRejection } from '../../packages/ui/scripts/design-protected.mjs';

const git = (cwd, ...args) => execFileSync('/usr/bin/git', args, { cwd, encoding: 'utf8', stdio: ['ignore', 'pipe', 'pipe'] }).trim();

test('isolated Git agrees with the existing synchronous reader', async () => {
  const state = mkdtempSync(join(tmpdir(), 'fleet-git-test-'));
  try {
    git(state, 'init', '-q', 'work');
    const work = join(state, 'work');
    git(work, 'config', 'user.email', 'test@example.invalid'); git(work, 'config', 'user.name', 'Test');
    writeFileSync(join(work, 'file.txt'), 'first\n');
    git(work, 'add', 'file.txt'); git(work, 'commit', '-qm', 'first'); const base = git(work, 'rev-parse', 'HEAD');
    writeFileSync(join(work, 'file.txt'), 'second\n');
    git(work, 'commit', '-qam', 'second'); const head = git(work, 'rev-parse', 'HEAD');
    git(state, 'clone', '-q', '--bare', work, 'git');
    const sync = openRepository(state), asyncGit = openAsyncRepository(state);
    const event = { kind: 'pull_request', base, head };
    await asyncGit.ensure(event);
    assert.equal(await asyncGit.diffBase(event), sync.diffBase(event));
    assert.equal(await asyncGit.fingerprint(head, 'file.txt'), sync.fingerprint(head, 'file.txt'));
    assert.deepEqual(await asyncGit.changes(event), candidateChanges(sync.dir, event));
    await assert.rejects(asyncGit.changes({ kind: 'merge_group', base: head, head: base }), ProtectedDesignRejection);
    assert.deepEqual(await asyncGit.archive(head, {}), sync.archive(head, {}));
  } finally { rmSync(state, { recursive: true, force: true }); }
});

test('pre-aborted Git never starts a process', async () => {
  const stop = new AbortController(); stop.abort();
  await assert.rejects(runGitProcess('/not-a-command', [], { signal: stop.signal }), /stopped/);
});

test('abort kills a running process group and does not deliver late output', async () => {
  const stop = new AbortController();
  const pending = runGitProcess(process.execPath, ['-e', 'setTimeout(() => process.stdout.write("late"), 10000)'], { signal: stop.signal });
  setTimeout(() => stop.abort(), 150);
  await assert.rejects(pending, /stopped/);
});

test('watchdog kills a running process', async () => {
  await assert.rejects(runGitProcess(process.execPath, ['-e', 'setTimeout(() => {}, 10000)'], { timeout: 150 }), /timed out/);
});

test('abort kills Git descendants before returning', async () => {
  const dir = mkdtempSync(join(tmpdir(), 'fleet-git-descendant-'));
  const marker = join(dir, 'late-write');
  try {
    const stop = new AbortController();
    const code = `const {spawn}=require('node:child_process');
      spawn(process.execPath,['-e',${JSON.stringify("setTimeout(() => require('node:fs').writeFileSync(process.argv[1], 'late'), 900)")},${JSON.stringify(marker)}],{stdio:'ignore'});
      setTimeout(()=>{},10000);`;
    const pending = runGitProcess(process.execPath, ['-e', code], { signal: stop.signal });
    setTimeout(() => stop.abort(), 300);
    await assert.rejects(pending, /stopped/);
    await new Promise(resolve => setTimeout(resolve, 800));
    assert.equal(existsSync(marker), false);
  } finally { rmSync(dir, { recursive: true, force: true }); }
});
