// The build guest has an immutable toolchain and a disposable, bounded checkout.
import { spawnSync } from 'node:child_process';
import { createHash } from 'node:crypto';
import { closeSync, constants, fstatSync, lstatSync, mkdirSync, openSync, readFileSync,
  readdirSync, realpathSync, symlinkSync } from 'node:fs';

const toolchain = '/opt/fleet-design-runtime/node_modules';
const source = '/work/source';
const app = source + '/dist/app';
const run = (command, args, stdin = 'ignore') =>
  spawnSync(command, args, { cwd: source, stdio: [stdin, 'ignore', 'ignore'] }).status === 0;

function regular(path, limit) {
  const fd = openSync(path, constants.O_RDONLY | constants.O_NOFOLLOW | constants.O_NONBLOCK);
  try {
    const info = fstatSync(fd);
    if (!info.isFile() || info.nlink !== 1 || info.size <= 0 || info.size > limit) throw new Error('invalid app file');
    return readFileSync(fd);
  } finally {
    closeSync(fd);
  }
}

try {
  for (const { path, sha256 } of JSON.parse(process.argv[2])) {
    if (!/^node_modules\/[A-Za-z0-9_.@+ /-]+$/.test(path) || path.split('/').some(p => ['.', '..'].includes(p)))
      process.exit(42);
    const file = realpathSync('/opt/fleet-design-runtime/' + path);
    if (!file.startsWith(toolchain + '/') ||
        createHash('sha256').update(readFileSync(file)).digest('hex') !== sha256) process.exit(42);
  }
} catch {
  process.exit(42);
}

try {
  mkdirSync(source);
  if (!run('/usr/bin/tar', ['--extract', '--no-same-owner', '--no-same-permissions', '--no-overwrite-dir'], 'inherit'))
    process.exit(43);
  symlinkSync(toolchain, source + '/node_modules', 'dir');
  if (!run(process.execPath, ['app/build.mjs'])) process.exit(43);

  const maxApp = 16 * 1024 * 1024;
  let remaining = maxApp;
  const files = Object.create(null);
  function record(name) {
    const bytes = regular(app + '/' + name, remaining);
    remaining -= bytes.length;
    files['/' + name] = bytes.toString('base64');
  }
  if (!lstatSync(source + '/dist').isDirectory() || !lstatSync(app).isDirectory()) process.exit(43);
  record('index.html');
  record('sw.js');
  for (const [dir, names] of [
    ['assets', /^(?:main|chunk|app|fleet(?:-180|-192|-512)?)-[A-Za-z0-9]+\.(?:js|css|svg|png)$/],
    ['manifests', /^[a-z][a-z0-9-]*\.webmanifest$/],
  ]) {
    if (!lstatSync(app + '/' + dir).isDirectory()) process.exit(43);
    for (const name of readdirSync(app + '/' + dir).sort()) {
      if (!names.test(name)) process.exit(43);
      record(dir + '/' + name);
    }
  }
  const html = Buffer.from(files['/index.html'], 'base64').toString('utf8');
  const main = /<script type="module" src="(\/assets\/main-[A-Za-z0-9]+\.js)"><\/script>/.exec(html)?.[1];
  if (!main || !Object.hasOwn(files, main) || !html.includes('<div id="app"></div>')) process.exit(43);
  process.stdout.write(JSON.stringify({ main, files }));
} catch {
  process.exitCode = 43;
}
