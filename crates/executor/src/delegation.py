"""Fleet MCP adapter. Invocation identity comes from the executor, never tool arguments."""
import json
import sqlite3
import sys
import time
import uuid

journal, context_json = sys.argv[1:]
context = json.loads(context_json)
db = sqlite3.connect(journal, timeout=5)
db.execute('PRAGMA synchronous=FULL')
AGENT_TOOL_ARGS = {'session_start': ['projectId', 'requestId', 'role', 'profileId', 'account', 'hostId', 'text', 'setup'], 'session_deliver': ['sessionId', 'requestId'], 'ask': ['requestId', 'question', 'resume'], 'reply': ['questionId', 'answer'], 'message_send': ['sessionId', 'requestId', 'text', 'delivery'], 'session_fork': ['sessionId', 'requestId', 'text', 'strategy'], 'session_transition': ['sessionId', 'profileId', 'account'], 'worker_done': ['outcome', 'summary'], 'session_resume': ['sessionId'], 'session_handoffs': ['sessionId'], 'queue_list': ['sessionId'], 'questions': ['sessionId', 'state'], 'execution_status': ['sessionId']}
AGENT_TOOL_REQUIRED = {'session_start': ['requestId'], 'ask': ['requestId', 'question'], 'reply': ['questionId', 'answer'], 'message_send': ['requestId', 'text'], 'session_fork': ['requestId'], 'session_transition': ['profileId'], 'worker_done': ['outcome']}
AGENT_TOOL_DESC = {'session_start': 'Start a new Fleet session in your project: requestId, role (pm|worker|reviewer from the project roles) or profileId, optional text (first turn), setup run|skip. You are woken with a message when it finishes.', 'session_deliver': 'Ask Fleet to open a PR from a project session branch (fleet/<sessionId>, already pushed) and merge it after checks. Call again with the same sessionId to read the state.', 'execution_status': 'State of your execution, or with sessionId the latest execution of a session you started'}
AGENT_TOOLS = ['session_start', 'session_deliver', 'session_transition', 'session_fork', 'session_resume', 'session_handoffs', 'message_send', 'queue_list', 'ask', 'reply', 'questions', 'worker_done', 'execution_status']
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
                for name in ['project_context','catalogue','pipeline_run','direct_search','pipeline_history','pipeline_get','question_ask','question_history','account_list']:
                    props = {'requestId': {'type':'string'}, 'query': {'type':'string'}, 'methodId': {'type':'string'}, 'text': {'type':'string'}, 'runId': {'type':'string'}}
                    result['tools'].append({'name':name,'annotations':{'readOnlyHint':name not in ['pipeline_run','direct_search','question_ask'],'destructiveHint':False,'idempotentHint':True,'openWorldHint':name in ['pipeline_run','direct_search']},'description':'Fleet scoped project operation; host supplies project identity. Effect calls require a stable requestId. A search may return a durable running run; read pipeline_get by runId, never redispatch after an uncertain response.','inputSchema':{'type':'object','additionalProperties':False,'properties':props}})
            for name in AGENT_TOOLS:
                result['tools'].append({'name': name, 'description': AGENT_TOOL_DESC.get(name, 'Fleet agent session tool ' + name + ' (bound to this session by the host)'), 'inputSchema': {'type': 'object', 'properties': {k: {'type': 'string'} for k in AGENT_TOOL_ARGS.get(name, [])}, 'required': AGENT_TOOL_REQUIRED.get(name, [])}})
        elif method == 'tools/call' and req['params'].get('name') in AGENT_TOOLS:
            key = context['commandId'] + ':agent:' + str(uuid.uuid4())
            body = json.dumps({k: context[k] for k in ['hostId', 'commandId', 'executionId']} | {'agentToolKey': key, 'request': {'jsonrpc': '2.0', 'id': 1, 'method': 'tools/call', 'params': {'name': req['params']['name'], 'arguments': req['params'].get('arguments') or {}}}}, sort_keys=True)
            with db:
                db.execute('INSERT INTO delegation_requests(id,body) VALUES(?,?)', (key, body))
                db.execute('INSERT INTO outbox(route,body) VALUES(?,?)', ('/api/hosts/agent-tools', body))
            deadline = time.monotonic() + 20
            response = None
            while time.monotonic() < deadline:
                row = db.execute('SELECT response FROM delegation_requests WHERE id=?', (key,)).fetchone()
                if row and row[0]:
                    response = json.loads(row[0]); break
                time.sleep(.05)
            if response and 'result' in response:
                result = response['result']
            else:
                result = {'content': [{'type': 'text', 'text': json.dumps(response or {'status': 'pending', 'instruction': 'Outcome unknown; inspect with execution_status before repeating effects.'})}], 'isError': True}
        elif method == 'ping':
            result = {}
        elif method == 'tools/call' and (req['params'].get('name') == 'request_child' or context.get('projectId') and req['params'].get('name') in ['project_context','catalogue','pipeline_run','direct_search','pipeline_history','pipeline_get','question_ask','question_history','account_list']):
            q = req['params'].get('arguments', {})
            operation = req['params']['name']
            if operation == 'request_child':
                if set(q) != {'requestId', 'profileId', 'input'} or q['profileId'] not in context.get('profiles',[]) or not all(isinstance(q[k], str) and 0 < len(q[k]) <= limit for k, limit in [('requestId', 200), ('input', 32000)]): raise ValueError('Invalid child request')
            else:
                if not isinstance(q,dict) or set(q)-{'requestId','query','methodId','text','runId'}: raise ValueError('Invalid project request')
                if operation in ['pipeline_run','direct_search','question_ask'] and not isinstance(q.get('requestId'),str): raise ValueError('A stable requestId is required for effects')
                request_id=q['requestId'] if operation in ['pipeline_run','direct_search','question_ask'] else operation+'-'+str(uuid.uuid4())
                q = {'operation':operation,'arguments':q,'requestId':request_id,'projectId':context['projectId']}
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
