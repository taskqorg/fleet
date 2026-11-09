"""Fleet MCP adapter. Invocation identity comes from the executor, never tool arguments."""
import json
import sqlite3
import sys
import time

journal, context_json = sys.argv[1:]
context = json.loads(context_json)
db = sqlite3.connect(journal, timeout=5)
db.execute('PRAGMA synchronous=FULL')
for line in sys.stdin:
    req = {}
    try:
        req = json.loads(line)
        if 'id' not in req:
            continue
        method = req.get('method')
        if method == 'initialize':
            result = {'protocolVersion': req.get('params', {}).get('protocolVersion', '2024-11-05'), 'capabilities': {'tools': {}}, 'serverInfo': {'name': 'fleet-delegation', 'version': '1'}}
        elif method == 'tools/list':
            result = {'tools': [{'name': 'request_child', 'annotations': {'readOnlyHint': False, 'destructiveHint': False, 'idempotentHint': True, 'openWorldHint': True}, 'description': 'Submit separate Fleet work. Reuse requestId on retry. After acceptance finish your turn so the child can run; do not wait or claim completion.', 'inputSchema': {'type': 'object', 'additionalProperties': False, 'required': ['requestId', 'profileId', 'input'], 'properties': {'requestId': {'type': 'string', 'minLength': 1, 'maxLength': 200}, 'profileId': {'type': 'string', 'enum': context.get('profiles', [])}, 'input': {'type': 'string', 'minLength': 1, 'maxLength': 32000}}}}]}
            if not context.get('profiles'): result['tools'] = []
            if context.get('projectId'):
                for name in ['project_context','catalogue','pipeline_run','direct_search','pipeline_history','question_ask','question_history','account_list']:
                    props = {'requestId': {'type':'string'}, 'query': {'type':'string'}, 'methodId': {'type':'string'}, 'text': {'type':'string'}}
                    result['tools'].append({'name':name,'description':'Fleet scoped project operation; project identity supplied by host. Reuse requestId after uncertain submission.','inputSchema':{'type':'object','additionalProperties':False,'properties':props}})
        elif method == 'ping':
            result = {}
        elif method == 'tools/call' and (req['params'].get('name') == 'request_child' or context.get('projectId') and req['params'].get('name') in ['project_context','catalogue','pipeline_run','direct_search','pipeline_history','question_ask','question_history','account_list']):
            q = req['params'].get('arguments', {})
            operation = req['params']['name']
            if operation == 'request_child':
                if set(q) != {'requestId', 'profileId', 'input'} or q['profileId'] not in context.get('profiles',[]) or not all(isinstance(q[k], str) and 0 < len(q[k]) <= limit for k, limit in [('requestId', 200), ('input', 32000)]): raise ValueError('Invalid child request')
            else:
                if not isinstance(q,dict) or set(q)-{'requestId','query','methodId','text'}: raise ValueError('Invalid project request')
                q = {'operation':operation,'arguments':q,'requestId':q.get('requestId',operation),'projectId':context['projectId']}
            key = context['commandId'] + ':' + q['requestId']
            body = json.dumps({k: context[k] for k in ['hostId', 'commandId', 'executionId']} | {'delegation': q}, sort_keys=True)
            with db:
                old = db.execute('SELECT body FROM delegation_requests WHERE id=?', (key,)).fetchone()
                if old and old[0] != body:
                    raise ValueError('Request identity conflict')
                if not old:
                    db.execute('INSERT INTO delegation_requests(id,body) VALUES(?,?)', (key, body))
                    db.execute('INSERT INTO outbox(route,body) VALUES(?,?)', ('/api/hosts/receipts', body))
            deadline = time.monotonic() + 8
            response = None
            while time.monotonic() < deadline:
                row = db.execute('SELECT response FROM delegation_requests WHERE id=?', (key,)).fetchone()
                if row and row[0]:
                    response = json.loads(row[0]); break
                time.sleep(.05)
            result = {'content': [{'type': 'text', 'text': json.dumps(response if operation != 'request_child' else {'status': 'accepted' if response and 'childExecutionId' in response else 'pending-or-rejected', 'response': response, 'instruction': 'Finish this turn. Child work runs after your completed receipt. Retry only with the same requestId; submission is not completion.'})}], 'isError': bool(response and 'error' in response)}
        else:
            raise ValueError('Unsupported method')
        print(json.dumps({'jsonrpc': '2.0', 'id': req['id'], 'result': result}), flush=True)
    except (ValueError, TypeError, KeyError, sqlite3.Error) as error:
        print(json.dumps({'jsonrpc': '2.0', 'id': req.get('id'), 'error': {'code': -32602, 'message': str(error)}}), flush=True)
