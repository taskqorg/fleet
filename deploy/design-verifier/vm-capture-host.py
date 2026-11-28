#!/usr/bin/python3
"""Host-only permit validation and reply on the dedicated capture socket."""
import hashlib
import os
from pathlib import Path
import socket
import sqlite3
import stat
import struct
import tempfile
import time

CONFIG = '/app/capture.json'
PEERCRED = struct.Struct('3i')
MAX_PERMIT = 1024


def configuration(broker):
    schema = broker.capture
    fd = broker.trusted_file(CONFIG)
    try:
        raw = os.read(fd, 8193)
    finally:
        broker.close_input(fd)
    if len(raw) > 8192:
        raise ValueError('capture configuration too large')
    obj = schema.exact_json(raw)
    if (set(obj) != {'version', 'host', 'clientUid', 'publicKey', 'launchSha256', 'producer', 'runtime'} or
            type(obj['version']) is not int or obj['version'] != 1 or obj['host'] != schema.HOST or
            type(obj['clientUid']) is not int or not 0 < obj['clientUid'] < 2**31 or
            not all(schema.matches(obj[k], schema.HASH) for k in ('publicKey', 'launchSha256'))):
        raise ValueError('invalid capture installation')
    producer, runtime = obj['producer'], obj['runtime']
    if (type(producer) is not dict or type(runtime) is not dict or
            set(producer) != {'commit', 'version', 'target'} or not schema.matches(producer['commit'], schema.COMMIT) or
            producer['target'] != 'linux-amd64' or not valid_version(producer['version']) or
            set(runtime) != {'imageSha256', 'fontSha256', 'lockSha256', 'node', 'chromium', 'playwright'} or
            not all(schema.matches(runtime[k], schema.HASH) for k in ('imageSha256', 'fontSha256', 'lockSha256')) or
            not all(valid_version(runtime[k]) for k in ('node', 'chromium', 'playwright'))):
        raise ValueError('invalid capture release and runtime binding')
    return obj


def valid_version(value):
    return isinstance(value, str) and value.isascii() and 0 < len(value) < 100


def verify_permit(broker, root, public_key, signature, message):
    # Ed25519's fixed SubjectPublicKeyInfo prefix followed by the 32-byte
    # operator-installed key. Only files holding public request data cross exec.
    key_der = bytes.fromhex('302a300506032b6570032100' + public_key)
    with tempfile.TemporaryFile(dir=root) as key, tempfile.TemporaryFile(dir=root) as sig, tempfile.TemporaryFile(dir=root) as data:
        for file, body in ((key, key_der), (sig, signature), (data, message)):
            file.write(body)
            file.seek(0)
        fds = tuple(file.fileno() for file in (key, sig, data))
        broker.run_checked(['/usr/bin/openssl', 'pkeyutl', '-verify', '-pubin', '-keyform', 'DER', '-rawin',
                            '-inkey', f'/proc/self/fd/{fds[0]}', '-sigfile', f'/proc/self/fd/{fds[1]}',
                            '-in', f'/proc/self/fd/{fds[2]}'], None, 5, pass_fds=fds)


def spend_permit(root, job, expires, now):
    # A transaction persists both the consumed job and the clock high-water
    # mark on the existing bounded volume. Expired rows may be pruned without
    # making them replayable after a crash, restart or wall-clock rollback.
    ledger = Path(root) / 'capture-permits.sqlite3'
    try:
        info = ledger.lstat()
    except FileNotFoundError:
        os.close(os.open(ledger, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, 0o600))
    else:
        if not stat.S_ISREG(info.st_mode) or info.st_uid != os.getuid() or info.st_nlink != 1 or info.st_mode & 0o077:
            raise ValueError('unexpected capture ledger ownership or mode')
    conn = sqlite3.connect(ledger, timeout=1)
    try:
        conn.execute('PRAGMA synchronous = FULL')
        conn.execute('BEGIN IMMEDIATE')
        conn.execute('CREATE TABLE IF NOT EXISTS clock (id INTEGER PRIMARY KEY CHECK (id = 1), seconds INTEGER NOT NULL)')
        conn.execute('CREATE TABLE IF NOT EXISTS spent (job TEXT PRIMARY KEY, expires INTEGER NOT NULL)')
        conn.execute('INSERT INTO clock VALUES (1, ?) ON CONFLICT(id) DO UPDATE SET seconds = max(seconds, excluded.seconds)', (now,))
        effective_now, = conn.execute('SELECT seconds FROM clock WHERE id = 1').fetchone()
        if expires <= effective_now:
            raise ValueError('capture permit is stale')
        conn.execute('DELETE FROM spent WHERE expires <= ?', (effective_now,))
        row = conn.execute('INSERT INTO spent VALUES (?, ?) ON CONFLICT(job) DO NOTHING', (job, expires))
        if row.rowcount != 1:
            raise ValueError('capture permit has already been spent')
        conn.commit()
        fd = os.open(root, os.O_RDONLY | os.O_DIRECTORY)
        try:
            os.fsync(fd)
        finally:
            os.close(fd)
    except sqlite3.Error as error:
        raise ValueError('capture permit ledger unavailable') from error
    finally:
        conn.close()


