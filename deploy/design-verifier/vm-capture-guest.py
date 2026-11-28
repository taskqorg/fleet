#!/usr/bin/python3
"""Stage one fixed capture job inside the already isolated guest."""
import hashlib
import os
from pathlib import Path
import pwd
import shutil
import stat
import subprocess

STATE = Path('/var/lib/fleet-design-capture-job')
GUEST_WORKER = 'fleet-render-worker'
UNIT = 'fleet-design-capture-worker.service'
RELEASE = '/opt/fleet-design-verifier/current/deploy/design-verifier'
OUTPUT_FILES = ('receipt.json', 'capture.tar')


def fixed_job(schema, read_exact, source, state=STATE, *, runner=None, worker=None):
    if worker is None:
        worker = pwd.getpwnam(GUEST_WORKER)
    if runner is None:
        runner = start_worker
    if list(state.iterdir()):
        raise ValueError('capture guest allocation contains leftovers')
    work = state / 'job'
    work.mkdir(mode=0o750)
    try:
        os.chown(state, 0, worker.pw_gid)
        state.chmod(0o750)
        os.chown(work, 0, worker.pw_gid)
        work.chmod(0o750)
        incoming = work / 'input'
        incoming.mkdir(mode=0o750)
        os.chown(incoming, 0, worker.pw_gid)
        incoming.chmod(0o750)
        def read(size):
            return read_exact(source, size)
        request_raw = read(schema.frame_size(read, schema.FILES[0][1]))
        request = schema.request_of(request_raw)
        for name, maximum, pin in schema.FILES:
            with (incoming / name).open('xb') as file:
                os.fchown(file.fileno(), 0, worker.pw_gid)
                os.fchmod(file.fileno(), 0o440)
                if pin is None:
                    file.write(request_raw)
                    continue
                left = schema.frame_size(read, maximum)
                digest = hashlib.sha256()
                while left:
                    part = read(min(left, schema.CHUNK))
                    file.write(part)
                    digest.update(part)
                    left -= len(part)
                if digest.hexdigest() != request[pin]:
                    raise ValueError('guest capture input digest differs')
        if source.read(1):
            raise ValueError('extra guest capture input')
        writable = work / 'results'
        writable.mkdir(mode=0o700)
        os.chown(writable, worker.pw_uid, worker.pw_gid)
        runner()
        if set(os.listdir(writable)) != {'output'}:
            raise ValueError('unexpected guest capture output directory')
        out_fd = os.open(writable / 'output', os.O_DIRECTORY | os.O_NOFOLLOW | os.O_RDONLY)
        try:
            if set(os.listdir(out_fd)) != set(OUTPUT_FILES):
                raise ValueError('unexpected guest capture output')
            receipt, archive = (read_output(out_fd, n, max_bytes, worker.pw_uid) for n, max_bytes in
                                zip(OUTPUT_FILES, (schema.RECEIPT_MAX, schema.ARCHIVE_MAX)))
            result = schema.REPLY.pack(b'FDR1', len(receipt), len(archive)) + receipt + archive
        finally:
            os.close(out_fd)
    finally:
        shutil.rmtree(work)
    if list(state.iterdir()):
        raise ValueError('capture guest cleanup left files')
    return 0, result


def read_output(directory, name, maximum, worker_uid):
    fd = os.open(name, os.O_NOFOLLOW | os.O_NONBLOCK | os.O_RDONLY, dir_fd=directory)
    with os.fdopen(fd, 'rb') as file:
        st = os.fstat(fd)
        if not stat.S_ISREG(st.st_mode) or st.st_nlink != 1 or st.st_uid != worker_uid or not 0 < st.st_size <= maximum:
            raise ValueError('unsafe guest output')
        data = file.read(st.st_size + 1)
        if len(data) != st.st_size:
            raise ValueError('guest output length changed')
        return data


def start_worker():
    subprocess.run(['/usr/bin/systemctl', 'start', UNIT], check=True, timeout=605,
                   stdin=subprocess.DEVNULL, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL,
                   env={'PATH': '/usr/bin:/bin', 'HOME': '/nonexistent', 'LANG': 'C'})


def run_worker():
    worker = pwd.getpwnam('fleet-render-worker')
    if worker.pw_uid == 0 or worker.pw_gid == 0:
        raise ValueError('capture requires a dedicated unprivileged renderer user')
    # The unit mounts this private tmpfs. Only its root inode needs ownership
    # transferred before the final uid/gid switch. No mount privilege is used.
    os.chown('/tmp', worker.pw_uid, worker.pw_gid)
    os.setgroups([])
    os.setgid(worker.pw_gid)
    os.setuid(worker.pw_uid)
    job = str(STATE / 'job')
    os.execve('/usr/bin/node', ['/usr/bin/node', RELEASE + '/designer.mjs', 'capture-job',
                              '--job-input', job + '/input', '--out', job + '/results/output'],
              {'PATH': '/usr/bin:/bin', 'HOME': '/nonexistent', 'LANG': 'C.UTF-8', 'TMPDIR': '/tmp'})


if __name__ == '__main__':
    run_worker()
