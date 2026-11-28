import { createHash, timingSafeEqual } from 'node:crypto';
import { createConnection } from 'node:net';
import { setTimeout as pause } from 'node:timers/promises';
import { DesignBlocked, RendererBusy, VerificationWait } from './provider.mjs';

// The broker socket is a host identity boundary, not an operator- or candidate-selected path.
export const VM_SOCKET = '/run/fleet-design-vm/broker.sock';
const MiB = 1024 * 1024;
const ops = {
  probe: { id: 1, request: 0, response: 1024, deadline: 420_000 },
  build: { id: 2, request: 96 * MiB + 96 * 1024 + 4, response: 24 * MiB, deadline: 455_000 },
  render: { id: 3, request: 32 * MiB, response: 32 * MiB, deadline: 460_000 },
  compare: { id: 4, request: 64 * MiB + 2048, response: 1024, deadline: 405_000 },
};
const sha = bytes => createHash('sha256').update(bytes).digest();
const equal = (a, b) => a.length === b.length && timingSafeEqual(a, b);
const text = (b, start, value) => b.subarray(start, start + value.length).equals(Buffer.from(value, 'ascii'));
const hex = value => typeof value === 'string' && /^[a-f0-9]{64}$/.test(value);

function boundedBytes(value, maximum) {
  if (typeof value !== 'string' && !Buffer.isBuffer(value) && !(value instanceof Uint8Array))
    throw new Error('Invalid byte input');
  const length = typeof value === 'string' ? Buffer.byteLength(value) : value.byteLength;
  if (length > maximum) throw new Error('Input exceeds its limit');
  return Buffer.from(value);
}

function payload(job, args, bytes) {
  if (job === 'render' && args.length === 1 && args[0] === '--probe' && bytes == null)
    return { name: 'probe', data: Buffer.alloc(0) };
  if (job === 'build' && args.length === 1 && typeof args[0] === 'string') {
    const deps = boundedBytes(args[0], 96 * 1024);
    if (!Array.isArray(JSON.parse(args[0]))) throw new Error('Invalid dependency input');
    const archive = boundedBytes(bytes, 96 * MiB);
    const header = Buffer.alloc(4); header.writeUInt32BE(deps.length);
    return { name: 'build', data: Buffer.concat([header, deps, archive]) };
  }
  if ((job === 'render' || job === 'compare') && args.length === 0 && bytes != null)
    return { name: job, data: boundedBytes(bytes, ops[job].request) };
  throw new Error('Unsupported protected operation');
}

// Async iteration applies stream backpressure. Lengths are checked before allocating a body.
function reader(socket) {
  const iterator = socket[Symbol.asyncIterator]();
  let chunk = Buffer.alloc(0), offset = 0;
  async function more() {
    const next = await iterator.next();
    if (next.done) return false;
    chunk = Buffer.from(next.value); offset = 0;
    return true;
  }
  return {
    async take(length, hasher) {
      const output = Buffer.alloc(length);
      let written = 0;
      while (written < length) {
        if (offset === chunk.length && !await more()) throw new Error('truncated frame');
        const count = Math.min(length - written, chunk.length - offset);
        chunk.copy(output, written, offset, offset + count);
        hasher?.update(chunk.subarray(offset, offset + count));
        written += count; offset += count;
      }
      return output;
    },
    async end() {
      if (offset !== chunk.length || await more()) throw new Error('unexpected trailing frame');
    },
  };
}

const runtimeKeys = ['chromium', 'fontSha256', 'imageSha256', 'lockSha256', 'node', 'playwright'];
const exact = (value, keys) => value && typeof value === 'object' && !Array.isArray(value) &&
  Object.keys(value).sort().join(',') === [...keys].sort().join(',');

export function validateVMConfig(config, build, release) {
  if (!exact(config, ['version', 'releaseCommit', 'runtime', 'launchManifest']) || config.version !== 1 ||
      !/^[a-f0-9]{40}$/.test(config.releaseCommit) || config.releaseCommit !== release?.commit ||
      release?.target !== 'linux-amd64' || typeof config.launchManifest !== 'string' ||
      Buffer.byteLength(config.launchManifest) > 4096 || !exact(config.runtime, runtimeKeys) ||
      !['imageSha256', 'fontSha256', 'lockSha256'].every(k => hex(config.runtime[k])) ||
      config.runtime.lockSha256 !== build?.files?.['pnpm-lock.yaml']?.split(':')[1] ||
      !['node', 'chromium', 'playwright'].every(k => typeof config.runtime[k] === 'string' && config.runtime[k] === build?.[k]))
    throw new Error('Protected VM identity does not match this signed release');
  let manifest;
  try { manifest = JSON.parse(config.launchManifest); } catch { throw new Error('Invalid protected launch manifest'); }
  const keys = ['version', 'goldenSha256', 'biosSha256', 'releaseSha256', 'guestWorkerSha256', 'runtimeSha256', 'releaseCommit'];
  if (!exact(manifest, keys) || manifest.version !== 1 || manifest.releaseCommit !== config.releaseCommit ||
      !keys.filter(k => k.endsWith('Sha256')).every(k => hex(manifest[k])))
    throw new Error('Protected launch manifest does not match this signed release');
  return { ...config, manifest, launchSha256: sha(Buffer.from(config.launchManifest)).toString('hex') };
}

