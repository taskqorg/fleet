import assert from 'node:assert/strict';
import { createHash } from 'node:crypto';
import { spawnSync } from 'node:child_process';
import { mkdtempSync, mkdirSync, readFileSync, rmSync, statSync, symlinkSync, writeFileSync } from 'node:fs';
import { createConnection, createServer } from 'node:net';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { test } from 'node:test';
import { decodeVMJob } from '../../deploy/design-verifier/parity.mjs';
import { DesignBlocked, RendererBusy, VerificationWait } from '../../deploy/design-verifier/provider.mjs';
import { validateVMConfig, validateVMProbe, vmBackend } from '../../deploy/design-verifier/vm-backend.mjs';

const hash = x => createHash('sha256').update(x).digest();
const runtime = { imageSha256: '1'.repeat(64), fontSha256: '2'.repeat(64), lockSha256: '3'.repeat(64),
  node: 'v22.18.0', chromium: '140.0', playwright: '1.56.1' };
const manifest = { version: 1, goldenSha256: 'a'.repeat(64), biosSha256: 'b'.repeat(64), releaseSha256: 'c'.repeat(64), guestWorkerSha256: 'd'.repeat(64), runtimeSha256: 'e'.repeat(64), releaseCommit: '5'.repeat(40) };
const config = { version: 1, releaseCommit: manifest.releaseCommit, runtime, launchManifest: JSON.stringify(manifest) };
const launchSha256 = hash(config.launchManifest).toString('hex');
const guest = { releaseCommit: manifest.releaseCommit, workerSha256: manifest.guestWorkerSha256, runtimeSha256: manifest.runtimeSha256 };
const build = { files: { 'pnpm-lock.yaml': '100644:' + runtime.lockSha256 },
  node: runtime.node, chromium: runtime.chromium, playwright: runtime.playwright };
const release = { commit: config.releaseCommit, target: 'linux-amd64' };

test('VM config and probe bind launch, signed release and every runtime pin', () => {
  assert.equal(validateVMConfig(config, build, release).launchSha256, launchSha256);
  const probe = Buffer.from(JSON.stringify({ node: runtime.node, chromium: runtime.chromium, runtime, guest }));
  assert.equal(validateVMProbe(probe, runtime, manifest), runtime);
  assert.throws(() => validateVMConfig({ ...config, releaseCommit: '6'.repeat(40) }, build, release));
  assert.throws(() => validateVMConfig({ ...config, runtime: { ...runtime, lockSha256: '7'.repeat(64) } }, build, release));
  assert.throws(() => validateVMProbe(Buffer.from(JSON.stringify({ node: runtime.node, chromium: runtime.chromium,
    runtime: { ...runtime, fontSha256: '8'.repeat(64) }, guest })), runtime, manifest), VerificationWait);
  for (const key of Object.keys(guest)) assert.throws(() => validateVMProbe(Buffer.from(JSON.stringify({ node: runtime.node, chromium: runtime.chromium, runtime, guest: { ...guest, [key]: 'bad' } })), runtime, manifest), VerificationWait);
});

