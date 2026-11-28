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
    'vm-guest-worker.mjs', 'vm-guest-worker.py', 'fleet-design-vm-worker.socket', 'fleet-design-vm-worker.service', 'fleet-design-vm.socket', 'fleet-design-vm.service', 'fleet-design-vm-guest.service',
    'fleet-design-verifier-vm.conf', 'fleet-design-vm-tmpfiles.conf', 'prepare-renderer-mounts.sh', 'VM-PROTOCOL.md']) {
    assert.ok(statSync(fileURLToPath(new URL('deploy/design-verifier/' + file, root))).isFile(), file);
  }
  assert.match(read('deploy/design-verifier/fleet-design-vm.socket'), /SocketGroup=fleet-design-vm-client/);
  assert.match(read('deploy/design-verifier/fleet-design-verifier-vm.conf'), /SupplementaryGroups=fleet-design-vm-client/);
  assert.match(read('deploy/design-verifier/fleet-design-vm-tmpfiles.conf'), /d \/run\/fleet-design-vm 0710 root fleet-design-vm-client/);
  assert.doesNotMatch(read('deploy/design-verifier/fleet-design-verifier.service'), /SupplementaryGroups=fleet-design-vm-client/);
});

test('the signed Linux release carries the fixed designer endpoint and its qualification tests', () => {
  const build = read('deploy/build-release.sh');
  assert.match(build, /esbuild[\s\S]*deploy\/design-verifier\/designer\.mjs/);
  assert.match(build, /target" = linux\/amd64[\s\S]*design-capture-vm\.test\.py "\$stage\/tests\/ui\/"/);
  assert.match(read('deploy/verify-release.sh'), /vm-transport\.test\.mjs/);
  for (const file of ['vm-capture.py', 'vm-capture-host.py', 'vm-capture-guest.py',
    'fleet-design-capture.socket', 'fleet-design-capture-tmpfiles.conf', 'fleet-design-vm-capture.conf',
    'fleet-design-vm-guest-capture.conf', 'fleet-design-vm-worker-capture.conf', 'fleet-design-vm-worker-capture-tmpfiles.conf']) {
    assert.ok(statSync(fileURLToPath(new URL('deploy/design-verifier/' + file, root))).isFile(), file);
  }
  const capture = read('deploy/design-verifier/fleet-design-capture.socket');
  assert.match(capture, /ListenStream=\/run\/fleet-design-capture\/broker\.sock/);
  assert.match(capture, /SocketGroup=fleet-design-capture-client/);
  assert.match(capture, /Service=fleet-design-vm\.service/);
  assert.match(read('deploy/design-verifier/fleet-design-vm-capture.conf'),
    /Sockets=fleet-design-vm\.socket fleet-design-capture\.socket/);
  assert.doesNotMatch(read('deploy/design-verifier/fleet-design-verifier.service'), /fleet-design-capture-client/);
  const guest = read('deploy/design-verifier/fleet-design-vm-worker.service');
  assert.match(guest, /User=fleet-render-worker/);
  assert.match(guest, /CapabilityBoundingSet=$/m);
  assert.match(guest, /PrivateNetwork=yes/);
  assert.match(guest, /NoNewPrivileges=yes/);
  assert.match(read('deploy/design-verifier/fleet-design-vm-worker.socket'), /SocketMode=0600/);
  assert.match(read('deploy/design-verifier/fleet-design-vm-guest.service'), /SuccessAction=poweroff/);
  assert.match(guest, /RestrictAddressFamilies=AF_UNIX AF_INET AF_INET6 AF_NETLINK/);
  assert.match(read('deploy/design-verifier/fleet-design-vm.service'), /RestrictAddressFamilies=AF_UNIX$/m);
  assert.match(read('deploy/design-verifier/fleet-design-vm-worker-capture.conf'),
    /TemporaryFileSystem=\/tmp:size=256M,mode=0700,uid=990,gid=990,nosuid,nodev,noexec/);
});
