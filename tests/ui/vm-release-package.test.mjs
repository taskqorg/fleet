import assert from 'node:assert/strict';
import { readFileSync, statSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { test } from 'node:test';

const root = new URL('../../', import.meta.url);
const read = path => readFileSync(new URL(path, root), 'utf8');

test('release build recipe includes the verifier, broker and guest entrypoints', () => {
  const build = read('deploy/build-release.sh');
  assert.match(build, /git archive "\$commit"/);
  assert.match(build, /cp -R config deploy "\$stage\/"/);
  assert.match(build, /esbuild[\s\S]*deploy\/design-verifier\/vm-guest-worker\.mjs/);
  assert.match(build, /cp "\$work\/verifier\/"\* "\$stage\/deploy\/design-verifier\/"/);
  assert.match(build, /target" = linux\/amd64[\s\S]*compare-png/);
  for (const file of ['vm-backend.mjs', 'vm-broker.py', 'vm-preflight.py', 'vm-guest.py',
    'vm-guest-worker.mjs', 'fleet-design-vm.socket', 'fleet-design-vm.service', 'fleet-design-vm-guest.service',
    'fleet-design-verifier-vm.conf', 'fleet-design-vm-tmpfiles.conf', 'prepare-renderer-mounts.sh', 'VM-PROTOCOL.md']) {
    assert.ok(statSync(fileURLToPath(new URL('deploy/design-verifier/' + file, root))).isFile(), file);
  }
  assert.match(read('deploy/design-verifier/fleet-design-vm.socket'), /SocketGroup=fleet-design-vm-client/);
  assert.match(read('deploy/design-verifier/fleet-design-verifier-vm.conf'), /SupplementaryGroups=fleet-design-vm-client/);
  assert.match(read('deploy/design-verifier/fleet-design-vm-tmpfiles.conf'), /d \/run\/fleet-design-vm 0710 root fleet-design-vm-client/);
  assert.doesNotMatch(read('deploy/design-verifier/fleet-design-verifier.service'), /SupplementaryGroups=fleet-design-vm-client/);
});
