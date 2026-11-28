#!/usr/bin/python3
"""One fixed job on the private virtio port. No host paths or commands from input."""
import hashlib
import os
import resource
import selectors
import time
import signal
import struct
import subprocess
import tempfile

REQUEST = struct.Struct('>4sBB2s32sQ32s')
RESPONSE = struct.Struct('>4sBBB1s32s32s32sQ32s')
LIMITS = {1: (0, 1024, 25), 2: (4 + 96 * 1024 + 96 * 1024 * 1024, 24 * 1024 * 1024, 60),
          3: (32 * 1024 * 1024, 32 * 1024 * 1024, 65), 4: (64 * 1024 * 1024 + 2048, 1024, 10)}
PORT = '/dev/virtio-ports/org.fleet.design-verifier'
WORKER = '/opt/fleet-design-verifier/current/deploy/design-verifier/vm-guest-worker.mjs'
WORKDIR = '/var/lib/fleet-design-vm-guest'
CHUNK = 65536


def read_exact(stream, n):
    data = bytearray()
    while len(data) < n:
        part = stream.read(n - len(data))
        if not part:
            raise EOFError('truncated frame')
        data.extend(part)
    return bytes(data)


def read_request(stream, tempdir):
    magic, version, op, reserved, nonce, size, expected = REQUEST.unpack(read_exact(stream, REQUEST.size))
    if magic != b'FVMQ' or version != 1 or reserved != b'\0\0' or op not in LIMITS or size > LIMITS[op][0]:
        raise ValueError('invalid request')
    hasher = hashlib.sha256()
    data = tempfile.TemporaryFile(dir=tempdir)
    try:
        left = size
        while left:
            part = read_exact(stream, min(CHUNK, left))
            data.write(part)
            hasher.update(part)
            left -= len(part)
        if hasher.digest() != expected:
            raise ValueError('request digest mismatch')
        data.seek(0)
        return op, nonce, expected, data
    except BaseException:
        data.close()
        raise


def run_job(op, payload, tempdir, worker=WORKER):
    _, maximum, seconds = LIMITS[op]
    process = None
    def limits():
        os.setsid()
        resource.setrlimit(resource.RLIMIT_CORE, (0, 0))
    try:
        process = subprocess.Popen(['/usr/sbin/runuser', '-u', 'fleet-render-worker', '--', '/usr/bin/node', worker, str(op)],
            stdin=payload, stdout=subprocess.PIPE, stderr=subprocess.DEVNULL, close_fds=True,
            env={'PATH': '/usr/bin:/bin', 'HOME': '/nonexistent', 'LANG': 'C.UTF-8'}, preexec_fn=limits)
        output = bytearray()
        deadline = time.monotonic() + seconds + 3
        with selectors.DefaultSelector() as selector:
            selector.register(process.stdout, selectors.EVENT_READ)
            while selector.get_map():
                left = deadline - time.monotonic()
                if left <= 0 or not selector.select(left):
                    raise TimeoutError('worker watchdog')
                part = os.read(process.stdout.fileno(), min(CHUNK, maximum + 1 - len(output)))
                if not part:
                    selector.unregister(process.stdout)
                else:
                    output.extend(part)
                    if len(output) > maximum:
                        raise ValueError('worker output limit')
        code = process.wait(timeout=max(0.01, deadline - time.monotonic()))
        if code == 42 and op == 2:
            return 1, b''
        if code != 0:
            return 2, b''
        return 0, bytes(output)
    except (TimeoutError, ValueError, subprocess.TimeoutExpired):
        return 2, b''
    finally:
        if process is not None and process.poll() is None:
            os.killpg(process.pid, signal.SIGKILL)
            process.wait(timeout=5)
        if process is not None:
            process.stdout.close()


def serve(stream, tempdir, runner=run_job):
    op, nonce, request_hash, payload = read_request(stream, tempdir)
    try:
        status, result = runner(op, payload, tempdir)
    finally:
        payload.close()
    if status not in (0, 1, 2) or status == 1 and op != 2 or status != 0 and result or len(result) > LIMITS[op][1]:
        raise ValueError('invalid fixed worker result')
    # Launch identity is host-owned. The guest must never supply it.
    header = RESPONSE.pack(b'FVMR', 1, op, status, b'\0', nonce, request_hash, b'\0' * 32,
                           len(result), hashlib.sha256(result).digest())
    for data in (header, result):
        view = memoryview(data)
        while view:
            count = stream.write(view)
            if not count:
                raise EOFError('guest response write failed')
            view = view[count:]
    stream.flush()


def main():
    os.umask(0o077)
    os.makedirs(WORKDIR, mode=0o700, exist_ok=True)
    try:
        with open(PORT, 'r+b', buffering=0) as stream:
            serve(stream, WORKDIR)
    finally:
        # The host requires QMP guest-shutdown and QEMU exit before accepting.
        subprocess.run(['/usr/bin/systemctl', 'poweroff'], check=False, timeout=10,
                       stdin=subprocess.DEVNULL, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)


if __name__ == '__main__':
    main()
