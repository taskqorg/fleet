// Builds the operator app into dist/app (docs/designs/fleet-operator-app.md §7): hashed ESM chunks per view, CSS from tokens,
// index.html, a service worker with the precache list, one manifest and icon per installable view, and a size report.
import { build } from 'esbuild';
import { createHash } from 'node:crypto';
import { mkdirSync, readFileSync, rmSync, writeFileSync } from 'node:fs';
import { gzipSync } from 'node:zlib';
import { tokensCss, tokens } from '../packages/ui/scripts/tokens.mjs';

const root = new URL('..', import.meta.url), out = new URL('../dist/app/', import.meta.url);
rmSync(out, { recursive: true, force: true });
mkdirSync(new URL('assets/', out), { recursive: true }); mkdirSync(new URL('manifests/', out), { recursive: true }); mkdirSync(new URL('icons/', out), { recursive: true });

const res = await build({
  entryPoints: [new URL('app/src/main.tsx', root).pathname], bundle: true, splitting: true, format: 'esm', minify: true, target: 'es2022',
  outdir: new URL('assets/', out).pathname, entryNames: 'main-[hash]', chunkNames: 'chunk-[hash]', jsx: 'automatic', jsxImportSource: 'preact',
  loader: { '.json': 'json' }, legalComments: 'none', metafile: true, write: true,
});
const files = Object.keys(res.metafile.outputs).map(p => p.split('/dist/app/')[1] ?? p.replace(/^.*dist\/app\//, ''));
const main = files.find(f => /assets\/main-.*\.js$/.test(f));

const css = tokensCss() + readFileSync(new URL('packages/ui/src/styles.css', root), 'utf8') + 'html,body,#app{height:100%}';
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
// The installable views are the registry workspaces marked app: true (packages/ui/src/registry.ts), read through esbuild.
const reg = await build({ entryPoints: [new URL('packages/ui/src/registry.ts', root).pathname], bundle: true, format: 'esm', write: false, loader: { '.json': 'json' } });
const { workspaces } = await import('data:text/javascript;base64,' + Buffer.from(reg.outputFiles[0].text).toString('base64'));
const VIEWS = Object.fromEntries(workspaces.filter(w => w.app).map(w => [w.id, ['Fleet ' + w.label, w.icon]]));
// The coordinator serves index.html for these screens only (go/coordinator/artifact_routes.go appRoutes).
writeFileSync(new URL('screens.json', out), JSON.stringify(Object.keys(VIEWS)));
const accent = tokens.color.light.accent;
for (const [view, [name]] of Object.entries(VIEWS)) {
  writeFileSync(new URL(`manifests/${view}.webmanifest`, out), JSON.stringify({ id: `/${view}`, name, short_name: name.replace('Fleet ', ''), start_url: `/${view}`, scope: '/', display: 'standalone', background_color: tokens.color.light.bg, theme_color: accent, icons: [
    { src: brand['fleet.svg'], sizes: 'any', type: 'image/svg+xml', purpose: 'any' },
    ...[192, 512].map(size => ({ src: brand[`fleet-${size}.png`], sizes: `${size}x${size}`, type: 'image/png', purpose: 'any' })),
  ] }, null, 1));
}

const html = `<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>Fleet</title><meta name="theme-color" content="${accent}">
<link rel="stylesheet" href="/${cssName}">
<link rel="icon" type="image/svg+xml" href="${brand['fleet.svg']}">
<link rel="apple-touch-icon" sizes="180x180" href="${brand['fleet-180.png']}">
<script>(function(){var v=location.pathname.split('/')[1];if(!${JSON.stringify(Object.keys(VIEWS))}.includes(v))v='overview';var l=document.createElement('link');l.rel='manifest';l.crossOrigin='use-credentials';l.href='/manifests/'+v+'.webmanifest';document.head.appendChild(l)})()</script>
<script type="module" src="/${main}"></script>
</head><body><div id="app"></div><noscript>Fleet needs JavaScript.</noscript></body></html>
`;
writeFileSync(new URL('index.html', out), html);

const version = createHash('sha256').update(files.join() + css).digest('hex').slice(0, 10);
const precache = ['/' + cssName, ...files.filter(f => f.startsWith('assets/')).map(f => '/' + f)];
writeFileSync(new URL('sw.js', out), `// Fleet service worker ${version}: app shell only; /api is never cached (design §5).
const CACHE='fleet-${version}',ASSETS=${JSON.stringify(precache)};
self.addEventListener('install',e=>e.waitUntil(caches.open(CACHE).then(c=>c.addAll([...ASSETS,'/overview']))));
self.addEventListener('activate',e=>e.waitUntil(caches.keys().then(ks=>Promise.all(ks.filter(k=>k!==CACHE).map(k=>caches.delete(k)))).then(()=>self.clients.claim())));
self.addEventListener('fetch',e=>{const u=new URL(e.request.url);if(e.request.method!=='GET'||u.origin!==location.origin||u.pathname.startsWith('/api/'))return;
 if(u.pathname.startsWith('/assets/'))return e.respondWith(caches.match(e.request).then(r=>r||fetch(e.request)));
 // Only the app's own routes are cached, each under its own path; /test-sites/* and others are never cached.
 const app=/^\\/(${Object.keys(VIEWS).join('|')})(\\/[^/]*)?$/.test(u.pathname);
 if(e.request.mode==='navigate'&&app)return e.respondWith(fetch(e.request).then(r=>{if(r.ok&&r.headers.get('content-type')?.startsWith('text/html')){const c=r.clone();caches.open(CACHE).then(x=>x.put(u.pathname,c))}return r}).catch(()=>caches.match(u.pathname).then(r=>r||caches.match('/overview'))));});
`);

let total = 0, totalGz = 0;
for (const f of files.filter(f => f.endsWith('.js'))) { const b = readFileSync(new URL(f, out)); total += b.length; totalGz += gzipSync(b).length; }
const mainB = readFileSync(new URL(main, out));
console.log(`app: dist/app, main ${(mainB.length / 1024).toFixed(0)} KB raw / ${(gzipSync(mainB).length / 1024).toFixed(0)} KB gzip; all JS ${(total / 1024).toFixed(0)} KB raw / ${(totalGz / 1024).toFixed(0)} KB gzip (target first-load <= 200 KB raw)`);
