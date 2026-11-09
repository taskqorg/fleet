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

const icons = JSON.parse(readFileSync(new URL('packages/ui/icons.json', root), 'utf8'));
const VIEWS = { overview: ['Fleet Overview', 'house'], tasks: ['Fleet Tasks', 'list'], conversations: ['Fleet Conversations', 'messages-square'], hosts: ['Fleet Hosts', 'panels-top-left'] };
const accent = tokens.color.light.accent;
for (const [view, [name, icon]] of Object.entries(VIEWS)) {
  const inner = icons[icon].replace(/<!--.*?-->/s, '').replace(/<svg[^>]*>/, '').replace(/<\/svg>\s*$/, '');
  writeFileSync(new URL(`icons/${view}.svg`, out), `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 512 512"><rect width="512" height="512" rx="96" fill="${accent}"/><g transform="translate(96 96) scale(13.33)" fill="none" stroke="#fff" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">${inner}</g></svg>`);
  writeFileSync(new URL(`manifests/${view}.webmanifest`, out), JSON.stringify({ id: `/${view}`, name, short_name: name.replace('Fleet ', ''), start_url: `/${view}`, scope: `/${view}`, display: 'standalone', background_color: tokens.color.light.bg, theme_color: accent, icons: [{ src: `/icons/${view}.svg`, sizes: 'any', type: 'image/svg+xml', purpose: 'any' }] }, null, 1));
}

const html = `<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>Fleet</title><meta name="theme-color" content="${accent}">
<link rel="stylesheet" href="/${cssName}">
<script>(function(){var v=location.pathname.split('/')[1];if(!${JSON.stringify(Object.keys(VIEWS))}.includes(v))v='overview';var l=document.createElement('link');l.rel='manifest';l.href='/manifests/'+v+'.webmanifest';document.head.appendChild(l);var i=document.createElement('link');i.rel='icon';i.href='/icons/'+v+'.svg';document.head.appendChild(i)})()</script>
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
 if(e.request.mode==='navigate')return e.respondWith(fetch(e.request).then(r=>{if(r.ok){const c=r.clone();caches.open(CACHE).then(x=>x.put('/overview',c))}return r}).catch(()=>caches.match('/overview')));});
`);

let total = 0, totalGz = 0;
for (const f of files.filter(f => f.endsWith('.js'))) { const b = readFileSync(new URL(f, out)); total += b.length; totalGz += gzipSync(b).length; }
const mainB = readFileSync(new URL(main, out));
console.log(`app: dist/app, main ${(mainB.length / 1024).toFixed(0)} KB raw / ${(gzipSync(mainB).length / 1024).toFixed(0)} KB gzip; all JS ${(total / 1024).toFixed(0)} KB raw / ${(totalGz / 1024).toFixed(0)} KB gzip (target first-load <= 200 KB raw)`);
