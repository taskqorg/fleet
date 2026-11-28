#!/usr/bin/python3
"""Socket-activated, one-job-at-a-time renderer VM broker; no Fleet credentials."""
import hashlib
import json
import os
import re
from pathlib import Path
import select
import shutil
import signal
import socket
import stat
import struct
import subprocess
import sys
import tempfile
import time

REQUEST = struct.Struct('>4sBB2s32sQ32s')
RESPONSE = struct.Struct('>4sBBB1s32s32s32sQ32s')
LIMITS = {1: (0, 1024, 25), 2: (4 + 96 * 1024 + 96 * 1024 * 1024, 24 * 1024 * 1024, 60),
          3: (32 * 1024 * 1024, 32 * 1024 * 1024, 65), 4: (64 * 1024 * 1024 + 2048, 1024, 10)}
MANIFEST = '/app/launch.json'
GOLDEN = '/image/base.qcow2'
STATE = '/state/work'
QEMU = '/usr/bin/qemu-system-x86_64'
QEMU_IMG = '/usr/bin/qemu-img'
BIOS = '/usr/share/seabios/bios-256k.bin'
CHUNK = 65536


def digest_file(fd, client=None):
    h = hashlib.sha256()
    os.lseek(fd, 0, os.SEEK_SET)
    while data := os.read(fd, 1024 * 1024):
        if client is not None:
            client_alive(client)
        h.update(data)
    os.lseek(fd, 0, os.SEEK_SET)
    return h.hexdigest()


def trusted_file(path):
    fd = os.open(path, os.O_RDONLY | os.O_NOFOLLOW | os.O_CLOEXEC)
    info = os.fstat(fd)
    if not stat.S_ISREG(info.st_mode) or info.st_uid != 0 or info.st_mode & 0o022:
        os.close(fd)
        raise ValueError('untrusted operator file')
    return fd


def validate_image_info(raw):
    if len(raw) > 65536:
        raise ValueError('image metadata too large')
    chain = json.loads(raw)
    if not isinstance(chain, list) or len(chain) != 1:
        raise ValueError('image backing chain is not allowed')
    image = chain[0]
    if (image.get('format') != 'qcow2' or image.get('backing-filename') or
            image.get('format-specific', {}).get('data', {}).get('data-file') or
            not isinstance(image.get('virtual-size'), int) or not 0 < image['virtual-size'] <= 25 * 1024**3):
        raise ValueError('unsupported golden image format')


def run_checked(args, client, timeout, pass_fds=(), capture=False):
    process = subprocess.Popen(args, pass_fds=pass_fds, stdin=subprocess.DEVNULL,
                               stdout=subprocess.PIPE if capture else subprocess.DEVNULL,
                               stderr=subprocess.DEVNULL, start_new_session=True,
                               env={'PATH': '/usr/bin:/bin', 'HOME': '/nonexistent', 'LANG': 'C'})
    deadline = time.monotonic() + timeout
    output = bytearray()
    eof = not capture
    try:
        while True:
            if client is not None:
                client_alive(client)
            left = deadline - time.monotonic()
            if left <= 0:
                raise TimeoutError('image operation timed out')
            if not eof:
                ready, _, _ = select.select([process.stdout], [], [], min(left, 0.1))
                if ready:
                    part = os.read(process.stdout.fileno(), min(CHUNK, 65537 - len(output)))
                    if part:
                        output.extend(part)
                        if len(output) > 65536:
                            raise ValueError('image metadata too large')
                    else:
                        eof = True
            elif process.poll() is None:
                time.sleep(min(left, 0.1))
            if eof and process.poll() is not None:
                if process.returncode != 0:
                    raise subprocess.CalledProcessError(process.returncode, args)
                return bytes(output)
    finally:
        if process.poll() is None:
            os.killpg(process.pid, signal.SIGKILL)
            process.wait(timeout=5)
        if process.stdout is not None:
            process.stdout.close()


def validate_image_fd(fd, client=None):
    raw = run_checked([QEMU_IMG, 'info', '--backing-chain', '--output=json', '/proc/self/fd/' + str(fd)],
                      client, 30, pass_fds=(fd,), capture=True)
    validate_image_info(raw)


