#!/usr/bin/env python3
"""Scoped Fleet static-site program worker. Host destinations never come from model input."""
import hashlib, json, os, pathlib, pwd, re, shutil, ssl, subprocess, sys, time, urllib.parse, urllib.request

def run(argv, cwd=None):
    result=subprocess.run(argv,cwd=cwd,stdout=subprocess.PIPE,stderr=subprocess.PIPE,timeout=90)
    if result.returncode: raise RuntimeError('Program command failed: '+argv[0]+' exit '+str(result.returncode))
    return result.stdout.decode().strip()

def account_environment(binding,harness):
    env={'HOME':binding.get('credentialHome') if binding.get('credentialAuthority')=='native-current' else binding['home']}
    env[{'codex':'CODEX_HOME','claude':'CLAUDE_CONFIG_DIR','antigravity':'GEMINI_HOME'}[harness]]=binding['nativeHome']
    if any(not isinstance(v,str) or not pathlib.Path(v).is_absolute() for v in env.values()):raise RuntimeError('Invalid host-owned account roots')
    return env

def account_status(binding,harness):
    roots={k:binding[k] for k in ['home','nativeHome','credentialHome','credentialAuthority','executable'] if isinstance(binding.get(k),str)}
    result={'roots':roots,'readiness':'unknown','nativeLoginObserved':None,'quota':'unknown','reason':'status-unknown'}
    if not os.path.isfile(binding['executable']) or not os.access(binding['executable'],os.X_OK):return {**result,'readiness':'unavailable','reason':'executable-missing'}
    env={'PATH':os.environ['PATH'],**account_environment(binding,harness)}
    if harness=='claude':env.update(USER=pwd.getpwuid(os.geteuid()).pw_name,LOGNAME=pwd.getpwuid(os.geteuid()).pw_name)
    args=['login','status'] if harness=='codex' else ['auth','status','--json'] if harness=='claude' else ['models']
    try:observed=subprocess.run([binding['executable'],*args],env=env,stdin=subprocess.DEVNULL,stdout=subprocess.PIPE,stderr=subprocess.PIPE,timeout=30)
    except (OSError,subprocess.TimeoutExpired):return result
    if harness=='antigravity':return {**result,'reason':'catalogue-only' if observed.returncode==0 and b'gemini-' in observed.stdout else 'status-unknown'}
    if harness=='claude':
        try:login=json.loads(observed.stdout).get('loggedIn')
        except (ValueError,AttributeError):return result
        if not isinstance(login,bool):return result
    else:
        out=observed.stdout+observed.stderr
        if observed.returncode==0 and b'Logged in using' in out:login=True
        elif b'Not logged in' in out:login=False
        else:return result
    approved=binding.get('readiness')=='ready'
    return {**result,'nativeLoginObserved':login,'readiness':'ready' if login and approved else 'unavailable','reason':'ready' if login and approved else 'binding-unprovisioned' if login else 'login-absent'}