async function fixture(t, behavior = {}) {
  const dir = mkdtempSync(join(tmpdir(), 'fleet-vm-test-'));
  const path = join(dir, 'b.sock'), nonce = Buffer.alloc(32, 9);
  const seen = [];
  const dials = [];
  const server = createServer(socket => {
    dials.push(true);
    if (dials.length <= (behavior.busyCount ?? 0)) {
      const busy = Buffer.alloc(40); busy.write('FVBU'); busy[4] = 1;
      socket.end(busy);
      return;
    }
    const challenge = Buffer.alloc(40); challenge.write('FVMB'); challenge[4] = 1; nonce.copy(challenge, 8);
    if (behavior.badChallenge) challenge[4] = 2;
    if (behavior.highChallenge) challenge[0] |= 0x80;
    socket.write(challenge.subarray(0, 11)); socket.write(challenge.subarray(11));
    let input = Buffer.alloc(0), done = false;
    socket.on('data', bytes => {
      input = Buffer.concat([input, bytes]);
      if (done || input.length < 80) return;
      const size = Number(input.readBigUInt64BE(40));
      if (input.length < 80 + size) return;
      done = true;
      const body = input.subarray(80, 80 + size);
      seen.push({ header: input.subarray(0, 80), body });
      if (behavior.stall) return;
      const value = behavior.output ?? Buffer.from('ok');
      const response = Buffer.alloc(144); response.write('FVMR'); response[4] = 1; response[5] = input[5];
      response[6] = behavior.status ?? 0;
      nonce.copy(response, 8); hash(body).copy(response, 40); Buffer.from(launchSha256, 'hex').copy(response, 72);
      response.writeBigUInt64BE(BigInt(behavior.length ?? value.length), 104); hash(value).copy(response, 112);
      if (behavior.highResponse) response[0] |= 0x80;
      if (behavior.wrongOperation) response[5] ^= 1;
      if (behavior.wrongRequestHash) response[40] ^= 1;
      if (behavior.badReserved) response[7] = 1;
      if (behavior.truncated) return socket.end(response.subarray(0, 100));
      if (behavior.wrongNonce) response[8] ^= 1;
      if (behavior.wrongLaunch) response[72] ^= 1;
      if (behavior.wrongHash) response[112] ^= 1;
      socket.write(response.subarray(0, 50)); socket.write(response.subarray(50));
      socket.end(Buffer.concat([value, behavior.trailing ? Buffer.from('x') : Buffer.alloc(0)]));
    });
  });
  await new Promise((resolve, reject) => { server.on('error', reject); server.listen(path, resolve); });
  t.after(async () => { await new Promise(resolve => server.close(resolve)); rmSync(dir, { recursive: true, force: true }); });
  return { seen, dials, client: vmBackend({ launchSha256 }, { connect: () => createConnection(path),
    deadlineOverride: behavior.deadline ?? 200, signal: behavior.signal, wait: behavior.wait }) };
}

test('socket probe result matches the protected runtime', async t => {
  const output = Buffer.from(JSON.stringify({ node: runtime.node, chromium: runtime.chromium, runtime, guest }));
  const { client } = await fixture(t, { output });
  const bytes = await client.run('render', ['--probe']);
  assert.equal(validateVMProbe(bytes, runtime, manifest), runtime);
});

test('protected stock probe retries busy responses within its original deadline', async t => {
  const output = Buffer.from(JSON.stringify({ node: runtime.node, chromium: runtime.chromium, runtime, guest }));
  const delays = [];
  const { client, dials, seen } = await fixture(t, { busyCount: 2, output, deadline: 2000,
    wait: async ms => delays.push(ms) });
  const bytes = await client.run('render', ['--probe']);
  assert.equal(validateVMProbe(bytes, runtime, manifest), runtime);
  assert.deepEqual(delays, [250, 500]);
  assert.equal(dials.length, 3);
  assert.equal(seen.length, 1);
  assert.equal(seen[0].header[5], 1);
});

test('persistent contention is explicit and cannot consume the protected job deadline', async t => {
  const { client, dials, seen } = await fixture(t, { busyCount: 9, deadline: 2000, wait: async () => {} });
  await assert.rejects(client.run('render', ['--probe']), RendererBusy);
  assert.equal(dials.length, 3);
  assert.equal(seen.length, 0);
});

test('guest bridge accepts only fixed bounded operations', () => {
  assert.deepEqual(decodeVMJob(1, Buffer.alloc(0)), { job: 'render', args: ['--probe'], bytes: undefined, max: 1024, timeout: 25_000 });
  const data = Buffer.from('[]'), header = Buffer.alloc(4); header.writeUInt32BE(data.length);
  const request = decodeVMJob(2, Buffer.concat([header, data, Buffer.from('tar')]));
  assert.equal(request.job, 'build');
  assert.deepEqual(request.args, ['[]']);
  assert.equal(request.bytes.toString(), 'tar');
  assert.equal(request.timeout, 60_000);
  assert.equal(decodeVMJob(3, Buffer.from('{}')).timeout, 65_000);
  assert.equal(decodeVMJob(4, Buffer.from('{}')).timeout, 10_000);
  assert.throws(() => decodeVMJob(1, Buffer.from('extra')), VerificationWait);
  assert.throws(() => decodeVMJob(2, Buffer.from([0, 0, 0, 9])), VerificationWait);
  assert.throws(() => decodeVMJob(5, Buffer.alloc(0)), VerificationWait);
});

