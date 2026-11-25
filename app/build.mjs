// Builds the operator app into dist/app (docs/designs/fleet-operator-app.md §7): hashed ESM chunks per view, CSS from tokens,
// index.html, a service worker with the precache list, one manifest and icon per installable view, and a size report.
import { build } from 'esbuild';
import { createHash } from 'node:crypto';
import { mkdirSync, readFileSync, rmSync, writeFileSync } from 'node:fs';
import { basename } from 'node:path';
import { gzipSync } from 'node:zlib';
import { uiCss, tokens } from '../packages/ui/scripts/tokens.mjs';
import { solidEsbuildPlugin } from '../packages/ui/scripts/solid-esbuild.mjs';

const root = new URL('..', import.meta.url), out = new URL('../dist/app/', import.meta.url);
// Source maps go to dist/app/sourcemaps, which the coordinator never serves (it serves assets/ and named entry files);
// the host that installs a release uploads them to Bugsink (docs/designs/fleet-error-tracking.md, Source maps).
const maps = new URL('sourcemaps/', out);
rmSync(out, { recursive: true, force: true });
mkdirSync(maps, { recursive: true });
mkdirSync(new URL('assets/', out), { recursive: true }); mkdirSync(new URL('manifests/', out), { recursive: true }); mkdirSync(new URL('icons/', out), { recursive: true });