def load_manifest(client=None):
    # /app and /image are read-only binds inside the fixed systemd RootDirectory.
    fd = trusted_file(MANIFEST)
    try:
        raw = os.read(fd, 8193)
    finally:
        os.close(fd)
    if len(raw) > 8192:
        raise ValueError('launch manifest too large')
    def unique(pairs):
        result = {}
        for key, value in pairs:
            if key in result:
                raise ValueError('duplicate manifest key')
            result[key] = value
        return result
    obj = json.loads(raw, object_pairs_hook=unique)
    fields = {'version', 'goldenSha256', 'biosSha256', 'releaseSha256', 'guestWorkerSha256', 'runtimeSha256', 'releaseCommit'}
    if set(obj) != fields or obj['version'] != 1 or any(
            not isinstance(obj[key], str) or len(obj[key]) != 64 or any(c not in '0123456789abcdef' for c in obj[key])
            for key in fields - {'version', 'releaseCommit'}) or not isinstance(obj['releaseCommit'], str) or not re.fullmatch(r'[a-f0-9]{40}', obj['releaseCommit']):
        raise ValueError('invalid launch manifest')
    image_fd = trusted_file(GOLDEN)
    try:
        if digest_file(image_fd, client) != obj['goldenSha256']:
            raise ValueError('golden digest mismatch')
        validate_image_fd(image_fd, client)
        bios_fd = trusted_file(BIOS)
        try:
            if digest_file(bios_fd, client) != obj['biosSha256']:
                raise ValueError('firmware digest mismatch')
            return hashlib.sha256(raw).digest(), image_fd, bios_fd
        except BaseException:
            os.close(bios_fd)
            raise
    except BaseException:
        os.close(image_fd)
        raise


def recv_exact(sock, length, deadline):
    result = bytearray()
    while len(result) < length:
        remaining = deadline - time.monotonic()
        if remaining <= 0 or not select.select([sock], [], [], remaining)[0]:
            raise TimeoutError('frame deadline')
        part = sock.recv(min(CHUNK, length - len(result)))
        if not part:
            raise EOFError('truncated frame')
        result.extend(part)
    return bytes(result)


def read_request(client, nonce, root, deadline):
    magic, version, op, reserved, got_nonce, size, expected = REQUEST.unpack(recv_exact(client, REQUEST.size, deadline))
    if magic != b'FVMQ' or version != 1 or op not in LIMITS or reserved != b'\0\0' or got_nonce != nonce or size > LIMITS[op][0]:
        raise ValueError('invalid request')
    payload = tempfile.TemporaryFile(dir=root)
    try:
        h = hashlib.sha256()
        left = size
        while left:
            part = recv_exact(client, min(CHUNK, left), deadline)
            payload.write(part)
            h.update(part)
            left -= len(part)
        if h.digest() != expected:
            raise ValueError('request digest')
        # Any readable byte here is a second request or a disconnected client.
        if select.select([client], [], [], 0)[0]:
            raise ValueError('unexpected request continuation')
        payload.seek(0)
        return op, size, expected, payload
    except BaseException:
        payload.close()
        raise


def send_all(sock, data, deadline):
    view = memoryview(data)
    while view:
        left = deadline - time.monotonic()
        if left <= 0 or not select.select([], [sock], [], left)[1]:
            raise TimeoutError('send deadline')
        written = sock.send(view)
        if written <= 0:
            raise EOFError('send closed')
        view = view[written:]


def client_alive(client):
    if select.select([client], [], [], 0)[0]:
        raise EOFError('client disconnected or sent an extra frame')


def await_guest(listener, process, client, deadline):
    while True:
        client_alive(client)
        if process.poll() is not None:
            raise RuntimeError('VM exited before channel opened')
        left = deadline - time.monotonic()
        if left <= 0:
            raise TimeoutError('guest boot')
        if select.select([listener], [], [], min(left, 0.2))[0]:
            return listener.accept()[0]


# QEMU connecting its chardev is not guest readiness. Start the worker budget
# only after the fixed supervisor has opened its virtio port and announced ready.
def wait_guest_ready(guest, process, client, deadline):
    expected = b'FVGR\x01\0\0\0'
    received = bytearray()
    while len(received) < len(expected):
        client_alive(client)
        if process.poll() is not None:
            raise RuntimeError('VM exited before supervisor readiness')
        left = deadline - time.monotonic()
        if left <= 0:
            raise TimeoutError('guest supervisor readiness')
        ready, _, _ = select.select([guest], [], [], min(left, 0.2))
        if ready:
            part = guest.recv(len(expected) - len(received))
            if not part:
                raise EOFError('guest disconnected before readiness')
            received.extend(part)
    if bytes(received) != expected:
        raise ValueError('invalid guest supervisor readiness')


def connect_qmp(path, process, client, deadline):
    while True:
        client_alive(client)
        if process.poll() is not None:
            raise RuntimeError('VM exited before QMP opened')
        s = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
        try:
            s.connect(path)
            s.settimeout(3)
            return s
        except (FileNotFoundError, ConnectionRefusedError):
            s.close()
            if time.monotonic() >= deadline:
                raise TimeoutError('QMP unavailable')
            time.sleep(0.05)