def main():
    path=pathlib.Path(sys.argv[1]); st=path.stat()
    if path.is_symlink() or st.st_uid!=os.getuid() or st.st_mode&0o077: raise RuntimeError('Private owned worker configuration required')
    config=json.loads(path.read_text())
    if config.get('executorConfig'):
        token=json.loads(pathlib.Path(config['executorConfig']).read_text())['FLEET_HOST_TOKEN']
    elif config.get('machineSecretFile'):
        token=next(line.split('=',1)[1].strip().strip('\"').strip("'") for line in pathlib.Path(config['machineSecretFile']).read_text().splitlines() if line.startswith('FLEET_MACHINE_SECRET='))
    else:token=config['machineSecret']
    journal=pathlib.Path(config['journal']); journal.mkdir(mode=0o700,parents=True,exist_ok=True)
    def api(route,body):
        req=urllib.request.Request(config['coordinator']+'/api/hosts/'+route,data=json.dumps(body).encode(),headers={'Authorization':'Bearer '+token,'Content-Type':'application/json','User-Agent':'Fleet-ProgramWorker/1'})
        with urllib.request.urlopen(req,timeout=30,context=tls) as r:return json.load(r)
    tls=ssl.create_default_context(cafile='/etc/ssl/cert.pem' if os.path.exists('/etc/ssl/cert.pem') else None)
    # Cloudflare Access service token for store tunnels; added to requests, never to presigned signatures (design section 3).
    access={}
    if config.get('cfAccessFile'):
        env=dict(line.removeprefix('export ').strip().split('=',1) for line in pathlib.Path(config['cfAccessFile']).read_text().splitlines() if '=' in line)
        access={'CF-Access-Client-Id':env['CF_ACCESS_CLIENT_ID'].strip('\'"'),'CF-Access-Client-Secret':env['CF_ACCESS_CLIENT_SECRET'].strip('\'"')}
    def store_request(url,method='GET',data=None):
        local=urllib.parse.urlsplit(url).hostname in ('127.0.0.1','localhost','::1')
        return urllib.request.urlopen(urllib.request.Request(url,data=data,method=method,headers={} if local else access),timeout=120,context=tls)
    # Artifacts: reserve, upload the parts to their presigned URLs, commit; when this host owns the store it also re-hashes
    # the staged object over loopback and reports the digest (P3-D1).
    def save(body,name,data,media):
        r=api('artifacts/reserve',{**body,'name':name,'mediaType':media,'size':len(data),'sha256':hashlib.sha256(data).hexdigest()})
        if r['state']=='stored':return r['artifactId']
        size=r['partSize'];numbers=list(range(1,max(1,-(-len(data)//size))+1));done=[]
        for n,u in [(p['partNumber'],p['url']) for p in api('artifacts/parts',{'hostId':body['hostId'],'artifactId':r['artifactId'],'partNumbers':numbers})['parts']]:
            with store_request(u,'PUT',data[(n-1)*size:n*size]) as reply:done.append({'partNumber':n,'etag':reply.headers['ETag']})
        c=api('artifacts/commit',{'hostId':body['hostId'],'artifactId':r['artifactId'],'parts':done})
        if c.get('verify'):
            digest=hashlib.sha256()
            with store_request(c['verify']['url']) as reply:
                for chunk in iter(lambda:reply.read(1<<20),b''):digest.update(chunk)
            api('artifacts/verified',{'hostId':body['hostId'],'artifactId':r['artifactId'],'storeId':c['verify']['storeId'],'sha256':digest.hexdigest()})
        return r['artifactId']
    def fetch(body,artifact_id):
        req=urllib.request.Request(config['coordinator']+'/api/hosts/artifacts/read',data=json.dumps({'hostId':body['hostId'],'commandId':body['commandId'],'artifactId':artifact_id}).encode(),headers={'Authorization':'Bearer '+token,'Content-Type':'application/json','User-Agent':'Fleet-ProgramWorker/1'})
        with urllib.request.urlopen(req,timeout=120,context=tls) as r:return r.read()
    while True:
        commands=api('poll',{'hostId':config['hostId'],'platform':sys.platform})['commands']
        for c in commands:
            if c['kind'] not in ['delivery-step','pipe-search','account-inspect']: raise RuntimeError('Unexpected command kind')
            body={'hostId':config['hostId'],'executionId':c['executionId'],'commandId':c['id']}
            state=journal/(c['id']+'.json')
            if state.exists():
                # An accepted effect may have happened. Never infer failure or replay it.
                api('receipts',{**body,'status':'unknown','error':'Worker journal exists; reconcile original effects before any retry'})
                continue
            state.write_text(json.dumps({'commandId':c['id'],'state':'accepted','at':time.time()}));state.chmod(0o600)
            api('receipts',{**body,'status':'accepted'});api('receipts',{**body,'status':'started'})
            workspace=journal/c['id']; workspace.mkdir(mode=0o700)
            started=time.time(); effect=False
            try:
                p=c['payload']
                if c['kind']=='account-inspect':
                    registry_path=pathlib.Path(config['nativeAccounts']);metadata=registry_path.stat()
                    if registry_path.is_symlink() or metadata.st_uid!=os.getuid() or metadata.st_mode&0o077:raise RuntimeError('Private owned native registry required')
                    registry=json.loads(registry_path.read_text());harness=p['harness']
                    if harness=='claude' and sys.platform not in ['darwin','win32']:raise RuntimeError('Claude placement denied')
                    if p.get('mode')=='catalogue':
                        bindings=[{'name':name,'executable':b['executable'],'environment':account_environment(b,harness),'behaviorApproved':b.get('readiness')=='ready'} for name,b in registry.items() if b.get('harness')==harness]
                        api('receipts',{**body,'accountCatalogue':{'accountId':p['accountId'],'bindings':bindings}})
                        result={'bindingCount':len(bindings),'authChanged':False}
                    else:
                        binding=registry.get(p['binding'])
                        result=account_status(binding,harness) if binding and binding.get('harness')==harness else {'readiness':'unavailable','reason':'binding-absent','nativeLoginObserved':None,'quota':'unknown'}
                        api('receipts',{**body,'accountObservation':{'accountId':p['accountId'],'binding':p['binding'],'platform':sys.platform,**result}})
                        result={**result,'accountId':p['accountId'],'binding':p['binding'],'authChanged':False}
                    save(body,'account-readiness.json',json.dumps(result).encode(),'application/json')
                    shutil.rmtree(workspace);state.write_text(json.dumps({'state':'completed'}));api('receipts',{**body,'status':'completed'});continue
                if c['kind']=='pipe-search':
                    effect=True
                    proc=subprocess.run(['python3',config['pipeWrapper'],config['pipeConnection'],p['runId']],input=json.dumps(p['arguments']).encode(),stdout=subprocess.PIPE,stderr=subprocess.PIPE,timeout=48,env={'PATH':os.environ['PATH']})
                    if proc.returncode:raise RuntimeError('Pipe host client failed')
                    value=json.loads(proc.stdout)
                    save(body,'pipe-response.json',json.dumps(value).encode(),'application/json')
                    shutil.rmtree(workspace);state.write_text(json.dumps({'state':'completed'}));api('receipts',{**body,'status':'completed'});continue
                project=config['projects'][p['projectId']]
                if p['kind'] not in config['kinds']:raise RuntimeError('Worker kind denied')
                for f in p['files']:
                    if not re.fullmatch(r'[A-Za-z0-9_-]+\.(html|css)',f['path']):raise RuntimeError('Static path denied')
                    content=fetch(body,f['artifactId'])
                    if hashlib.sha256(content).hexdigest()!=f['sha256']:raise RuntimeError('Material digest mismatch')
                    (workspace/f['path']).write_bytes(content)
                html=(workspace/'index.html').read_text() if not p.get('branch') else '<html>'
                if '<html' not in html.lower() or '<script' in html.lower() or re.search(r'(?:src|href)\s*=\s*["\'](?:https?:)?//',html,re.I):
                    # Source links are allowed; remotely loaded resources are not.
                    if '<html' not in html.lower() or '<script' in html.lower() or re.search(r'src\s*=\s*["\'](?:https?:)?//',html,re.I):raise RuntimeError('Static verification failed')
                result={'kind':p['kind'],'projectId':p['projectId'],'deliveryId':p['deliveryId'],'files':[{'path':f['path'],'sha256':f['sha256']} for f in p['files']]}
                if p['kind']=='integrate':
                    repo=workspace/'repo';run(['git','clone','--quiet',project['repository'],str(repo)])
                if p['kind']=='integrate' and p.get('branch'):
                    # Agent path: the session already pushed its branch; open a PR and merge it after checks.
                    integration={'mergeMethod':'squash','autoMerge':True,**(p.get('integration') or {})}
                    base=project.get('defaultBranch','main');branch=p['branch']
                    if not re.fullmatch(r'[A-Za-z0-9_.-]+/[0-9a-f-]{36}',branch):raise RuntimeError('Session branch denied')
                    head=run(['git','ls-remote','origin','refs/heads/'+branch],repo).split()
                    if not head:raise RuntimeError('Session branch not pushed')
                    result.update(commit=head[0],repository=project['repository'],verified=True,mode='pr',branch=branch)
                    effect=True
                    existing=json.loads(run(['gh','pr','list','--head',branch,'--state','all','--json','url'],repo))
                    url=existing[0]['url'] if existing else run(['gh','pr','create','--base',base,'--head',branch,'--title','Fleet session '+branch.split('/',1)[1],'--body','Fleet session delivery '+p['deliveryId']],repo).splitlines()[-1]
                    if integration['autoMerge']:
                        checks=subprocess.run(['gh','pr','checks',url,'--watch','--fail-fast','--interval','15'],cwd=repo,stdout=subprocess.PIPE,stderr=subprocess.STDOUT,timeout=1800)
                        if checks.returncode and b'no checks reported' not in checks.stdout:raise RuntimeError('PR checks did not pass; PR left open')
                        if json.loads(run(['gh','pr','view',url,'--json','state'],repo)).get('state')=='OPEN':run(['gh','pr','merge',url,'--'+integration['mergeMethod']],repo)
                    view=json.loads(run(['gh','pr','view',url,'--json','state,mergeCommit'],repo))
                    result.update(prUrl=url,prState=view.get('state'))
                    if view.get('state')=='MERGED':result.update(mergedSha=(view.get('mergeCommit') or {}).get('oid'))
                elif p['kind']=='integrate':
                    for f in p['files']:shutil.copyfile(workspace/f['path'],repo/f['path'])
                    if project.get('contextDirectory'):
                        context=pathlib.Path(project['contextDirectory']);(repo/'.fleet').mkdir(exist_ok=True)
                        for name in ['project.json','rules.md','reference.json']:shutil.copyfile(context/name,repo/'.fleet'/name)
                    run(['git','add','--']+[f['path'] for f in p['files']]+(['.fleet'] if project.get('contextDirectory') else []),repo)
                    who=p.get('identity') or {'name':'Fleet delivery','email':'fleet@ufobe.com'}
                    run(['git','-c','user.name='+who['name'],'-c','user.email='+who['email'],'commit','--allow-empty','-m','Publish Fleet delivery '+p['deliveryId']],repo)
                    commit=run(['git','rev-parse','HEAD'],repo)
                    # W3: integration.mode from project settings; pr is the default. GitHub only (gh); GitLab later.
                    integration={'mode':'pr','mergeMethod':'squash','autoMerge':True,**(p.get('integration') or {})}
                    base=project.get('defaultBranch','main'); branch=base if integration['mode']=='direct' else 'fleet/delivery-'+p['deliveryId']
                    # No retry if push acknowledgement is uncertain.
                    effect=True
                    run(['git','push','origin','HEAD:refs/heads/'+branch],repo)
                    remote=run(['git','ls-remote','origin','refs/heads/'+branch],repo).split()[0]
                    if remote!=commit:raise RuntimeError('Source readback mismatch')
                    result.update(commit=commit,repository=project['repository'],verified=True,mode=integration['mode'],branch=branch)
                    if integration['mode']=='direct':result.update(mergedSha=commit)
                    else:
                        url=run(['gh','pr','create','--base',base,'--head',branch,'--title','Fleet delivery '+p['deliveryId'],'--body','Fleet delivery '+p['deliveryId']],repo).splitlines()[-1]
                        # autoMerge: Fleet merges itself after checks pass (GitHub auto-merge is unavailable on free private repos).
                        if integration['autoMerge']:
                            checks=subprocess.run(['gh','pr','checks',url,'--watch','--fail-fast','--interval','15'],cwd=repo,stdout=subprocess.PIPE,stderr=subprocess.STDOUT,timeout=1800)
                            if checks.returncode and b'no checks reported' not in checks.stdout:raise RuntimeError('PR checks did not pass; PR left open')
                            run(['gh','pr','merge',url,'--'+integration['mergeMethod']],repo)
                        view=json.loads(run(['gh','pr','view',url,'--json','state,mergeCommit'],repo))
                        result.update(prUrl=url,prState=view.get('state'))
                        # Merged gives base-updated; autoMerge off leaves prState OPEN.
                        if view.get('state')=='MERGED':result.update(mergedSha=(view.get('mergeCommit') or {}).get('oid'))
                elif p['kind']=='publish-failure':raise RuntimeError('Controlled publication refusal: acceptance failure fixture; current version unchanged')
                else:
                    root=pathlib.Path(project['publicationRoot']);releases=root/'releases';releases.mkdir(parents=True,exist_ok=True)
                    target=releases/p['deliveryId']
                    if target.exists():raise RuntimeError('Release exists; reconcile before retry')
                    target.mkdir()
                    for f in p['files']:shutil.copyfile(workspace/f['path'],target/f['path'])
                    effect=True
                    link=root/('next-'+c['id']);link.symlink_to(target);os.replace(link,root/'current')
                    headers={}
                    if config.get('siteReadSecretFile'):
                        secret=next(line.split('=',1)[1].strip().strip('\"').strip("'") for line in pathlib.Path(config['siteReadSecretFile']).read_text().splitlines() if line.startswith('FLEET_BROWSER_SECRET='))
                        headers={'Authorization':'Bearer '+secret}
                    with urllib.request.urlopen(urllib.request.Request(project['checkUrl'],headers=headers),timeout=20) as response:observed=response.read()
                    if hashlib.sha256(observed).hexdigest()!=hashlib.sha256((target/'index.html').read_bytes()).hexdigest():raise RuntimeError('Published HTTP content readback mismatch')
                    result.update(url=project['publicUrl'],release=p['deliveryId'],httpVerified=True)
                shutil.rmtree(workspace)
                result.update(durationMs=round((time.time()-started)*1000),usage=None,workingAreaRemoved=True)
                save(body,'delivery-result.json',json.dumps(result,indent=2).encode(),'application/json')
                api('events',{**body,'events':[{'id':c['id']+':result','seq':0,'type':'program.result','data':result}]})
                state.write_text(json.dumps({'state':'effect-completed','result':result}));api('receipts',{**body,'status':'completed'})
            except Exception as e:
                message=str(e) if isinstance(e,RuntimeError) else type(e).__name__
                state.write_text(json.dumps({'state':'failed-or-uncertain','cause':message}));api('receipts',{**body,'status':'unknown' if effect else 'failed','error':message})
            finally:shutil.rmtree(workspace,ignore_errors=True)
        if '--once' in sys.argv:return
        time.sleep(2)
if __name__=='__main__':main()
