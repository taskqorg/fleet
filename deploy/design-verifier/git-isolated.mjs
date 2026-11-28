import { spawn } from 'node:child_process';
import { link, lstat, mkdir, mkdtemp, readdir, rm, unlink } from 'node:fs/promises';
import { join } from 'node:path';
import { fileURLToPath } from 'node:url';
import { DesignBlocked, VerificationWait } from './provider.mjs';
import { ProtectedDesignRejection } from '../../packages/ui/scripts/design-protected.mjs';

// The child and every Git descendant occupy a private process group. A failed cleanup
// poisons the service: it must not publish while a previous Git operation may run.
export function runGitProcess(command, args, { input, signal, timeout = 120_000, onUnconfirmed = () => {}, kill = process.kill.bind(process), cleanupTimeout = 3000 } = {}) {
  if (signal?.aborted) return Promise.reject(new Error('Verifier stopped'));
  return new Promise((resolve, reject) => {
    const child = spawn(command, args, { detached: true, env: { PATH: '/usr/bin:/bin' },
      stdio: ['pipe', 'pipe', 'ignore'] });
    const chunks = [];
    let size = 0, failure, closed = false, settleTimer;
    const terminate = reason => {
      failure ??= reason;
      if (child.pid) { try { kill(-child.pid, 'SIGKILL'); } catch (err) { if (err.code !== 'ESRCH') onUnconfirmed(); } }
      // Never wait forever on an uninterruptible process or inherited pipe.
      settleTimer ??= setTimeout(() => {
        if (closed) return;
        closed = true; onUnconfirmed();
        clearTimeout(timer); signal?.removeEventListener('abort', abort);
        child.stdin.destroy(); child.stdout.destroy(); child.stderr?.destroy(); child.unref();
        reject(new Error('Git process cleanup unconfirmed'));
      }, cleanupTimeout);
    };
    const abort = () => terminate('Verifier stopped');
    signal?.addEventListener('abort', abort, { once: true });
    if (signal?.aborted) abort();
    const timer = setTimeout(() => terminate('Git operation timed out'), timeout);
    child.stdout.on('data', bytes => {
      size += bytes.length;
      if (size > 140 * 1024 * 1024) terminate('Git output exceeded its limit');
      else chunks.push(bytes);
    });
    child.stdin.on('error', () => {});
    child.on('error', () => { failure ??= 'Git process could not start'; });
    child.on('close', async code => {
      if (closed) return;
      // close reaps the leader, but a Git descendant could have outlived it.
      // Keep the same bound and fail closed unless the whole group disappears.
      const deadline = performance.now() + cleanupTimeout;
      let groupExists = false;
      do {
        groupExists = false;
        if (child.pid) {
          try { kill(-child.pid, 0); groupExists = true; }
          catch (err) { if (err.code !== 'ESRCH') groupExists = true; }
        }
        if (groupExists) {
          if (!failure) terminate('Git process left descendants');
          if (performance.now() < deadline) await new Promise(done => setTimeout(done, 30));
        }
      } while (groupExists && performance.now() < deadline);
      closed = true;
      clearTimeout(timer); clearTimeout(settleTimer);
      signal?.removeEventListener('abort', abort);
      if (groupExists) { onUnconfirmed(); return reject(new Error('Git process cleanup unconfirmed')); }
      if (failure || code !== 0) return reject(new Error(failure ?? 'Git process failed'));
      resolve(Buffer.concat(chunks));
    });
    child.stdin.end(input);
  });
}

const packName = /^pack-[a-f0-9]{40}\.pack$/;
const packIndex = name => name.replace(/\.pack$/, '.idx');

async function ifPresent(read, path) {
  try { return await read(path); }
  catch (err) { if (err.code !== 'ENOENT') throw err; }
}

