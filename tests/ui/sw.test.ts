// The service worker is a self-unregistering cleanup stub that clears legacy precaches without intercepting fetches.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { existsSync, readFileSync } from 'node:fs';

test('service worker cleanup stub', { skip: !existsSync('dist/app/sw.js') && 'run pnpm run build first' }, () => {
  const sw = readFileSync('dist/app/sw.js', 'utf8');
  assert.ok(sw.includes('skipWaiting'), 'activates immediately');
  assert.ok(sw.includes('caches.delete'), 'clears legacy fleet caches');
  assert.ok(sw.includes('registration.unregister'), 'unregisters itself');
  assert.ok(!sw.includes('addAll') && !sw.includes("'fetch'"), 'does not precache or intercept fetches');
});
