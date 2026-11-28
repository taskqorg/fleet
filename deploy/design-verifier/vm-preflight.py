#!/usr/bin/python3
"""Read-only admission for one manually started, credential-free render VM."""
import json
import os
from pathlib import Path
import re
import stat
import subprocess
import sys

STATE = Path('/state')
IMAGE = Path('/image/base.qcow2')
VOLUME = '/var/lib/fleet-render-vm-volume.img'
VOLUME_BYTES = 6 * 1024**3
DIGEST = re.compile(r'^[0-9a-f]{64}$')


def check_mount(info, backing, sectors, total):
    fs = info.get('filesystems', [])
    if len(fs) != 1:
        raise ValueError('state must be one mounted filesystem')
    mount = fs[0]
    if mount.get('target') != '/state' or mount.get('fstype') != 'ext4' or not re.fullmatch(r'/dev/loop[0-9]+(?:\[/\])?', mount.get('source', '')):
        raise ValueError('state must be the dedicated ext4 loop mount')
    if not re.fullmatch(r'[0-9]+:[0-9]+', mount.get('maj:min', '')):
        raise ValueError('loop device identity missing')
    if backing.strip().lstrip('/') != VOLUME.lstrip('/') or int(sectors) * 512 != VOLUME_BYTES or not 0 < total <= VOLUME_BYTES:
        raise ValueError('state loop backing or size differs from the fixed volume')
    return mount['maj:min']


def check_io(text, devices):
    limits = {}
    for line in text.splitlines():
        parts = line.split()
        if not parts:
            continue
        limits[parts[0]] = dict(part.split('=', 1) for part in parts[1:] if '=' in part)
    for device in devices:
        item = limits.get(device, {})
        for key, maximum in [('rbps', 40 * 1024**2), ('wbps', 20 * 1024**2)]:
            value = item.get(key, '')
            if not value.isdecimal() or not 0 < int(value) <= maximum:
                raise ValueError('I/O limit missing for device ' + device)


def check_limits(memory, high, swap, cpu, pids):
    def bounded(value, maximum):
        return value.isdecimal() and 0 < int(value) <= maximum
    if not bounded(memory.strip(), 2 * 1024**3) or not bounded(high.strip(), 1600 * 1024**2):
        raise ValueError('memory cgroup limits missing')
    if swap.strip() != '0' or not bounded(pids.strip(), 128):
        raise ValueError('swap or PID cgroup limit missing')
    parts = cpu.split()
    if len(parts) != 2 or not all(part.isdecimal() and int(part) > 0 for part in parts) or int(parts[0]) > 2 * int(parts[1]):
        raise ValueError('CPU cgroup limit missing')


def check_clean_work(path):
    with os.scandir(path) as entries:
        if any(entry.name.startswith('run.') for entry in entries):
            raise ValueError('stale per-boot work needs operator review')


def main():
    for hidden in ['/run', '/etc', '/home', '/root']:
        try:
            with os.scandir(hidden) as entries:
                if any(entries):
                    raise ValueError('unexpected contents in isolated ' + hidden)
        except PermissionError:
            if hidden not in ['/home', '/root']:
                raise
    with os.scandir('/var') as entries:
        if any(entry.name != 'tmp' for entry in entries):
            raise ValueError('unexpected contents in isolated /var')
    sockets = subprocess.run(['/usr/bin/find', '/usr', '/state', '-xdev', '-type', 's', '-print', '-quit'],
                             check=True, stdout=subprocess.PIPE, stderr=subprocess.DEVNULL, timeout=60)
    if sockets.stdout:
        raise ValueError('runtime or state contains a Unix socket')
    image_hash = os.environ.get('IMAGE_SHA256', '')
    release_hash = os.environ.get('RELEASE_SHA256', '')
    probe_hash = os.environ.get('PROBE_SHA256', '')
    if not all(DIGEST.fullmatch(v) for v in [image_hash, release_hash, probe_hash]):
        raise ValueError('image, release and probe hashes are required')
    image_stat = IMAGE.lstat()
    if not stat.S_ISREG(image_stat.st_mode) or image_stat.st_uid != 0 or image_stat.st_mode & 0o022 or image_stat.st_size > 6 * 1024**3:
        raise ValueError('invalid golden image')
    # Broker validates the opened image digest and qcow2 chain on the same FD it launches.
    result = subprocess.run(['/usr/bin/findmnt', '--json', '--target', str(STATE), '--output', 'TARGET,FSTYPE,SOURCE,MAJ:MIN'],
                            check=True, stdout=subprocess.PIPE, stderr=subprocess.DEVNULL, timeout=5)
    info = json.loads(result.stdout)
    backing = Path('/volume/backing.img').lstat()
    if not stat.S_ISREG(backing.st_mode) or backing.st_uid != 0 or backing.st_mode & 0o077 or backing.st_size != VOLUME_BYTES or backing.st_blocks * 512 < VOLUME_BYTES:
        raise ValueError('bounded volume backing is not root-owned and fully allocated')
    device = info['filesystems'][0]['maj:min']
    if not re.fullmatch(r'[0-9]+:[0-9]+', device):
        raise ValueError('invalid device identity')
    loop = Path('/sys/dev/block') / device
    fs = os.statvfs(STATE)
    loop_id = check_mount(info, (loop / 'loop/backing_file').read_text(), (loop / 'size').read_text(), fs.f_frsize * fs.f_blocks)
    if fs.f_bavail <= 0:
        raise ValueError('bounded state volume has no free blocks')
    if STATE.stat().st_uid != os.getuid():
        raise ValueError('bounded state has the wrong owner')
    for name in ['work', 'tmp', 'vartmp']:
        path = STATE / name
        info = path.lstat()
        if not stat.S_ISDIR(info.st_mode) or info.st_uid != os.getuid() or info.st_mode & 0o077 or info.st_dev != STATE.stat().st_dev or not os.access(path, os.W_OK):
            raise ValueError('work and temporary paths must be writable on the bounded volume')
    check_clean_work(STATE / 'work')
    if not os.access('/dev/kvm', os.R_OK | os.W_OK):
        raise ValueError('KVM device unavailable')
    cgroup = Path('/proc/self/cgroup').read_text().strip()
    if not cgroup.startswith('0::/') or '..' in cgroup.split('/'):
        raise ValueError('unified cgroup unavailable')
    group = Path('/sys/fs/cgroup') / cgroup[4:]
    check_limits(*[(group / name).read_text() for name in ['memory.max', 'memory.high', 'memory.swap.max', 'cpu.max', 'pids.max']])
    check_io((group / 'io.max').read_text(), {loop_id, f'{os.major(image_stat.st_dev)}:{os.minor(image_stat.st_dev)}', f'{os.major(backing.st_dev)}:{os.minor(backing.st_dev)}'})
    print('preflight passed: exact loop mount, image and effective I/O limits')


if __name__ == '__main__':
    try:
        main()
    except (OSError, ValueError, KeyError, IndexError, TypeError, subprocess.SubprocessError, json.JSONDecodeError) as error:
        print('preflight failed: ' + str(error), file=sys.stderr)
        sys.exit(1)
