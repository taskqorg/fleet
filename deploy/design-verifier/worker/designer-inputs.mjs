// Preloaded for the designer build only. Observe the normal esbuild calls in
// app/build.mjs, including its separate workspace registry build.
import { createHash } from 'node:crypto';
import fs, { readFileSync, realpathSync, writeFileSync } from 'node:fs';
import { createRequire, registerHooks, syncBuiltinESMExports } from 'node:module';
import { resolve as resolvePath } from 'node:path';
import { fileURLToPath, pathToFileURL } from 'node:url';

const source = realpathSync(process.cwd()) + '/';
const modules = realpathSync(source + 'node_modules') + '/';
const buildURL = pathToFileURL(source + 'app/build.mjs').href;
const used = new Map();
const wrapper = 'fleet:design-esbuild';
let esbuild;
let builds = 0;

function dependencyPath(absolute) {
  const installed = absolute.slice(modules.length);
  const packageFile = installed.replace(/^\.pnpm\/[^/]+\/node_modules\//, '');
  // Use shorter pnpm aliases only when they resolve to these exact installed bytes.
  for (const candidate of [packageFile, '.pnpm/node_modules/' + packageFile]) {
    try {
      if (realpathSync(modules + candidate) === absolute) return 'node_modules/' + candidate;
    } catch (error) {
      if (error.code !== 'ENOENT') throw error;
    }
  }
  return 'node_modules/' + installed;
}

function input(absolute) {
  const name = absolute.startsWith(modules) ? dependencyPath(absolute) :
    absolute.startsWith(source) ? absolute.slice(source.length) : null;
  if (!name) throw new Error('Build input outside the isolated source and toolchain');
  if (!used.has(name)) used.set(name, createHash('sha256').update(originalReadFileSync(absolute)).digest('hex'));
}

// uiCss reads its styles directly rather than through esbuild. Observe those reads
// so the recipe binds exactly the stylesheets used to produce the captured CSS.
const originalReadFileSync = readFileSync;
fs.readFileSync = function (path, ...args) {
  const bytes = originalReadFileSync(path, ...args);
  const name = path instanceof URL ? fileURLToPath(path) : path;
  if (typeof name === 'string' && name.startsWith(source + 'packages/ui/src/styles/'))
    input(realpathSync(name));
  return bytes;
};
syncBuiltinESMExports();

registerHooks({
  resolve(specifier, context, next) {
    const result = next(specifier, context);
    if (result.url.startsWith('file:') && fileURLToPath(result.url).startsWith(modules))
      input(fileURLToPath(result.url));
    if (specifier === 'esbuild' && context.parentURL === buildURL) {
      esbuild = result.url;
      return { url: wrapper, shortCircuit: true };
    }
    return result;
  },
  load(url, context, next) {
    if (url !== wrapper) return next(url, context);
    return { format: 'module', shortCircuit: true, source: `
      import actual from ${JSON.stringify(esbuild)};
      import { record } from ${JSON.stringify(import.meta.url)};
      export async function build(options) {
        const result = await actual.build({ ...options, metafile: true });
        record(result.metafile);
        return result;
      }
    ` };
  },
});

export function observedInputs() { return [...used.keys()].sort(); }

export function record(meta) {
  builds++;
  const absolute = path => realpathSync(resolvePath(source, path));
  for (const path of Object.keys(meta.inputs)) {
    const file = absolute(path);
    if (file.startsWith(source)) input(file);
  }
  for (const output of Object.values(meta.outputs)) {
    for (const [path, contribution] of Object.entries(output.inputs)) {
      if (contribution.bytesInOutput) input(absolute(path));
    }
  }
}

process.on('exit', code => {
  if (code !== 0 || builds !== 2) return;
  const require = createRequire(esbuild);
  input(realpathSync(require.resolve('@esbuild/linux-x64/bin/esbuild')));
  for (const name of ['tokens.json', 'brand/fleet.svg', 'brand/fleet-180.png', 'brand/fleet-192.png', 'brand/fleet-512.png'])
    input(realpathSync(source + 'packages/ui/' + name));
  writeFileSync(resolvePath(source, '../designer-inputs.json'),
    JSON.stringify([...used.keys()].sort().map(path => ({ path, sha256: used.get(path) }))));
});
