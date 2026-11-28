#!/usr/bin/env python3
"""Fixed VM framing and fail-closed behavior; no VM or host services are started."""
import hashlib
import importlib.util
import io
import os
from pathlib import Path
from unittest.mock import patch
import socket
import subprocess
import tempfile
import threading
import time
import unittest

ROOT = Path(__file__).resolve().parents[2]
def load(name, path):
    spec = importlib.util.spec_from_file_location(name, ROOT / path)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module
broker = load('broker', 'deploy/design-verifier/vm-broker.py')
guest = load('guest', 'deploy/design-verifier/vm-guest.py')
H = lambda b: hashlib.sha256(b).digest()


class TransportTests(unittest.TestCase):
    def setUp(self):
        # This fixture runs outside the protected service cgroup.
        self.cgroup = patch.object(broker, 'require_empty_job_cgroup', return_value=None)
        self.cgroup.start()
        self.addCleanup(self.cgroup.stop)

    def request(self, op, nonce, body=b'', digest=None):
        return broker.REQUEST.pack(b'FVMQ', 1, op, b'\0\0', nonce, len(body), digest or H(body)) + body

    def test_image_format_rejects_external_data_and_backing(self):
        import json
        valid = {'format': 'qcow2', 'virtual-size': 12 * 1024**3}
        broker.validate_image_info(json.dumps([valid]).encode())
        broker.validate_image_info(json.dumps([{**valid, 'virtual-size': 25 * 1024**3}]).encode())
        for image in ({**valid, 'backing-filename': '/other'}, {**valid, 'format':'raw'},
                      {**valid, 'format-specific': {'data': {'data-file':'/other'}}},
                      {**valid, 'virtual-size': 26 * 1024**3}):
            with self.assertRaises(ValueError):
                broker.validate_image_info(json.dumps([image]).encode())
        with self.assertRaises(ValueError):
            broker.validate_image_info(json.dumps([valid, valid]).encode())

    def test_guest_fixed_request_and_response(self):
        nonce = b'n' * 32
        source = io.BytesIO(self.request(1, nonce))
        op, got_nonce, digest, payload = guest.read_request(source, tempfile.gettempdir())
        self.assertEqual((op, got_nonce, digest, payload.read()), (1, nonce, H(b''), b''))
        payload.close()
        for raw in (self.request(1, nonce, b'x'), self.request(3, nonce, b'abc', b'0' * 32), self.request(1, nonce)[:-1]):
            with self.assertRaises((ValueError, EOFError)):
                guest.read_request(io.BytesIO(raw), tempfile.gettempdir())
        source = io.BytesIO(self.request(1, nonce))
        guest.serve(source, tempfile.gettempdir(), lambda op, data, root: (0, b'{"probe":true}'))
        source.seek(broker.REQUEST.size)
        response = guest.RESPONSE.unpack(source.read(guest.RESPONSE.size))
        self.assertEqual(response[3:8], (0, b'\0', nonce, H(b''), b'\0' * 32))
        self.assertEqual(source.read(), b'{"probe":true}')

    def test_authorized_socket_challenge_and_post_cleanup_reply(self):
        client, server = socket.socketpair()
        order = []
        def runner(c, op, nonce, digest, payload, fd, bios_fd, root):
            self.assertEqual(payload.read(), b'')
            order.extend(['job', 'shutdown', 'reap', 'cleanup'])
            return 0, b'{"ready":true}'
        def work():
            with server:
                broker.serve(server, lambda _client: (b'i' * 32, os.open('/dev/null', os.O_RDONLY), os.open('/dev/null', os.O_RDONLY)), runner, tempfile.gettempdir())
        thread = threading.Thread(target=work); thread.start()
        challenge = broker.recv_exact(client, 40, time.monotonic() + 3)
        self.assertEqual(challenge[:8], b'FVMB\x01\0\0\0')
        nonce = challenge[8:]
        client.sendall(self.request(1, nonce))
        header = broker.RESPONSE.unpack(broker.recv_exact(client, 144, time.monotonic() + 3))
        self.assertEqual(order, ['job', 'shutdown', 'reap', 'cleanup'])
        self.assertEqual((header[2], header[3], header[5], header[6], header[7]), (1, 0, nonce, H(b''), b'i' * 32))
        self.assertEqual(broker.recv_exact(client, header[8], time.monotonic() + 3), b'{"ready":true}')
        self.assertEqual(client.recv(1), b'')
        client.close(); thread.join(timeout=3); self.assertFalse(thread.is_alive())

    def test_real_js_client_and_python_broker_framing(self):
        module = Path(os.environ.get('T199_CLIENT_MODULE', str(ROOT / 'deploy/design-verifier/vm-backend.mjs')))
        if not module.is_file():
            self.skipTest('client lands from the parallel T199 branch')
        with tempfile.TemporaryDirectory() as root:
            path = str(Path(root) / 'broker.sock')
            listener = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
            listener.bind(path); listener.listen(1)
            errors = []
            def serve():
                try:
                    client, _ = listener.accept()
                    with client:
                        broker.serve(client, lambda _client: (b'i' * 32, os.open('/dev/null', os.O_RDONLY), os.open('/dev/null', os.O_RDONLY)),
                                     lambda *_: (0, b'{"probe":"fixture"}'), root)
                except BaseException as error:
                    errors.append(error)
            thread = threading.Thread(target=serve, daemon=True); thread.start()
            js = """import { createConnection } from 'node:net';
import { pathToFileURL } from 'node:url';
const { vmBackend } = await import(pathToFileURL(process.argv[1]));
const v = vmBackend({launchSha256: '69'.repeat(32)}, {connect:()=>createConnection(process.argv[2]),deadlineOverride:5000});
const result = await v.run('render', ['--probe']);
if (result.toString() !== '{\"probe\":\"fixture\"}') throw Error('unexpected reply');
"""
            try:
                completed = subprocess.run(['node', '--input-type=module', '-e', js, str(module), path],
                                           capture_output=True, timeout=8)
                self.assertEqual(completed.returncode, 0, completed.stderr.decode())
                thread.join(timeout=2)
                self.assertFalse(thread.is_alive())
                self.assertEqual(errors, [])
            finally:
                listener.close()

    def test_invalid_request_never_reaches_vm(self):
        for kind in ('nonce', 'hash', 'oversize', 'extra'):
            client, server = socket.socketpair()
            nonce = b'n' * 32
            request = self.request(1, b'x' * 32 if kind == 'nonce' else nonce,
                                   b'x' if kind == 'oversize' else b'', b'x' * 32 if kind == 'hash' else None)
            client.sendall(request + (b'bad' if kind == 'extra' else b''))
            with self.assertRaises(ValueError, msg=kind):
                broker.read_request(server, nonce, tempfile.gettempdir(), time.monotonic() + 1)
            client.close(); server.close()

    def test_guest_ready_is_required_before_job_clock(self):
        guest_sock, peer = socket.socketpair(); client, cpeer = socket.socketpair()
        class Process:
            def poll(self): return None
        try:
            peer.sendall(b'FVGR\x01\0\0\0')
            broker.wait_guest_ready(guest_sock, Process(), client, time.monotonic() + 1)
            peer.sendall(b'notready')
            with self.assertRaises(ValueError): broker.wait_guest_ready(guest_sock, Process(), client, time.monotonic() + 1)
            with self.assertRaises(TimeoutError): broker.wait_guest_ready(guest_sock, Process(), client, time.monotonic() + 0.02)
            cpeer.close()
            with self.assertRaises(EOFError): broker.wait_guest_ready(guest_sock, Process(), client, time.monotonic() + 1)
        finally:
            for sock in (guest_sock, peer, client, cpeer): sock.close()

    def test_guest_shutdown_event_required_and_response_bound(self):
        qmp, qpeer = socket.socketpair(); worker, wpeer = socket.socketpair(); client, cpeer = socket.socketpair()
        nonce = b'n' * 32; body = b'{"ok":true}'
        wpeer.sendall(broker.RESPONSE.pack(b'FVMR', 1, 1, 0, b'\0', nonce, H(b''), b'\0' * 32, len(body), H(body)) + body)
        qpeer.sendall(b'{"event":"SHUTDOWN","data":{"guest":true,"reason":"guest-shutdown"}}\n')
        class Process:
            returncode = 0
            def poll(self): return 0
        self.assertEqual(broker.wait_guest_response(worker, qmp, Process(), client, 1, nonce, H(b''), time.monotonic() + 2), (0, body))
        for s in (qmp, qpeer, worker, wpeer, client, cpeer): s.close()

    def test_guest_failure_and_spoofed_shutdown_do_not_pass(self):
        for kind in ('wrong-digest', 'reboot', 'guest-identity'):
            qmp, qpeer = socket.socketpair(); worker, wpeer = socket.socketpair(); client, cpeer = socket.socketpair()
            nonce = b'n' * 32; body = b'{}'
            digest = b'x' * 32 if kind == 'wrong-digest' else H(body)
            identity = b'x' * 32 if kind == 'guest-identity' else b'\0' * 32
            wpeer.sendall(broker.RESPONSE.pack(b'FVMR', 1, 1, 0, b'\0', nonce, H(b''), identity, len(body), digest) + body)
            reason = 'guest-reset' if kind == 'reboot' else 'guest-shutdown'
            qpeer.sendall(('{"event":"SHUTDOWN","data":{"guest":true,"reason":"' + reason + '"}}\n').encode())
            class Process:
                returncode = 0
                def poll(self): return 0
            with self.assertRaises(ValueError, msg=kind):
                broker.wait_guest_response(worker, qmp, Process(), client, 1, nonce, H(b''), time.monotonic() + 2)
            for s in (qmp, qpeer, worker, wpeer, client, cpeer): s.close()

    def test_disconnected_client_stops_wait_for_guest(self):
        listener = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
        client, peer = socket.socketpair(); peer.close()
        class Process:
            def poll(self): return None
        with self.assertRaises(EOFError):
            broker.await_guest(listener, Process(), client, time.monotonic() + 1)
        listener.close(); client.close()

    def test_unconfirmed_image_cleanup_leaves_durable_fence(self):
        preflight = load('preflight', 'deploy/design-verifier/vm-preflight.py')
        with tempfile.TemporaryDirectory() as root:
            with patch.object(broker, 'send_all'), \
                 patch.object(broker, 'read_request', return_value=(1, 0, H(b''), io.BytesIO())), \
                 patch.object(broker, 'load_manifest', side_effect=broker.CleanupUnconfirmed('helper could not be reaped')):
                with self.assertRaises(broker.CleanupUnconfirmed):
                    broker.serve(None, broker.load_manifest, root=root)
            self.assertEqual(len(list(Path(root).glob('run.fence.*'))), 1)
            with self.assertRaises(ValueError): preflight.check_clean_work(root)

    def test_run_directory_failure_keeps_fence_if_cgroup_cleanup_unconfirmed(self):
        real = tempfile.mkdtemp
        with tempfile.TemporaryDirectory() as root:
            def mkdir(*args, **kwargs):
                if kwargs.get('prefix') == 'run.': raise OSError('fixture mkdir failure')
                return real(*args, **kwargs)
            with patch.object(broker.tempfile, 'mkdtemp', side_effect=mkdir), \
                 patch.object(broker, 'send_all'), \
                 patch.object(broker, 'read_request', return_value=(1, 0, H(b''), io.BytesIO())), \
                 patch.object(broker, 'require_empty_job_cgroup', side_effect=RuntimeError('unconfirmed')):
                with self.assertRaises(broker.CleanupUnconfirmed):
                    broker.serve(None, lambda _: (b'i' * 32, os.open('/dev/null', os.O_RDONLY), os.open('/dev/null', os.O_RDONLY)), root=root)
            self.assertEqual(len(list(Path(root).glob('run.fence.*'))), 1)

    def test_image_helper_kill_failure_is_unconfirmed(self):
        class Helper:
            pid = 12345
            stdout = None
            def poll(self): return None
        with patch.object(broker.subprocess, 'Popen', return_value=Helper()), \
             patch.object(broker.time, 'sleep', side_effect=TimeoutError('fixture timeout')), \
             patch.object(broker.os, 'killpg', side_effect=PermissionError('fixture kill failure')):
            with self.assertRaises(broker.CleanupUnconfirmed):
                broker.run_checked(['fixture'], None, 5)

    def test_cleanup_failure_cannot_send_success(self):
        client, server = socket.socketpair(); called = []
        def runner(*args): called.append('cleanup failed'); raise RuntimeError('cleanup')
        def work():
            with server:
                try: broker.serve(server, lambda _client: (b'i' * 32, os.open('/dev/null', os.O_RDONLY), os.open('/dev/null', os.O_RDONLY)), runner, tempfile.gettempdir())
                except RuntimeError: pass
        thread = threading.Thread(target=work); thread.start()
        nonce = broker.recv_exact(client, 40, time.monotonic() + 3)[8:]
        client.sendall(self.request(1, nonce))
        self.assertEqual(client.recv(1), b'')
        self.assertEqual(called, ['cleanup failed'])
        client.close(); thread.join(timeout=3)


if __name__ == '__main__': unittest.main()
