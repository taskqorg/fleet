import { createHash } from 'node:crypto';
import { readFileSync, statSync } from 'node:fs';

// These are executed by the normal release build. pinning them fences the
// builder as well as the dependency image independently of the impact policy.
const paths = [
  'app/build.mjs', 'package.json', 'pnpm-lock.yaml', 'pnpm-workspace.yaml',
  'tsconfig.json', 'app/tsconfig.json', 'packages/ui/tsconfig.json',
  'packages/ui/scripts/solid-esbuild.mjs', 'packages/ui/scripts/tokens.mjs',
  'packages/ui/src/registry.ts', 'packages/ui/src/states.ts',
];
const pins = JSON.parse(readFileSync('config/tool-pins.json')).targets['linux-amd64'];
const files = Object.fromEntries(paths.map(path => {
  const mode = statSync(path).mode & 0o111 ? '100755' : '100644';
  const hash = createHash('sha256').update(readFileSync(path)).digest('hex');
  return [path, mode + ':' + hash];
}));
console.log(JSON.stringify({ files, chromium: pins.chromium.version, playwright: pins.playwright.version, node: pins.node.version }));