export function validateVMProbe(bytes, expected, manifest) {
  let probe;
  try { probe = JSON.parse(bytes); } catch { throw new VerificationWait('Protected VM probe is not valid JSON'); }
  if (!exact(probe, ['node', 'chromium', 'runtime', 'guest']) || !exact(probe.runtime, runtimeKeys) ||
      !exact(probe.guest, ['releaseCommit', 'workerSha256', 'runtimeSha256']) ||
      probe.node !== expected.node || probe.chromium !== expected.chromium ||
      !runtimeKeys.every(k => probe.runtime[k] === expected[k]) ||
      probe.guest.releaseCommit !== manifest.releaseCommit || probe.guest.workerSha256 !== manifest.guestWorkerSha256 ||
      probe.guest.runtimeSha256 !== manifest.runtimeSha256)
    throw new VerificationWait('Protected VM probe did not match guest release and runtime pins');
  return expected;
}

// connect is injected only by local tests; production callers use the fixed socket.
export function vmBackend(expected, { connect = () => createConnection(VM_SOCKET), deadlineOverride, signal, wait = pause } = {}) {
  if (!hex(expected?.launchSha256)) throw new Error('Invalid protected VM launch identity');
  return {
    async run(job, args, bytes) {
      let request;
      try { request = payload(job, args, bytes); }
      catch { throw new VerificationWait('Unsupported or invalid isolated VM operation'); }
      const spec = ops[request.name];
      if (request.data.length > spec.request) throw new VerificationWait('Isolated VM request exceeds its limit');
      const requestHash = sha(request.data), launch = Buffer.from(expected.launchSha256, 'hex');
      const deadline = performance.now() + (deadlineOverride ?? spec.deadline);
      for (let attempt = 0; ; attempt++) {
        let socket, timer;
        try {
          if (signal?.aborted) throw new Error('stopped');
          socket = connect();
          const abort = () => socket.destroy(new Error('stopped'));
          signal?.addEventListener('abort', abort, { once: true });
          if (signal?.aborted) abort();
          socket.once('close', () => signal?.removeEventListener('abort', abort));
          timer = setTimeout(() => socket.destroy(new Error('deadline')), Math.max(1, deadline - performance.now()));
          const input = reader(socket);
          const challenge = await input.take(40);
          if (text(challenge, 0, 'FVBU') && challenge[4] === 1 && !challenge.subarray(5).some(Boolean)) {
            await input.end();
            if (attempt >= 2 || deadline - performance.now() <= 750)
              throw new RendererBusy();
            clearTimeout(timer);
            socket.destroy();
            await wait(250 * (attempt + 1), undefined, { signal });
            continue;
          }
          if (!text(challenge, 0, 'FVMB') || challenge[4] !== 1 || challenge.subarray(5, 8).some(Boolean))
            throw new Error('invalid challenge');
          const nonce = challenge.subarray(8, 40);
          const header = Buffer.alloc(80);
          header.write('FVMQ', 0, 'ascii'); header[4] = 1; header[5] = spec.id;
          nonce.copy(header, 8); header.writeBigUInt64BE(BigInt(request.data.length), 40); requestHash.copy(header, 48);
          socket.write(header);
          if (request.data.length) socket.write(request.data);
          const response = await input.take(144);
          if (!text(response, 0, 'FVMR') || response[4] !== 1 || response[5] !== spec.id || response[7] !== 0 ||
              !equal(response.subarray(8, 40), nonce) || !equal(response.subarray(40, 72), requestHash) ||
              !equal(response.subarray(72, 104), launch)) throw new Error('response identity mismatch');
          const length = response.readBigUInt64BE(104);
          if (length > BigInt(spec.response)) throw new Error('oversized response');
          const responseHash = createHash('sha256');
          const result = await input.take(Number(length), responseHash);
          if (!equal(response.subarray(112, 144), responseHash.digest())) throw new Error('response digest mismatch');
          await input.end(); // EOF binds success to broker completion and teardown, with no trailing frames.
          if (response[6] === 1 && request.name === 'build' && result.length === 0)
            throw new DesignBlocked('Isolated build rejected selected dependency or source inputs');
          if (response[6] !== 0) throw new Error('broker unavailable or unconfirmed');
          return result;
        } catch (error) {
          if (error instanceof DesignBlocked || error instanceof RendererBusy) throw error;
          throw new VerificationWait('Protected VM execution or identity could not be confirmed');
        } finally {
          clearTimeout(timer);
          socket?.destroy();
        }
      }
    },
  };
}
