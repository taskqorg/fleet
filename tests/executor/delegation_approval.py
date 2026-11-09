"""Installed Codex config parsing only; no server start or model request."""
import json
import os
import pathlib
import subprocess
import tempfile

source = pathlib.Path('crates/executor/src/main.rs').read_text()
assert 'mcp_servers.fleet.enabled_tools=[\\"request_child\\"]' in source
assert 'mcp_servers.fleet.tools.request_child.approval_mode=\\"approve\\"' in source
assert 'default_tools_approval_mode' not in source
assert 'approval_policy=\\"never\\"' in pathlib.Path('crates/executor/src/native.rs').read_text()
with tempfile.TemporaryDirectory(prefix='fleet-approval-') as root:
    env = {k: v for k, v in os.environ.items() if k in ['PATH', 'TMPDIR', 'SYSTEMROOT']}
    pathlib.Path(root+'/codex').mkdir()
    env.update(HOME=root, CODEX_HOME=root+'/codex')
    args = ['codex', 'app-server', '--strict-config', '--print-startup-config', '-c', 'approval_policy="never"', '-c', 'mcp_servers.fleet.command="python3"', '-c', 'mcp_servers.fleet.enabled_tools=["request_child"]']
    good = subprocess.run(args+['-c', 'mcp_servers.fleet.tools.request_child.approval_mode="approve"'], env=env, capture_output=True, text=True, timeout=15)
    assert good.returncode == 0, good.stderr
    assert json.loads(good.stdout)['appServerVersion'] == '0.167.0'
    bad = subprocess.run(args+['-c', 'mcp_servers.fleet.tools.request_child.approval_mode="invalid-mode"'], env=env, capture_output=True, text=True, timeout=15)
    assert bad.returncode != 0
print('Installed 0.167.0 accepts exact per-tool mapping and rejects invalid mode; no native turn submitted')