def qmp_negotiate(qmp):
    # QMP emits newline-separated JSON. Greeting and capability reply only;
    # unexpected or oversized data fail closed.
    qmp_file = qmp.makefile('rb', buffering=0)
    def line():
        raw = qmp_file.readline(65537)
        if not raw or len(raw) > 65536:
            raise ValueError('invalid QMP')
        return json.loads(raw)
    if 'QMP' not in line():
        raise ValueError('missing QMP greeting')
    qmp.sendall(b'{"execute":"qmp_capabilities"}\r\n')
    if 'return' not in line():
        raise ValueError('QMP negotiation failed')
    qmp.setblocking(False)
    return qmp_file


def wait_guest_response(guest, qmp, process, client, op, nonce, request_hash, deadline):
    # Buffer limits apply before accepting a guest-declared length.
    qmp_buffer = bytearray()
    response = bytearray()
    wanted = RESPONSE.size
    header = None
    shutdown = False
    worker_done = False
    while True:
        client_alive(client)
        left = deadline - time.monotonic()
        if left <= 0:
            raise TimeoutError('guest or shutdown deadline')
        readable = [qmp]
        if len(response) < wanted:
            readable.append(guest)
        ready, _, _ = select.select(readable, [], [], min(left, 0.2))
        if qmp in ready:
            part = qmp.recv(CHUNK)
            if part:
                qmp_buffer.extend(part)
                if len(qmp_buffer) > 65536:
                    raise ValueError('QMP buffer limit')
                while b'\n' in qmp_buffer:
                    line, _, tail = qmp_buffer.partition(b'\n')
                    qmp_buffer = bytearray(tail)
                    event = json.loads(line)
                    if event.get('event') == 'SHUTDOWN':
                        data = event.get('data', {})
                        if data.get('guest') is not True or data.get('reason') != 'guest-shutdown':
                            raise ValueError('unexpected QMP shutdown')
                        shutdown = True
            elif not shutdown:
                raise EOFError('QMP disconnected before shutdown')
        if guest in ready:
            part = guest.recv(min(CHUNK, wanted - len(response)))
            if not part:
                raise EOFError('guest response truncated')
            response.extend(part)
            if header is None and len(response) == RESPONSE.size:
                header = RESPONSE.unpack(response)
                magic, version, got_op, status, reserved, got_nonce, got_hash, guest_identity, size, expected = header
                if (magic != b'FVMR' or version != 1 or got_op != op or status not in (0, 1, 2) or
                        status == 1 and op != 2 or reserved != b'\0' or got_nonce != nonce or got_hash != request_hash or
                        guest_identity != b'\0' * 32 or size > LIMITS[op][1] or status != 0 and size):
                    raise ValueError('invalid guest response')
                wanted = RESPONSE.size + size
        if header is not None and len(response) == wanted and not worker_done:
            worker_done = True
            deadline = time.monotonic() + 30
        if header is not None and len(response) == wanted and shutdown and process.poll() is not None:
            if select.select([guest], [], [], 0)[0] and guest.recv(1):
                raise ValueError('guest sent extra response bytes')
            if process.returncode != 0:
                raise RuntimeError('VM exit was not clean')
            body = bytes(response[RESPONSE.size:])
            if hashlib.sha256(body).digest() != header[-1]:
                raise ValueError('guest response digest')
            return header[3], body


def require_empty_job_cgroup():
    group = Path('/proc/self/cgroup').read_text().strip()
    if not group.startswith('0::/') or '..' in group.split('/'):
        raise RuntimeError('unified cgroup unavailable')
    procs = Path('/sys/fs/cgroup') / group[4:] / 'cgroup.procs'
    members = {int(value) for value in procs.read_text().split()}
    if members != {os.getpid()}:
        raise RuntimeError('job cgroup still has other processes')


