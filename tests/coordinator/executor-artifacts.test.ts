// End to end on one host: the real executor binary, the coordinator and an S3 store (the in-process fake). The executor
// registers its store through its outbox, a fake Codex writes a 48 MiB model and a page into FLEET_OUTPUT_DIR, the executor
// collects them into its spool, uploads the parts, re-hashes over loopback, and the run completes once both are stored.
import {test} from 'node:test';import assert from 'node:assert/strict';import {createHash} from 'node:crypto';import {execFileSync,spawn} from 'node:child_process';
import {existsSync,mkdtempSync,writeFileSync,chmodSync,mkdirSync} from 'node:fs';import {tmpdir} from 'node:os';import {resolve} from 'node:path';
import {createApp} from './test-app.ts';import {fakeStore} from './s3-fake.ts';
import {executorBinary} from './executor-binary.ts';
let binary='';
const FAKE_CODEX=`#!/usr/bin/env python3
import json,os,pathlib,sys
for line in sys.stdin:
 q=json.loads(line)
 if q.get('method')=='initialize':print(json.dumps({'id':q['id'],'result':{'userAgent':'codex/fake'}}),flush=True)
 elif q.get('method') in ['thread/start','thread/resume']:print(json.dumps({'id':q['id'],'result':{'thread':{'id':'fake-thread'}}}),flush=True)
 elif q.get('method')=='turn/start':
  print(json.dumps({'id':q['id'],'result':{'turn':{'id':'fake-turn'}}}),flush=True)
  out=pathlib.Path(os.environ['FLEET_OUTPUT_DIR']);out.mkdir(parents=True,exist_ok=True)
  (out/'index.html').write_text('<h1>model</h1>')
  with open(out/'model.glb','wb') as f:
   for i in range(48):f.write(bytes([i])*(1<<20))
  print(json.dumps({'method':'item/completed','params':{'threadId':'fake-thread','turnId':'fake-turn','item':{'type':'agentMessage','id':'m','text':'done'}}}),flush=True)
  print(json.dumps({'method':'turn/completed','params':{'threadId':'fake-thread','turn':{'id':'fake-turn','status':'completed'}}}),flush=True)
  break
`;
const until=async<T>(what:string,f:()=>Promise<T|undefined|false>,ms=120000)=>{const end=Date.now()+ms;for(;;){const v=await f();if(v)return v;if(Date.now()>end)throw Error('timeout: '+what);await new Promise(r=>setTimeout(r,250))}};
test('the executor collects output, uploads it to its own store and the run completes with stored artifacts',{timeout:900000},async()=>{
 binary=executorBinary();
 const root=mkdtempSync(tmpdir()+'/fleet-e2e-'),fake=await fakeStore();mkdirSync(root+'/home',{mode:0o700});mkdirSync(root+'/account',{mode:0o700});
 writeFileSync(root+'/codex',FAKE_CODEX);chmodSync(root+'/codex',0o755);
 writeFileSync(root+'/store.key',JSON.stringify({accessKeyId:'k',secretAccessKey:'s'}),{mode:0o600});
 writeFileSync(root+'/store.json',JSON.stringify({id:'e2e-store',zone:'e2e',bucket:'fleet',endpoint:fake.url,localEndpoint:fake.url,keyFile:root+'/store.key',capacityBytes:1e12,backingPath:root}));
 const app=createApp({database:'memory://',browserSecret:'b',machineSecret:'m',hostId:'e2e',storeEndpointHosts:['127.0.0.1'],placementIntervalMs:0});await app.listen({port:0,host:'127.0.0.1'});
 const executor=spawn(binary,[],{env:{PATH:process.env.PATH,HOME:root+'/home',FLEET_COORDINATOR_URL:app.listeningOrigin,FLEET_HOST_ID:'e2e',FLEET_HOST_TOKEN:'m',FLEET_DATA_DIR:root+'/data',FLEET_CODEX_BIN:root+'/codex',FLEET_NATIVE_HOME:root+'/home',FLEET_CODEX_HOME:root+'/account',FLEET_STORE_CONFIG:root+'/store.json',FLEET_CF_ACCESS_FILE:root+'/none'},stdio:['ignore','ignore','pipe']});
 let stderr='';executor.stderr.on('data',d=>stderr+=d);
 const api=async(url:string,payload?:any)=>(await app.inject({method:payload?'POST':'GET',url,headers:{authorization:'Bearer b'},payload})).json();
 try{
  await until('store registration',async()=>(await api('/api/ops/stores_list',{})).stores.find((s:any)=>s.id==='e2e-store'&&s.state==='reachable'));
  const session=(await api('/api/sessions',{})).sessionId;const e=await api('/api/sessions/'+session+'/messages',{requestId:'r1',text:'make a model'});
  const done=await until('run completed',async()=>{const s=await api('/api/sessions/'+session);const x=s.executions.find((x:any)=>x.id===e.id);return ['completed','failed'].includes(x?.state)&&s});
  const run=done.executions.find((x:any)=>x.id===e.id);assert.equal(run.state,'completed',JSON.stringify(done.events.map((x:any)=>[x.type,x.data]))+stderr);
  const results=await until('both artifacts stored',async()=>{const r=(await api('/api/sessions/'+session)).results;return r.length===2&&r});
  const model=results.find((r:any)=>r.path==='model.glb'),expected=Buffer.concat(Array.from({length:48},(_,i)=>Buffer.alloc(1<<20,i)));
  assert.equal(model.sha256,createHash('sha256').update(expected).digest('hex'));assert.equal(model.mediaType,'model/gltf-binary');
  assert.ok(fake.objects.get(`_/prod/${model.sha256}`)?.equals(expected),'model in the store under its content key');
  assert.ok(![...fake.objects.keys()].some(k=>k.includes('.pending/')),'no staging object left');
  assert.ok(fake.requests.filter(r=>r.method==='PUT'&&r.url.includes('partNumber=')).length>=2,'multipart');
  const page=(await app.inject({url:'/api/results/'+encodeURIComponent(results.find((r:any)=>r.path==='index.html').id),headers:{authorization:'Bearer b'}}));assert.equal(page.body,'<h1>model</h1>');
  assert.ok(!fake.requests.some(r=>r.headers['cf-access-client-id']),'loopback requests carry no Access headers');
 }finally{executor.kill('SIGTERM');await new Promise(r=>executor.once('close',r));await app.close()}});
