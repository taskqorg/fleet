import {recovery} from './recovery.js';
import Fastify from 'fastify';
import Database from 'better-sqlite3';
import {randomUUID,timingSafeEqual,createHash} from 'node:crypto';
import {mkdirSync,readFileSync,realpathSync} from 'node:fs';
import {dirname,resolve,sep} from 'node:path';
import {registerLab,profileDigest,readTrustedProfile,listProfiles} from './lab.js';
import {delegation} from './delegation.js';
import {registerProjects} from './projects.js';
import {accounts} from './accounts.js';
import {delivery} from './delivery.js';
import {ui} from './ui.js';
import {agentTools} from './agent-tools.js';
export function createApp(config:{database:string,browserSecret:string,machineSecret:string,hostId:string,browserOrigin?:string,browserProxyAuth?:boolean,profilesDirectory?:string,maxArtifactBytes?:number,projectConfigDirectory?:string,pipeConnectionFile?:string,integrationHostId?:string,publicationHostId?:string,testSiteRoot?:string,defaultAccountBinding?:string}) {
 if(!config.browserSecret||!config.machineSecret||config.browserSecret===config.machineSecret) throw Error('Distinct credentials required');
 if(config.browserProxyAuth&&!config.browserOrigin)throw Error('Proxy browser auth requires configured browser origin');
 mkdirSync(dirname(config.database),{recursive:true});
 const db=new Database(config.database); db.pragma('journal_mode = WAL'); db.pragma('synchronous = FULL');
 db.exec(`CREATE TABLE IF NOT EXISTS records(kind TEXT,id TEXT PRIMARY KEY,data TEXT);`);
 const all=(kind:string):any[]=>db.prepare('SELECT data FROM records WHERE kind=? ORDER BY rowid').all(kind).map((r:any)=>JSON.parse(r.data));
 const get=(id:string,kind:string):any=>{const r:any=db.prepare('SELECT data FROM records WHERE id=? AND kind=?').get(id,kind);if(!r) throw Object.assign(Error('Not found'),{statusCode:404});return JSON.parse(r.data)};
 const put=(kind:string,v:any)=>db.prepare('INSERT INTO records VALUES(?,?,?) ON CONFLICT(id) DO UPDATE SET data=excluded.data WHERE records.kind=excluded.kind').run(kind,v.id,JSON.stringify(v));
 const maxArtifactBytes=config.maxArtifactBytes??16*1024*1024;
 if(!Number.isSafeInteger(maxArtifactBytes)||maxArtifactBytes<1)throw Error('Invalid artifact bound');
 const app=Fastify({bodyLimit:Math.ceil(maxArtifactBytes*4/3)+65536});
 const fail=(code:number,message:string)=>{throw Object.assign(Error(message),{statusCode:code})};
 const text=(v:any,max=32000):string=>typeof v==='string'&&v.length>0&&v.length<=max?v:fail(400,'Invalid text');
 const equal=(a:string,b:string)=>Buffer.byteLength(a)===Buffer.byteLength(b)&&timingSafeEqual(Buffer.from(a),Buffer.from(b));
 app.addHook('onRequest',async(req,reply)=>{
  reply.header('X-Content-Type-Options','nosniff').header('Referrer-Policy','no-referrer');
  if(req.url.split('?')[0]==='/'||(['/lab','/projects','/recovery','/accounts'].includes(req.url.split('?')[0])&&!config.browserProxyAuth)) return;
  const machine=req.url.startsWith('/api/hosts/');
  const supplied=(req.headers.authorization||'').replace(/^Bearer /,'');
  if(!equal(supplied,machine?config.machineSecret:config.browserSecret)) fail(401,'Unauthorized');
  if(!machine&&config.browserProxyAuth&&!['GET','HEAD','OPTIONS'].includes(req.method)&&req.headers.origin!==config.browserOrigin)fail(403,'Origin required');
  if(!machine&&req.headers.origin&&req.headers.origin!==(config.browserOrigin??`${req.protocol}://${req.headers.host}`)) fail(403,'Origin rejected');
 });
 let deliveryRuntime:any,agent:any;
 const related=(d:any)=>({...d,parentState:get(d.parentExecutionId,'execution').state,childState:get(d.childExecutionId,'execution').state,continuationState:d.continuationExecutionId?get(d.continuationExecutionId,'execution').state:null});
 const owned=(b:any)=>{if(![config.hostId,config.integrationHostId,config.publicationHostId].filter(Boolean).includes(b.hostId)) fail(403,'Host rejected'); if(b.executionId&&get(b.executionId,'execution').hostId!==b.hostId) fail(403,'Execution rejected')};
 app.get('/',async(_,r)=>r.type('text/html').send(ui(config.browserProxyAuth)));
 app.get('/test-sites/:project/:file',async(req:any,r)=>{if(!config.testSiteRoot||!['hello-world','hello-world-alt'].includes(req.params.project)||!/^[a-zA-Z0-9_-]+\.(html|css)$/.test(req.params.file))fail(404,'Unknown test site');const root=realpathSync(resolve(config.testSiteRoot!,req.params.project)),path=realpathSync(resolve(root,'current',req.params.file));if(!path.startsWith(root+sep))fail(403,'Test-site path rejected');r.header('Content-Security-Policy',"sandbox allow-same-origin; default-src 'none'; style-src 'self' 'unsafe-inline'; img-src data:; frame-ancestors 'self'");return r.type(req.params.file.endsWith('.css')?'text/css':'text/html').send(readFileSync(path))});
 app.get('/api/profiles',async()=>({profiles:listProfiles(config.profilesDirectory||'config/agents')}));
 app.post('/api/sessions',async()=>{const s={id:randomUUID(),createdAt:Date.now()};put('session',s);return {sessionId:s.id,...s}});
 app.post('/api/sessions/:id/profile',async(req:any)=>db.transaction(()=>{
  const s=get(req.params.id,'session');if(!req.body||Object.keys(req.body).some(k=>!['profileId','account'].includes(k))||typeof req.body.profileId!=='string')fail(400,'Expected profileId only');
  if(all('execution').some(e=>e.sessionId===s.id&&!['completed','failed','stopped'].includes(e.state)))fail(409,'Execution active');
  if(req.body.account!==undefined&&(typeof req.body.account!=='string'||!/^(?:[a-z][a-z0-9-]{0,79}|[a-f0-9-]{36})$/.test(req.body.account)))fail(400,'Invalid named host account');
  if(s.nativeSessionId)return agent.transition({sessionId:s.id,profileId:req.body.profileId,account:req.body.account??null});
  const selected=readTrustedProfile(config.profilesDirectory||'config/agents',req.body.profileId);
  s.account=req.body.account;
  s.agentConfig=readTrustedProfile(config.profilesDirectory||'config/agents',req.body.profileId);put('session',s);return {sessionId:s.id,profileId:s.agentConfig.id,account:s.account};
 })());
 app.get('/api/sessions/:id',async(req:any)=>{const s=get(req.params.id,'session');return {...s,...agent.sessionView(s.id),sessionId:s.id,delegations:all('delegation').filter(d=>d.parentSessionId===s.id||d.childSessionId===s.id).map(related),messages:all('message').filter(x=>x.sessionId===s.id),executions:all('execution').filter(x=>x.sessionId===s.id),events:all('event').filter(x=>x.sessionId===s.id),results:all('artifact').filter(x=>x.sessionId===s.id).map(({contentBase64,...x})=>x)}});
 app.get('/api/sessions/:id/events',async(req:any)=>{get(req.params.id,'session');const after=Number(req.query.after??-1),limit=Number(req.query.limit??100);if(!Number.isSafeInteger(after)||!Number.isSafeInteger(limit)||limit<1||limit>500)fail(400,'Invalid page');const events=all('event').filter(x=>x.sessionId===req.params.id).slice(after+1,after+1+limit);return {events,nextCursor:events.length?after+events.length:after}});
 app.get('/api/executions/:id/differences',async(req:any)=>{
  const current=get(req.params.id,'execution'),previous=get(text(req.query.previous,300),'execution');
  if(current.sessionId!==previous.sessionId)fail(403,'Different session');
  const artifacts=all('artifact'),before=artifacts.filter(x=>x.executionId===previous.id),after=artifacts.filter(x=>x.executionId===current.id);
  const files=[...new Set([...before,...after].map(x=>x.path))].sort().map(path=>{
   const left=before.find(x=>x.path===path),right=after.find(x=>x.path===path);
   let textChange:any=null;
   if(/\.(html|css)$/i.test(path)&&(!left||left.size<=131072)&&(!right||right.size<=131072)){
    const before=left?Buffer.from(left.contentBase64,'base64').toString('utf8').split('\n'):[],after=right?Buffer.from(right.contentBase64,'base64').toString('utf8').split('\n'):[];
    let start=0,end=0;while(start<before.length&&start<after.length&&before[start]===after[start])start++;
    while(end<before.length-start&&end<after.length-start&&before[before.length-1-end]===after[after.length-1-end])end++;
    textChange={firstLine:start+1,removed:before.slice(start,before.length-end),added:after.slice(start,after.length-end)};
   }
   return {path,textChange,state:!left?'added':!right?'removed':left.sha256===right.sha256?'unchanged':'modified',previous:left?{id:left.id,sha256:left.sha256}:null,current:right?{id:right.id,sha256:right.sha256}:null};
  });
  return {sessionId:current.sessionId,previousExecutionId:previous.id,executionId:current.id,files};
 });
 app.get('/api/executions/:id/events',async(req:any)=>{get(req.params.id,'execution');return {events:all('event').filter(x=>x.executionId===req.params.id).sort((a,b)=>a.seq-b.seq)}});
 const managed=accounts(app,{all,get,put},config);
 const enqueue=(s:any,input:string,requestId:string)=>{
  const binding=managed.resolve(s.account,s.agentConfig?.harness??'codex',config.hostId);
  const e={...(s.agentConfig?{agentConfig:s.agentConfig,profileSha256:profileDigest(s.agentConfig)}:{}),...(s.account||binding?{requestedAccount:s.account??null,effectiveAccount:binding??{binding:s.account,legacy:true}}:{}),id:randomUUID(),executionId:'',sessionId:s.id,hostId:config.hostId,state:'queued',createdAt:Date.now(),model:s.agentConfig?.model??'gpt-6.1-sol',effort:s.agentConfig?s.agentConfig.effort:'low'};e.executionId=e.id;
  const command:any={id:randomUUID(),executionId:e.id,sessionId:s.id,hostId:config.hostId,kind:'start',payload:{harness:s.agentConfig?.harness??'codex',...(s.account||binding&&!s.nativeSessionId?{account:binding?.binding??s.account,...(binding?{managedAccount:binding}:{})}:{}),text:input,...(s.projectId?{projectId:s.projectId}:{}),model:e.model,effort:e.effort,...(s.agentConfig?{agentConfig:s.agentConfig}:{}),instructions:s.agentConfig?'':'Follow the user request and its scope. Discuss, answer questions, or review existing results when requested; do not create or modify a website unless requested. When creating a static website, deliver output/index.html and retain all source assets. Do not spawn subagents.',...(s.nativeSessionId?{nativeSessionId:s.nativeSessionId}:{})},status:'pending'};agent.decorate(s,e,command);
  put('execution',e);put('message',{id:randomUUID(),sessionId:s.id,executionId:e.id,requestId:requestId,text:input});
  put('command',command);return e;
 };
 agent=agentTools({all,get,put},{directory:config.profilesDirectory||'config/agents',hostId:config.hostId,enqueue,resolveAccount:(account:string,harness:string)=>managed.resolve(account,harness,config.hostId)});
 const delegates=delegation({all,get,put},config.profilesDirectory||'config/agents',enqueue);
 app.get('/api/executions/:id/delegations',async(req:any)=>{get(req.params.id,'execution');return {delegations:all('delegation').filter(d=>d.parentExecutionId===req.params.id||d.childExecutionId===req.params.id||d.continuationExecutionId===req.params.id).map(related)}});
 app.post('/api/sessions/:id/messages',async(req:any)=>db.transaction(()=>{
  const s=get(req.params.id,'session'),b=req.body; if(!b||Array.isArray(b)||typeof b!=='object'||Object.keys(b).some(k=>!['requestId','text','delivery'].includes(k))||!Object.hasOwn(b,'requestId')||!Object.hasOwn(b,'text'))fail(400,'Expected requestId, text and optional delivery');text(b.requestId,200);text(b.text);return agent.submit({sessionId:s.id,requestId:b.requestId,text:b.text,delivery:b.delivery});
 })());
 app.post('/api/executions/:id/stop',async(req:any)=>{const e=get(req.params.id,'execution');let c=all('command').find(x=>x.executionId===e.id&&x.kind==='stop');if(!c){c={id:randomUUID(),executionId:e.id,sessionId:e.sessionId,hostId:e.hostId,kind:'stop',payload:{},status:'pending'};put('command',c)}e.stopRequested=true;put('execution',e);return {...c,stop:agent.stopOutcome(e)}});
 app.post('/api/agent-operations/:name',async(req:any)=>{const op=agent.operations[req.params.name];if(!op)fail(404,'Unknown agent operation');return db.transaction(()=>op(req.body??{}))()});
 app.post('/api/agent-tools',async(req:any)=>agent.mcp(req.body,(n:string,a:any)=>db.transaction(()=>agent.operations[n](a))(),'fleet-agent'));
 app.post('/api/hosts/agent-tools',async(req:any)=>{const b=req.body;owned(b);const c=get(b.commandId,'command');if(c.executionId!==b.executionId||c.hostId!==b.hostId)fail(403,'Command rejected');return agent.mcp(b.request,(n:string,a:any)=>db.transaction(()=>agent.bound(n,a,b.executionId))(),'fleet-agent')});
 app.get('/api/reconciliation',async()=>({reconciliation:all('reconciliation').at(-1)??null}));
 app.post('/api/hosts/poll',async(req:any)=>db.transaction(()=>{owned(req.body);put('host-observation',{id:req.body.hostId,lastSeenAt:Date.now(),...( ['darwin','win32','linux'].includes(req.body.platform)?{platform:req.body.platform}:{} ),...(Array.isArray(req.body.capabilities)&&req.body.capabilities.every((x:any)=>typeof x==='string')?{capabilities:req.body.capabilities}:{})});delegates.reconcile();agent.promote();const ready=(command:any)=>{if(!agent.ready(command))return false;const execution=get(command.executionId,'execution');if(command.kind!=='stop'&&!delegates.ready(execution))return false;if(!execution.experimentId||command.kind==='stop')return true;const experiment=get(execution.experimentId,'experiment');const index=experiment.trials.findIndex((t:any)=>t.executionId===execution.id);return experiment.trials.slice(0,index).every((t:any)=>['completed','failed','stopped'].includes(get(t.executionId,'execution').state))};return {commands:all('command').filter(x=>x.hostId===req.body.hostId&&x.status==='pending'&&ready(x)).map(x=>get(x.id,'command'))}})());
 app.post('/api/hosts/receipts',async(req:any)=>{if(req.body?.accountObservation||req.body?.accountCatalogue){const b=req.body;owned(b);const c=get(b.commandId,'command'),v=b.accountObservation??b.accountCatalogue;if(c.hostId!==b.hostId||c.executionId!==b.executionId||c.kind!=='account-inspect'||c.payload.accountId!==v.accountId||c.payload.binding!==(v.binding??null))fail(403,'Account inspection ownership rejected');return (b.accountCatalogue?managed.catalogue:managed.report)({...v,hostId:c.payload.nativeHostId})}if(req.body?.delegation?.operation){const b=req.body;owned(b);const c=get(b.commandId,'command'),session=get(c.sessionId,'session'),q=b.delegation;if(c.executionId!==b.executionId||c.hostId!==b.hostId||!session.projectId||q.projectId!==session.projectId||!['project_context','catalogue','pipeline_run','direct_search','pipeline_history','pipeline_get','question_ask','question_history','account_list'].includes(q.operation))fail(403,'Project grant rejected');const result=projects[q.operation]({...q.arguments,projectId:session.projectId},{kind:'native-agent',executionId:b.executionId,sessionId:session.id});if(['pipeline_run','direct_search'].includes(q.operation)&&result instanceof Promise){const run=all('pipeline-run').find(r=>r.projectId===session.projectId&&r.requestId===q.arguments?.requestId);if(!run)return result;result.catch(()=>{const current=get(run.id,'pipeline-run');if(current.state==='running'){current.state='unknown';current.error={code:'dispatch-unknown',message:'Native tool dispatch outcome unconfirmed; inspect original evidence, do not replay'};current.steps[0].state='unknown';current.steps[0].error=current.error;current.durationMs=null;put('pipeline-run',current)}});return run}return result}return db.transaction(()=>{const b=req.body;owned(b);const c=get(b.commandId,'command');if(c.executionId!==b.executionId||c.hostId!==b.hostId)fail(403,'Command rejected');if(b.delegation)return delegates.request(b,c);if(!['accepted','started','completed','failed','stopped','unknown'].includes(b.status))fail(400,'Invalid status');const ranks:any={pending:0,accepted:1,started:2,unknown:2,completed:3,failed:3,stopped:3}; if(ranks[b.status]<ranks[c.status]||(ranks[c.status]===3&&b.status!==c.status))fail(409,'Receipt regression'); if(b.nativeSessionId){const known=agent.knownNative(get(b.executionId,'execution'));if(known&&known!==b.nativeSessionId)fail(409,'Native identity conflict')} c.status=b.status;put('command',c);const e=get(b.executionId,'execution');if(!['completed','failed','stopped'].includes(e.state)&&(c.kind!=='steer'&&(c.kind!=='stop'||b.status==='stopped')))e.state=b.status;e.updatedAt=Date.now();if(b.status==='started'&&!e.startedAt)e.startedAt=e.updatedAt;if(c.kind!=='steer'&&['completed','failed','stopped'].includes(b.status)&&!e.finishedAt)e.finishedAt=e.updatedAt;if(b.error)e.error=b.error;put('execution',e);if(b.nativeSessionId&&c.kind==='start')agent.captureNative(e,text(b.nativeSessionId,300));agent.onReceipt(c,e,b.status);if(['completed','failed','stopped'].includes(e.state))agent.promote();deliveryRuntime?.advance();return {ok:true}})()});
 const transportRecovery=recovery(app,{all,get,put,transaction:(f:any)=>db.transaction(f)()},owned,!!config.browserProxyAuth);
 app.post('/api/hosts/events',async(req:any)=>transportRecovery.ingest(req.body));
 app.post('/api/hosts/artifacts',async(req:any)=>{const b=req.body;owned(b);if(typeof b.path!=='string'||!/^([a-zA-Z0-9_-]+\/)*[a-zA-Z0-9_.-]+$/.test(b.path)||b.path.split('/').some((x:string)=>x==='.'||x==='..'))fail(400,'Invalid path');if(typeof b.contentBase64!=='string'||b.contentBase64.length>4*Math.ceil(maxArtifactBytes/3)||! /^(?:[A-Za-z0-9+/]{4})*(?:[A-Za-z0-9+/]{2}==|[A-Za-z0-9+/]{3}=)?$/.test(b.contentBase64))fail(400,'Invalid content');const digest=createHash('sha256').update(Buffer.from(b.contentBase64,'base64')).digest('hex');if(b.sha256&&b.sha256!==digest)fail(400,'Digest mismatch');const id=`artifact:${b.executionId}:${b.path}`;const previous=all('artifact').find(x=>x.id===id);if(previous&&(previous.contentBase64!==b.contentBase64||previous.mediaType!==b.mediaType))fail(409,'Artifact conflict');if(Buffer.from(b.contentBase64,'base64').length>maxArtifactBytes)fail(413,'Artifact too large');put('artifact',{id,sha256:digest,executionId:b.executionId,sessionId:get(b.executionId,'execution').sessionId,path:b.path,mediaType:b.mediaType,contentBase64:b.contentBase64,size:Buffer.from(b.contentBase64,'base64').length});return {resultId:id,url:`/api/results/${encodeURIComponent(id)}`}});
 app.get('/api/results/:id',async(req:any,r)=>{const a=get(req.params.id,'artifact');r.header('Content-Security-Policy',"sandbox allow-scripts; default-src 'none'; script-src 'unsafe-inline'; style-src 'unsafe-inline'; img-src data:");return r.type(a.mediaType||'application/octet-stream').send(Buffer.from(a.contentBase64,'base64'))});
 const projects=registerProjects(app,{all,get,put},{directory:config.projectConfigDirectory||'config',pipeConnectionFile:config.pipeConnectionFile,proxy:config.browserProxyAuth,searchHostId:config.integrationHostId});
 Object.assign(projects,managed.operations);
 deliveryRuntime=delivery({all,get,put},config,enqueue,projects,managed.resolve,managed.preferred);
 Object.assign(projects,deliveryRuntime.operations);
 deliveryRuntime.advance();
 agent.reconcile();
 registerLab(app,{all,get,put,transaction:fn=>db.transaction(fn)()},{hostId:config.hostId,profilesDirectory:config.profilesDirectory||'config/agents',browserProxyAuth:config.browserProxyAuth});
 app.addHook('onClose',async()=>{db.close()});return app;
}