async function discardFetchSlot(slot, objects) {
  const source = join(slot, 'objects', 'pack');
  for (const name of await ifPresent(readdir, source) ?? []) {
    if (!packName.test(name)) continue;
    const cached = join(objects, 'pack', name);
    const published = await ifPresent(lstat, cached);
    if (!published || await ifPresent(lstat, join(objects, 'pack', packIndex(name)))) continue;
    const staged = await ifPresent(lstat, join(source, name));
    // A matching inode proves this slot owns the unindexed link from interrupted promotion.
    if (staged?.isFile() && staged.dev === published.dev && staged.ino === published.ino) await unlink(cached);
  }
  await rm(slot, { recursive: true });
}

async function reapFetchSlots(directory, objects) {
  await mkdir(directory, { mode: 0o700, recursive: true });
  const info = await lstat(directory);
  if (!info.isDirectory() || info.uid !== process.getuid() || (info.mode & 0o077))
    throw new VerificationWait('Untrusted Git fetch slot directory');
  for (const entry of await readdir(directory, { withFileTypes: true })) {
    if (!entry.isDirectory() || !/^slot-[a-zA-Z0-9]{6}$/.test(entry.name))
      throw new VerificationWait('Unexpected Git fetch slot entry');
    // The sole owner is the verifier unit. Each Git fetch writes only to its own unique slot.
    await discardFetchSlot(join(directory, entry.name), objects);
  }
}

export async function retainFetchPacks(slot, objects, signal) {
  const source = join(slot, 'objects', 'pack');
  const entries = await readdir(source, { withFileTypes: true });
  const files = new Set(entries.filter(entry => entry.isFile()).map(entry => entry.name));
  const packs = [...files].filter(name => packName.test(name));
  for (const pack of packs) {
    const index = packIndex(pack);
    if (!files.has(index)) throw new VerificationWait('Git fetch pack has no complete index');
    // Hard links publish only completed files. Git discovers the pack through the index, linked last.
    for (const name of [pack, index]) {
      signal?.throwIfAborted();
      try { await link(join(source, name), join(objects, 'pack', name)); }
      catch (err) { if (err.code !== 'EEXIST') throw err; }
    }
  }
}

export function openAsyncRepository(state, { signal, onUnconfirmed } = {}) {
  let poisoned = false;
  let slotsReady;
  const slots = join(state, 'git-fetch');
  const objects = join(state, 'git', 'objects');
  const fail = () => { poisoned = true; onUnconfirmed?.(); };
  const worker = fileURLToPath(new URL('./git-worker.mjs', import.meta.url));
  async function call(method, ...args) {
    if (poisoned || signal?.aborted) throw new Error('Verifier stopped');
    const bytes = await runGitProcess(process.execPath, [worker], {
      input: JSON.stringify({ state, method, args }), signal, onUnconfirmed: fail,
    });
    if (poisoned || signal?.aborted) throw new Error('Verifier stopped');
    const reply = JSON.parse(bytes.toString('utf8'));
    if (!reply.ok) {
      if (reply.type === 'ProtectedDesignRejection') throw new ProtectedDesignRejection(reply.message);
      if (reply.type === 'DesignBlocked') throw new DesignBlocked(reply.message);
      if (reply.type === 'VerificationWait') throw new VerificationWait(reply.message);
      throw new Error('Git operation failed');
    }
    return method === 'archive' ? Buffer.from(reply.value, 'base64') : reply.value;
  }
  return { dir: join(state, 'git'), async ensure(value) {
    await (slotsReady ??= reapFetchSlots(slots, objects).catch(error => {
      slotsReady = undefined;
      throw error;
    }));
    const slot = await mkdtemp(join(slots, 'slot-'));
    try {
      if (await call('ensure', value, slot)) {
        await retainFetchPacks(slot, objects, signal);
        await call('ensure', value);
      }
    }
    finally { if (!poisoned) await discardFetchSlot(slot, objects); }
  },
    diffBase: value => call('diffBase', value), fingerprint: (head, path) => call('fingerprint', head, path),
    archive: (head, files) => call('archive', head, files),
    measureInputs: (head, recipes) => call('measureInputs', head, recipes),
    groupChain: value => call('groupChain', value), changes: value => call('changes', value) };
}
