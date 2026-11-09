"""Fleet MCP adapter. Invocation identity comes from the executor, never tool arguments."""
import json
import sqlite3
import sys
import time
import uuid
import mimetypes
import os
import shutil

journal, context_json = sys.argv[1:]
context = json.loads(context_json)
db = sqlite3.connect(journal, timeout=5)
db.execute('PRAGMA synchronous=FULL')
AGENT_TOOL_ARGS = {'session_start': ['projectId', 'requestId', 'role', 'profileId', 'account', 'hostId', 'text', 'setup'], 'session_deliver': ['sessionId', 'requestId'], 'ask': ['requestId', 'question', 'resume'], 'reply': ['questionId', 'answer'], 'message_send': ['sessionId', 'requestId', 'text', 'delivery'], 'session_fork': ['sessionId', 'requestId', 'text', 'strategy'], 'session_transition': ['sessionId', 'profileId', 'account'], 'worker_done': ['outcome', 'summary'], 'session_resume': ['sessionId'], 'session_handoffs': ['sessionId'], 'queue_list': ['sessionId'], 'questions': ['sessionId', 'state'], 'execution_status': ['sessionId']}
AGENT_TOOL_REQUIRED = {'session_start': ['requestId'], 'ask': ['requestId', 'question'], 'reply': ['questionId', 'answer'], 'message_send': ['requestId', 'text'], 'session_fork': ['requestId'], 'session_transition': ['profileId'], 'worker_done': ['outcome']}
AGENT_TOOL_DESC = {'session_start': 'Start a new Fleet session in your project: requestId, role (pm|worker|reviewer from the project roles) or profileId, optional text (first turn), setup run|skip. You are woken with a message when it finishes.', 'session_deliver': 'Ask Fleet to open a PR from a project session branch (fleet/<sessionId>, already pushed) and merge it after checks. Call again with the same sessionId to read the state.', 'execution_status': 'State of your execution, or with sessionId the latest execution of a session you started'}
# Artifact tools run on this host (design section 8): the executor's transfer worker uploads and downloads; the agent never
# receives store URLs or the Access token. artifact_save takes a path inside the working area.
ARTIFACT_TOOLS = {
 'artifact_save': {'description': 'Store a file of your working area as an artifact of this execution now (otherwise FLEET_OUTPUT_DIR is collected when the run ends). required=true makes this run wait for it.', 'properties': {'path': {'type': 'string'}, 'name': {'type': 'string'}, 'media_type': {'type': 'string'}, 'required': {'type': 'boolean'}}, 'required': ['path']},
 'artifact_list': {'description': 'List artifact metadata of this project (or of an execution or task). Never returns content.', 'properties': {'execution_id': {'type': 'string'}, 'task_id': {'type': 'string'}, 'media_type': {'type': 'string'}}, 'required': []},
 'artifact_get': {'description': 'Download an artifact into FLEET_INPUT_DIR and return its local path and metadata.', 'properties': {'artifact_id': {'type': 'string'}}, 'required': ['artifact_id']},
}
def transfer(kind, body, wait, until):
    key = str(uuid.uuid4())
    with db:
        db.execute("INSERT INTO transfers(id,kind,body,created_at) VALUES(?,?,?,?)", (key, kind, json.dumps(body), int(time.time() * 1000)))
    deadline = time.monotonic() + wait
    while time.monotonic() < deadline:
        row = db.execute('SELECT state,response FROM transfers WHERE id=?', (key,)).fetchone()
        if row and row[1]:
            r = json.loads(row[1])
            if row[0] == 'failed' or until(row[0], r): return {**r, 'transferId': key}
        time.sleep(.1)
    return {'state': 'pending', 'transferId': key, 'instruction': 'Still in progress on this host; list artifacts later.'}
def artifact_tool(name, q):
    if name == 'artifact_save':
        # Open component by component from the working area, never following a link, and copy from that open file.
        parts = [p for p in q['path'].split('/') if p not in ('', '.')]
        if not parts or '..' in parts: raise ValueError('path must be a file inside your working area')
        fd = os.open(context['workdir'], os.O_RDONLY | os.O_DIRECTORY)
        try:
            for p in parts[:-1]:
                nxt = os.open(p, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW, dir_fd=fd); os.close(fd); fd = nxt
            src = os.open(parts[-1], os.O_RDONLY | os.O_NOFOLLOW, dir_fd=fd)
        finally:
            os.close(fd)
        try:
            import stat as _stat
            if not _stat.S_ISREG(os.fstat(src).st_mode): raise ValueError('path must be a regular file')
            key = str(uuid.uuid4()); os.makedirs(context['spool'], exist_ok=True); spool = os.path.join(context['spool'], key)
            with os.fdopen(os.dup(src), 'rb') as fin, open(spool, 'xb') as fout: shutil.copyfileobj(fin, fout)
        finally:
            os.close(src)
        path = os.path.join(context['workdir'], *parts); root = context['workdir']
        body = {'path': spool, 'spool': True, 'previews': True, 'name': q.get('name') or os.path.relpath(path, root), 'mediaType': q.get('media_type') or mimetypes.guess_type(path)[0] or 'application/octet-stream', 'executionId': context['executionId'], 'required': q.get('required') is True}
        with db:
            db.execute("INSERT INTO transfers(id,kind,body,created_at) VALUES(?,?,?,?)", (key, 'upload', json.dumps(body), int(time.time() * 1000)))
        deadline = time.monotonic() + 30
        while time.monotonic() < deadline:
            row = db.execute('SELECT state,response FROM transfers WHERE id=?', (key,)).fetchone()
            if row and row[1] and ('artifactId' in json.loads(row[1]) or row[0] == 'failed'): return json.loads(row[1])
            time.sleep(.1)
        return {'artifactId': key, 'state': 'pending'}
    if name == 'artifact_list':
        return transfer('list', {'executionId': context['executionId'], 'filter': {'executionId': q.get('execution_id'), 'taskId': q.get('task_id'), 'mediaType': q.get('media_type')}}, 20, lambda s, r: s == 'done')
    return transfer('get', {'executionId': context['executionId'], 'artifactId': q['artifact_id'], 'inputDir': context['inputDir']}, 600, lambda s, r: s == 'done')
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
            if context.get('workdir'):
                for name, spec in ARTIFACT_TOOLS.items():
                    result['tools'].append({'name': name, 'description': spec['description'], 'annotations': {'readOnlyHint': name != 'artifact_save', 'destructiveHint': False, 'idempotentHint': name != 'artifact_save', 'openWorldHint': False}, 'inputSchema': {'type': 'object', 'properties': spec['properties'], 'required': spec['required'], 'additionalProperties': False}})
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
        elif method == 'tools/call' and req['params'].get('name') in ARTIFACT_TOOLS and context.get('workdir'):
            q = req['params'].get('arguments') or {}
            spec = ARTIFACT_TOOLS[req['params']['name']]
            if not isinstance(q, dict) or set(q) - set(spec['properties']) or any(k not in q for k in spec['required']) or any(not isinstance(v, (str, bool)) for v in q.values()): raise ValueError('Invalid artifact tool arguments')
            try:
                result = {'content': [{'type': 'text', 'text': json.dumps(artifact_tool(req['params']['name'], q))}]}
            except (OSError, ValueError) as error:
                result = {'content': [{'type': 'text', 'text': json.dumps({'error': 'artifact tool refused: ' + (str(error) if isinstance(error, ValueError) else 'path must be a regular file inside your working area, not a link')})}], 'isError': True}
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
