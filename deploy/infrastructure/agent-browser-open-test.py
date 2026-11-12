"""Requirement: Fleet-only browser access. Risk: credentials in argv/env or retained config."""
import json
import os
from pathlib import Path
import subprocess
import tempfile

helper = Path(__file__).with_name('agent-browser-open.sh').resolve()
with tempfile.TemporaryDirectory() as directory:
    root = Path(directory)
    config = root / '.config/ufobe-agents'
    config.mkdir(parents=True)
    (config / 'site-access.env').write_text('CF_ACCESS_CLIENT_ID=invented-id\nCF_ACCESS_CLIENT_SECRET=invented-secret\n')
    fake = root / 'orca'
    fake.write_text('''#!/usr/bin/env python3
import json,os,sys,shlex,stat
from pathlib import Path
args=sys.argv[1:]
assert 'invented-secret' not in str(args)
assert 'CF_ACCESS_CLIENT_SECRET' not in os.environ
assert args[-3:]==['--environment','local','--json']
result={}
if args[0]=='tab':result={'browserPageId':'invented-page'}
if args[0]=='exec':
 command=shlex.split(args[args.index('--command')+1])
 assert command[:3]==['open','https://fleet.ufobe.com','--config']
 path=Path(command[3]);assert stat.S_IMODE(path.stat().st_mode)==0o600
 assert json.loads(json.loads(path.read_text())['headers'])=={'CF-Access-Client-Id':'invented-id','CF-Access-Client-Secret':'invented-secret'}
 Path(os.environ['HOME'],'temporary-path').write_text(str(path))
if args[0]=='eval':
 assert not Path(Path(os.environ['HOME'],'temporary-path').read_text()).exists()
 result={'result':'true'}
print(json.dumps({'ok':True,'result':result}))
''')
    fake.chmod(0o700)
    result = subprocess.run(['bash', str(helper)], env={**os.environ, 'HOME': directory, 'ORCA_CLI_COMMAND': str(fake)}, capture_output=True, text=True)
    assert result.returncode == 0, result.stderr
    assert 'invented-secret' not in result.stdout + result.stderr
    assert 'browserPageId=invented-page' in result.stdout
print('ok: fixed origin, local host, secret-safe argv/environment, 0600 config and deletion')
