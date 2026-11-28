import { lstatSync, readFileSync, realpathSync, statfsSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { resolve, sep } from 'node:path';

// A designer CLI has no implicit systemd unit. Its own unified cgroup must be
// bounded before launching any guest; no environment variable can assert this.
export function validateAllocation(cgroup, read) {
  const match = /^0::(\/[^\n]*)$/m.exec(cgroup);
  if (!match || match[1].split('/').some(part => part === '..')) throw new Error('A unified cgroup allocation is required');
  const memory = read(match[1], 'memory.max').trim();
  const pids = read(match[1], 'pids.max').trim();
  const cpu = read(match[1], 'cpu.max').trim();
  if (!/^[1-9][0-9]*$/.test(memory) || !/^[1-9][0-9]*$/.test(pids) || !/^[1-9][0-9]* [1-9][0-9]*$/.test(cpu))
    throw new Error('Capture requires finite memory, PID and CPU allocation');
}

export function assertAllocation() {
  const root = '/sys/fs/cgroup';
  validateAllocation(readFileSync('/proc/self/cgroup', 'utf8'), (group, name) => {
    const dir = realpathSync(resolve(root, '.' + group));
    if (dir !== root && !dir.startsWith(root + sep)) throw new Error('Cgroup escaped the unified hierarchy');
    return readFileSync(resolve(dir, name), 'utf8');
  });
}

// Import can expand a compressed Git bundle before validating its tree. Require
// a private, dedicated bounded tmpfs mount rather than the host's shared /tmp.
export function validateScratch(path, real, info, fs, mounts, uid) {
  const entries = mounts.trim().split('\n').map(line => {
    const [before, after] = line.split(' - ');
    if (!after) return null;
    const fields = before.split(' ');
    const decode = value => value?.replace(/\\([0-7]{3})/g, (_, n) => String.fromCharCode(parseInt(n, 8)));
    return { device: fields[2], root: decode(fields[3]), mount: decode(fields[4]), type: after.split(' ')[0] };
  }).filter(Boolean);
  const entry = entries.find(item => item.mount === path && item.type === 'tmpfs');
  // Reject a bind of a shared tmpfs subdirectory and another visible mount of
  // the same filesystem. The provisioner must still isolate mount namespaces.
  const mounted = entry && entry.root === '/' && entries.filter(item => item.device === entry.device).length === 1;
  if (path !== real || !info.isDirectory() || info.uid !== uid || (info.mode & 0o777) !== 0o700 ||
      !mounted || fs.type !== 0x01021994 || !Number.isSafeInteger(fs.bsize * fs.blocks) ||
      fs.bsize * fs.blocks <= 0 || fs.bsize * fs.blocks > 256 * 1024 * 1024)
    throw new Error('Capture requires a private dedicated tmpfs mount of at most 256 MiB as TMPDIR');
  return path;
}

export function assertScratch() {
  const path = resolve(tmpdir());
  return validateScratch(path, realpathSync(path), lstatSync(path), statfsSync(path),
    readFileSync('/proc/self/mountinfo', 'utf8'), process.getuid());
}