def receive_request(broker, client, root, config, payload):
    schema = broker.capture
    deadline = time.monotonic() + 5
    def read(size):
        return broker.recv_exact(client, size, deadline)
    if read(4) != b'FDC1':
        raise ValueError('wrong capture protocol')
    permit_raw = read(schema.frame_size(read, MAX_PERMIT))
    request_raw = read(schema.frame_size(read, schema.FILES[0][1]))
    request = schema.request_of(request_raw)
    if request['base'] != config['producer']['commit']:
        raise ValueError('unapproved capture source release')
    expires, signature, message = schema.permit_of(permit_raw, request, request_raw, schema.installation_digest(config), int(time.time()))
    verify_permit(broker, root, config['publicKey'], signature, message)
    now = int(time.time())
    if now >= expires:
        raise ValueError('capture authorization expired')
    spend_permit(root, request['job'], expires, now)
    deadline = time.monotonic() + 30
    request_frame = schema.FRAME.pack(len(request_raw)) + request_raw
    payload.write(request_frame)
    digest = hashlib.sha256(request_frame)
    for _, maximum, key in schema.FILES[1:]:
        left = schema.frame_size(read, maximum)
        prefix = schema.FRAME.pack(left)
        payload.write(prefix)
        digest.update(prefix)
        file_digest = hashlib.sha256()
        while left:
            part = read(min(left, schema.CHUNK))
            payload.write(part)
            digest.update(part)
            file_digest.update(part)
            left -= len(part)
        if file_digest.hexdigest() != request[key]:
            raise ValueError('capture input digest differs')
    broker.client_alive(client)
    payload.seek(0)
    return request, hashlib.sha256(request_raw).hexdigest(), digest.digest(), expires


def accepted_result(broker, result, request, request_hash, config):
    schema = broker.capture
    if len(result) < schema.REPLY.size:
        raise ValueError('short capture output')
    magic, receipt_size, archive_size = schema.REPLY.unpack_from(result)
    if (magic != b'FDR1' or not 0 < receipt_size <= schema.RECEIPT_MAX or
            not 0 < archive_size <= schema.ARCHIVE_MAX or len(result) != schema.REPLY.size + receipt_size + archive_size):
        raise ValueError('invalid capture output framing')
    boundary = schema.REPLY.size + receipt_size
    receipt = schema.exact_json(result[schema.REPLY.size:boundary])
    archive = receipt.get('capture')
    source = receipt.get('source')
    if (type(source) is not dict or set(source) != {'sha256', 'baseTree', 'tree', 'patchSha256', 'archiveSha256'} or
            not all(schema.matches(source[key], schema.HASH) for key in ('sha256', 'patchSha256', 'archiveSha256')) or
            not all(schema.matches(source[key], schema.COMMIT) for key in ('baseTree', 'tree'))):
        raise ValueError('capture source receipt is incomplete')
    if (set(receipt) != {'version', 'request', 'requestSha256', 'producer', 'runtime', 'source', 'capture'} or
            type(receipt['version']) is not int or receipt['version'] != 1 or receipt['request'] != request or
            receipt['requestSha256'] != request_hash or receipt['producer'] != config['producer'] or
            receipt['runtime'] != config['runtime'] or archive != {
                'name': 'capture.tar', 'bytes': archive_size, 'sha256': hashlib.sha256(memoryview(result)[boundary:]).hexdigest(),
            }):
        raise ValueError('capture output differs from the admitted request, release or archive')


def serve_capture(broker, client, root=None, *, runner=None, manifest_loader=None, config_loader=configuration):
    root = broker.STATE if root is None else root
    runner = broker.run_vm if runner is None else runner
    manifest_loader = broker.load_manifest if manifest_loader is None else manifest_loader
    config = config_loader(broker)
    _, uid, _ = PEERCRED.unpack(client.getsockopt(socket.SOL_SOCKET, socket.SO_PEERCRED, PEERCRED.size))
    if uid != config['clientUid']:
        raise ValueError('unapproved capture client user')
    with broker.attempt(root) as handles:
        payload = tempfile.TemporaryFile(dir=root)
        handles.append(payload)
        request, request_hash, payload_hash, expires = receive_request(broker, client, root, config, payload)
        _, image, bios = manifest_loader(client, expected_identity=bytes.fromhex(config['launchSha256']),
                                         expected_commit=config['producer']['commit'])
        handles.extend((image, bios))
        broker.client_alive(client)
        if time.time() >= expires:
            raise ValueError('capture permit expired before launch')
        status, result = runner(client, broker.capture.OPERATION, os.urandom(32), payload_hash, payload, image, bios, root)
        if status != 0:
            raise ValueError('isolated capture did not complete')
        accepted_result(broker, result, request, request_hash, config)
    broker.client_alive(client)
    if time.time() >= expires:
        raise ValueError('capture permit expired before return')
    broker.send_all(client, result, time.monotonic() + 15)
