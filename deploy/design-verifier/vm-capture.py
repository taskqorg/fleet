#!/usr/bin/python3
"""Fixed designer capture framing shared by the host broker and VM supervisor."""
import hashlib
import json
import re
import struct
import uuid

OPERATION = 5
FILES = (('request.json', 8192, None), ('base.bundle', 60 << 20, 'baseSha256'),
         ('overlay.json', 3 << 20, 'overlaySha256'), ('fixture.json', 256 << 10, 'fixtureSha256'))
FRAME = struct.Struct('>I')
REPLY = struct.Struct('>4sII')
RECEIPT_MAX = 8192
ARCHIVE_MAX = 100 << 20
INPUT_MAX = sum(size + FRAME.size for _, size, _ in FILES)
OUTPUT_MAX = REPLY.size + RECEIPT_MAX + ARCHIVE_MAX
WORK_SECONDS = 620
PERMIT_SECONDS = 900
HOST = 'pds18-native'
HASH = re.compile(r'[0-9a-f]{64}')
COMMIT = re.compile(r'[0-9a-f]{40}')
UUID = re.compile(r'[0-9a-f]{8}(?:-[0-9a-f]{4}){3}-[0-9a-f]{12}')
CHUNK = 65536


def exact_json(raw):
    def unique(pairs):
        result = {}
        for key, value in pairs:
            if key in result:
                raise ValueError('duplicate capture key')
            result[key] = value
        return result
    def invalid_constant(_):
        raise ValueError('invalid JSON constant')
    result = json.loads(raw.decode('utf-8'), object_pairs_hook=unique, parse_constant=invalid_constant)
    if type(result) is not dict:
        raise ValueError('capture JSON must be an object')
    return result


def encode(value):
    return json.dumps(value, separators=(',', ':'), ensure_ascii=True).encode('ascii')


def matches(value, regex):
    return isinstance(value, str) and regex.fullmatch(value) is not None


def job_id(execution):
    digest = hashlib.sha256(b'fleet:design-capture:v1:' + execution.encode('ascii')).digest()
    return str(uuid.UUID(bytes=digest[:16], version=5))


def request_of(raw):
    obj = exact_json(raw)
    keys = ('project', 'task', 'session', 'execution', 'version', 'job', 'nonce', 'base',
            'baseSha256', 'overlaySha256', 'fixtureSha256')
    if (tuple(obj) != keys or type(obj['version']) is not int or obj['version'] != 1 or obj['project'] != 'fleet' or
            not all(matches(obj[k], UUID) for k in ('task', 'session', 'execution', 'job')) or
            obj['execution'] == obj['job'] or obj['job'] != job_id(obj['execution']) or not matches(obj['base'], COMMIT) or
            not all(matches(obj[k], HASH) for k in ('nonce', 'baseSha256', 'overlaySha256', 'fixtureSha256')) or
            encode(obj) != raw):
        raise ValueError('invalid immutable capture request')
    return obj


def permit_of(raw, request, request_raw, now):
    obj = exact_json(raw)
    if set(obj) != {'expiresAt', 'signature'}:
        raise ValueError('invalid capture permit')
    expires, signature = obj['expiresAt'], obj['signature']
    if (type(expires) is not int or not now < expires <= now + PERMIT_SECONDS or
            not matches(signature, re.compile(r'[0-9a-f]{128}'))):
        raise ValueError('expired or invalid capture permit')
    message = b'fleet:design-capture-permit:v1\n' + encode({
        'job': request['job'], 'host': HOST, 'requestSha256': hashlib.sha256(request_raw).hexdigest(), 'expiresAt': expires,
    })
    return expires, bytes.fromhex(signature), message


def frame_size(read, maximum):
    size, = FRAME.unpack(read(FRAME.size))
    if not 0 < size <= maximum:
        raise ValueError('capture frame outside bounds')
    return size
