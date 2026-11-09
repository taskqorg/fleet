"""Read-only, newline JSON-RPC MCP. Data is supplied by the host adapter."""
import json
import sys

rules = json.loads(sys.argv[1])
for line in sys.stdin:
    try:
        request = json.loads(line)
        if 'id' not in request:
            continue
        method = request.get('method')
        params = request.get('params', {})
        error = None
        if method == 'initialize':
            result = {'protocolVersion': params.get('protocolVersion', '2024-11-05'), 'capabilities': {'tools': {}}, 'serverInfo': {'name': 'fleet-design-rules', 'version': '1.0.0'}}
        elif method == 'ping':
            result = {}
        elif method == 'tools/list':
            result = {'tools': [{'name': 'get_design_rules', 'description': 'Read project website design rules.', 'inputSchema': {'type': 'object', 'properties': {}, 'additionalProperties': False}, 'annotations': {'readOnlyHint': True, 'destructiveHint': False, 'idempotentHint': True}}]}
        elif method == 'tools/call' and params.get('name') == 'get_design_rules' and params.get('arguments', {}) == {}:
            result = {'content': [{'type': 'text', 'text': json.dumps(rules, sort_keys=True)}], 'isError': False}
        else:
            error = {'code': -32601, 'message': 'Unsupported method, tool or arguments'}
        response = {'jsonrpc': '2.0', 'id': request['id']}
        response.update({'error': error} if error else {'result': result})
        print(json.dumps(response), flush=True)
    except (ValueError, TypeError, KeyError):
        print(json.dumps({'jsonrpc': '2.0', 'id': None, 'error': {'code': -32700, 'message': 'Invalid request'}}), flush=True)
