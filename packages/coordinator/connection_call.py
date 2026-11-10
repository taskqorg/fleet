"""Fleet connection client (docs/designs/fleet-lab.md, Connections). One client, two entry points:

  connection_call.py call <connection.json> <label> <calls dir> <tool>   arguments as JSON on stdin, result JSON on stdout
  connection_call.py mcp  <connection.json> <label> <calls dir>          MCP stdio server with the connection's tools

The connection record is a private host file {clientPath, clientSha256, pipeConfigPath, tools}. The client library (Pipe's
pipe_mcp.py) is loaded only when its SHA-256 matches clientSha256, from the bytes that were hashed. The label (the Fleet
step execution ID) goes to Pipe as X-Pipe-Task. Every call appends one line to <calls dir>/.connection-calls.jsonl:
{tool, request_id, outcome: ok|error|unknown, error_code, usage}; the coordinator turns each line into a tool_run.
"""
import hashlib
import json
import os
import re
import stat
import sys
import types
import uuid
from pathlib import Path

sys.dont_write_bytecode = True
# Pipe's error reasons that leave no doubt about the outcome; provider-body and provider-timeout leave the charge unknown.
UNKNOWN = {'provider-body', 'provider-timeout', 'client-timeout', 'invalid-response'}


def private(path):
    p = Path(path)
    m = p.lstat()
    if not p.is_absolute() or p.is_symlink() or not stat.S_ISREG(m.st_mode) or m.st_uid != os.getuid() or m.st_mode & 0o077:
        raise ValueError('private connection configuration required')
    return p


def load(record_path):
    record = json.loads(private(record_path).read_text())
    source = Path(record['clientPath']).read_bytes()
    if hashlib.sha256(source).hexdigest() != record.get('clientSha256'):
        raise PermissionError('client-hash-mismatch')
    module = types.ModuleType('fleet_connection_client')
    module.__file__ = record['clientPath']
    exec(compile(source, record['clientPath'], 'exec'), module.__dict__)
    config = json.loads(private(record['pipeConfigPath']).read_text())
    config['url'] = config['url'].rstrip('/')
    secrets = [config.get('token', '')]
    if config.get('access_file'):
        secrets.extend(module.access_headers(config['access_file']).values())
    return record, module, config, [s for s in secrets if s]


def redact(value, secrets):
    if isinstance(value, str):
        for secret in secrets:
            value = value.replace(secret, '[redacted]')
        return value
    if isinstance(value, list):
        return [redact(v, secrets) for v in value]
    if isinstance(value, dict):
        return {k: redact(v, secrets) for k, v in value.items()}
    return value


def classify(result):
    """(outcome, error_code, output, usage) from the client's MCP tool result."""
    text = '\n'.join(c.get('text', '') for c in result.get('content') or [] if c.get('type') == 'text')
    if not result.get('isError'):
        try:
            output = json.loads(text)
        except ValueError:
            return 'unknown', 'invalid-response', {'text': text}, None
        return 'ok', None, output, output.get('usage') if isinstance(output, dict) else None
    m = re.search(r'HTTP (\d+): ?(.*)', text, re.S)
    if m:
        status, body = int(m.group(1)), m.group(2)
        try:
            code = json.loads(body).get('error') or f'http-{status}'
        except (ValueError, AttributeError):
            code = f'http-{status}'
    elif 'timed out' in text:
        code = 'client-timeout'
    elif 'unreachable' in text:
        code = 'unreachable'
    else:
        code = 'client-error'
    return ('unknown' if code in UNKNOWN else 'error'), code, {'error': code, 'text': text}, None


def call(loaded, label, calls_dir, tool, arguments):
    record, module, config, secrets = loaded
    if tool not in record.get('tools', []):
        result = {'isError': True, 'content': [{'type': 'text', 'text': f'tool {tool} not granted by this connection'}]}
        outcome, code, output, usage = 'error', 'tool-not-granted', {'error': 'tool-not-granted'}, None
    else:
        os.environ['PIPE_TASK'] = label
        try:
            result = module.answer({'method': 'tools/call', 'params': {'name': tool, 'arguments': arguments}}, config, 'fleet')
        except Exception as error:  # The client failed before or after sending: the outcome is unknown.
            result = {'isError': True, 'content': [{'type': 'text', 'text': 'client failed: ' + type(error).__name__}]}
            outcome, code, output, usage = 'unknown', 'client-exception', {'error': 'client-exception'}, None
        else:
            outcome, code, output, usage = classify(result)
    line = {'tool': tool, 'request_id': str(uuid.uuid4()), 'label': label, 'outcome': outcome, 'error_code': code, 'usage': usage}
    Path(calls_dir).mkdir(parents=True, exist_ok=True)
    with open(Path(calls_dir) / '.connection-calls.jsonl', 'a') as f:
        f.write(json.dumps(line) + '\n')
    return redact(result, secrets), redact({**line, 'output': output}, secrets)


def refused(error):
    code = str(error) if isinstance(error, PermissionError) else 'connection-unavailable'
    return code, {'isError': True, 'content': [{'type': 'text', 'text': 'Connection unavailable: ' + code}]}


def main(argv):
    mode, record_path, label, calls_dir = argv[1:5]
    try:
        loaded = load(record_path)
    except Exception as error:
        loaded, failure = None, refused(error)
    if mode == 'call':
        tool = argv[5]
        if loaded is None:
            print(json.dumps({'tool': tool, 'outcome': 'error', 'error_code': failure[0], 'usage': None, 'output': None}))
            return
        print(json.dumps(call(loaded, label, calls_dir, tool, json.load(sys.stdin))[1]))
        return
    tools = []
    if loaded:
        granted = loaded[0].get('tools', [])
        tools = [{k: v for k, v in t.items() if k != 'field'} for t in getattr(loaded[1], 'TOOLS', []) if t.get('name') in granted]
    for raw in sys.stdin:
        try:
            message = json.loads(raw)
        except ValueError:
            continue
        if 'id' not in message:
            continue
        method, params = message.get('method'), message.get('params') or {}
        reply = {'jsonrpc': '2.0', 'id': message['id']}
        if method == 'initialize':
            reply['result'] = {'protocolVersion': params.get('protocolVersion', '2025-06-18'), 'capabilities': {'tools': {}}, 'serverInfo': {'name': 'fleet-connection', 'version': '1'},
                               'instructions': getattr(loaded[1], 'INSTRUCTIONS', '') if loaded else ''}
        elif method == 'ping':
            reply['result'] = {}
        elif method == 'tools/list':
            reply['result'] = {'tools': tools}
        elif method == 'tools/call':
            reply['result'] = call(loaded, label, calls_dir, params.get('name'), params.get('arguments') or {})[0] if loaded else failure[1]
        else:
            reply['error'] = {'code': -32601, 'message': 'method not found'}
        print(json.dumps(reply), flush=True)


if __name__ == '__main__':
    main(sys.argv)
