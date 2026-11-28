#!/usr/bin/env python3
"""Exercise the real two protocols with an invented signing key and simulated VM events."""
from contextlib import ExitStack
import hashlib
import importlib.util
import io
import os
from pathlib import Path
import select
import signal
import socket
import struct
import subprocess
import tempfile
import threading
import time
import unittest
from unittest.mock import patch

ROOT = Path(__file__).resolve().parents[2] / 'deploy/design-verifier'


def load(name):
    spec = importlib.util.spec_from_file_location(name, ROOT / (name + '.py'))
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


broker = load('vm-broker')
host = load('vm-capture-host')
guest = load('vm-guest')
capture_guest = load('vm-capture-guest')
guest_worker = load('vm-guest-worker')
capture = broker.capture
HASH = lambda data: hashlib.sha256(data).hexdigest()
SIGNING_SEED = '9d61b19deffd5a60ba844af492ec2cc4' + '4449c5697b326919703bac031cae7f60'
PUBLIC_KEY = 'd75a980182b10ab7d54bfed3c964073a0ee172f3daa62325af021a68f707511a'


def frame(data):
    return capture.FRAME.pack(len(data)) + data


class CaptureTests(unittest.TestCase):
    def setUp(self):
        self.root = Path(self.enterContext(tempfile.TemporaryDirectory()))
        self.key = self.root / 'invented-key.der'
        self.key.write_bytes(bytes.fromhex('302e020100300506032b657004220420' + SIGNING_SEED))
        self.staging = self.root / 'staging'
        self.staging.mkdir()
        self.files = (b'invented git bundle', b'{"overlay":"fixture"}\n', b'{"fixture":"invented"}\n')
        execution = 'cc5fa420-047a-47a3-9450-0b7342810cb6'
        self.request = {
            'project': 'fleet', 'task': '42f5d989-991a-44d6-a105-c61be0fa3839',
            'session': 'e9280db1-e3d9-4aba-95f9-f430d37e3f0b', 'execution': execution, 'version': 1,
            'job': capture.job_id(execution), 'nonce': '3' * 64, 'base': 'a' * 40,
            **dict(zip(('baseSha256', 'overlaySha256', 'fixtureSha256'), map(HASH, self.files))),
        }
        self.config = {
            'version': 1, 'host': 'pds18-native', 'clientUid': os.getuid(), 'publicKey': PUBLIC_KEY,
            'launchSha256': 'b' * 64,
            'producer': {'commit': 'a' * 40, 'version': '1.fixture', 'target': 'linux-amd64'},
            'runtime': {'imageSha256': '0' * 64, 'fontSha256': '1' * 64, 'lockSha256': '2' * 64,
                        'node': 'test-node', 'chromium': 'test-chromium', 'playwright': 'test-playwright'},
        }
        self.expires = int(time.time()) + 100
        self.enterContext(patch.object(broker, 'require_empty_job_cgroup', return_value=None))
        self.calls = []
        self.errors = []

    def signed(self, request=None, sign_request=None, mutation=None, expires=None):
        request = self.request if request is None else request
        signing = request if sign_request is None else sign_request
        expiry = self.expires if expires is None else expires
        installation = capture.installation_digest(self.config)
        message = b'fleet:design-capture-permit:v2\n' + capture.encode({
            'job': signing['job'], 'host': 'pds18-native', 'requestSha256': HASH(capture.encode(signing)),
            'installationSha256': installation, 'expiresAt': expiry,
        })
        if mutation:
            message = mutation(message)
        msg = self.root / 'invented-message'
        msg.write_bytes(message)
        signature = subprocess.check_output(['/usr/bin/openssl', 'pkeyutl', '-sign', '-keyform', 'DER', '-rawin',
                                             '-inkey', str(self.key), '-in', str(msg)], timeout=3)
        return frame(capture.encode({'expiresAt': expiry, 'installationSha256': installation, 'signature': signature.hex()})) + frame(capture.encode(request))

    def wire(self, **kwargs):
        return b'FDC1' + self.signed(**kwargs) + b''.join(map(frame, self.files))

    def manifest(self, _client, *, expected_identity, expected_commit):
        self.assertEqual(expected_identity, bytes.fromhex(self.config['launchSha256']))
        self.assertEqual(expected_commit, self.request['base'])
        self.calls.append('manifest')
        return expected_identity, os.open('/dev/null', os.O_RDONLY), os.open('/dev/null', os.O_RDONLY)

    def reply(self, *, commit=None, runtime=None, archive=b'invented archive'):
        measured = {
            'version': 1, 'request': self.request, 'requestSha256': HASH(capture.encode(self.request)),
            'producer': {**self.config['producer'], 'commit': commit or self.request['base']},
            'runtime': runtime or self.config['runtime'],
            'source': {'sha256': '1'*64, 'baseTree': '2'*40, 'tree': '3'*40,
                       'patchSha256': '4'*64, 'archiveSha256': '5'*64},
            'capture': {'name': 'capture.tar', 'bytes': len(archive), 'sha256': HASH(archive)},
        }
        receipt = capture.encode(measured)
        return capture.REPLY.pack(b'FDR1', len(receipt), len(archive)) + receipt + archive

    def run_vm(self, client, op, nonce, request_hash, payload, image, bios, root):
        self.calls.append('start')
        self.assertEqual(op, 5)
        self.assertEqual(len(nonce), 32)
        expected = frame(capture.encode(self.request)) + b''.join(map(frame, self.files))
        self.assertEqual(payload.read(), expected)  # the host authorization frame stops at the host
        self.assertEqual(request_hash.hex(), HASH(expected))
        self.assertEqual(len(list(root.glob('run.fence.*'))), 1)
        return 0, self.reply()

    def connected(self, runner=None, manifest=None, config=None, staging=None):
        client, server = socket.socketpair()
        client.settimeout(3)
        server.setblocking(False)
        config = self.config if config is None else config

        def receive():
            with server:
                try:
                    host.serve_capture(broker, server, self.staging if staging is None else staging,
                                       runner=runner or self.run_vm, manifest_loader=manifest or self.manifest,
                                       config_loader=lambda _: config)
                except BaseException as error:
                    self.errors.append(error)
        thread = threading.Thread(target=receive, daemon=True)
        thread.start()
        return client, thread

    def exchange(self, raw, **kwargs):
        client, thread = self.connected(**kwargs)
        try:
            data = bytearray()
            try:
                client.sendall(raw)
                while part := client.recv(65536):
                    data.extend(part)
            except (BrokenPipeError, ConnectionResetError):
                pass
            return bytes(data)
        finally:
            client.close()
            thread.join(3)
            self.assertFalse(thread.is_alive())

    def test_capture_receipt_after_cgroup_and_fence_cleanup_then_replay_denied(self):
        def cgroup():
            self.assertTrue(list(self.staging.glob('run.fence.*')))
            self.calls.append('cgroup empty')
        wire = self.wire()
        with patch.object(broker, 'require_empty_job_cgroup', side_effect=cgroup):
            self.assertEqual(self.exchange(wire), self.reply())
        self.assertEqual(self.calls, ['manifest', 'start', 'cgroup empty'])
        self.assertFalse(list(self.staging.glob('run.*')))
        self.assertEqual(self.exchange(wire), b'')
        self.assertIsInstance(self.errors[-1], ValueError)
        self.assertEqual(self.calls.count('start'), 1)

    def test_rejected_identity_signature_validity_and_frame_never_start_a_vm(self):
        other = {**self.request, 'nonce': '4' * 64}
        for name, raw, config in (
            ('wrong peer uid', self.wire(), {**self.config, 'clientUid': os.getuid() + 1}),
            ('wrong key', self.wire(), {**self.config, 'publicKey': '2' * 64}),
            ('wrong producer version', self.wire(), {**self.config, 'producer': {**self.config['producer'], 'version': 'other'}}),
            ('wrong renderer runtime', self.wire(), {**self.config, 'runtime': {**self.config['runtime'], 'node': 'other'}}),
            ('wrong golden launch pin', self.wire(), {**self.config, 'launchSha256': '4' * 64}),
            ('purpose', self.wire(mutation=lambda msg: msg.replace(b'capture-permit', b'preview-permit')), self.config),
            ('host', self.wire(mutation=lambda msg: msg.replace(b'pds18-native', b'another-host')), self.config),
            ('request digest', self.wire(request=other, sign_request=self.request), self.config),
            ('wrong durable job', self.wire(request={**self.request, 'job': self.request['task']}), self.config),
            ('expired', self.wire(expires=int(time.time()) - 1), self.config),
            ('too far in future', self.wire(expires=int(time.time()) + 1000), self.config),
            ('protected protocol on capture socket', b'FVMQ', self.config),
            ('oversized permit', b'FDC1' + struct.pack('>I', 1025), self.config),
            ('wrong release', self.wire(request={**self.request, 'base': 'd' * 40}), self.config),
        ):
            with self.subTest(name=name):
                self.assertEqual(self.exchange(raw, config=config), b'')
                self.assertTrue(self.errors)
        self.assertEqual(self.calls, [])

    def test_valid_permit_rejects_wrong_inputs_extra_and_does_not_reissue(self):
        ticket = b'FDC1' + self.signed()
        for name, payload in (
            ('bad input digest', frame(b'substituted') + b''.join(map(frame, self.files[1:]))),
            ('oversized bundle', struct.pack('>I', capture.FILES[1][1] + 1)),
            ('extra input', b''.join(map(frame, self.files)) + b'added bytes'),
        ):
            with self.subTest(name=name), tempfile.TemporaryDirectory() as directory:
                root = Path(directory)
                self.assertEqual(self.exchange(ticket + payload, staging=root), b'')
                self.assertEqual(self.exchange(self.wire(), staging=root), b'')
        self.assertEqual(self.calls, [])

    def test_ledger_does_not_reaccept_after_wall_clock_rollback(self):
        host.spend_permit(self.staging, self.request['job'], 110, 100)
        host.spend_permit(self.staging, self.request['task'], 130, 115)
        with self.assertRaisesRegex(ValueError, 'stale'):
            host.spend_permit(self.staging, self.request['job'], 110, 101)

    def test_wrong_launch_and_result_binding(self):
        def wrong_launch(*_, **_kw):
            raise ValueError('golden digest mismatch')
        self.assertEqual(self.exchange(self.wire(), manifest=wrong_launch), b'')
        self.assertEqual(self.calls, [])
        good = self.reply()
        for output in (good[:-1], good+b'junk', self.reply(commit='d'*40),
                       self.reply(runtime={**self.config['runtime'], 'node': 'wrong'}),
                       good[:-1]+bytes([good[-1] ^ 1])):
            with self.subTest(output=len(output)), tempfile.TemporaryDirectory() as directory:
                self.assertEqual(self.exchange(self.wire(), staging=Path(directory), runner=lambda *_: (0, output)), b'')

    def test_capture_manifest_mismatch_is_rejected_before_opening_golden(self):
        manifest = capture.encode({'version': 1, 'releaseCommit': 'a'*40, **{
            k: 'f'*64 for k in ('goldenSha256', 'biosSha256', 'releaseSha256', 'guestWorkerSha256', 'runtimeSha256')
        }})
        with patch.object(broker, 'trusted_file', return_value=100) as trusted, \
             patch.object(broker.os, 'read', return_value=manifest), patch.object(broker.os, 'close'):
            for identity, commit in ((b'wrong', self.request['base']), (hashlib.sha256(manifest).digest(), 'd'*40)):
                with self.assertRaises(ValueError):
                    broker.load_manifest(expected_identity=identity, expected_commit=commit)
            self.assertEqual(trusted.call_count, 2)  # only the manifest was read

    def test_cancelled_client_reaps_real_simulated_process_and_withholds_output(self):
        running = threading.Event()
        killed = threading.Event()
        def runner(client, *_):
            with subprocess.Popen(['/usr/bin/sleep', '10'], start_new_session=True) as process:
                running.set()
                try:
                    self.assertTrue(select.select([client], [], [], 2)[0])
                    broker.client_alive(client)
                finally:
                    self.assertTrue(broker.cleanup_process(process, 2))
                    self.assertEqual(process.returncode, -signal.SIGKILL)
                    killed.set()
        client, thread = self.connected(runner=runner)
        with client:
            client.sendall(self.wire())
            self.assertTrue(running.wait(2))
        thread.join(3)
        self.assertFalse(thread.is_alive())
        self.assertTrue(killed.is_set())
        self.assertIsInstance(self.errors[-1], EOFError)
        self.assertFalse(list(self.staging.glob('run.*')))

    def test_unconfirmed_teardown_retains_fence_and_sends_no_output(self):
        with patch.object(broker, 'require_empty_job_cgroup', side_effect=RuntimeError('leftover')):
            self.assertEqual(self.exchange(self.wire()), b'')
        self.assertIsInstance(self.errors[-1], broker.CleanupUnconfirmed)
        self.assertEqual(len(list(self.staging.glob('run.fence.*'))), 1)
        with self.assertRaises(ValueError):
            load('vm-preflight').check_clean_work(self.staging)

    def test_guest_requires_shut_down_process_and_eof(self):
        op, nonce, request_hash = 5, b'n' * 32, b'd' * 32
        body = self.reply()
        header = broker.RESPONSE.pack(b'FVMR', 1, op, 0, b'\0', nonce, request_hash, b'\0'*32,
                                      len(body), hashlib.sha256(body).digest())
        class Process:
            returncode = 0
            def poll(self):
                return self.returncode
        for case in ('complete', 'unexpected shutdown', 'extra output', 'nonzero exit', 'delayed channel EOF'):
            with self.subTest(case=case), ExitStack() as stack:
                def pair():
                    return tuple(stack.enter_context(sock) for sock in socket.socketpair())
                qmp, qpeer = pair()
                worker, wpeer = pair()
                client, _ = pair()
                reason = b'guest-reset' if case == 'unexpected shutdown' else b'guest-shutdown'
                qpeer.sendall(b'{"event":"SHUTDOWN","data":{"guest":true,"reason":"' + reason + b'"}}\n')
                wpeer.sendall(header + body + (b'extra' if case == 'extra output' else b''))
                process = Process()
                if case == 'nonzero exit':
                    process.returncode = 1
                if case == 'delayed channel EOF':
                    stop = threading.Event()
                    seen = []
                    def wait():
                        seen.append(broker.wait_guest_response(worker, qmp, process, client, op, nonce,
                                                                request_hash, time.monotonic() + 1))
                        stop.set()
                    thread = threading.Thread(target=wait, daemon=True)
                    thread.start()
                    self.assertFalse(stop.wait(0.04))
                    wpeer.close()
                    self.assertTrue(stop.wait(1))
                    self.assertEqual(seen, [(0, body)])
                    thread.join(1)
                    continue
                wpeer.close()
                action = lambda: broker.wait_guest_response(worker, qmp, process, client, op, nonce,
                                                             request_hash, time.monotonic() + 1)
                if case == 'complete':
                    self.assertEqual(action(), (0, body))
                else:
                    with self.assertRaises((ValueError, RuntimeError)):
                        action()

    def test_guest_stages_only_four_files_for_fixed_worker_and_removes_job(self):
        raw = frame(capture.encode(self.request)) + b''.join(map(frame, self.files))
        worker = type('Worker', (), {'pw_uid': os.getuid(), 'pw_gid': os.getgid()})
        # Mode and directory behavior are real. The test runs without VM root.
        with patch.object(capture_guest.os, 'chown'), patch.object(capture_guest.os, 'fchown'):
            def fixed():
                work = self.staging / 'job'
                for folder in (work, work/'input'):
                    self.assertEqual(folder.stat().st_mode & 0o777, 0o750)
                self.assertEqual(set(os.listdir(work/'input')), {name for name, _, _ in capture.FILES})
                out = work/'results'/'output'
                out.mkdir()
                (out/'receipt.json').write_bytes(b'test receipt')
                (out/'capture.tar').write_bytes(b'test archive')
            status, out = capture_guest.fixed_job(capture, guest.read_exact, io.BytesIO(raw), self.staging,
                                                  runner=fixed, worker=worker)
            self.assertEqual((status, out), (0, capture.REPLY.pack(b'FDR1', 12, 12)+b'test receipttest archive'))
            self.assertEqual(list(self.staging.iterdir()), [])
            (self.staging/'stale').touch()
            with self.assertRaises(ValueError):
                capture_guest.fixed_job(capture, guest.read_exact, io.BytesIO(raw), self.staging,
                                        runner=fixed, worker=worker)

    def test_capture_contention_answers_stock_probe_and_reserves_next_slot(self):
        gate = threading.Event()
        capture_finished = threading.Event()
        stop = threading.Event()

        def long_capture(_client):
            try:
                self.assertTrue(gate.wait(3))
            finally:
                capture_finished.set()

        with tempfile.TemporaryDirectory(dir='/tmp') as folder, ExitStack() as stack:
            def listener(name):
                sock = stack.enter_context(socket.socket(socket.AF_UNIX))
                address = str(Path(folder)/name)
                sock.bind(address)
                sock.listen(4)
                return sock, address

            def connect(address):
                client = stack.enter_context(socket.socket(socket.AF_UNIX))
                client.settimeout(1)
                client.connect(address)
                return client

            protected, protected_address = listener('protected')
            capture_socket, capture_address = listener('capture')
            def probe(client):
                broker.serve(client, manifest_loader=lambda _: (
                    b'i'*32, os.open('/dev/null', os.O_RDONLY), os.open('/dev/null', os.O_RDONLY)),
                    runner=lambda *_: (0, b'{"probe":true}'), root=self.staging)
            thread = threading.Thread(target=broker.schedule,
                args=({broker.PROTECTED_SOCKET: protected, broker.CAPTURE_SOCKET: capture_socket},
                      {broker.PROTECTED_SOCKET: probe, broker.CAPTURE_SOCKET: long_capture}, stop), daemon=True)
            thread.start()
            try:
                capture_client = connect(capture_address)
                self.assertEqual(guest.read_exact(capture_client.makefile('rb', buffering=0),4), b'FDCA')
                first = connect(protected_address)
                self.assertEqual(guest.read_exact(first.makefile('rb', buffering=0),40), b'FVBU\x01'+bytes(35))
                self.assertEqual(first.recv(1), b'')
                gate.set()
                self.assertTrue(capture_finished.wait(1))
                self.assertEqual(capture_client.recv(1), b'')
                competing = connect(capture_address)
                self.assertEqual(guest.read_exact(competing.makefile('rb', buffering=0),4), b'FDB1')
                self.assertEqual(competing.recv(1), b'')
                for delay in (0, 0.25, 0.5):
                    time.sleep(delay)
                    retry = connect(protected_address)
                    challenge = guest.read_exact(retry.makefile('rb', buffering=0), 40)
                    if challenge[:4] == b'FVMB':
                        break
                    self.assertEqual(challenge, b'FVBU\x01' + bytes(35))
                    self.assertEqual(retry.recv(1), b'')
                self.assertEqual(challenge[:4], b'FVMB')
                empty = hashlib.sha256(b'').digest()
                retry.sendall(broker.REQUEST.pack(b'FVMQ',1,1,b'\0\0',challenge[8:],0,empty))
                header = broker.RESPONSE.unpack(guest.read_exact(retry.makefile('rb', buffering=0),broker.RESPONSE.size))
                self.assertEqual(header[2:4], (1,0))
                self.assertEqual(guest.read_exact(retry.makefile('rb', buffering=0),header[8]), b'{"probe":true}')
                self.assertEqual(retry.recv(1), b'')
            finally:
                gate.set()
                stop.set()
                thread.join(2)
            self.assertFalse(thread.is_alive())

    def test_failed_cleanup_during_accept_cannot_admit_either_listener(self):
        for contender in (broker.PROTECTED_SOCKET, broker.CAPTURE_SOCKET):
            with self.subTest(contender=contender), tempfile.TemporaryDirectory(dir='/tmp') as folder, ExitStack() as stack:
                ending = threading.Event()
                stop = threading.Event()
                entered = threading.Event()
                owner = []
                starts = []

                def fail_cleanup(_client):
                    starts.append('capture')
                    owner.append(threading.current_thread())
                    entered.set()
                    if not ending.wait(3):
                        raise AssertionError('fixture did not reach accept')
                    raise broker.CleanupUnconfirmed('invented teardown failure')

                class Listener:
                    def __init__(self, name, kind):
                        self.address = str(Path(folder) / name)
                        self.kind = kind
                        self.sock = stack.enter_context(socket.socket(socket.AF_UNIX))
                        self.sock.bind(self.address)
                        self.sock.listen(2)
                        self.calls = 0

                    def fileno(self):
                        return self.sock.fileno()

                    def accept(self):
                        incoming = self.sock.accept()
                        self.calls += 1
                        if self.kind == contender and (self.calls == 2 or contender == broker.PROTECTED_SOCKET):
                            # The select and accept started with a live owner.
                            # End it before the scheduler decides admission.
                            ending.set()
                            owner[0].join(2)
                        return incoming

                    def connect(self):
                        client = stack.enter_context(socket.socket(socket.AF_UNIX))
                        client.settimeout(2)
                        client.connect(self.address)
                        return client

                listeners = {broker.PROTECTED_SOCKET: Listener('protected', broker.PROTECTED_SOCKET),
                             broker.CAPTURE_SOCKET: Listener('capture', broker.CAPTURE_SOCKET)}
                handlers = {broker.PROTECTED_SOCKET: lambda _client: starts.append('protected'),
                            broker.CAPTURE_SOCKET: fail_cleanup}
                scheduler = threading.Thread(target=broker.schedule, args=(listeners, handlers, stop), daemon=True)
                scheduler.start()
                try:
                    first = listeners[broker.CAPTURE_SOCKET].connect()
                    self.assertEqual(guest.read_exact(first.makefile('rb', buffering=0), 4), b'FDCA')
                    self.assertTrue(entered.wait(1))
                    second = listeners[contender].connect()
                    self.assertEqual(second.recv(4), b'')
                    scheduler.join(2)
                    self.assertFalse(scheduler.is_alive())
                    self.assertEqual(starts, ['capture'])
                finally:
                    ending.set()
                    stop.set()
                    scheduler.join(2)

    def test_static_guest_worker_runs_one_fixed_unprivileged_program(self):
        with patch.object(guest_worker.subprocess, 'run', return_value=type('Result', (), {'returncode': 0})()) as run:
            client = type('Client', (), {'recv': lambda _self, _size: b''})()
            self.assertEqual(guest_worker.run_job(5, client), (0, b''))
            with self.assertRaises(ValueError):
                guest_worker.run_job(9, client)
        argv, = run.call_args.args
        self.assertEqual(argv, ['/usr/bin/node', str(guest_worker.RELEASE / 'designer.mjs'), 'capture-job',
                                '--job-input', str(guest_worker.STATE / 'input'),
                                '--out', str(guest_worker.STATE / 'results' / 'output')])
        self.assertEqual(run.call_args.kwargs['env'], guest_worker.ENV)

    def test_private_guest_handoff_requires_root_and_has_fixed_reply(self):
        with tempfile.TemporaryDirectory() as temp, socket.socket(socket.AF_UNIX) as listener:
            path = str(Path(temp) / 'worker')
            listener.bind(path)
            listener.listen(1)
            calls = []
            def run(op, client):
                calls.append((op, client.recv(8), client.recv(1)))
                return 0, b'probe-measured'
            with patch.object(guest_worker.struct, 'unpack', return_value=(1, 0, 0)):
                worker = threading.Thread(target=guest_worker.serve, args=(listener, run))
                worker.start()
                with socket.socket(socket.AF_UNIX) as root:
                    root.settimeout(2)
                    root.connect(path)
                    with root.makefile('rb', buffering=0) as ready:
                        self.assertEqual(guest.read_exact(ready, 8), b'FVWK\x01\0\0\0')
                    self.assertEqual(guest.worker_job(root, 1, io.BytesIO(b'payload'), 32, 2), (0, b'probe-measured'))
                worker.join(2)
                self.assertFalse(worker.is_alive())
            self.assertEqual(calls, [(1, b'payload', b'')])
            with socket.socket(socket.AF_UNIX) as foreign:
                foreign.connect(path)
                with patch.object(guest_worker.struct, 'unpack', return_value=(1, 65533, 65533)):
                    with self.assertRaisesRegex(ValueError, 'untrusted guest supervisor'):
                        guest_worker.serve(listener, run)
            self.assertEqual(len(calls), 1)

    def test_protected_socket_does_not_admit_internal_capture_operation(self):
        nonce = b'n'*32
        client, server = socket.socketpair()
        with client, server:
            client.sendall(broker.REQUEST.pack(b'FVMQ', 1, 5, b'\0\0', nonce, 0, hashlib.sha256(b'').digest()))
            with self.assertRaises(ValueError):
                broker.read_request(server, nonce, self.staging, time.monotonic()+1)


if __name__ == '__main__':
    unittest.main()
