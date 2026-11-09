import {recovery} from './recovery.js';
import Fastify from 'fastify';
import {createStore,type Store,every,filter,map,LONG_OPERATIONS,READ_OPERATIONS,executionScope} from './store.js';
import {randomUUID,timingSafeEqual,createHash} from 'node:crypto';
import {mkdirSync,readFileSync,realpathSync,existsSync} from 'node:fs';
import {dirname,resolve,sep,join} from 'node:path';
import fastifyStatic from '@fastify/static';
import {registerStream} from './stream.js';
import {sessionModel} from './sessions.js';
import {registerLab,profileDigest,readTrustedProfile,listProfiles} from './lab.js';
import {delegation} from './delegation.js';
import {registerProjects} from './projects.js';
import {accounts} from './accounts.js';
import {delivery} from './delivery.js';
import {ui} from './ui.js';
import {agentTools} from './agent-tools.js';
export function createApp(config:{appDir?:string,ownerEmail?:string,changeLogRetentionDays?:number,nativeHostIds?:string[],database:string,store?:Store,browserSecret:string,machineSecret:string,hostId:string,browserOrigin?:string,browserProxyAuth?:boolean,profilesDirectory?:string,maxArtifactBytes?:number,projectConfigDirectory?:string,pipeConnectionFile?:string,integrationHostId?:string,publicationHostId?:string,testSiteRoot?:string,defaultAccountBinding?:string}) {
 if(!config.browserSecret||!config.machineSecret||config.browserSecret===config.machineSecret) throw Error('Distinct credentials required');
 if(config.browserProxyAuth&&!config.browserOrigin)throw Error('Proxy browser auth requires configured browser origin');
 const ownStore=!config.store,store=config.store??createStore(config.database),{all,get,put}=store;
 const maxArtifactBytes=config.maxArtifactBytes??16*1024*1024;
 const retentionDays=config.changeLogRetentionDays??7;if(!(retentionDays>0))throw Error('Invalid change log retention');
 const sessionList=sessionModel({query:(s:string,p?:any[])=>store.query(s,p??[])});
 if(!Number.isSafeInteger(maxArtifactBytes)||maxArtifactBytes<1)throw Error('Invalid artifact bound');
 const app=Fastify({bodyLimit:Math.ceil(maxArtifactBytes*4/3)+65536});
 // Startup work runs in onReady hooks, in registration order: schema first, then module recovery.
 app.decorate('store',store);
 // Each route runs isolated in store.atomic, as every handler was on synchronous SQLite. Routes that can wait on a
 // pipeline run do not: they reach LONG_OPERATIONS, which open short transactions around their own steps.
 // Host traffic for one execution (receipts, event batches, artifacts) reads and writes the same documents; it waits on a
 // per-execution lock instead of failing SERIALIZABLE checks against itself. Other executions still run in parallel.
 // Host ingest of one execution (event batches, artifacts) writes only that execution's events, transport records and artifacts,
 // which no other handler writes: it runs in the execution's scope, in parallel with other executions and with GLOBAL writers.
 const ingestScope=(req:any)=>typeof req.body?.executionId==='string'?executionScope(req.body.executionId):fail(400,'Expected executionId');
 const ingestRoutes=new Set(['/api/hosts/events','/api/hosts/artifacts']);
 const longRoutes=new Set(['/api/stream','/assets/*','/assets/','/api/pipeline-runs','/api/project-operations/:name','/api/project-tools','/api/ops/:name','/api/tools','/api/hosts/receipts','/api/agent-tools','/api/hosts/agent-tools']);
 // Reads (GET, HEAD) run in a read-only snapshot; every other route is one GLOBAL write (store.atomic) unless it is host ingest.
 app.addHook('onRoute',(o:any)=>{if(longRoutes.has(o.url))return;const h=o.handler,method=[o.method].flat()[0];
  o.handler=['GET','HEAD'].includes(method)?function(this:any,req:any,reply:any){return store.read(()=>h.call(this,req,reply))}
   :ingestRoutes.has(o.url)?function(this:any,req:any,reply:any){return store.atomic(()=>h.call(this,req,reply),ingestScope(req))}
   :function(this:any,req:any,reply:any){return store.atomic(()=>h.call(this,req,reply))}});
 if(ownStore)app.addHook('onReady',()=>store.migrate());
 const fail=(code:number,message:string)=>{throw Object.assign(Error(message),{statusCode:code})};
 const text=(v:any,max=32000):string=>typeof v==='string'&&v.length>0&&v.length<=max?v:fail(400,'Invalid text');
 const equal=(a:string,b:string)=>Buffer.byteLength(a)===Buffer.byteLength(b)&&timingSafeEqual(Buffer.from(a),Buffer.from(b));
 app.addHook('onRequest',async(req,reply)=>{
  reply.header('X-Content-Type-Options','nosniff').header('Referrer-Policy','no-referrer');
  // Bearer only for /api/* and /test-sites/*. App files and app routes are public: Cloudflare Access and the guard stand in
  // front of the whole site, and the data they show comes through /api/*. Legacy pages keep today's rule (public outside proxy mode).
  const path=req.url.split('?')[0],legacy=path==='/legacy'||path.startsWith('/legacy/');
  if(!path.startsWith('/api/')&&!path.startsWith('/test-sites/')&&(!legacy||!config.browserProxyAuth)) return;
  const machine=req.url.startsWith('/api/hosts/');
  const supplied=(req.headers.authorization||'').replace(/^Bearer /,'');
  if(!equal(supplied,machine?config.machineSecret:config.browserSecret)) fail(401,'Unauthorized');
  if(!machine&&config.browserProxyAuth&&!['GET','HEAD','OPTIONS'].includes(req.method)&&req.headers.origin!==config.browserOrigin)fail(403,'Origin required');
  if(!machine&&req.headers.origin&&req.headers.origin!==(config.browserOrigin??`${req.protocol}://${req.headers.host}`)) fail(403,'Origin rejected');
 });
 let deliveryRuntime:ReturnType<typeof delivery>,agent:ReturnType<typeof agentTools>;
 const related=async (d:any)=>({...d,parentState:(await get(d.parentExecutionId,'execution')).state,childState:(await get(d.childExecutionId,'execution')).state,continuationState:d.continuationExecutionId?(await get(d.continuationExecutionId,'execution')).state:null});
 const owned=async (b:any)=>{if(![config.hostId,config.integrationHostId,config.publicationHostId,...(config.nativeHostIds??[])].filter(Boolean).includes(b.hostId)) fail(403,'Host rejected'); if(b.executionId&&(await get(b.executionId,'execution')).hostId!==b.hostId) fail(403,'Execution rejected')};
 app.get('/legacy',async(_,r)=>r.type('text/html').send(ui(config.browserProxyAuth)));
 // Operator app (docs/designs/fleet-operator-app.md): '/' opens the Overview; old page paths moved under /legacy.
 app.get('/',async(_,r)=>r.redirect('/overview'));
 for(const old of ['lab','projects','recovery','accounts'])app.get('/'+old,async(_,r)=>r.code(301).redirect('/legacy/'+old));
 // The built app (npm run build -> dist/app); hashed assets are immutable, the entry files are revalidated.
 const appDir=resolve(config.appDir??'dist/app');
 if(existsSync(join(appDir,'assets')))app.register(fastifyStatic,{root:join(appDir,'assets'),prefix:'/assets/',immutable:true,maxAge:'365d',index:false});
 const appFile=(name:string,type:string)=>async(_:any,r:any)=>{if(!existsSync(join(appDir,name)))fail(404,'App not built');return r.header('Cache-Control','no-cache').type(type).send(readFileSync(join(appDir,name)))};
 app.get('/sw.js',appFile('sw.js','text/javascript'));
 app.get('/manifests/:name',async(req:any,r)=>{if(!/^[a-z]+\.webmanifest$/.test(req.params.name))fail(404,'Unknown manifest');return appFile('manifests/'+req.params.name,'application/manifest+json')(req,r)});
 app.get('/icons/:name',async(req:any,r)=>{if(!/^[a-z0-9-]+\.svg$/.test(req.params.name))fail(404,'Unknown icon');return appFile('icons/'+req.params.name,'image/svg+xml')(req,r)});
 for(const route of ['/overview','/tasks','/tasks/:id','/conversations','/conversations/:id','/hosts','/hosts/:id'])app.get(route,appFile('index.html','text/html'));
 app.get('/test-sites/:project/:file',async(req:any,r)=>{if(!config.testSiteRoot||!['hello-world','hello-world-alt'].includes(req.params.project)||!/^[a-zA-Z0-9_-]+\.(html|css)$/.test(req.params.file))fail(404,'Unknown test site');const root=realpathSync(resolve(config.testSiteRoot!,req.params.project)),path=realpathSync(resolve(root,'current',req.params.file));if(!path.startsWith(root+sep))fail(403,'Test-site path rejected');r.header('Content-Security-Policy',"sandbox allow-same-origin; default-src 'none'; style-src 'self' 'unsafe-inline'; img-src data:; frame-ancestors 'self'");return r.type(req.params.file.endsWith('.css')?'text/css':'text/html').send(readFileSync(path))});
 const profiles_list=()=>({profiles:listProfiles(config.profilesDirectory||'config/agents')});
 app.get('/api/profiles',async()=>profiles_list());
 const session_create=async ()=>{const s={id:randomUUID(),createdAt:Date.now()};await put('session',s);return {sessionId:s.id,...s}};
 app.post('/api/sessions',async()=>(await store.tx(session_create)));
 const session_profile=async ({sessionId,...body}:any)=>{const req={body};
  const s=(await get(sessionId,'session'));if(!req.body||Object.keys(req.body).some(k=>!['profileId','account','workingDirectory'].includes(k))||typeof req.body.profileId!=='string')fail(400,'Expected profileId only');
  if((await all('execution',{sessionId:s.id})).some(e=>e.sessionId===s.id&&!['completed','failed','stopped'].includes(e.state)))fail(409,'Execution active');
  if(req.body.account!==undefined&&(typeof req.body.account!=='string'||!/^(?:[a-z][a-z0-9-]{0,79}|[a-f0-9-]{36})$/.test(req.body.account)))fail(400,'Invalid named host account');
  if(req.body.workingDirectory!==undefined&&(typeof req.body.workingDirectory!=='string'||!req.body.workingDirectory.startsWith('/')||req.body.workingDirectory.includes('..')))fail(400,'Invalid working directory');if(req.body.workingDirectory!==undefined){s.workingDirectory=req.body.workingDirectory;await put('session',s)}
  if(s.nativeSessionId)return (await agent.transition({sessionId:s.id,profileId:req.body.profileId,account:req.body.account??null}));
  const selected=readTrustedProfile(config.profilesDirectory||'config/agents',req.body.profileId);
  s.account=req.body.account;
  s.agentConfig=readTrustedProfile(config.profilesDirectory||'config/agents',req.body.profileId);await put('session',s);return {sessionId:s.id,profileId:s.agentConfig.id,account:s.account};
 };
 app.post('/api/sessions/:id/profile',async(req:any)=>(await store.tx(async ()=>(await session_profile({...req.body,sessionId:req.params.id})))));
 const session_get=async (b:any)=>{const s=(await get(b?.sessionId,'session'));return {...s,...(await agent.sessionView(s.id)),sessionId:s.id,delegations:(await map((await all('delegation')).filter(d=>d.parentSessionId===s.id||d.childSessionId===s.id),related)),messages:(await all('message',{sessionId:s.id})).filter(x=>x.sessionId===s.id),executions:(await all('execution',{sessionId:s.id})).filter(x=>x.sessionId===s.id),events:(await all('event',{sessionId:s.id})).filter(x=>x.sessionId===s.id),results:(await all('artifact',{sessionId:s.id},['contentBase64'])).filter(x=>x.sessionId===s.id).map(({contentBase64,...x})=>x)}};
 app.get('/api/sessions/:id',async(req:any)=>(await session_get({sessionId:req.params.id})));
 app.get('/api/sessions/:id/events',async(req:any)=>{(await get(req.params.id,'session'));const after=Number(req.query.after??-1),limit=Number(req.query.limit??100);if(!Number.isSafeInteger(after)||!Number.isSafeInteger(limit)||limit<1||limit>500)fail(400,'Invalid page');const events=(await all('event',{sessionId:req.params.id})).filter(x=>x.sessionId===req.params.id).slice(after+1,after+1+limit);return {events,nextCursor:events.length?after+events.length:after}});
 const execution_differences=async (b:any)=>{
  const current=(await get(b?.executionId,'execution')),previous=(await get(text(b?.previous,300),'execution'));
  if(current.sessionId!==previous.sessionId)fail(403,'Different session');
  const artifacts=(await all('artifact')),before=artifacts.filter(x=>x.executionId===previous.id),after=artifacts.filter(x=>x.executionId===current.id);
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
 };
 app.get('/api/executions/:id/differences',async(req:any)=>(await execution_differences({executionId:req.params.id,previous:req.query.previous})));
 const execution_events=async (b:any)=>{(await get(b?.executionId,'execution'));return {events:(await all('event',{executionId:b.executionId})).filter(x=>x.executionId===b.executionId).sort((a,b)=>a.seq-b.seq)}};
 app.get('/api/executions/:id/events',async(req:any)=>(await execution_events({executionId:req.params.id})));
 const managed=accounts(app,store,config);
 // Git network for agents: profile gitNetwork wins; default on for pm/reviewer/worker sessions of projects with repos.
 const gitNetwork=(s:any)=>s.agentConfig?.gitNetwork??(!!s.projectId&&['pm','reviewer','worker'].includes(s.role)&&projects.project_context({projectId:s.projectId}).settings.repos.length>0);
 const enqueue=async (s:any,input:string,requestId:string)=>{
  // Execution host follows the account binding: the session's last host first, then the default host, then extra native hosts.
  const harness=s.agentConfig?.harness??'codex',last=(await all('execution',{sessionId:s.id})).filter(x=>x.sessionId===s.id&&x.hostId).at(-1)?.hostId;
  const candidates=[...new Set([last,config.hostId,...(config.nativeHostIds??[])].filter((h:any)=>h===config.hostId||(config.nativeHostIds??[]).includes(h)))];
  let hostId=config.hostId,binding:any;const errors:any[]=[];
  for(const h of candidates){try{binding=(await managed.resolve(s.account,harness,h));hostId=h;errors.length=0;break}catch(error){errors.push(error)}}
  if(errors.length)throw errors.find((x:any)=>x.statusCode===403)??errors[candidates.indexOf(config.hostId)]??errors[0];
  const e={...(s.agentConfig?{agentConfig:s.agentConfig,profileSha256:profileDigest(s.agentConfig)}:{}),...(s.account||binding?{requestedAccount:s.account??null,effectiveAccount:binding??{binding:s.account,legacy:true}}:{}),id:randomUUID(),executionId:'',sessionId:s.id,hostId,state:'queued',createdAt:Date.now(),model:s.agentConfig?.model??'gpt-6.1-sol',effort:s.agentConfig?s.agentConfig.effort:'low'};e.executionId=e.id;
  const command:any={id:randomUUID(),executionId:e.id,sessionId:s.id,hostId,kind:'start',payload:{harness:s.agentConfig?.harness??'codex',...(s.account||binding&&!s.nativeSessionId?{account:binding?.binding??s.account,...(binding?{managedAccount:binding}:{})}:{}),text:input,...(s.projectId?{projectId:s.projectId,project:projects.project_context({projectId:s.projectId}).settings}:{}),...(gitNetwork(s)?{gitNetwork:true}:{}),setup:s.setup??'run',...(s.workingDirectory?{workingDirectory:s.workingDirectory}:{}),model:e.model,effort:e.effort,...(s.agentConfig?{agentConfig:s.agentConfig}:{}),instructions:s.agentConfig?'':'Follow the user request and its scope. Discuss, answer questions, or review existing results when requested; do not create or modify a website unless requested. When creating a static website, deliver output/index.html and retain all source assets. Do not spawn subagents.',...(s.nativeSessionId?{nativeSessionId:s.nativeSessionId}:{})},status:'pending'};(await agent.decorate(s,e,command));
  await put('execution',e);await put('message',{id:randomUUID(),sessionId:s.id,executionId:e.id,requestId:requestId,text:input});
  await put('command',command);return e;
 };
 // Account for a new project session: the named one, else the project preference, else the first enabled account of the harness with a ready binding (on hostId when given). The binding picks the host.
 const pickAccount=async (harness:string,projectId:string,hostId?:string)=>{const ms=(await all('managed-account')).filter(m=>m.enabled&&(m.harness??'codex')===harness&&(m.bindings??[]).some((b:any)=>b.readiness==='ready'&&(!hostId||b.hostId===hostId)));const pref=(await managed.preferred(projectId));return (ms.find(m=>m.name===pref)??ms[0])?.name};
 agent=agentTools(store,{directory:config.profilesDirectory||'config/agents',hostId:config.hostId,enqueue,pickAccount,deliver:async (b:any)=>(await deliveryRuntime.operations.session_deliver(b)),project:(projectId:string)=>projects.project_context({projectId}).settings,resolveAccount:async(account:string,harness:string)=>{let last:any;for(const h of [config.hostId,...(config.nativeHostIds??[])]){try{const r=await managed.resolve(account,harness,h);if(r)return r}catch(e){last=e}}if(last)throw last}});
 // Executions bound to a host, account or environment nothing registered are marked with a problem (design §1).
 const detectProblems=async()=>{const environments:Record<string,string[]>={};for(const id of projects.project_list?(await projects.project_list()).projects.map((p:any)=>p.id):[]){try{environments[id]=projects.project_context({projectId:id}).settings.environments.map((e:any)=>e.hostId)}catch{}}
  return store.executions.detectProblems({hosts:[config.hostId,config.integrationHostId,config.publicationHostId,...(config.nativeHostIds??[])].filter(Boolean) as string[],environments})};
 const delegates=delegation(store,config.profilesDirectory||'config/agents',enqueue);
 app.get('/api/executions/:id/delegations',async(req:any)=>{(await get(req.params.id,'execution'));return {delegations:(await map((await all('delegation')).filter(d=>d.parentExecutionId===req.params.id||d.childExecutionId===req.params.id||d.continuationExecutionId===req.params.id),related))}});
 app.post('/api/sessions/:id/messages',async(req:any)=>(await store.tx(async ()=>{
  const s=(await get(req.params.id,'session')),b=req.body; if(!b||Array.isArray(b)||typeof b!=='object'||Object.keys(b).some(k=>!['requestId','text','delivery'].includes(k))||!Object.hasOwn(b,'requestId')||!Object.hasOwn(b,'text'))fail(400,'Expected requestId, text and optional delivery');text(b.requestId,200);text(b.text);return (await agent.submit({sessionId:s.id,requestId:b.requestId,text:b.text,delivery:b.delivery}));
 })));
 // Session finish/archive: one teardown command on the last execution's host (W2 executor removes the worktree).
 const session_archive=async (b:any)=>{const s=(await get(b?.sessionId,'session')),es=(await all('execution',{sessionId:s.id})).filter(x=>x.sessionId===s.id);if(es.some(e=>!['completed','failed','stopped','unknown'].includes(e.state)))fail(409,'Execution active');
  const old=(await all('command',{sessionId:s.id,kind:'teardown'})).find(c=>c.sessionId===s.id&&c.kind==='teardown');if(old)return old;s.archivedAt=Date.now();await put('session',s);const last=es.filter(e=>e.hostId).at(-1);if(!last)return {sessionId:s.id,archivedAt:s.archivedAt,teardown:null};
  const c={id:randomUUID(),executionId:last.id,sessionId:s.id,hostId:last.hostId,kind:'teardown',payload:{...(s.projectId?{projectId:s.projectId,project:projects.project_context({projectId:s.projectId}).settings}:{}),...(s.workingDirectory?{workingDirectory:s.workingDirectory}:{})},status:'pending'};await put('command',c);return c};
 const execution_stop=async (b:any)=>{const e=(await get(b?.executionId,'execution'));let c=(await all('command',{executionId:e.id,kind:'stop'})).find(x=>x.executionId===e.id&&x.kind==='stop');if(!c){c={id:randomUUID(),executionId:e.id,sessionId:e.sessionId,hostId:e.hostId,kind:'stop',payload:{},status:'pending'};await put('command',c)}e.stopRequested=true;await put('execution',e);return {...c,stop:(await agent.stopOutcome(e))}};
 app.post('/api/executions/:id/stop',async(req:any)=>(await store.tx(async ()=>(await execution_stop({executionId:req.params.id})))));
 app.post('/api/agent-operations/:name',async(req:any)=>{const op=agent.operations[req.params.name];if(!op)fail(404,'Unknown agent operation');return (await store.tx(async ()=>(await op(req.body??{}))))});
 app.post('/api/agent-tools',async(req:any)=>(await agent.mcp(req.body,async (n:string,a:any)=>READ_OPERATIONS.has(n)?store.read(()=>agent.operations[n](a)):store.tx(()=>agent.operations[n](a)),'fleet-agent')));
 app.post('/api/hosts/agent-tools',async(req:any)=>{const b=req.body;(await owned(b));const c=(await get(b.commandId,'command'));if(c.executionId!==b.executionId||c.hostId!==b.hostId)fail(403,'Command rejected');return (await agent.mcp(b.request,async (n:string,a:any)=>READ_OPERATIONS.has(n)?store.read(()=>agent.bound(n,a,b.executionId)):store.tx(()=>agent.bound(n,a,b.executionId)),'fleet-agent'))});
 app.get('/api/reconciliation',async()=>({reconciliation:(await all('reconciliation')).at(-1)??null}));
 app.post('/api/hosts/poll',async(req:any)=>(await store.tx(async ()=>{(await owned(req.body));await put('host-observation',{id:req.body.hostId,lastSeenAt:Date.now(),...( ['darwin','win32','linux'].includes(req.body.platform)?{platform:req.body.platform}:{} ),...(Array.isArray(req.body.capabilities)&&req.body.capabilities.every((x:any)=>typeof x==='string')?{capabilities:req.body.capabilities}:{})});(await delegates.reconcile());await detectProblems();(await agent.promote());const pending=await filter(await all('command',{hostId:req.body.hostId,status:'pending'}),(x:any)=>x.hostId===req.body.hostId&&x.status==='pending');
  // ponytail: executions read once for all pending commands; delegates.ready only rewrites the execution of its own start command.
  const executions=await store.many(pending.map((c:any)=>c.executionId),'execution');
  const ready=async (command:any)=>{if(!(await agent.ready(command)))return false;const execution=executions.get(command.executionId)??(await get(command.executionId,'execution'));if(command.kind!=='stop'&&!(await delegates.ready(execution)))return false;if(!execution.experimentId||command.kind==='stop')return true;const experiment=(await get(execution.experimentId,'experiment'));const index=experiment.trials.findIndex((t:any)=>t.executionId===execution.id);return (await every(experiment.trials.slice(0,index),async(t:any)=>['completed','failed','stopped'].includes((await get(t.executionId,'execution')).state)))};const chosen=await filter(pending,ready),fresh=await store.many(chosen.map((x:any)=>x.id),'command');return {commands:await map(chosen,async (x:any)=>fresh.get(x.id)??(await get(x.id,'command')))}})));
 app.post('/api/hosts/receipts',async(req:any)=>{if(req.body?.accountObservation||req.body?.accountCatalogue)return store.atomic(async()=>{const b=req.body;(await owned(b));const c=(await get(b.commandId,'command')),v=b.accountObservation??b.accountCatalogue;if(c.hostId!==b.hostId||c.executionId!==b.executionId||c.kind!=='account-inspect'||c.payload.accountId!==v.accountId||c.payload.binding!==(v.binding??null))fail(403,'Account inspection ownership rejected');return (b.accountCatalogue?managed.catalogue:managed.report)({...v,hostId:c.payload.nativeHostId})});if(req.body?.delegation?.operation){const b=req.body;(await owned(b));const c=(await get(b.commandId,'command')),session=(await get(c.sessionId,'session')),q=b.delegation;if(c.executionId!==b.executionId||c.hostId!==b.hostId||!session.projectId||q.projectId!==session.projectId||!['project_context','catalogue','pipeline_run','direct_search','pipeline_history','pipeline_get','question_ask','question_history','account_list'].includes(q.operation))fail(403,'Project grant rejected');let started:(run:any)=>void=()=>{};const begun=new Promise<any>(r=>started=r);const call=()=>projects[q.operation]({...q.arguments,projectId:session.projectId},{kind:'native-agent',executionId:b.executionId,sessionId:session.id},started);const result=LONG_OPERATIONS.has(q.operation)?call():store.atomic(call);if(['pipeline_run','direct_search'].includes(q.operation)){const run=await Promise.race([begun,result.then(()=>null,()=>null)]);if(!run)return result;result.catch(async ()=>{const current=(await get(run.id,'pipeline-run'));if(current.state==='running'){current.state='unknown';current.error={code:'dispatch-unknown',message:'Native tool dispatch outcome unconfirmed; inspect original evidence, do not replay'};current.steps[0].state='unknown';current.steps[0].error=current.error;current.durationMs=null;await put('pipeline-run',current)}});return run}return result}return (await store.tx(async ()=>{const b=req.body;(await owned(b));const c=(await get(b.commandId,'command'));if(c.executionId!==b.executionId||c.hostId!==b.hostId)fail(403,'Command rejected');if(b.delegation)return (await delegates.request(b,c));if(!['accepted','started','completed','failed','stopped','unknown'].includes(b.status))fail(400,'Invalid status');const ranks:any={pending:0,accepted:1,started:2,unknown:2,completed:3,failed:3,stopped:3}; if(ranks[b.status]<ranks[c.status]||(ranks[c.status]===3&&b.status!==c.status))fail(409,'Receipt regression'); if(b.nativeSessionId){const known=(await agent.knownNative((await get(b.executionId,'execution'))));if(known&&known!==b.nativeSessionId)fail(409,'Native identity conflict')} c.status=b.status;await put('command',c);const e=(await get(b.executionId,'execution'));if(!['completed','failed','stopped'].includes(e.state)&&(c.kind!=='steer'&&(c.kind!=='stop'||b.status==='stopped')))e.state=b.status;e.updatedAt=Date.now();if(b.status==='started'&&!e.startedAt)e.startedAt=e.updatedAt;if(c.kind!=='steer'&&['completed','failed','stopped'].includes(b.status)&&!e.finishedAt)e.finishedAt=e.updatedAt;if(b.error)e.error=b.error;await put('execution',e);if(b.status!=='unknown')await store.executions.markObservation(e.id,'confirmed');if(b.nativeSessionId&&c.kind==='start')(await agent.captureNative(e,text(b.nativeSessionId,300)));(await agent.onReceipt(c,e,b.status));if(['completed','failed','stopped'].includes(e.state))(await agent.promote());(await deliveryRuntime?.advance());return {ok:true}}))});
 const transportRecovery=recovery(app,{...store,transaction:store.tx},owned,!!config.browserProxyAuth);
 app.post('/api/hosts/events',async(req:any)=>(await transportRecovery.ingest(req.body)));
 app.post('/api/hosts/artifacts',async(req:any)=>{const b=req.body;(await owned(b));if(typeof b.path!=='string'||!/^([a-zA-Z0-9_-]+\/)*[a-zA-Z0-9_.-]+$/.test(b.path)||b.path.split('/').some((x:string)=>x==='.'||x==='..'))fail(400,'Invalid path');if(typeof b.contentBase64!=='string'||b.contentBase64.length>4*Math.ceil(maxArtifactBytes/3)||! /^(?:[A-Za-z0-9+/]{4})*(?:[A-Za-z0-9+/]{2}==|[A-Za-z0-9+/]{3}=)?$/.test(b.contentBase64))fail(400,'Invalid content');const digest=createHash('sha256').update(Buffer.from(b.contentBase64,'base64')).digest('hex');if(b.sha256&&b.sha256!==digest)fail(400,'Digest mismatch');const id=`artifact:${b.executionId}:${b.path}`;const previous=(await all('artifact',{id:id})).find(x=>x.id===id);if(previous&&(previous.contentBase64!==b.contentBase64||previous.mediaType!==b.mediaType))fail(409,'Artifact conflict');if(Buffer.from(b.contentBase64,'base64').length>maxArtifactBytes)fail(413,'Artifact too large');await put('artifact',{id,sha256:digest,executionId:b.executionId,sessionId:(await get(b.executionId,'execution')).sessionId,path:b.path,mediaType:b.mediaType,contentBase64:b.contentBase64,size:Buffer.from(b.contentBase64,'base64').length});return {resultId:id,url:`/api/results/${encodeURIComponent(id)}`}});
 app.get('/api/results/:id',async(req:any,r)=>{const a=(await get(req.params.id,'artifact'));r.header('Content-Security-Policy',"sandbox allow-scripts; default-src 'none'; script-src 'unsafe-inline'; style-src 'unsafe-inline'; img-src data:");return r.type(a.mediaType||'application/octet-stream').send(Buffer.from(a.contentBase64,'base64'))});
 const projects=(registerProjects(app,store,{directory:config.projectConfigDirectory||'config',pipeConnectionFile:config.pipeConnectionFile,proxy:config.browserProxyAuth,searchHostId:config.integrationHostId}));
 Object.assign(projects,managed.operations);
 deliveryRuntime=delivery(store,config,enqueue,projects,managed.resolve,managed.preferred);
 Object.assign(projects,deliveryRuntime.operations);
 app.addHook('onReady',()=>store.atomic(async()=>{await deliveryRuntime.advance();await agent.reconcile()}));
 registerLab(app,{...store,transaction:store.tx},{hostId:config.hostId,profilesDirectory:config.profilesDirectory||'config/agents',browserProxyAuth:config.browserProxyAuth});
 // One operation table: served as MCP tools (/api/tools) and as POST /api/ops/<name>; the web UI calls the same names.
 // ponytail: legacy per-feature routes stay as aliases. UI-only routes left (violations): lab experiment POSTs (/api/experiments*).
 // Tasks, questions and executions (design §1-2). Operator answers carry by 'operator'; agents answer through reply.
 const str=(v:any,name:string,max=4000)=>typeof v==='string'&&v.length>0&&v.length<=max?v:fail(400,'Expected '+name);
 const typedOps={
  task_create:async(b:any)=>{const id=await store.tasks.create({projectId:b.projectId??null,title:str(b.title,'title',500),expectedOutcome:b.expectedOutcome??null,priority:b.priority,completionRequirements:b.completionRequirements,resultDestination:b.resultDestination??null,createdBy:'operator',dependsOn:Array.isArray(b.dependsOn)?b.dependsOn.map((x:any)=>str(x,'dependsOn',200)):[]});return store.tasks.get(id)},
  task_update:(b:any)=>store.tasks.update({...b,taskId:str(b?.taskId,'taskId',200)}),
  task_get:(b:any)=>store.tasks.get(str(b?.taskId,'taskId',200)),
  task_list:async(b:any)=>({tasks:await store.tasks.list({projectId:b?.projectId,state:b?.state})}),
  questions_list:async(b:any)=>({questions:await store.tasks.questions({taskId:b?.taskId,state:b?.state,kind:b?.kind})}),
  questions_answer:async(b:any)=>{const q=await store.tasks.answer(str(b?.questionId,'questionId',200),{answer:str(b.answer,'answer'),by:'operator',artifactIds:Array.isArray(b.artifactIds)?b.artifactIds:undefined});return (await store.tasks.questions({})).find(x=>x.id===q.id)},
  executions_list:async(b:any)=>({executions:await store.executions.list({taskId:b?.taskId,projectId:b?.projectId,parentExecutionId:b?.parentExecutionId,problem:b?.problem===true})}),
  // Operator app reads (docs/designs/fleet-operator-app.md §6); agents can call them too.
  changes_since:(b:any)=>{const c=b?.cursor;if(c!==null&&c!==undefined&&(!Number.isSafeInteger(c)||c<0))fail(400,'Invalid cursor');return store.changes.since(c??null,b?.limit??500)},
  task_page:(b:any)=>store.tasks.page({filter:b?.filter,group:b?.group,limit:b?.limit,after:b?.after}),
  sessions_list:(b:any)=>sessionList.page({filter:b?.filter,limit:b?.limit,after:b?.after}),
  list_membership:async(b:any)=>{const ids=Array.isArray(b?.ids)?b.ids:fail(400,'Expected ids');if(!Array.isArray(b?.lists))fail(400,'Expected lists');
   return {lists:await map(b.lists,async(l:any)=>l?.name==='tasks'?{name:'tasks',rows:await store.tasks.membership(l.filter??{},l.group,ids),counts:(await store.tasks.page({filter:l.filter,group:l.group,limit:1})).counts}
    :l?.name==='sessions'?{name:'sessions',rows:await sessionList.membership(l.filter??{},ids),counts:(await sessionList.page({filter:l.filter,limit:1})).counts}:fail(400,'Unknown list'))}},
  hosts_list:async()=>{const configured=[config.hostId,config.integrationHostId,config.publicationHostId,...(config.nativeHostIds??[])].filter(Boolean) as string[];
   const observed=await all('host-observation'),ids=[...new Set([...configured,...observed.map((h:any)=>h.id)])];
   const running=await store.query("SELECT host_id,harness,count(*)::int AS n FROM execution WHERE state IN ('preparing','running') AND host_id IS NOT NULL GROUP BY 1,2");
   const queued=await store.query("SELECT data->>'hostId' AS host_id,count(*)::int AS n FROM document WHERE kind='command' AND data->>'status'='pending' GROUP BY 1");
   return {staleAfterMs:30000,hosts:ids.map(id=>{const o:any=observed.find((h:any)=>h.id===id);const by:Record<string,number>={};for(const r of running)if(r.host_id===id)by[r.harness??'unknown']=r.n;
    return {id,configured:configured.includes(id),platform:o?.platform??null,lastSeenAt:o?.lastSeenAt??null,capabilities:o?.capabilities??[],runningByHarness:by,running:Object.values(by).reduce((a,n)=>a+n,0),slots:null,queued:queued.find((q:any)=>q.host_id===id)?.n??0,stores:'unavailable'}})}},
  me:async()=>({email:config.ownerEmail??null,roles:['operator','architect'],grantsVersion:'1',changeLogRetentionDays:retentionDays}),
 };
 const ops:Record<string,(b:any)=>any>={...typedOps,...projects,...transportRecovery.operations,...agent.operations,session_create,session_profile,session_archive,execution_stop,session_get,execution_differences,execution_events,profiles_list};
 registerStream(app,store,{retentionDays});
 const runOp=async (n:string,b:any)=>{const op=ops[n];if(!op)fail(404,'Unknown operation');return READ_OPERATIONS.has(n)?store.read(()=>op(b??{})):LONG_OPERATIONS.has(n)?op(b??{}):op===projects[n]||op===transportRecovery.operations[n]?store.atomic(()=>op(b??{})):(await store.tx(()=>op(b??{})))};
 app.post('/api/ops/:name',async(req:any)=>(await runOp(req.params.name,req.body)));
 app.post('/api/tools',async(req:any)=>{const q=req.body;if(q?.method==='initialize')return {jsonrpc:'2.0',id:q.id,result:{protocolVersion:'2025-06-18',capabilities:{tools:{}},serverInfo:{name:'fleet',version:'1'}}};if(q?.method==='tools/list')return {jsonrpc:'2.0',id:q.id,result:{tools:Object.keys(ops).map(name=>({name,inputSchema:{type:'object'}}))}};if(q?.method!=='tools/call'||!ops[q.params?.name])fail(400,'Unknown tool');return {jsonrpc:'2.0',id:q.id,result:{content:[{type:'text',text:JSON.stringify(await runOp(q.params.name,q.params.arguments))}]}}});
 app.addHook('onClose',async()=>{if(ownStore)await store.close()});return app;
}
