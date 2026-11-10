"""Test stand-in for connection_call.py `call` (pipelines.test.ts) and for fleet-gateway's spool. Usage:
fake_connection.py <answers dir> call <connection> <tool>. <answers dir>/<connection>/connection.json says how to answer:
{"answer": "ok"|"job"|"<error code>", "log": <file counting calls>, "jobState": <file>}; the gateway directory is <answers dir>/../gateway.
Like the gateway, it refuses a token the snapshot does not hold and appends one spool line per call."""
import hashlib, json, os, sys, time, uuid
from pathlib import Path
_, answers, mode, connection, tool = sys.argv[:5]
r = json.loads((Path(answers) / connection / 'connection.json').read_text())
args = json.load(sys.stdin)
token = hashlib.sha256(os.environ.get('FLEET_RUN_TOKEN', '').encode()).hexdigest()
gateway = Path(answers).parent / 'gateway'
snapshot = json.loads((gateway / 'snapshot.json').read_text())
run = next((t for t in snapshot['tokens'] if t['sha256'] == token and connection in t['connections']), None)
with open(r['log'], 'a') as f:
    f.write(json.dumps({'tool': tool, 'executionId': run and run['executionId'], 'arguments': args}) + '\n')
if run is None:
    out = {'outcome': 'error', 'error_code': 'unauthenticated', 'usage': None, 'output': {'error': 'unauthenticated'}}
elif tool == 'job_status':
    state = Path(r['jobState']).read_text().strip()
    out = {'outcome': 'ok', 'error_code': None, 'usage': None, 'output': {'state': state, 'output': {'model': 'm.glb'}, 'usage': {'credits': 7}}}
elif r['answer'] == 'job':
    out = {'outcome': 'ok', 'error_code': None, 'usage': None, 'output': {'jobId': 'job-1'}}
elif r['answer'] == 'ok':
    out = {'outcome': 'ok', 'error_code': None, 'usage': {'credits': 2}, 'output': {'results': [{'url': 'https://example.com/a', 'title': 'A'}], 'echo': args, 'usage': {'credits': 2}}}
else:
    code = r['answer']
    out = {'outcome': 'unknown' if code in ('provider-timeout', 'provider-body') else 'error', 'error_code': code, 'usage': None, 'output': {'error': code}}
call_id = str(uuid.uuid4())
if run is not None:
    spool = gateway / 'state' / 'spool'
    spool.mkdir(parents=True, exist_ok=True)
    credits = (out['usage'] or {}).get('credits')
    with open(spool / (time.strftime('%Y-%m-%d', time.gmtime()) + '.jsonl'), 'a') as f:
        f.write(json.dumps({'callId': call_id, 'executionId': run['executionId'], 'tokenSha256': token, 'connection': connection, 'tool': tool,
                            'outcome': out['outcome'], 'errorCode': out['error_code'], 'status': 200,
                            'usage': None if credits is None else {'unit': 'credits', 'amount': credits}, 'startedAt': 0, 'ms': 1}) + '\n')
print(json.dumps({'tool': tool, 'callId': call_id, **out}))
