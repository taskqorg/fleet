import { execFileSync } from 'node:child_process';
import { createHash } from 'node:crypto';
import { existsSync } from 'node:fs';
import { join } from 'node:path';
import { DesignBlocked, sha, VerificationWait } from './provider.mjs';

const maxArchive = 96 * 1024 * 1024;
const sha256 = bytes => createHash('sha256').update(bytes).digest('hex');
export const sourcePath = path => typeof path === 'string' && path.length < 512 && path.split('/').every(
  part => /^[A-Za-z0-9_.@+ -]{1,120}$/.test(part) && !['.', '..', '.git'].includes(part.toLowerCase()));

export function fetchGitObjects(gitAt, slot, objects, remote, source, negotiationTip) {
  gitAt(slot, ['init', '--bare', '.']);
  gitAt(slot, ['fetch', '--keep', '--no-tags', '--no-write-fetch-head', '--no-recurse-submodules',
    '--no-auto-maintenance', '--no-write-commit-graph', '--refmap=',
    ...(negotiationTip ? ['--negotiation-tip=' + negotiationTip] : []), '--', remote, 'refs/heads/main', source],
  { GIT_ALTERNATE_OBJECT_DIRECTORIES: JSON.stringify(objects) });
}

// This Git process only reads objects. It has no checkout, hooks, helpers or credentials.
export function validateArchiveTree(git, tree, signedBuildFiles) {
  const rows = new TextDecoder('utf-8', { fatal: true }).decode(git('ls-tree', '-r', '-z', '--full-tree', tree)).split('\0');
  rows.pop();
  for (const row of rows) {
    const entry = /^100(?:644|755) blob ([a-f0-9]{40})\t(.*)$/.exec(row);
    if (!entry || !sourcePath(entry[2])) throw new DesignBlocked('Candidate tree contains an unsupported file type or name');
    const parts = entry[2].split('/');
    if (parts.includes('node_modules') || parts[0] === 'dist')
      throw new DesignBlocked('Candidate tree contains a reserved build output path');
    if (/^(?:package|tsconfig|jsconfig)\.json$/.test(parts.at(-1)) && !Object.hasOwn(signedBuildFiles, entry[2]))
      throw new DesignBlocked('Candidate tree contains an unpinned module or compiler configuration');
    if (entry[2].split('/').at(-1) === '.gitattributes' &&
        /export-(?:ignore|subst)/.test(git('cat-file', 'blob', entry[1]).toString('utf8')))
      throw new DesignBlocked('Candidate Git export attributes can change checkout content');
  }
}

export function openRepository(state) {
  const dir = join(state, 'git');
  const env = { PATH: '/usr/bin:/bin', GIT_CONFIG_NOSYSTEM: '1', GIT_CONFIG_GLOBAL: '/dev/null',
    GIT_TERMINAL_PROMPT: '0', GIT_ALLOW_PROTOCOL: 'https', GIT_NO_REPLACE_OBJECTS: '1' };
  const gitAt = (cwd, args, extraEnv) => execFileSync('/usr/bin/git', ['-c', 'core.hooksPath=/dev/null', ...args],
    { env: { ...env, ...extraEnv }, cwd, encoding: 'buffer', stdio: ['ignore', 'pipe', 'pipe'],
      timeout: 60_000, maxBuffer: maxArchive });
  const git = (...args) => gitAt(existsSync(dir) ? dir : state, args);
  if (!existsSync(join(dir, 'HEAD'))) git('init', '--bare', dir);
  const remote = 'https://github.com/taskqorg/fleet.git';
  function read(...args) { return git(...args).toString('utf8').trim(); }
  function has(oid) {
    try { return read('cat-file', '-t', oid) === 'commit'; } catch { return false; }
  }
  function blob(head, path) {
    if (!sha(head) || !sourcePath(path)) throw new DesignBlocked('Unsupported path in a candidate input');
    const entry = git('--literal-pathspecs', 'ls-tree', '-z', '--full-tree', head, '--', path).toString('utf8');
    if (!entry) return null;
    const match = /^(100(?:644|755)) blob ([a-f0-9]{40})\t/.exec(entry);
    if (!match || entry.slice(match[0].length) !== path + '\0')
      throw new DesignBlocked('A candidate input is outside the supported regular Git tree');
    return { mode: match[1], bytes: git('cat-file', 'blob', match[2]) };
  }
  return {
    dir,
    ensure({ base, head, kind, number, ref }, fetchSlot) {
      if (!sha(base) || !sha(head)) throw new VerificationWait('Invalid Git commit identity');
      const basePresent = has(base);
      if (basePresent && has(head)) return false;
      if (!fetchSlot) throw new VerificationWait('The provider commits are not available');
      const source = kind === 'merge_group' ? ref : 'refs/pull/' + number + '/head';
      const objects = join(dir, 'objects');
      fetchGitObjects(gitAt, fetchSlot, objects, remote, source, basePresent ? base : undefined);
      const env = { GIT_ALTERNATE_OBJECT_DIRECTORIES: JSON.stringify(objects) };
      for (const oid of [base, head]) {
        if (gitAt(fetchSlot, ['cat-file', '-t', oid], env).toString().trim() !== 'commit')
          throw new VerificationWait('The provider commits are not available');
      }
      return true;
    },
    fingerprint(head, path) {
      const file = blob(head, path);
      return file && file.mode + ':' + sha256(file.bytes);
    },
    diffBase({ base, head }) { return read('merge-base', base, head); },
    archive(head, signedBuildFiles) {
      if (!sha(head)) throw new VerificationWait('Invalid render commit');
      validateArchiveTree(git, head, signedBuildFiles);
      return git('archive', '--format=tar', head);
    },
    measureInputs(head, recipes) {
      if (!sha(head)) throw new VerificationWait('Invalid recipe source revision');
      const hashes = new Map();
      return recipes.map(({ scopeKey, inputs }) => {
        const changed = [], dependencies = [];
        let matched = 0;
        for (const { path, sha256 } of inputs) {
          if (!sourcePath(path) || !/^[a-f0-9]{64}$/.test(sha256))
            throw new VerificationWait('A selected recipe input could not be measured');
          if (path.startsWith('node_modules/')) {
            dependencies.push({ path, sha256 });
            continue;
          }
          if (!hashes.has(path)) {
            const file = blob(head, path);
            hashes.set(path, file ? createHash('sha256').update(file.bytes).digest('hex') : null);
          }
          if (hashes.get(path) === sha256) matched++;
          else changed.push(path);
        }
        return { scopeKey, matched, changed, dependencies };
      });
    },
    groupChain({ base, head }) {
      const forks = read('merge-base', base, head);
      if (forks !== base) throw new VerificationWait('The queue candidate is not based on current Fleet main');
      const lines = read('log', '--format=%H %P', '--first-parent', '--no-show-signature', base + '..' + head);
      const chain = lines ? lines.split('\n').map(line => line.split(' ')) : [];
      if (!chain.length || chain.some((row, i) => row.length !== 3 || !row.every(sha) ||
          row[1] !== (chain[i + 1]?.[0] ?? base))) throw new VerificationWait('The queue merge chain is incomplete');
      return chain.map(([commit, parent, original]) => ({ commit, parent, original }));
    },
  };
}
