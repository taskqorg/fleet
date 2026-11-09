"""Use Pipe's existing read-lane client; never run its installer or personal config discovery."""
import importlib.util
import json
import os
from pathlib import Path
import stat
import sys

def private(path):
    p=Path(path)
    m=p.lstat()
    if not p.is_absolute() or p.is_symlink() or not stat.S_ISREG(m.st_mode) or m.st_uid!=os.getuid() or m.st_mode & 0o077:
        raise ValueError('private connection configuration required')
    return p

try:
    connection=json.loads(private(sys.argv[1]).read_text())
    client=Path(connection['clientPath'])
    if not client.is_absolute() or not client.is_file():
        raise ValueError('client unavailable')
    config=json.loads(private(connection['pipeConfigPath']).read_text())
    config['url']=config['url'].rstrip('/')
    spec=importlib.util.spec_from_file_location('fleet_pipe_client',client)
    module=importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    os.environ['PIPE_TASK']=sys.argv[2]
    result=module.answer({'method':'tools/call','params':{'name':'web_search','arguments':json.load(sys.stdin)}},config,'fleet')
    secrets=[config.get('token','')]
    if config.get('access_file'):
        secrets.extend(module.access_headers(config['access_file']).values())
    def redact(value):
        if isinstance(value,str):
            for secret in secrets:
                if secret:value=value.replace(secret,'[redacted]')
            return value
        if isinstance(value,list):return [redact(v) for v in value]
        if isinstance(value,dict):return {k:redact(v) for k,v in value.items()}
        return value
    print(json.dumps(redact(result)))
except Exception as error:
    print(json.dumps({'isError':True,'content':[{'type':'text','text':'Pipe connection unavailable: '+type(error).__name__}]}))