def run_vm(client, op, nonce, request_hash, payload, launch_fd, bios_fd, root):
    run = Path(tempfile.mkdtemp(prefix='run.', dir=root))
    overlay = run / 'overlay.qcow2'
    guest_path, qmp_path = str(run / 'guest.sock'), str(run / 'qmp.sock')
    process = guest = listener = qmp = qmp_file = None
    try:
        listener = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
        listener.bind(guest_path)
        os.chmod(guest_path, 0o600)
        listener.listen(1)
        backing = '/proc/self/fd/' + str(launch_fd)
        run_checked([QEMU_IMG, 'create', '-q', '-f', 'qcow2', '-F', 'qcow2', '-b', backing, str(overlay)],
                    client, 30, pass_fds=(launch_fd,))
        client_alive(client)
        args = [QEMU, '-no-user-config', '-nodefaults', '-machine', 'pc,accel=kvm', '-cpu', 'host',
                '-bios', '/proc/self/fd/' + str(bios_fd), '-boot', 'order=c', '-smp', '2', '-m', '1280', '-display', 'none',
                '-monitor', 'none', '-serial', 'none', '-nic', 'none', '-no-reboot',
                '-qmp', 'unix:' + qmp_path + ',server=on,wait=off',
                '-chardev', 'socket,id=guest,path=' + guest_path,
                '-device', 'virtio-serial-pci', '-device', 'virtserialport,chardev=guest,name=org.fleet.design-verifier',
                '-drive', 'file=' + str(overlay) + ',format=qcow2,if=virtio,cache=none,rerror=report,werror=report']
        process = subprocess.Popen(args, stdin=subprocess.DEVNULL, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL,
                                   pass_fds=(launch_fd, bios_fd), close_fds=True, start_new_session=True,
                                   env={'PATH': '/usr/bin:/bin', 'HOME': '/nonexistent', 'LANG': 'C'})
        boot_deadline = time.monotonic() + 90
        qmp = connect_qmp(qmp_path, process, client, boot_deadline)
        qmp_file = qmp_negotiate(qmp)
        guest = await_guest(listener, process, client, boot_deadline)
        guest.setblocking(False)
        wait_guest_ready(guest, process, client, boot_deadline)
        send_deadline = time.monotonic() + 10
        size = os.fstat(payload.fileno()).st_size
        send_all(guest, REQUEST.pack(b'FVMQ', 1, op, b'\0\0', nonce, size, request_hash), send_deadline)
        while part := payload.read(CHUNK):
            client_alive(client)
            send_all(guest, part, send_deadline)
        return wait_guest_response(guest, qmp, process, client, op, nonce, request_hash,
                                   time.monotonic() + LIMITS[op][2] + 5)
    finally:
        cleanup_failed = False
        try:
            if process is not None and process.poll() is None:
                try:
                    os.killpg(process.pid, signal.SIGKILL)
                except ProcessLookupError:
                    pass
                process.wait(timeout=10)
            if process is not None:
                try:
                    os.killpg(process.pid, 0)
                except ProcessLookupError:
                    pass
                else:
                    cleanup_failed = True
        except (OSError, subprocess.SubprocessError):
            cleanup_failed = True
        for handle in (qmp_file, guest, listener, qmp):
            if handle is not None:
                try:
                    handle.close()
                except OSError:
                    cleanup_failed = True
        if not cleanup_failed:
            try:
                shutil.rmtree(run)
            except OSError:
                cleanup_failed = True
        if cleanup_failed or run.exists():
            raise RuntimeError('VM cleanup incomplete')
        require_empty_job_cgroup()


def serve(client, manifest_loader=load_manifest, runner=run_vm, root=STATE):
    nonce = os.urandom(32)
    send_all(client, b'FVMB\x01\0\0\0' + nonce, time.monotonic() + 5)
    op, _, request_hash, payload = read_request(client, nonce, root, time.monotonic() + 10)
    launch_fd = bios_fd = None
    try:
        identity, launch_fd, bios_fd = manifest_loader(client)
        client_alive(client)
        status, result = runner(client, op, nonce, request_hash, payload, launch_fd, bios_fd, root)
        if status not in (0, 1, 2) or status == 1 and op != 2 or status != 0 and result or len(result) > LIMITS[op][1]:
            raise ValueError('invalid worker result')
        # runner must return only after successful QMP shutdown, reap and cleanup.
        response = RESPONSE.pack(b'FVMR', 1, op, status, b'\0', nonce, request_hash, identity,
                                 len(result), hashlib.sha256(result).digest())
        send_all(client, response, time.monotonic() + 5)
        send_all(client, result, time.monotonic() + 5)
    finally:
        payload.close()
        if launch_fd is not None:
            os.close(launch_fd)
        if bios_fd is not None:
            os.close(bios_fd)


class StopRequested(Exception):
    pass


def stop_requested(_signum, _frame):
    signal.signal(signal.SIGTERM, signal.SIG_IGN)
    signal.signal(signal.SIGINT, signal.SIG_IGN)
    raise StopRequested('broker stopping')


def main():
    signal.signal(signal.SIGTERM, stop_requested)
    signal.signal(signal.SIGINT, stop_requested)
    os.umask(0o077)
    if os.environ.get('LISTEN_PID') != str(os.getpid()) or os.environ.get('LISTEN_FDS') != '1':
        raise RuntimeError('expected exactly one systemd-owned listener')
    listener = socket.socket(fileno=3)
    if listener.family != socket.AF_UNIX or listener.type & socket.SOCK_STREAM != socket.SOCK_STREAM:
        raise RuntimeError('invalid listener')
    while True:
        try:
            client, _ = listener.accept()
            client.setblocking(False)
            with client:
                serve(client)
        except StopRequested:
            break
        except (OSError, ValueError, EOFError, TimeoutError, RuntimeError, subprocess.SubprocessError, json.JSONDecodeError) as error:
            print('renderer job unconfirmed: ' + type(error).__name__, file=sys.stderr, flush=True)


if __name__ == '__main__':
    main()
