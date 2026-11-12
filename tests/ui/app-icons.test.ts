// Requirement: browser tabs and installed workspaces use the existing Fleet F.
// Risk: build emits missing assets, wrong PNG sizes, or divergent workspace branding.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync, readdirSync } from 'node:fs';

const root = new URL('../../dist/app/', import.meta.url);
test('built manifests and HTML reference the shared Fleet icons', () => {
  const html = readFileSync(new URL('index.html', root), 'utf8');
  const manifests = readdirSync(new URL('manifests/', root));
  assert.ok(manifests.length > 0);
  let shared;
  for (const name of manifests) {
    const manifest = JSON.parse(readFileSync(new URL(`manifests/${name}`, root), 'utf8'));
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
