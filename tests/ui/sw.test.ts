// The service worker caches only the app's own routes (review fix 10); legacy pages and test sites are never cached.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { existsSync, readFileSync } from 'node:fs';

test('service worker app-route pattern', { skip: !existsSync('dist/app/sw.js') && 'run npm run build first' }, () => {
  const sw = readFileSync('dist/app/sw.js', 'utf8');
  const m = sw.match(/const app=(\/.*\/)\.test/)!;
  const re = new RegExp(m[1].slice(1, -1));
  for (const p of ['/overview', '/tasks', '/tasks/abc', '/conversations/x', '/hosts/mac']) assert.ok(re.test(p), p);
  for (const p of ['/legacy', '/legacy/lab', '/test-sites/a/b.html', '/api/ops/me', '/tasks/a/b', '/']) assert.ok(!re.test(p), p);
  assert.ok(!sw.includes("put('/overview'"), 'navigations are cached under their own path');
});
