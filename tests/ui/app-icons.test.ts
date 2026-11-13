// Requirement: browser tabs and installed workspaces use the existing Fleet F.
// Requirement: authenticated Chromium installation retains Fleet identity and standalone navigation.
// Risk: credentialless manifest requests hit Access login; navigation leaves the installed scope.
// Risk: build emits missing assets, wrong PNG sizes, or divergent workspace branding.
import { test } from 'node:test';
import { runInNewContext } from 'node:vm';
import { execFileSync } from 'node:child_process';
import assert from 'node:assert/strict';
import { readFileSync, readdirSync } from 'node:fs';

const root = new URL('../../dist/app/', import.meta.url);
test('built manifests and HTML reference the shared Fleet icons', () => {
  execFileSync(process.execPath, ['app/build.mjs'], { cwd: new URL('../../', import.meta.url), stdio: 'pipe' });
  const html = readFileSync(new URL('index.html', root), 'utf8');
  const manifests = readdirSync(new URL('manifests/', root));
  assert.ok(manifests.length > 0);
  const screens = JSON.parse(readFileSync(new URL('screens.json', root), 'utf8'));
  const manifestScript = html.match(/<script>([\s\S]*?)<\/script>/)![1];
  for (const path of ['/', '/lab', '/unknown', ...screens.flatMap((view: string) => [`/${view}`, `/${view}/invented-detail`])]) {
    const links: { rel?: string; crossOrigin?: string; href?: string }[] = [];
    runInNewContext(manifestScript, {
      location: { pathname: path },
      document: { createElement: () => ({}), head: { appendChild: (link: typeof links[number]) => links.push(link) } },
    });
    assert.equal(links.length, 1);
    assert.equal(links[0].rel, 'manifest');
    assert.equal(links[0].crossOrigin, 'use-credentials');
    const view = path.split('/')[1];
    assert.equal(links[0].href, `/manifests/${screens.includes(view) ? view : 'overview'}.webmanifest`);
  }
  let shared;
  for (const name of manifests) {
    const manifest = JSON.parse(readFileSync(new URL(`manifests/${name}`, root), 'utf8'));
    const view = name.replace('.webmanifest', '');
    assert.ok(screens.includes(view));
    assert.equal(manifest.id, `/${view}`);
    assert.equal(manifest.start_url, `/${view}`);
    assert.equal(manifest.scope, '/');
    assert.equal(manifest.display, 'standalone');
    assert.ok(manifest.name.startsWith('Fleet '));
    for (const size of [192, 512]) assert.ok(manifest.icons.some((icon: { sizes: string; type: string }) => icon.sizes === `${size}x${size}` && icon.type === 'image/png'));
    shared ??= manifest.icons;
    assert.deepEqual(manifest.icons, shared);
    for (const icon of manifest.icons) {
      const bytes = readFileSync(new URL(icon.src.slice(1), root));
      if (icon.type === 'image/png') {
        assert.equal(bytes.subarray(1, 4).toString(), 'PNG');
        assert.equal(`${bytes.readUInt32BE(16)}x${bytes.readUInt32BE(20)}`, icon.sizes);
      } else {
        const css = readFileSync(new URL('../../packages/ui/src/styles.css', import.meta.url), 'utf8');
        const mark = decodeURIComponent(css.match(/--brand-mark:url\("data:image\/svg\+xml,([^\"]+)/)![1]);
        assert.ok(bytes.toString().includes(mark.match(/<path[^>]+\/>/)![0]));
        assert.ok(html.includes(`rel="icon" type="image/svg+xml" href="${icon.src}"`));
      }
    }
  }
  const touch = html.match(/rel="apple-touch-icon" sizes="180x180" href="([^"]+)"/);
  assert.ok(touch);
  const png = readFileSync(new URL(touch[1].slice(1), root));
  assert.equal(png.readUInt32BE(16), 180);
  assert.equal(png.readUInt32BE(20), 180);
});
