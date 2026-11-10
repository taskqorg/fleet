"""Fleet connection client (docs/designs/fleet-connections.md §5). One client, two entry points:

  connection_call.py call <connection> <tool>        arguments as JSON on stdin, one call line as JSON on stdout
  connection_call.py mcp  <connection> <tools.json>  MCP stdio server with the connection's tools

Calls go to fleet-gateway: POST <FLEET_GATEWAY_URL>/v1/call/<connection>/<tool> with the run's token (FLEET_RUN_TOKEN) as
the bearer. The gateway holds the keys and records every call in its spool, so this client writes no record of its own. A
remote host adds the Cloudflare Access service token from FLEET_CF_ACCESS_FILE (default ~/.config/fleet/cf-access-machine.env).
"""
import json
import os
import re
import ssl
import sys
import urllib.error
import urllib.request
from pathlib import Path

sys.dont_write_bytecode = True
# Gateway reasons that leave the provider charge unknown; every other reason is a plain error.
UNKNOWN = {'provider-body', 'provider-timeout', 'client-timeout', 'invalid-response'}
TIMEOUT = 60


def access_headers():
    path = Path(os.environ.get('FLEET_CF_ACCESS_FILE') or Path.home() / '.config/fleet/cf-access-machine.env')
    try:
        values = dict(l.removeprefix('export ').strip().split('=', 1) for l in path.read_text().splitlines() if '=' in l)
    except OSError:
        return {}
    names = {'CF_ACCESS_CLIENT_ID': 'CF-Access-Client-Id', 'CF_ACCESS_CLIENT_SECRET': 'CF-Access-Client-Secret'}
    return {h: values[n].strip('\'"') for n, h in names.items() if n in values}


def tls_context():
    """python.org builds on macOS ship no CA bundle; the OS one lives here. Elsewhere the default store is right."""
    return ssl.create_default_context(cafile='/etc/ssl/cert.pem' if os.path.exists('/etc/ssl/cert.pem') else None)


def call(connection, tool, arguments):
    """One gateway call: {tool, callId, outcome: ok|error|unknown, error_code, usage, output}."""
    url, token = os.environ.get('FLEET_GATEWAY_URL', '').rstrip('/'), os.environ.get('FLEET_RUN_TOKEN', '')
    line = {'tool': tool, 'callId': None, 'outcome': 'error', 'error_code': None, 'usage': None, 'output': None}
    if not url or not token:
        return {**line, 'error_code': 'connection-unavailable'}
    headers = {'Authorization': 'Bearer ' + token, 'Content-Type': 'application/json', 'User-Agent': 'fleet-connection/1'}
    if url.startswith('https://'):
        headers.update(access_headers())
    request = urllib.request.Request(f'{url}/v1/call/{connection}/{tool}', json.dumps(arguments).encode(), headers, method='POST')
    try:
        with urllib.request.urlopen(request, timeout=TIMEOUT, context=tls_context() if url.startswith('https://') else None) as r:
            status, call_id, error, body = r.status, r.headers.get('x-fleet-call-id'), r.headers.get('x-fleet-error'), r.read()
    except urllib.error.HTTPError as e:
        status, call_id, error, body = e.code, e.headers.get('x-fleet-call-id'), e.headers.get('x-fleet-error'), e.read()
    except TimeoutError:
        return {**line, 'outcome': 'unknown', 'error_code': 'client-timeout'}
    except (urllib.error.URLError, OSError):
        return {**line, 'error_code': 'unreachable'}
    line['callId'] = call_id
    try:
        output = json.loads(body)
    except ValueError:
        output = {'text': body[:2000].decode('utf8', 'replace')}
        if 200 <= status < 300:
            return {**line, 'outcome': 'unknown', 'error_code': 'invalid-response', 'output': output}
    if 200 <= status < 300:
        return {**line, 'outcome': 'ok', 'output': output, 'usage': output.get('usage') if isinstance(output, dict) else None}
    # The gateway names its own refusals in x-fleet-error; a provider reply passed through is http-<status>.
    code = f'http-{status}' if not error or re.fullmatch(r'provider-\d+', error) else error
    return {**line, 'outcome': 'unknown' if code in UNKNOWN else 'error', 'error_code': code, 'output': output}


def mcp(connection, spec):
    tools = spec.get('tools', [])
    names = {t['name'] for t in tools}
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
            reply['result'] = {'protocolVersion': params.get('protocolVersion', '2025-06-18'), 'capabilities': {'tools': {}},
                               'serverInfo': {'name': connection, 'version': '1'}, 'instructions': spec.get('instructions', '')}
        elif method == 'ping':
            reply['result'] = {}
        elif method == 'tools/list':
            reply['result'] = {'tools': tools}
        elif method == 'tools/call':
            name = params.get('name')
            line = call(connection, name, params.get('arguments') or {}) if name in names else {'outcome': 'error', 'error_code': 'tool-not-granted', 'output': None}
            ok = line['outcome'] == 'ok'
            text = json.dumps(line['output']) if ok else f"Call {line['outcome']}: {line['error_code']}" + (f" {json.dumps(line['output'])}" if line.get('output') else '')
            reply['result'] = {'content': [{'type': 'text', 'text': text}], 'isError': not ok}
        else:
            reply['error'] = {'code': -32601, 'message': 'method not found'}
        print(json.dumps(reply), flush=True)


def main(argv):
    mode, connection, rest = argv[1], argv[2], argv[3]
    if mode == 'call':
        print(json.dumps(call(connection, rest, json.load(sys.stdin))))
    else:
        mcp(connection, json.loads(rest))


if __name__ == '__main__':
    main(sys.argv)
