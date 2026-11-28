import { spawn } from 'node:child_process';
import { fileURLToPath } from 'node:url';
import { DesignBlocked, VerificationWait } from './provider.mjs';
import { ProtectedDesignRejection } from '../../packages/ui/scripts/design-protected.mjs';

// The child and every Git descendant occupy a private process group. A failed cleanup
// poisons the service: it must not publish while a previous Git operation may run.
export function runGitProcess(command, args, { input, signal, timeout = 120_000, onUnconfirmed = () => {} } = {}) {
  if (signal?.aborted) return Promise.reject(new Error('Verifier stopped'));
  return new Promise((resolve, reject) => {
    const child = spawn(command, args, { detached: true, env: { PATH: '/usr/bin:/bin' },
      stdio: ['pipe', 'pipe', 'ignore'] });
    const chunks = [];
    let size = 0, failure, closed = false, settleTimer;
    const terminate = reason => {
      failure ??= reason;
      if (child.pid) { try { process.kill(-child.pid, 'SIGKILL'); } catch (err) { if (err.code !== 'ESRCH') onUnconfirmed(); } }
      // Never wait forever on an uninterruptible process or inherited pipe.
      settleTimer ??= setTimeout(() => { if (!closed) { onUnconfirmed(); reject(new Error('Git process cleanup unconfirmed')); } }, 3000);
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
      // close reaps the leader, but a Git descendant could have outlived it.
      // Keep the same bound and fail closed unless the whole group disappears.
      const deadline = Date.now() + 3000;
      let groupExists = false;
      do {
        groupExists = false;
        if (child.pid) {
          try { process.kill(-child.pid, 0); groupExists = true; }
          catch (err) { if (err.code !== 'ESRCH') groupExists = true; }
        }
        if (groupExists) {
          if (!failure) terminate('Git process left descendants');
          if (Date.now() < deadline) await new Promise(done => setTimeout(done, 30));
        }
      } while (groupExists && Date.now() < deadline);
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

export function openAsyncRepository(state, { signal, onUnconfirmed } = {}) {
  let poisoned = false;
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
  return { dir: state + '/git', ensure: value => call('ensure', value),
    diffBase: value => call('diffBase', value), fingerprint: (head, path) => call('fingerprint', head, path),
    archive: (head, files) => call('archive', head, files),
    measureInputs: (head, recipes) => call('measureInputs', head, recipes),
    groupChain: value => call('groupChain', value), changes: value => call('changes', value) };
}
