import { execFileSync } from 'node:child_process';
import { createHash } from 'node:crypto';
import { existsSync } from 'node:fs';
import { join } from 'node:path';
import { sha, VerificationWait } from './provider.mjs';

// This Git process only reads objects. It has no checkout, hooks, helpers or credentials.
export function openRepository(state) {
  const dir = join(state, 'git');
  const env = { PATH: '/usr/bin:/bin', GIT_CONFIG_NOSYSTEM: '1', GIT_CONFIG_GLOBAL: '/dev/null',
    GIT_TERMINAL_PROMPT: '0', GIT_ALLOW_PROTOCOL: 'https', GIT_NO_REPLACE_OBJECTS: '1' };
  const git = (...args) => execFileSync('/usr/bin/git', ['-c', 'core.hooksPath=/dev/null', ...args],
    { env, cwd: existsSync(dir) ? dir : state, encoding: 'buffer', stdio: ['ignore', 'pipe', 'pipe'], timeout: 60_000 });
  if (!existsSync(join(dir, 'HEAD'))) git('init', '--bare', dir);
  const remote = 'https://github.com/taskqorg/fleet.git';
  function read(...args) { return git(...args).toString('utf8').trim(); }
  function has(oid) {
    try { return read('cat-file', '-t', oid) === 'commit'; } catch { return false; }
  }
  return {
    dir,
    ensure({ base, head, kind, number, ref }) {
      if (!sha(base) || !sha(head)) throw new VerificationWait('Invalid Git commit identity');
      if (!has(base) || !has(head)) {
        const source = kind === 'merge_group' ? ref : 'refs/pull/' + number + '/head';
        git('fetch', '--no-tags', '--no-write-fetch-head', '--no-recurse-submodules', remote,
          '+refs/heads/main:refs/fleet-design/main', '+' + source + ':refs/fleet-design/last-candidate');
      }
      if (!has(base) || !has(head)) throw new VerificationWait('The provider commits are not available');
    },
    measureInputs(head, recipes) {
      if (!sha(head)) throw new VerificationWait('Invalid recipe source revision');
      const hashes = new Map();
      return recipes.map(({ scopeKey, inputs }) => {
        const changed = [], dependencies = [];
        let matched = 0;
        for (const { path, sha256 } of inputs) {
          if (typeof path !== 'string' || path.split('/').some(part => !part || part === '.' || part === '..') ||
              /[\\\x00-\x1f]/.test(path) || !/^[a-f0-9]{64}$/.test(sha256))
            throw new VerificationWait('A selected recipe input could not be measured');
          if (path.startsWith('node_modules/')) {
            dependencies.push(path);
            continue;
          }
          if (!hashes.has(path)) {
            const entry = read('--literal-pathspecs', 'ls-tree', '-z', '--full-tree', head, '--', path);
            const oid = /^100(?:644|755) blob ([a-f0-9]{40})\t/.exec(entry)?.[1];
            hashes.set(path, oid ? createHash('sha256').update(git('cat-file', 'blob', oid)).digest('hex') : null);
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
