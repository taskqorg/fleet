"""Test stand-in for connection_call.py `call` (pipelines.test.ts). The connection record says how to answer:
{"answer": "ok"|"job"|"<error code>", "log": <file counting calls>, "jobState": <file holding running|succeeded>}."""
import json, sys, uuid
from pathlib import Path
_, mode, record, label, calls, tool = sys.argv[:6]
r = json.loads(Path(record).read_text())
args = json.load(sys.stdin)
with open(r['log'], 'a') as f:
    f.write(json.dumps({'tool': tool, 'label': label, 'arguments': args}) + '\n')
if tool == 'job_status':
    state = Path(r['jobState']).read_text().strip()
    out = {'outcome': 'ok', 'error_code': None, 'usage': None, 'output': {'state': state, 'output': {'model': 'm.glb'}, 'usage': {'credits': 7}}}
elif r['answer'] == 'job':
    out = {'outcome': 'ok', 'error_code': None, 'usage': None, 'output': {'jobId': 'job-1'}}
elif r['answer'] == 'ok':
    out = {'outcome': 'ok', 'error_code': None, 'usage': {'credits': 2}, 'output': {'results': [{'url': 'https://example.com/a', 'title': 'A'}], 'echo': args, 'usage': {'credits': 2}}}
else:
    code = r['answer']
    out = {'outcome': 'unknown' if code in ('provider-timeout', 'provider-body') else 'error', 'error_code': code, 'usage': None, 'output': {'error': code}}
Path(calls).mkdir(parents=True, exist_ok=True)
with open(Path(calls) / '.connection-calls.jsonl', 'a') as f:
    f.write(json.dumps({'tool': tool, 'request_id': str(uuid.uuid4()), 'label': label, 'outcome': out['outcome'], 'error_code': out['error_code'], 'usage': out['usage']}) + '\n')
print(json.dumps({'tool': tool, **out}))
