import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import { fileURLToPath } from 'node:url';
import { test } from 'node:test';

for (const [name, script] of [
  ['protected VM transport protocol', 'vm-transport.test.py'],
  ['designer capture authorization, VM transport and teardown', 'design-capture-vm.test.py'],
]) {
  test(name, () => {
    const result = spawnSync('python3', ['-B', fileURLToPath(new URL('./' + script, import.meta.url))],
      { encoding: 'utf8', timeout: 15_000 });
    assert.equal(result.status, 0, result.stderr || result.error?.message);
  });
}
