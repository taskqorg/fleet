import test from 'node:test';
import assert from 'node:assert/strict';
import { execFileSync } from 'node:child_process';
import { mkdirSync, mkdtempSync, readFileSync, writeFileSync, rmSync, existsSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { openAsyncRepository, runGitProcess } from '../../deploy/design-verifier/git-isolated.mjs';
import { fetchGitObjects, openRepository } from '../../deploy/design-verifier/git.mjs';
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

test('fetch retry acquires exact objects without touching a stranded durable ref lock', t => {
  const root = mkdtempSync(join(tmpdir(), 'fleet-git-retry-'));
  t.after(() => rmSync(root, { recursive: true, force: true }));
  git(root, 'init', '-q', '--initial-branch=main', 'remote');
  const remote = join(root, 'remote');
  const commit = message => {
    git(remote, '-c', 'user.email=test@example.invalid', '-c', 'user.name=Test',
      'commit', '-qm', message, '--allow-empty');
    return git(remote, 'rev-parse', 'HEAD');
  };
  const base = commit('base');
  const head = commit('candidate');
  const source = 'refs/pull/1/head';
  git(remote, 'update-ref', 'refs/heads/main', base);
  git(remote, 'update-ref', source, head);
  const { dir } = openRepository(root);
  mkdirSync(join(dir, 'refs', 'fleet-design'));
  const lock = join(dir, 'refs', 'fleet-design', 'last-candidate.lock');
  writeFileSync(lock, 'fixture stale lock');
  const gitAt = (cwd, args, env) => execFileSync('/usr/bin/git', args, {
    cwd, env, stdio: ['ignore', 'pipe', 'pipe'],
  });
  const fetch = (...args) => fetchGitObjects(gitAt, mkdtempSync(join(root, 'slot-')),
    join(dir, 'objects'), remote, ...args);
  fetch(source);
  assert.equal(git(dir, 'cat-file', '-t', base), 'commit');
  assert.equal(git(dir, 'cat-file', '-t', head), 'commit');
  const next = commit('retry');
  git(remote, 'update-ref', source, next);
  fetch(source, base);
  assert.equal(git(dir, 'cat-file', '-t', next), 'commit');
  assert.equal(readFileSync(lock, 'utf8'), 'fixture stale lock');
  assert.equal(git(dir, 'for-each-ref'), '');
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

test('unconfirmed kill releases child and pipe references within the cleanup deadline', async () => {
  const { spawn } = await import('node:child_process');
  const moduleURL = new URL('../../deploy/design-verifier/git-isolated.mjs', import.meta.url).href;
  const code = `import {runGitProcess} from ${JSON.stringify(moduleURL)};
    const stop = new AbortController();
    const task = runGitProcess(process.execPath, ['-e','setTimeout(()=>{},1500)'], {
      signal:stop.signal, cleanupTimeout:100,
      kill:()=>{const e = new Error('fixture'); e.code='EPERM'; throw e;},
      onUnconfirmed:()=>{}
    });
    setTimeout(()=>stop.abort(),50);
    try { await task; process.exitCode=1; } catch(e) {
      if (!e.message.includes('cleanup unconfirmed')) process.exitCode=2;
    }`;
  const start = Date.now();
  const child = spawn(process.execPath, ['--input-type=module', '-e', code], { stdio: 'ignore' });
  const codeResult = await new Promise((resolve, reject) => { child.on('error', reject); child.on('close', resolve); });
  assert.equal(codeResult, 0);
  assert.ok(Date.now() - start < 1000, 'issuer retained the stuck child or a pipe');
});