const res = await build({
  entryPoints: [new URL('app/src/main.tsx', root).pathname], bundle: true, splitting: true, format: 'esm', minify: true, target: 'es2022',
  outdir: new URL('assets/', out).pathname, entryNames: 'main-[hash]', chunkNames: 'chunk-[hash]',
  conditions: ['solid'], plugins: [solidEsbuildPlugin({ sourceMap: true })],
  loader: { '.json': 'json' }, legalComments: 'none', metafile: true, write: false, sourcemap: 'external', sourcesContent: true,
});
// Each script gets a debug ID (the form `sentry-cli sourcemaps inject` writes): a stack frame of the script names its
// source map in Bugsink without guessing by release or path. The ID is derived from the script, so a rebuild of the same
// commit gives the same IDs. The snippet is appended, so the map's lines stay valid.
const debugId = text => { const h = createHash('sha256').update(text).digest('hex'); return `${h.slice(0, 8)}-${h.slice(8, 12)}-4${h.slice(13, 16)}-${(8 | parseInt(h[16], 16) & 3).toString(16)}${h.slice(17, 20)}-${h.slice(20, 32)}`; };
const ids = {};
for (const f of res.outputFiles) if (f.path.endsWith('.js')) {
  const id = ids[f.path] = debugId(f.text);
  const js = f.text + `!function(){try{var e="undefined"!=typeof window?window:"undefined"!=typeof globalThis?globalThis:"undefined"!=typeof self?self:{},n=(new e.Error).stack;n&&(e._sentryDebugIds=e._sentryDebugIds||{},e._sentryDebugIds[n]="${id}",e._sentryDebugIdIdentifier="sentry-dbid-${id}")}catch(e){}}();\n//# debugId=${id}\n`;
  writeFileSync(f.path, js);
}
for (const f of res.outputFiles) if (f.path.endsWith('.js.map')) {
  const id = ids[f.path.slice(0, -4)], map = JSON.parse(f.text);
  writeFileSync(new URL(basename(f.path), maps), JSON.stringify({ ...map, debug_id: id, debugId: id }));
}
for (const p of Object.keys(res.metafile.outputs)) if (p.endsWith('.map')) delete res.metafile.outputs[p];
const toRel = p => p.split('/dist/app/')[1] ?? p.replace(/^.*dist\/app\//, '');
const files = Object.keys(res.metafile.outputs).map(toRel);
const main = files.find(f => /assets\/main-.*\.js$/.test(f));
const mainOut = Object.entries(res.metafile.outputs).find(([p]) => toRel(p) === main)?.[1];
const preloads = (mainOut?.imports ?? [])
  .filter(i => i.kind === 'import-statement')
  .map(i => toRel(i.path))
  .filter(f => f.startsWith('assets/') && f.endsWith('.js'))
  .map(f => `<link rel="modulepreload" href="/${f}">`)
  .join('');

const css = uiCss('html,body,#app{height:100%}');
const cssName = `assets/app-${createHash('sha256').update(css).digest('hex').slice(0, 10)}.css`;
writeFileSync(new URL(cssName, out), css);

// Reuse the navigation's Fleet mark for browser tabs and every installed workspace.
const brand = {};
for (const name of ['fleet.svg', 'fleet-180.png', 'fleet-192.png', 'fleet-512.png']) {
  const bytes = readFileSync(new URL(`packages/ui/brand/${name}`, root));
  const dot = name.lastIndexOf('.');
  const path = `assets/${name.slice(0, dot)}-${createHash('sha256').update(bytes).digest('hex').slice(0, 10)}${name.slice(dot)}`;
  writeFileSync(new URL(path, out), bytes);
  brand[name] = '/' + path;
  files.push(path);
}
// The installable views are the registry's seven rail sections (packages/ui/src/registry.ts, nav: true), read through esbuild:
// the address bar shows their target routes (app/src/routes.ts), whose first segment names the section.
const reg = await build({ entryPoints: [new URL('packages/ui/src/registry.ts', root).pathname], bundle: true, format: 'esm', write: false, loader: { '.json': 'json' } });
const { workspaces } = await import('data:text/javascript;base64,' + Buffer.from(reg.outputFiles[0].text).toString('base64'));
const VIEWS = Object.fromEntries(workspaces.filter(w => w.nav).map(w => [w.id, ['Fleet ' + w.label, w.icon]]));
// The sections with a manifest. The coordinator serves index.html for every path outside /api/ and the static files, so
// earlier routes and unknown paths reach the app's redirect and not-found page (go/coordinator/artifact_routes.go appRoutes).
writeFileSync(new URL('screens.json', out), JSON.stringify(Object.keys(VIEWS)));
// The paths the service worker caches: the sections and the earlier screens that redirect to them.
const ROUTED = [...new Set(workspaces.map(w => w.id))];
const accent = tokens.color.light.accent;
for (const [view, [name]] of Object.entries(VIEWS)) {
  writeFileSync(new URL(`manifests/${view}.webmanifest`, out), JSON.stringify({ id: `/${view}`, name, short_name: name.replace('Fleet ', ''), start_url: `/${view}`, scope: '/', display: 'standalone', background_color: tokens.color.light.bg, theme_color: accent, icons: [
    { src: brand['fleet.svg'], sizes: 'any', type: 'image/svg+xml', purpose: 'any' },
    ...[192, 512].map(size => ({ src: brand[`fleet-${size}.png`], sizes: `${size}x${size}`, type: 'image/png', purpose: 'any' })),
  ] }, null, 1));
}

const html = `<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><meta name="color-scheme" content="light dark">
<title>Fleet</title><meta name="theme-color" content="${accent}">
<link rel="stylesheet" href="/${cssName}">
<link rel="icon" type="image/svg+xml" href="${brand['fleet.svg']}">
<link rel="apple-touch-icon" sizes="180x180" href="${brand['fleet-180.png']}">
<script>
(() => {
  const workspaces = ${JSON.stringify(Object.keys(VIEWS))};
  const manifest = document.createElement('link');
  manifest.rel = 'manifest';
  manifest.crossOrigin = 'use-credentials';
  function updateManifest() {
    const view = location.pathname.split('/')[1];
    const href = '/manifests/' + (workspaces.includes(view) ? view : 'home') + '.webmanifest';
    if (manifest.getAttribute('href') !== href) manifest.href = href;
  }
  updateManifest();
  document.head.appendChild(manifest);
  navigation.addEventListener('currententrychange', updateManifest);
})();
</script>
${preloads}<script type="module" src="/${main}"></script>
</head><body><div id="app"></div><noscript>Fleet needs JavaScript.</noscript></body></html>
`;
writeFileSync(new URL('index.html', out), html);

const version = createHash('sha256').update([...files].sort().join()).update(html).digest('hex').slice(0, 10);
const precache = ['/' + cssName, ...files.filter(f => f.startsWith('assets/')).map(f => '/' + f)];
writeFileSync(new URL('sw.js', out), `// Fleet service worker ${version}: app shell only; /api is never cached (design §5).
const CACHE='fleet-${version}',ASSETS=${JSON.stringify(precache)};
self.addEventListener('install',e=>e.waitUntil(caches.open(CACHE).then(c=>c.addAll([...ASSETS,'/home']))));
// The app's Update button asks a waiting worker to take over at once (app/src/version.ts), so one click updates.
self.addEventListener('message',e=>{if(e.data==='skip-waiting')self.skipWaiting()});
self.addEventListener('activate',e=>e.waitUntil(caches.keys().then(ks=>Promise.all(ks.filter(k=>k!==CACHE).map(k=>caches.delete(k)))).then(()=>self.clients.claim())));
self.addEventListener('fetch',e=>{const u=new URL(e.request.url);if(e.request.method!=='GET'||u.origin!==location.origin||u.pathname.startsWith('/api/'))return;
 if(u.pathname.startsWith('/assets/'))return e.respondWith(caches.match(e.request,{cacheName:CACHE}).then(r=>r||fetch(e.request).then(res=>{if(res.ok){const c=res.clone();caches.open(CACHE).then(x=>x.put(e.request,c))}return res})));
 // Only the app's own routes are cached (a section or an earlier screen, up to three segments deep), each under its own path; others are never cached.
 const app=/^\\/(${ROUTED.join('|')})(\\/[^/]+){0,3}$/.test(u.pathname);
 // The shell is one page for every route: a visit opens this version's cached copy at once (the HTML through Access costs
 // 300-400 ms) and refreshes it from the network behind. Data comes from the read mirror and the API, never from here;
 // a new version arrives with its own worker (version.ts updates). Without a cached copy it is the network, as before.
 // ?signin (the expired session's Sign in again) always goes to the network, where Access sends it to its sign-in.
 if(e.request.mode==='navigate'&&app&&!u.searchParams.has('signin')){const net=fetch(e.request).then(r=>{if(r.ok&&r.headers.get('content-type')?.startsWith('text/html')){const c=r.clone();caches.open(CACHE).then(x=>x.put(u.pathname,c))}return r});
  return e.respondWith(caches.open(CACHE).then(c=>c.match(u.pathname).then(r=>r||c.match('/home'))).then(r=>{if(r){e.waitUntil(net.catch(()=>{}));return r}return net.catch(()=>caches.match('/home'))}))}});
`);

let total = 0, totalGz = 0;
for (const f of files.filter(f => f.endsWith('.js'))) { const b = readFileSync(new URL(f, out)); total += b.length; totalGz += gzipSync(b).length; }
const mainB = readFileSync(new URL(main, out));
console.log(`app: dist/app, main ${(mainB.length / 1024).toFixed(0)} KB raw / ${(gzipSync(mainB).length / 1024).toFixed(0)} KB gzip; all JS ${(total / 1024).toFixed(0)} KB raw / ${(totalGz / 1024).toFixed(0)} KB gzip (target first-load <= 200 KB raw)`);