test('one framed challenge and response binds probe and build payloads', async t => {
  const { seen, client } = await fixture(t);
  assert.equal((await client.run('render', ['--probe'])).toString(), 'ok');
  assert.equal(seen[0].header.toString('ascii', 0, 4), 'FVMQ');
  assert.equal(seen[0].header[5], 1);
  assert.equal(seen[0].body.length, 0);
  const deps = JSON.stringify([{ path: 'node_modules/x', sha256: 'a'.repeat(64) }]);
  await client.run('build', [deps], Buffer.from('tar'));
  assert.equal(seen[1].header[5], 2);
  assert.equal(seen[1].body.readUInt32BE(0), Buffer.byteLength(deps));
  assert.equal(seen[1].body.subarray(4, 4 + Buffer.byteLength(deps)).toString(), deps);
  assert.equal(seen[1].body.subarray(4 + Buffer.byteLength(deps)).toString(), 'tar');
  assert.deepEqual(seen[1].header.subarray(48, 80), hash(seen[1].body));
  await client.run('render', [], '{}');
  await client.run('compare', [], '{}');
  assert.equal(seen[2].header[5], 3);
  assert.equal(seen[3].header[5], 4);
  await assert.rejects(client.run('build', ['[' + ' '.repeat(96 * 1024) + ']'], Buffer.from('tar')), VerificationWait);
  await assert.rejects(client.run('unknown', [], Buffer.alloc(0)), VerificationWait);
});

for (const behavior of [{ badChallenge: true }, { highChallenge: true }, { highResponse: true }, { wrongOperation: true }, { wrongRequestHash: true },
  { badReserved: true }, { truncated: true }, { wrongNonce: true }, { wrongLaunch: true }, { wrongHash: true },
  { length: 1025 }, { trailing: true }, { stall: true }, { status: 2, output: Buffer.alloc(0) }]) {
  test('transport failures stay unconfirmed: ' + JSON.stringify(behavior), async t => {
    const { client } = await fixture(t, behavior);
    await assert.rejects(client.run('render', ['--probe']), VerificationWait);
  });
}

test('classified build block is distinct from transport failure', async t => {
  const { client } = await fixture(t, { status: 1, output: Buffer.alloc(0) });
  await assert.rejects(client.run('build', ['[]'], Buffer.from('tar')), DesignBlocked);
});

test('installer stages exactly the supported fixed bwrap mount targets', t => {
  const root = mkdtempSync(join(tmpdir(), 'fleet-render-mounts-'));
  t.after(() => rmSync(root, { recursive: true, force: true }));
  const helper = new URL('../../deploy/design-verifier/prepare-renderer-mounts.sh', import.meta.url).pathname;
  const install = readFileSync(new URL('../../deploy/design-verifier/install-renderer.sh', import.meta.url), 'utf8');
  assert.match(install, /prepare-renderer-mounts\.sh.*\$stage\/rootfs/);
  const run = () => spawnSync('/bin/sh', [helper, root], { encoding: 'utf8' });
  assert.equal(run().status, 0);
  assert.equal(run().status, 0);
  assert.equal(readFileSync(join(root, 'trusted-compare')).length, 0);
  assert.equal(statSync(join(root, 'trusted')).mode & 0o777, 0o755);
  assert.equal(statSync(join(root, 'trusted-compare')).mode & 0o777, 0o644);
  rmSync(join(root, 'trusted'), { recursive: true });
  symlinkSync('/invented', join(root, 'trusted'));
  assert.notEqual(run().status, 0);
  rmSync(join(root, 'trusted')); mkdirSync(join(root, 'trusted'));
  rmSync(join(root, 'trusted-compare')); symlinkSync('/invented', join(root, 'trusted-compare'));
  assert.notEqual(run().status, 0);
  rmSync(join(root, 'trusted-compare')); writeFileSync(join(root, 'trusted-compare'), 'preserve');
  assert.notEqual(run().status, 0);
  assert.equal(readFileSync(join(root, 'trusted-compare'), 'utf8'), 'preserve');
});

test('stopping aborts an in-flight VM request', async t => {
  const stop = new AbortController();
  const { client } = await fixture(t, { stall: true, signal: stop.signal });
  const pending = client.run('render', ['--probe']);
  await new Promise(resolve => setTimeout(resolve, 10));
  stop.abort();
  await assert.rejects(pending, VerificationWait);
});
