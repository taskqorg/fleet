import { createHash } from 'node:crypto';
import { readFileSync, lstatSync } from 'node:fs';
import { isAbsolute, relative, resolve, sep } from 'node:path';
import { fileURLToPath } from 'node:url';
import { build } from 'esbuild';
import { NOW } from '../book/clock.ts';
import { captureMatrix } from './capture-scene.mjs';
import { solidEsbuildPlugin } from './solid-esbuild.mjs';
import { uiCss } from './tokens.mjs';

export const repository = fileURLToPath(new URL('../../../', import.meta.url));
const book = 'packages/ui/book';
const scripts = 'packages/ui/scripts';
export const sha256 = bytes => createHash('sha256').update(bytes).digest('hex');
const sceneCss = 'html,body,#app{height:100%}';
const bundleOptions = {
  bundle: true, minify: true, write: false, format: 'iife', target: 'es2022',
  conditions: ['solid'], plugins: [solidEsbuildPlugin()], legalComments: 'none',
};

export function previewDocument(js, title, css, recipe) {
  const restrictions = recipe
    ? '<meta http-equiv="Content-Security-Policy" content="default-src \'none\'; img-src data:; style-src \'unsafe-inline\'; script-src \'unsafe-inline\'">'
    : '';
  return `<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><meta name="color-scheme" content="light dark">${restrictions}
<title>${title}</title><style>${css}</style></head>
<body${recipe ? ` data-preview-recipe="${recipe}"` : ''}><div id="app"></div><script>${js.replace(/<\/script/gi, '<\\/script')}</script></body></html>
`;
}

export function buildPage(options) {
  return build({ ...bundleOptions, absWorkingDir: repository, ...options });
}

const js = JSON.stringify;
function entrySources(scenes, initial) {
  const imports = scenes.map((scene, i) => `import scene${i} from ${js('./' + scene.dir + '/scene.tsx')};`);
  const entries = scenes.map((scene, i) =>
    `{ route: ${js(scene.route)}, states: ${js(Object.values(scene.states))}, details: ${Boolean(scene.details)}, view: scene${i} }`);
  return `import { mountScenes } from './${book}/pages.tsx';\n${imports.join('\n')}\nmountScenes([${entries.join(',')}], ${js(initial)});`;
}

// Freeze the wall clock and date formatting inside this isolated page. rAF and performance timers stay native.
const clock = `Date.now=()=>${NOW};
for (const part of ['Date','Day','FullYear','Hours','Minutes','Seconds','Milliseconds','Month']) {
  Date.prototype['get'+part] = Date.prototype['getUTC'+part];
  if (part !== 'Day') Date.prototype['set'+part] = Date.prototype['setUTC'+part];
}
Date.prototype.getTimezoneOffset = () => 0;
const defaultFormat = Intl.DateTimeFormat;
const dateFormat = (locales, options) => new defaultFormat(locales ?? 'en-US', {timeZone:'UTC', ...options});
Intl.DateTimeFormat = new Proxy(defaultFormat, {
  apply(_target, _self, args) { return dateFormat(...args); },
  construct(_target, args) { return dateFormat(...args); }
});
`;

export async function buildCatalog(scenes) {
  const bundle = await buildPage({
    stdin: { contents: entrySources(scenes, '/overview'), resolveDir: repository },
  });
  return previewDocument(clock + bundle.outputFiles[0].text, 'Fleet page mockups', uiCss(sceneCss));
}

const portable = path => path.split(sep).join('/');
const localInputs = [
  `${scripts}/scene-build.mjs`, `${scripts}/scenes.ts`, `${scripts}/solid-esbuild.mjs`, `${scripts}/tokens.mjs`,
  `${scripts}/capture-scene.mjs`, `${scripts}/build-scene.mjs`, `${book}/clock.ts`, 'packages/ui/tokens.json', 'packages/ui/icons.json',
  'pnpm-lock.yaml', 'docs/design-system/owner-rules.md', 'docs/design-system/principles.md',
];

function localPath(path) {
  const file = resolve(repository, path);
  const logical = portable(relative(repository, file));
  if (isAbsolute(logical) || logical === '..' || logical.startsWith('../'))
    throw new Error(`Preview dependency outside repository: ${path}`);
  return logical;
}

function linkedSpecs(start) {
  const specs = new Set();
  function read(path) {
    if (specs.has(path)) return;
    specs.add(path);
    const source = readFileSync(resolve(repository, path), 'utf8');
    for (const [, href] of source.matchAll(/\]\(([^)\s#]+)(?:#[^)]+)?\)/g)) {
      if (!href.startsWith('.')) continue;
      const linked = localPath(resolve(repository, path, '..', href));
      if (/^docs\/design-system\/(?:controls|patterns)\/[^/]+\.md$/.test(linked)) read(linked);
    }
  }
  read(start);
  return [...specs];
}

export function assertPreviewInputs(scene, inputs) {
  for (const path of inputs) {
    const source = localPath(path);
    if (source.startsWith('node_modules/')) continue;
    const allowed = source.startsWith('packages/ui/src/') || source.startsWith('packages/view-url/') ||
      source === 'packages/ui/icons.json' || source === 'packages/ui/tokens.json' ||
      source === 'contracts/vocabulary.json' ||
      source.startsWith(scene.dir + '/') ||
      /^packages\/ui\/book\/(?:pages|scene-shell|scene-parts|focus-demo|entity-cards|fixtures|clock|nav)\.tsx?$/.test(source);
    if (!allowed) throw new Error(`${scene.key}: preview import outside shared UI and scene: ${source}`);
    const file = resolve(repository, source);
    if (!lstatSync(file).isFile()) throw new Error(`${scene.key}: preview input must be a regular file: ${source}`);
  }
}

export async function buildScene(scene, state) {
  if (!Object.hasOwn(scene.states, state)) throw new Error(`${scene.key}: unknown state ${state}`);
  const path = scene.states[state];
  const bundle = await buildPage({
    stdin: { contents: entrySources([scene], path), resolveDir: repository },
    metafile: true,
  });
  const builtInputs = Object.keys(bundle.metafile.inputs).filter(path => path !== '<stdin>');
  assertPreviewInputs(scene, builtInputs);
  const inputs = [...new Set([...localInputs, ...linkedSpecs(scene.spec), ...builtInputs,
    ...['fixture.ts', 'scene.json'].map(name => scene.dir + '/' + name)].map(localPath))].sort();
  const css = uiCss(sceneCss);
  const recipe = {
    version: 1, scene: scene.key, state, path, landmark: scene.stateLandmarks[state] ?? scene.landmark,
    clock: NOW, timeZone: 'UTC', locale: 'en-US', dpr: 1, reducedMotion: true, matrix: captureMatrix,
    css: sha256(css),
    inputs: inputs.map(path => ({ path, sha256: sha256(readFileSync(resolve(repository, path))) })),
  };
  const digest = sha256('fleet:preview-recipe:v1\0' + JSON.stringify(recipe));
  const html = previewDocument(clock + bundle.outputFiles[0].text, 'Fleet scene preview', css, digest);
  return { recipe: { ...recipe, digest, html: sha256(html) }, html };
}
