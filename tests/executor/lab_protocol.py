"""Read-only MCP wire regression; no model calls."""
# fleet-stack L4: preserve the read-only design-rules MCP wire after replacing host Python.
from executor_binary import executor_binary
import json
import subprocess
from pathlib import Path
root = Path(__file__).resolve().parents[2]
data = json.loads((root/'config/agents/design-rules-data.json').read_text())
requests = [
 {'jsonrpc':'2.0','id':1,'method':'initialize','params':{'protocolVersion':'2024-11-05'}},
 {'jsonrpc':'2.0','method':'notifications/initialized'},
 {'jsonrpc':'2.0','id':2,'method':'tools/list'},
 {'jsonrpc':'2.0','id':3,'method':'tools/call','params':{'name':'get_design_rules','arguments':{}}},
 {'jsonrpc':'2.0','id':4,'method':'tools/call','params':{'name':'write_rules','arguments':{}}}]
p = subprocess.run([executor_binary(),'mcp','design-rules',json.dumps(data)],input=''.join(json.dumps(r)+'\n' for r in requests),text=True,capture_output=True,timeout=5,check=True)
r = [json.loads(l) for l in p.stdout.splitlines()]
assert len(r)==4 and r[0]['result']['capabilities']=={'tools':{}}
assert r[1]['result']['tools'][0]['name']=='get_design_rules'
assert json.loads(r[2]['result']['content'][0]['text'])==data
assert r[3]['error']['code']==-32601
print('MCP initialize/list/call/refusal passed')
