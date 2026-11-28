#!/usr/bin/python3
"""One fixed job on the private virtio port. No host paths or commands from input."""
import hashlib
import importlib.util
from pathlib import Path
import os
import socket
import struct
import tempfile

REQUEST = struct.Struct('>4sBB2s32sQ32s')
RESPONSE = struct.Struct('>4sBBB1s32s32s32sQ32s')
LIMITS = {1: (0, 1024, 25), 2: (4 + 96 * 1024 + 96 * 1024 * 1024, 24 * 1024 * 1024, 60),
          3: (32 * 1024 * 1024, 32 * 1024 * 1024, 65), 4: (64 * 1024 * 1024 + 2048, 1024, 10)}
PORT = '/dev/virtio-ports/org.fleet.design-verifier'
WORKER_SOCKET = '/run/fleet-design-vm-worker.sock'
WORKER_REPLY = struct.Struct('>BQ')
WORKDIR = '/var/lib/fleet-design-vm-guest'
CHUNK = 65536


def load_sibling(name):
    spec = importlib.util.spec_from_file_location(name, Path(__file__).with_name(name + '.py'))
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


capture = load_sibling('vm-capture')


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
    maximum = capture.INPUT_MAX if op == capture.OPERATION else LIMITS.get(op, (-1,))[0]
    if magic != b'FVMQ' or version != 1 or reserved != b'\0\0' or size > maximum:
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


def worker_job(client, op, payload, maximum, seconds):
    client.settimeout(seconds)
    client.sendall(bytes([op]))
    if payload is not None:
        while chunk := payload.read(CHUNK):
            client.sendall(chunk)
    client.shutdown(socket.SHUT_WR)
    with client.makefile('rb') as reply:
        status, size = WORKER_REPLY.unpack(read_exact(reply, WORKER_REPLY.size))
        if status not in (0, 1, 2) or size > maximum or status and size:
            raise ValueError('invalid guest worker result')
        data = read_exact(reply, size)
        if reply.read(1):
            raise ValueError('extra guest worker result')
        return status, data


def run_job(client, op, payload, tempdir):
    try:
        if op == capture.OPERATION:
            def start_capture_worker():
                if worker_job(client, op, None, 0, 605) != (0, b''):
                    raise ValueError('fixed capture worker did not complete')
            return load_sibling('vm-capture-guest').fixed_job(capture, read_exact, payload, runner=start_capture_worker)
        _, maximum, seconds = LIMITS[op]
        return worker_job(client, op, payload, maximum, seconds + 3)
    except (OSError, EOFError, ValueError):
        return 2, b''


def serve(stream, tempdir, runner):
    op, nonce, request_hash, payload = read_request(stream, tempdir)
    try:
        status, result = runner(op, payload, tempdir)
    finally:
        payload.close()
    maximum = capture.OUTPUT_MAX if op == capture.OPERATION else LIMITS[op][1]
    if status not in (0, 1, 2) or status == 1 and op != 2 or status != 0 and result or len(result) > maximum:
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
    with socket.socket(socket.AF_UNIX, socket.SOCK_STREAM) as worker:
        worker.settimeout(5)
        worker.connect(WORKER_SOCKET)
        with worker.makefile('rb', buffering=0) as ready:
            if read_exact(ready, 8) != b'FVWK\x01\0\0\0':
                raise ValueError('fixed worker did not confirm readiness')
        with open(PORT, 'r+b', buffering=0) as stream:
            if stream.write(b'FVGR\x01\0\0\0') != 8:
                raise EOFError('guest readiness write failed')
            stream.flush()
            serve(stream, WORKDIR, lambda op, payload, tempdir: run_job(worker, op, payload, tempdir))
    # PID 1 powers off the guest on either terminal unit outcome.


if __name__ == '__main__':
    main()
