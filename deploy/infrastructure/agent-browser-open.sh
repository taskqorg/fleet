#!/usr/bin/env bash
# Open the production Fleet origin in Orca; credentials never enter argv or output.
set -euo pipefail
[[ $# == 0 ]] || { echo 'Usage: deploy/infrastructure/agent-browser-open.sh' >&2; exit 2; }
(
  set -a
  . "$HOME/.config/ufobe-agents/site-access.env"
  python3 - <<'PY'
import json, os, shlex, signal, subprocess, tempfile

cli = os.environ.get('ORCA_CLI_COMMAND', 'orca')
def interrupted(*_):
    raise SystemExit('Browser setup interrupted.')

for sig in (signal.SIGTERM, signal.SIGHUP):
    signal.signal(sig, interrupted)

def call(*args):
    result = subprocess.run([cli, *args, '--environment', 'local', '--json'], capture_output=True, text=True,
                            env={k: v for k, v in os.environ.items() if k not in ('CF_ACCESS_CLIENT_ID', 'CF_ACCESS_CLIENT_SECRET')})
    try:
        data = json.loads(result.stdout)
    except ValueError:
        raise SystemExit('Orca returned no valid response; check that Orca is running.')
    if result.returncode or not data.get('ok'):
        raise SystemExit('Orca browser command failed; check Orca and the site access preflight.')
    return data['result']

headers = {'CF-Access-Client-Id': os.environ.get('CF_ACCESS_CLIENT_ID', ''),
           'CF-Access-Client-Secret': os.environ.get('CF_ACCESS_CLIENT_SECRET', '')}
if not all(headers.values()):
    raise SystemExit('Site access credentials are missing.')
page = call('tab', 'create', '--url', 'about:blank')['browserPageId']
fd, path = tempfile.mkstemp(prefix='fleet-browser-', suffix='.json')
try:
    with os.fdopen(fd, 'w') as stream:
        json.dump({'headers': json.dumps(headers)}, stream)
    call('exec', '--page', page, '--command', 'open https://fleet.ufobe.com --config ' + shlex.quote(path))
finally:
    os.unlink(path)
probe = "(async()=>{let r=await fetch('/api/ops/me',{method:'POST',headers:{'Content-Type':'application/json'},body:'{}'});return r.ok&&(await r.json()).email==='site-agents'})()"
result = call('eval', '--page', page, '--expression', probe)
if result.get('result') not in ('true', True):
    raise SystemExit('Fleet identity preflight failed; close this tab and check token expiry and access policy.')
print('Fleet opened directly as site-agents; browserPageId=' + page)
print('Use --page ' + page + ' on subsequent Orca commands; close the tab when finished.')
PY
)
