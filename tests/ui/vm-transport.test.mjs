import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import { fileURLToPath } from 'node:url';
import { test } from 'node:test';

// The Python broker and the Node verifier must agree on framing and failures.
test('protected VM transport protocol', () => {
  const result = spawnSync('python3', [fileURLToPath(new URL('./vm-transport.test.py', import.meta.url))],
    { encoding: 'utf8', timeout: 15_000 });
  assert.equal(result.status, 0, result.stderr || result.error?.message);
});
