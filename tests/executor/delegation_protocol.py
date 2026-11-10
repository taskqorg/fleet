"""Deterministic MCP/outbox protocol check. No native calls or network."""
# fleet-stack L4: preserve MCP grants and durable transport after replacing host Python.
from executor_binary import executor_binary
import json
import os
import sqlite3
import subprocess
import tempfile
import threading

with tempfile.TemporaryDirectory() as root:
    journal = root + '/journal.sqlite'
    db = sqlite3.connect(journal)
    db.executescript('CREATE TABLE outbox(route TEXT,body TEXT);CREATE TABLE delegation_requests(id TEXT PRIMARY KEY,body TEXT,response TEXT);')
    db.close()
    context = {'hostId': 'mac', 'commandId': 'trusted-command', 'executionId': 'trusted-execution', 'profiles': ['website-reviewer']}
    process = subprocess.Popen([executor_binary(), 'mcp', 'delegation', journal, json.dumps(context)], stdin=subprocess.PIPE, stdout=subprocess.PIPE, text=True)
    try:
        def call(method, params={}):
            process.stdin.write(json.dumps({'jsonrpc': '2.0', 'id': 1, 'method': method, 'params': params})+'\n');process.stdin.flush()
            return json.loads(process.stdout.readline())
        tools = call('tools/list')['result']['tools']
        # The child request never names a host (the coordinator places it); session_start may pin one on purpose.
        assert tools[0]['name']=='request_child' and 'hostId' not in json.dumps(tools[0])
        assert tools[0]['annotations']['readOnlyHint'] is False
        assert tools[0]['annotations']['openWorldHint'] is True
        def deliver():
            import time
            db = sqlite3.connect(journal)
            deadline = time.monotonic()+5
            while time.monotonic()<deadline:
                row = db.execute('SELECT body FROM outbox').fetchone()
                if row:
                    body = json.loads(row[0]);assert body['commandId']=='trusted-command'
                    with db: db.execute('UPDATE delegation_requests SET response=?', (json.dumps({'childExecutionId': 'durable-child'}),))
                    break
                time.sleep(.01)
            else: raise AssertionError('No durable outbox request')
            db.close()
        worker = threading.Thread(target=deliver);worker.start()
        params = {'name': 'request_child', 'arguments': {'requestId': 'one', 'profileId': 'website-reviewer', 'input': 'review actual sources'}}
        assert 'durable-child' in json.dumps(call('tools/call', params))
        worker.join()
        assert 'durable-child' in json.dumps(call('tools/call', params))
        db = sqlite3.connect(journal);assert db.execute('SELECT count(*) FROM outbox').fetchone()[0]==1;db.close()
        params['arguments']['input']='changed'
        assert 'error' in call('tools/call',params)
        params['arguments']['hostId']='forged'
        assert 'error' in call('tools/call',params)
    finally:
        process.stdin.close();process.wait(timeout=5)
print('MCP durable transport check passed')
