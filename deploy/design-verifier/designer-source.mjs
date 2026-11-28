import { execFileSync } from 'node:child_process';
import { createHash } from 'node:crypto';
import { constants, fstatSync, lstatSync, mkdirSync, mkdtempSync, openSync, readFileSync, readdirSync, readSync, realpathSync, rmSync, writeFileSync, closeSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { dirname, join, resolve, sep } from 'node:path';
import { sourcePath, validateArchiveTree } from './git.mjs';

const maxArchive = 96 * 1024 * 1024;
export const maxOverlay = 2 * 1024 * 1024;
export const maxBaseBundle = 60 * 1024 * 1024;
export const hash = bytes => createHash('sha256').update(bytes).digest('hex');
const oid = /^[a-f0-9]{40}$/;
const draftPath = /^(?:app\/src\/|packages\/(?:ui\/(?:src\/|brand\/|(?:tokens|icons)\.json$)|view-url\/)|docs\/design-system\/)/;

// The packer runs under the designer's existing sandbox. It never invokes Git
// or follows checkout symlinks. Only explicitly named draft files are read.
export function packOverlay(checkout, base, paths) {
  if (!oid.test(base) || !Array.isArray(paths) || !paths.length || paths.length > 512 || new Set(paths).size !== paths.length)
    throw new Error('Expected a base SHA and distinct explicit draft paths');
  const root = realpathSync(checkout);
  const files = [];
  let total = 0;
  for (const path of [...paths].sort()) {
    if (!sourcePath(path) || !draftPath.test(path)) throw new Error('Unsupported draft path');
    let current = root;
    const parts = path.split('/');
    let missing = false;
    for (const part of parts) {
      current = join(current, part);
      try {
        const info = lstatSync(current);
        if (info.isSymbolicLink()) throw new Error('Draft path contains a symlink');
      } catch (error) {
        if (error.code === 'ENOENT') { missing = true; break; }
        throw error;
      }
    }
    if (missing) { files.push({ path, deleted: true }); continue; }
    const real = realpathSync(current);
    if (!real.startsWith(root + sep)) throw new Error('Draft path escaped checkout');
    const fd = openSync(current, constants.O_RDONLY | constants.O_NOFOLLOW | constants.O_NONBLOCK);
    try {
      const stat = fstatSync(fd);
      if (!stat.isFile() || stat.size > maxOverlay - total) throw new Error('Draft file is invalid or oversized');
      const buffer = Buffer.alloc(stat.size + 1);
      let size = 0, count;
      while (size < buffer.length && (count = readSync(fd, buffer, size, buffer.length - size, null)) > 0) size += count;
      if (size !== stat.size) throw new Error('Draft file changed while being read');
      total += size;
      if (total > maxOverlay) throw new Error('Draft files exceed the capture limit');
      files.push({ path, bytes: buffer.subarray(0, size).toString('base64') });
    } finally { closeSync(fd); }
  }
  return { version: 1, base, files };
}

// The input bundle and overlay are bounded byte strings. Git only runs in this
// new private repository; neither checkout config nor checkout object/index data
// are consulted, and no candidate executable runs on the host.
export function designerSource(baseBundle, overlay, signedFiles, replay, scratchRoot = tmpdir()) {
  if (!Buffer.isBuffer(baseBundle) || !baseBundle.length || baseBundle.length > maxBaseBundle ||
      !overlay || Object.keys(overlay).sort().join(',') !== 'base,files,version' || overlay.version !== 1 || !oid.test(overlay.base) || !Array.isArray(overlay.files))
    throw new Error('Expected a bounded base bundle and overlay');
  const scratch = mkdtempSync(join(scratchRoot, 'fleet-designer-source-'));
  const env = { PATH: '/usr/bin:/bin', GIT_CONFIG_NOSYSTEM: '1', GIT_CONFIG_GLOBAL: '/dev/null',
    GIT_TERMINAL_PROMPT: '0', GIT_ALLOW_PROTOCOL: 'file', GIT_NO_REPLACE_OBJECTS: '1', GIT_ATTR_NOSYSTEM: '1',
    GIT_AUTHOR_NAME: 'Fleet design capture', GIT_AUTHOR_EMAIL: 'capture@example.invalid',
    GIT_COMMITTER_NAME: 'Fleet design capture', GIT_COMMITTER_EMAIL: 'capture@example.invalid',
    GIT_AUTHOR_DATE: '2000-01-01T00:00:00Z', GIT_COMMITTER_DATE: '2000-01-01T00:00:00Z' };
  const git = (args, input, maxBuffer = maxArchive) => execFileSync('/usr/bin/git',
    ['-c', 'core.hooksPath=/dev/null', '-c', 'core.attributesFile=/dev/null', ...args],
    { cwd: scratch, env, input, stdio: ['pipe', 'pipe', 'pipe'], timeout: 20_000, maxBuffer });
  try {
    git(['init', '-q', '--template=']);
    // The signed base is the single declared boundary of a depth-one source artifact.
    writeFileSync(join(scratch, '.git', 'shallow'), overlay.base + '\n', { mode: 0o600, flag: 'wx' });
    const bundlePath = join(scratch, 'base.bundle');
    writeFileSync(bundlePath, baseBundle, { mode: 0o600, flag: 'wx' });
    // Bundle output names the base explicitly; no remote or author configuration is used.
    git(['fetch', '--no-tags', '--no-write-fetch-head', bundlePath, overlay.base]);
    const base = overlay.base;
    if (git(['cat-file', '-t', base]).toString().trim() !== 'commit') throw new Error('Base is not a commit');
    const baseTree = git(['rev-parse', base + '^{tree}']).toString().trim();
    if (!oid.test(baseTree)) throw new Error('Base tree is invalid');
    git(['fsck', '--full', '--strict', '--no-reflogs']);
    git(['read-tree', base]);
    const names = new Set();
    let total = 0;
    if (replay) {
      if (replay.base !== base || !Buffer.isBuffer(replay.patch) || replay.patch.length > maxOverlay)
        throw new Error('The replay base or patch is invalid');
      if (replay.patch.length) git(['apply', '--cached', '--binary', '--whitespace=nowarn', '-'], replay.patch);
    } else {
      if (overlay.files.length > 512) throw new Error('Too many draft paths');
      for (const file of overlay.files) {
        if (!file || !sourcePath(file.path) || !draftPath.test(file.path) || names.has(file.path) || Object.hasOwn(signedFiles, file.path))
          throw new Error('The overlay names an unsupported or protected path');
        names.add(file.path);
        if (file.deleted === true && Object.keys(file).sort().join(',') === 'deleted,path') {
          git(['update-index', '--force-remove', '--', file.path]);
        } else {
          if (Object.keys(file).sort().join(',') !== 'bytes,path' || typeof file.bytes !== 'string' ||
              !/^(?:[A-Za-z0-9+/]{4})*(?:[A-Za-z0-9+/]{2}==|[A-Za-z0-9+/]{3}=)?$/.test(file.bytes))
            throw new Error('Invalid overlay file');
          const bytes = Buffer.from(file.bytes, 'base64');
          total += bytes.length;
          if (total > maxOverlay) throw new Error('The draft overlay exceeds the capture limit');
          const old = git(['ls-files', '--stage', '--', file.path]).toString();
          const mode = /^(100(?:644|755)) /.exec(old)?.[1] ?? '100644';
          const blob = git(['hash-object', '-w', '--stdin'], bytes).toString().trim();
          git(['update-index', '--add', '--cacheinfo', mode + ',' + blob + ',' + file.path]);
        }
      }
    }
    const tree = git(['write-tree']).toString().trim();
    if (replay && tree !== replay.tree) throw new Error('The retained overlay reconstructed a different Git tree');
    const paths = git(['diff', '--name-only', '--no-renames', '-z', base, tree]).toString().split('\0').filter(Boolean);
    for (const path of paths) if (!draftPath.test(path) || Object.hasOwn(signedFiles, path))
      throw new Error('The draft changes a path outside the designer overlay: ' + JSON.stringify(path));
    validateArchiveTree((...args) => git(args), tree, signedFiles);
    const fingerprints = Object.create(null);
    const entries = git(['ls-tree', '-r', '-z', '--full-tree', tree]).toString().split('\0').filter(Boolean);
    for (const entry of entries) {
      const match = /^(100(?:644|755)) blob ([a-f0-9]{40})\t(.*)$/.exec(entry);
      if (!match || !sourcePath(match[3])) throw new Error('Unsupported source tree entry');
      const bytes = git(['cat-file', 'blob', match[2]]);
      fingerprints[match[3]] = hash(bytes);
      if (Object.hasOwn(signedFiles, match[3]) && match[1] + ':' + fingerprints[match[3]] !== signedFiles[match[3]])
        throw new Error('The base differs from the signed build pin: ' + match[3]);
    }
    for (const path of Object.keys(signedFiles)) if (!Object.hasOwn(fingerprints, path))
      throw new Error('The signed build pin is absent: ' + path);
    const patch = replay?.patch ?? git(['diff', '--no-ext-diff', '--no-textconv', '--no-renames', '--binary', '--full-index', base, tree, '--'], undefined, maxOverlay);
    if (patch.length > maxOverlay) throw new Error('The draft overlay exceeds the capture limit');
    const snapshot = git(['-c', 'commit.gpgSign=false', 'commit-tree', tree, '-p', base, '-m', 'Fleet design capture']).toString().trim();
    const archive = git(['-c', 'tar.umask=0022', 'archive', '--format=tar', snapshot]);
    // Confirm the export contains exactly the validated tree, even if Git's
    // attribute or archive behavior differs on the operator host.
    const exported = join(scratch, 'exported');
    mkdirSync(exported, { mode: 0o700 });
    execFileSync('/usr/bin/tar', ['-xf', '-', '-C', exported, '--no-same-owner', '--no-same-permissions'],
      { input: archive, env: {}, stdio: ['pipe', 'ignore', 'pipe'], timeout: 20_000 });
    const remaining = new Set(Object.keys(fingerprints));
    function verifyExport(dir, prefix = '') {
      for (const name of readdirSync(dir)) {
        const path = prefix ? prefix + '/' + name : name;
        const item = join(dir, name);
        const info = lstatSync(item);
        if (info.isDirectory()) verifyExport(item, path);
        else if (!info.isFile() || !remaining.delete(path) || hash(readFileSync(item)) !== fingerprints[path])
          throw new Error('Export differs from the validated Git tree');
      }
    }
    verifyExport(exported);
    if (remaining.size) throw new Error('Export omits validated Git tree inputs');
    return { base, baseTree, tree, patch, archive, paths, fingerprints };
  } finally { rmSync(scratch, { recursive: true, force: true }); }
}

// Execute only inside the separately isolated, credential-free importer. The
// caller must verify release pins and allocation before invoking this function.
export function verifyRequestedSource({ baseBundle, overlayBytes, patch, replay }, signedFiles, scratchRoot) {
  const overlay = JSON.parse(overlayBytes.toString('utf8'));
  if (hash(overlayBytes) !== replay.overlaySha256) throw new Error('Original overlay digest differs');
  const source = designerSource(baseBundle, overlay, signedFiles, undefined, scratchRoot);
  if (source.base !== replay.base || source.baseTree !== replay.baseTree || source.tree !== replay.tree ||
      JSON.stringify(source.paths) !== JSON.stringify(replay.paths) ||
      !source.patch.equals(patch) || hash(source.archive) !== replay.archiveSha256)
    throw new Error('Original overlay does not reconstruct the retained source');
}
