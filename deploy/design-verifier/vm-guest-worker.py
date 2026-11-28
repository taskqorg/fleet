#!/usr/bin/python3
"""Serve the root supervisor once from a static unprivileged guest unit."""
import os
from pathlib import Path
import pwd
import selectors
import signal
import socket
import struct
import subprocess
import time

RELEASE = Path('/opt/fleet-design-verifier/current/deploy/design-verifier')
STATE = Path('/var/lib/fleet-design-capture-job/job')
REPLY = struct.Struct('>BQ')
LIMITS = {1: (1024, 25), 2: (24 << 20, 60), 3: (32 << 20, 65), 4: (1024, 10)}
ENV = {'PATH': '/usr/bin:/bin', 'HOME': '/nonexistent', 'LANG': 'C.UTF-8', 'TMPDIR': '/tmp'}


def run_job(op, client):
    if op == 5:
        if client.recv(1):
            raise ValueError('capture accepts only its fixed staged input')
        args = ['/usr/bin/node', str(RELEASE / 'designer.mjs'), 'capture-job', '--job-input',
                str(STATE / 'input'), '--out', str(STATE / 'results' / 'output')]
        result = subprocess.run(args, stdin=subprocess.DEVNULL, stdout=subprocess.DEVNULL,
                                stderr=subprocess.DEVNULL, env=ENV, timeout=595)
        return (0 if result.returncode == 0 else 2), b''
    if op not in LIMITS:
        raise ValueError('unknown protected worker operation')
    maximum, seconds = LIMITS[op]
    args = ['/usr/bin/node', str(RELEASE / 'vm-guest-worker.mjs'), str(op)]
    with subprocess.Popen(args, stdin=client, stdout=subprocess.PIPE, stderr=subprocess.DEVNULL,
                          env=ENV, start_new_session=True) as process:
        output = bytearray()
        deadline = time.monotonic() + seconds + 2
        try:
            with selectors.DefaultSelector() as selector:
                selector.register(process.stdout, selectors.EVENT_READ)
                while True:
                    left = deadline - time.monotonic()
                    if left <= 0 or not selector.select(left):
                        raise TimeoutError('worker watchdog')
                    part = os.read(process.stdout.fileno(), min(65536, maximum + 1 - len(output)))
                    if not part:
                        break
                    output.extend(part)
                    if len(output) > maximum:
                        raise ValueError('worker output limit')
            code = process.wait(timeout=max(0.01, deadline - time.monotonic()))
            if code == 42 and op == 2:
                return 1, b''
            return (0, bytes(output)) if code == 0 else (2, b'')
        finally:
            if process.poll() is None:
                os.killpg(process.pid, signal.SIGKILL)
                process.wait(timeout=5)


def serve(listener, runner=run_job):
    with listener.accept()[0] as client:
        _, uid, _ = struct.unpack('3i', client.getsockopt(socket.SOL_SOCKET, socket.SO_PEERCRED, 12))
        if uid != 0:
            raise ValueError('untrusted guest supervisor')
        client.settimeout(610)
        client.sendall(b'FVWK\x01\0\0\0')
        operation = client.recv(1)
        if len(operation) != 1:
            raise ValueError('missing fixed operation')
        # Passing the descriptor as stdin requires blocking I/O in Node.
        client.setblocking(True)
        try:
            status, output = runner(operation[0], client)
        except (OSError, EOFError, ValueError, subprocess.SubprocessError):
            status, output = 2, b''
        client.settimeout(5)
        client.sendall(REPLY.pack(status, len(output)))
        client.sendall(output)


def main():
    user = pwd.getpwnam('fleet-render-worker')
    if (os.getuid(), os.getgid()) != (user.pw_uid, user.pw_gid) or user.pw_uid == 0 or user.pw_gid == 0:
        raise ValueError('worker must start as its dedicated unprivileged account')
    if os.environ.get('LISTEN_PID') != str(os.getpid()) or os.environ.get('LISTEN_FDS') != '1':
        raise ValueError('missing fixed systemd socket')
    with socket.socket(fileno=3) as listener:
        if listener.family != socket.AF_UNIX or listener.getsockname() != '/run/fleet-design-vm-worker.sock':
            raise ValueError('wrong fixed guest socket')
        serve(listener)


if __name__ == '__main__':
    main()
