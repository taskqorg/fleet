import {recovery} from './recovery.ts';
import Fastify from 'fastify';
import {createStore,type Store,filter,map,LONG_OPERATIONS,READ_OPERATIONS,executionScope} from './store.ts';
import {randomUUID,timingSafeEqual,createHash} from 'node:crypto';
import {mkdirSync,mkdtempSync,readFileSync,realpathSync,existsSync} from 'node:fs';
import {tmpdir} from 'node:os';
import {dirname,join,resolve,sep} from 'node:path';
import fastifyStatic from '@fastify/static';
import {registerStream} from './stream.ts';
import {sessionModel} from './sessions.ts';
import {profileDigest,readTrustedProfile,listProfiles} from './profiles.ts';
import {delegation} from './delegation.ts';
import {registerProjects} from './projects.ts';
import {accounts} from './accounts.ts';
import {connectionRegistry} from './connections.ts';
import {delivery} from './delivery.ts';
import {agentTools} from './agent-tools.ts';
import {threads} from './threads.ts';
import {storeRegistry,accessToken} from './stores.ts';
import {artifactModel,type Artifacts} from './artifacts.ts';
import {placementWorker} from './placement.ts';
import {labExamples} from './lab/examples.ts';
import {pipelineRunner} from './pipelines.ts';
import {labExperiments} from './lab/experiments.ts';
import {labOperations,registerLab} from './lab/routes.ts';
import {peopleRegistry,requestPerson,actor,mayAccessProject,director} from './people.ts';
import {hostRegistry,type Host} from './hosts.ts';
import type {HostEvents,PollReply,PollRequest,Receipt} from '../../contracts/generated/host-protocol.ts';
declare module 'fastify' {interface FastifyRequest {fleetHost:Host}}
export function createApp(config:{appDir?:string,peopleFile?:string,changeLogRetentionDays?:number,database:string,store?:Store,browserSecret:string,hostId:string,browserOrigin?:string,browserProxyAuth?:boolean,profilesDirectory?:string,storeSecrets?:string,storeEndpointHosts?:string[],cfAccessFile?:string,defaultArtifactLocations?:string[],artifactRetryWindowMs?:number,placementIntervalMs?:number,projectConfigDirectory?:string,connectionsDir?:string,secretDir?:string,gatewayDir?:string,connectionClient?:string[],pipelineTickMs?:number,testSiteRoot?:string,defaultAccountBinding?:string,labDirectory?:string}) {
 if(!config.browserSecret) throw Error('Browser credential required');
 if(config.browserProxyAuth&&!config.browserOrigin)throw Error('Proxy browser auth requires configured browser origin');
 const ownStore=!config.store,store=config.store??createStore(config.database),{all,get,put}=store;
 const retentionDays=config.changeLogRetentionDays??7;if(!(retentionDays>0))throw Error('Invalid change log retention');
 // App screens come from the build (dist/app/screens.json, written from the registry's app workspaces); before a build
 // there are none and every app path is a 404.
 const screens:string[]=(()=>{try{const v=JSON.parse(readFileSync(join(resolve(config.appDir??'dist/app'),'screens.json'),'utf8'));return Array.isArray(v)?v.filter((x:any)=>typeof x==='string'&&/^[a-z]+$/.test(x)):[]}catch{return []}})();
 const APP_ROUTES=screens.flatMap(s=>['/'+s,'/'+s+'/:id']);
 const PUBLIC_ROUTES=new Set(['/',...APP_ROUTES,'/assets/*','/sw.js','/manifests/:name','/icons/:name','/lab','/lab/:id']);
 const sessionList=sessionModel({query:(s:string,p?:any[])=>store.query(s,p??[])});
 // No artifact bytes pass through the coordinator (design §3): Fastify's default body limit applies.
 const app=Fastify();
 // Startup work runs in onReady hooks, in registration order: schema first, then module recovery.
 app.decorate('store',store);
 // Each route runs isolated in store.atomic, as every handler was on synchronous SQLite. Routes that can wait on a
 // pipeline run do not: they reach LONG_OPERATIONS, which open short transactions around their own steps.
 // Host traffic for one execution (receipts, event batches, artifacts) reads and writes the same documents; it waits on a
 // per-execution lock instead of failing SERIALIZABLE checks against itself. Other executions still run in parallel.
 // Host ingest of one execution (event batches, artifacts) writes only that execution's events, transport records and artifacts,
 // which no other handler writes: it runs in the execution's scope, in parallel with other executions and with GLOBAL writers.
 const ingestScope=(req:any)=>typeof req.body?.executionId==='string'?executionScope(req.body.executionId):fail(400,'Expected executionId');
 const ingestRoutes=new Set(['/api/hosts/events']);
 const longRoutes=new Set(['/api/stream','/api/threads/:scope/:id/export','/assets/*','/assets/','/api/hosts/artifacts/list','/api/hosts/artifacts/reserve','/api/hosts/artifacts/parts','/api/hosts/artifacts/commit','/api/hosts/artifacts/verified','/api/hosts/artifacts/renew','/api/hosts/artifacts/failed','/api/hosts/artifacts/declare','/api/hosts/artifacts/get','/api/hosts/artifacts/read','/api/results/:id','/api/pipeline-runs','/api/pipelines/:id/runs','/api/project-operations/:name','/api/project-tools','/api/ops/:name','/api/tools','/api/hosts/receipts','/api/hosts/agent-tools']);
 // Reads (GET, HEAD) run in a read-only snapshot; every other route is one GLOBAL write (store.atomic) unless it is host ingest.
 // Lab experiment routes call operations (runOp opens their transactions); reconcile needs committed state.
 app.addHook('onRoute',(o:any)=>{if(longRoutes.has(o.url)||/^\/api\/lab\/(experiments|ratings|scores|registry)/.test(o.url))return;const h=o.handler,method=[o.method].flat()[0];
  o.handler=['GET','HEAD'].includes(method)?function(this:any,req:any,reply:any){return store.read(()=>h.call(this,req,reply))}
   :ingestRoutes.has(o.url)?function(this:any,req:any,reply:any){return store.atomic(()=>h.call(this,req,reply),ingestScope(req))}
   :function(this:any,req:any,reply:any){return store.atomic(()=>h.call(this,req,reply))}});
 // Every handler runs with the request person (people.ts actor()); host routes run with none.
 app.addHook('onRoute',(o:any)=>{const h=o.handler;o.handler=function(this:any,req:any,reply:any){return requestPerson.run(req.person??null,()=>h.call(this,req,reply))}});
 if(ownStore)app.addHook('onReady',()=>store.migrate());
 const people=peopleRegistry(config.peopleFile,store),hosts=hostRegistry(store,people,config.browserSecret);app.addHook('onReady',()=>people.refresh());
 const fail=(code:number,message:string)=>{throw Object.assign(Error(message),{statusCode:code})};
 const text=(v:any,max=32000):string=>typeof v==='string'&&v.length>0&&v.length<=max?v:fail(400,'Invalid text');
 const equal=(a:string,b:string)=>Buffer.byteLength(a)===Buffer.byteLength(b)&&timingSafeEqual(Buffer.from(a),Buffer.from(b));
 app.addHook('onRequest',async(req,reply)=>{
  reply.header('X-Content-Type-Options','nosniff').header('Referrer-Policy','no-referrer');
  // Fail closed on the matched route, not the raw URL: only the app shell routes (data comes through /api/*, behind Access and
  // the guard) skip the Bearer. Unknown routes and everything else need it.
  const route=req.routeOptions?.url;
  if(route&&req.method==='GET'&&PUBLIC_ROUTES.has(route)) return;
  const machine=typeof route==='string'&&route.startsWith('/api/hosts/');
  const supplied=(req.headers.authorization||'').replace(/^Bearer /,'');
  // A host is the host its own secret names (§3); a browser request carries the browser secret.
  if(machine){await people.refresh();const h=await hosts.fromSecret(supplied);if(!h)fail(401,'Unauthorized');(req as any).fleetHost=h}
  else if(!equal(supplied,config.browserSecret)) fail(401,'Unauthorized');
  // Two credential kinds never mix (§1): a host never carries a person; a browser request carries the person the guard verified.
  const named=req.headers['x-fleet-person'];
  if(machine){if(named!==undefined)fail(403,'Host requests carry no person');}
  else{if(typeof named!=='string'||!named)fail(401,'Person required');await people.refresh();const p=people.personFor(named as string);if(!p)fail(403,'Unknown person');
   (req as any).person=p}
  if(!machine&&config.browserProxyAuth&&!['GET','HEAD','OPTIONS'].includes(req.method)&&req.headers.origin!==config.browserOrigin)fail(403,'Origin required');
  if(!machine&&req.headers.origin&&req.headers.origin!==(config.browserOrigin??`${req.protocol}://${req.headers.host}`)) fail(403,'Origin rejected');
 });
 // The body hostId of a host request is the derived host: a different one is 403, a missing one is filled in.
 app.addHook('preHandler',async(req:any)=>{const h=req.fleetHost;if(!h||!req.body||typeof req.body!=='object')return;
  if(req.body.hostId!==undefined&&req.body.hostId!==h.id)fail(403,'Host rejected');req.body.hostId=h.id});
 // Visibility (§2): a person other than the owner works in the scope of their grants (row-level security per transaction) plus
 // the rows they hold; architect views need the architect role, except on a row the person holds.
 store.setScope(()=>{const p=requestPerson.getStore();if(!p||p.owner)return null;const ids=p.grants.filter(g=>g.kind==='project').map(g=>g.id);
  return {projectIds:(p.roles.includes('architect')?',':'')+ids.join(','),person:p.email}});
 const ARCHITECT_OPS=/^(account_|lab_|connection|recovery|reconcil|transport_|artifact_rule|profiles_list)/,ARCHITECT_ROUTES=/^\/api\/(lab|accounts|recovery|reconciliation|recovery-tools|profiles)(\/|$)/;
 const opName=(req:any)=>['/api/ops/:name','/api/project-operations/:name'].includes(req.routeOptions?.url)?req.params?.name:['/api/tools','/api/project-tools'].includes(req.routeOptions?.url)&&req.body?.method==='tools/call'?req.body?.params?.name:null;
 // Held-row exception (§2, decision 15): a holder works with their own subscription (the operation's accountId) without the
 // architect role, lists the subscriptions they hold and creates their own.
 const ownSubscription=async(name:string|null,b:any,p:any)=>!!name&&/^account_/.test(name)&&(['account_list','account_create'].includes(name)||typeof b?.accountId==='string'&&(await store.unscoped(()=>get(b.accountId,'managed-account').catch(()=>null)))?.holder===p.email);
 app.addHook('preHandler',async(req:any)=>{const p=req.person;if(!p||p.owner)return;const name=opName(req),route=req.routeOptions?.url??'';
  const args=req.body?.method==='tools/call'?req.body?.params?.arguments:req.body;
  if((name&&ARCHITECT_OPS.test(name)||ARCHITECT_ROUTES.test(route))&&!p.roles.includes('architect')&&!(await ownSubscription(name,args,p)))fail(403,'Architect role required');
  // Project settings and context come from config files, outside row-level security: a named project must be granted.
  const projectId=args?.projectId??(route.startsWith('/api/projects/:')?req.params?.id??req.params?.project:undefined)??req.query?.projectId;
  if(typeof projectId==='string'&&!mayAccessProject(p,projectId))fail(404,'Unknown project')});
 // A write outside the person's scope is refused by row-level security (42501): answer 403, not 500.
 app.setErrorHandler((e:any,_req:any,reply:any)=>{if(e?.code==='42501'){e.statusCode=403;e.message='Not in your scope'}reply.send(e)});
 let artifacts:Artifacts;
 let deliveryRuntime:ReturnType<typeof delivery>,agent:ReturnType<typeof agentTools>,pipelineTimer:any,ticking=false;
 const related=async (d:any)=>({...d,parentState:(await get(d.parentExecutionId,'execution')).state,childState:(await get(d.childExecutionId,'execution')).state,continuationState:d.continuationExecutionId?(await get(d.continuationExecutionId,'execution')).state:null});
 // Host routes only: the preHandler has already set b.hostId to the host the secret names.
 const owned=async (b:any)=>{if(typeof b.hostId!=='string') fail(403,'Host rejected'); if(b.executionId&&(await store.executions.get(b.executionId)).hostId!==b.hostId) fail(403,'Execution rejected')};
 // Operator app (docs/designs/fleet-operator-app.md): '/' opens the Overview. The old pages are gone (docs/designs/legacy-inventory.md).
 app.get('/',async(_,r)=>r.redirect('/overview'));
 // The built app (npm run build -> dist/app); hashed assets are immutable, the entry files are revalidated.
 const appDir=resolve(config.appDir??'dist/app');
 if(existsSync(join(appDir,'assets')))app.register(fastifyStatic,{root:join(appDir,'assets'),prefix:'/assets/',immutable:true,maxAge:'365d',index:false});
 const appFile=(name:string,type:string)=>async(_:any,r:any)=>{if(!existsSync(join(appDir,name)))fail(404,'App not built');return r.header('Cache-Control','no-cache').type(type).send(readFileSync(join(appDir,name)))};
 app.get('/sw.js',appFile('sw.js','text/javascript'));
 app.get('/manifests/:name',async(req:any,r)=>{if(!/^[a-z]+\.webmanifest$/.test(req.params.name))fail(404,'Unknown manifest');return appFile('manifests/'+req.params.name,'application/manifest+json')(req,r)});
 app.get('/icons/:name',async(req:any,r)=>{if(!/^[a-z0-9-]+\.svg$/.test(req.params.name))fail(404,'Unknown icon');return appFile('icons/'+req.params.name,'image/svg+xml')(req,r)});
 // /lab: the app's "Lab is being rebuilt" page until Stage 3 of docs/designs/fleet-lab.md.
 for(const route of new Set([...APP_ROUTES,'/lab','/lab/:id']))app.get(route,appFile('index.html','text/html'));
 app.get('/test-sites/:project/:file',async(req:any,r)=>{if(!config.testSiteRoot||!['hello-world','hello-world-alt'].includes(req.params.project)||!/^[a-zA-Z0-9_-]+\.(html|css)$/.test(req.params.file))fail(404,'Unknown test site');const root=realpathSync(resolve(config.testSiteRoot!,req.params.project)),path=realpathSync(resolve(root,'current',req.params.file));if(!path.startsWith(root+sep))fail(403,'Test-site path rejected');r.header('Content-Security-Policy',"sandbox allow-same-origin; default-src 'none'; style-src 'self' 'unsafe-inline'; img-src data:; frame-ancestors 'self'");return r.type(req.params.file.endsWith('.css')?'text/css':'text/html').send(readFileSync(path))});
 const profiles_list=()=>({profiles:listProfiles(config.profilesDirectory||'config/agents')});
 app.get('/api/profiles',async()=>profiles_list());
 const session_create=async ()=>{const s={id:randomUUID(),createdAt:Date.now(),directedBy:director()};await put('session',s);return {sessionId:s.id,...s}};
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
 const session_get=async (b:any)=>{const s=(await get(b?.sessionId,'session'));return {...s,...(await agent.sessionView(s.id)),sessionId:s.id,delegations:(await map((await all('delegation')).filter(d=>d.parentSessionId===s.id||d.childSessionId===s.id),related)),messages:(await all('message',{sessionId:s.id})).filter(x=>x.sessionId===s.id),executions:(await all('execution',{sessionId:s.id})).filter(x=>x.sessionId===s.id)}};
 app.get('/api/sessions/:id',async(req:any)=>(await session_get({sessionId:req.params.id})));
 // Conversation threads (docs/designs/fleet-conversations.md): pages of typed blocks per task or session, search and export.
 // feedCursor is read in the same snapshot as the page, so the client follows the feed from there and misses no row.
 const thread=threads({query:store.query,artifacts:async(executionId:string)=>all('artifact',{executionId},['contentBase64']),cursor:async()=>(await store.changes.since(null)).cursor});
 const thread_entries=(b:any)=>thread.entries(b),thread_blocks=(b:any)=>thread.blocks(b),thread_search=(b:any)=>thread.search(b),thread_export=(b:any)=>thread.exportText(b,store.read);
 const threadKey=(req:any)=>req.params.scope==='task'?{task:req.params.id}:req.params.scope==='session'?{session:req.params.id}:fail(404,'Unknown thread scope');
 const intOf=(v:any)=>v===undefined?undefined:/^\d+$/.test(v)?Number(v):fail(400,'Expected integer');
 const pointOf=(v:any)=>{if(v===undefined)return undefined;const m=/^(\d+:\d+)(?:@(\d+))?$/.exec(String(v));return m?{key:m[1],offset:Number(m[2]??0)}:fail(400,'Expected seq:index@offset')};
 app.get('/api/threads/:scope/:id/entries',async(req:any)=>thread_entries({key:threadKey(req),limit:intOf(req.query.limit)??100,before:req.query.before,after:req.query.after,around:req.query.around}));
 app.get('/api/threads/:scope/:id/blocks',async(req:any)=>thread_blocks({key:threadKey(req),rows:String(req.query.rows??'').split(',').filter(Boolean).map(x=>{const i=x.indexOf(':');return {kind:x.slice(0,i),id:x.slice(i+1)}})}));
 app.get('/api/threads/:scope/:id/search',async(req:any)=>thread_search({key:threadKey(req),q:req.query.q,before:req.query.before,limit:intOf(req.query.limit)??50}));
 // Streams markdown page by page, each page in its own snapshot, so a long thread is never held as one string.
 app.get('/api/threads/:scope/:id/export',async(req:any,reply:any)=>{const {Readable}=await import('node:stream');const b={key:threadKey(req),from:pointOf(req.query.from),to:pointOf(req.query.to)};
  await store.read(()=>thread.entries({key:b.key,limit:1}));// 404 before the stream starts
  return reply.type('text/markdown; charset=utf-8').send(Readable.from(thread.exportPages(b,store.read)))});
 const execution_differences=async (b:any)=>{
  const current=(await get(b?.executionId,'execution')),previous=(await get(text(b?.previous,300),'execution'));
  if(current.sessionId!==previous.sessionId)fail(403,'Different session');
  const before=await all('artifact',{executionId:previous.id}),after=await all('artifact',{executionId:current.id});
  const lines=async(x:any)=>(await artifacts.readBuffer(x.artifactId,131072)).toString('utf8').split('\n');
  const files=await map([...new Set([...before,...after].map((x:any)=>x.path))].sort(),async (path:any)=>{
   const left=before.find(x=>x.path===path),right=after.find(x=>x.path===path);
   let textChange:any=null;
   if(/\.(html|css)$/i.test(path)&&(!left||left.size<=131072)&&(!right||right.size<=131072)){
    const before=left?await lines(left):[],after=right?await lines(right):[];
    let start=0,end=0;while(start<before.length&&start<after.length&&before[start]===after[start])start++;
    while(end<before.length-start&&end<after.length-start&&before[before.length-1-end]===after[after.length-1-end])end++;
    textChange={firstLine:start+1,removed:before.slice(start,before.length-end),added:after.slice(start,after.length-end)};
   }
   return {path,textChange,state:!left?'added':!right?'removed':left.sha256===right.sha256?'unchanged':'modified',previous:left?{id:left.id,sha256:left.sha256}:null,current:right?{id:right.id,sha256:right.sha256}:null};
  });
  return {sessionId:current.sessionId,previousExecutionId:previous.id,executionId:current.id,files};
 };
 app.get('/api/executions/:id/differences',async(req:any)=>(await execution_differences({executionId:req.params.id,previous:req.query.previous})));
 const execution_events=async (b:any)=>{(await get(b?.executionId,'execution'));return {events:(await all('event',{executionId:b.executionId})).filter(x=>x.executionId===b.executionId&&x.type!=='execution.ended').sort((a,b)=>a.seq-b.seq)}};
 app.get('/api/executions/:id/events',async(req:any)=>(await execution_events({executionId:req.params.id})));
 const managed=accounts(app,store,{...config,hosts,personFor:people.personFor});
 // Git network for agents: profile gitNetwork wins; default on for pm/reviewer/worker sessions of projects with repos.
 const gitNetwork=(s:any)=>s.agentConfig?.gitNetwork??(!!s.projectId&&['pm','reviewer','worker'].includes(s.role)&&projects.project_context({projectId:s.projectId}).settings.repos.length>0);
 // extra: payload fields for the start command and fields for the execution record (pipeline agent steps).
 // Host and account choice is the coordinator's own work: it reads every host and account (store.unscoped, §2).
 const enqueue=(s:any,input:string,requestId:string,extra:{payload?:any,execution?:any,host?:string}={})=>store.unscoped(async()=>{
  // Execution host follows the account binding: the session's last host first, then the default host, then extra native hosts.
  // Director (§5): the session's, else inherited from the execution or pipeline run that created it, else the request person
  // (the owner for timers and workers). Agents never set it.
  if(!s.directedBy){const from=async(id:any,kind:string)=>typeof id==='string'?await get(id,kind).catch(()=>null):null;
   s.directedBy=(await from(s.createdByExecutionId,'execution'))?.directedBy??(await from(s.parentExecutionId,'execution'))?.directedBy??(await from(s.pipelineRunId,'pipeline-run'))?.requestedBy?.email??director();await put('session',s)}
  const harness=s.agentConfig?.harness??'codex',last=(await all('execution',{sessionId:s.id})).filter(x=>x.sessionId===s.id&&x.hostId).at(-1)?.hostId;
  // A pipeline agent step with overrides.host, or a session started with hostId, runs only there: no fallback, the resolve error is the refusal.
  if(extra.host===undefined&&s.hostId)extra={...extra,host:s.hostId};
  const executionHosts=await hosts.executionIds();
  if(extra.host!==undefined&&!executionHosts.includes(extra.host))fail(400,'Unknown host '+extra.host);
  const candidates=extra.host!==undefined?[extra.host]:[...new Set([last,config.hostId,...executionHosts].filter((h:any)=>executionHosts.includes(h)))];
  let hostId=config.hostId,binding:any;const errors:any[]=[];
  for(const h of candidates){try{binding=(await managed.resolve(s.account,harness,h,s.directedBy,s.projectId??null));hostId=h;errors.length=0;break}catch(error){errors.push(error)}}
  if(errors.length)throw errors.find((x:any)=>x.statusCode===403)??errors[candidates.indexOf(config.hostId)]??errors[0];
  // No subscription: the host's own native login runs it, which is its holder's (§4: a login is used by its person).
  if(!binding&&s.directedBy&&(await hosts.byId(hostId))?.holder!==s.directedBy)fail(409,`No subscription of ${s.directedBy} applies on host ${hostId}`);
  const e={...(s.agentConfig?{agentConfig:s.agentConfig,profileSha256:profileDigest(s.agentConfig)}:{}),...(s.account||binding?{requestedAccount:s.account??null,effectiveAccount:binding??{binding:s.account,legacy:true}}:{}),id:randomUUID(),executionId:'',sessionId:s.id,hostId,state:'queued',createdAt:Date.now(),directedBy:binding?.taken?binding.holder:s.directedBy,...(binding?.taken?{takenFrom:s.directedBy}:{}),model:s.agentConfig?.model??'gpt-6.1-sol',effort:s.agentConfig?s.agentConfig.effort:'low'};e.executionId=e.id;
  const command:any={id:randomUUID(),executionId:e.id,sessionId:s.id,hostId,kind:'start',payload:{harness:s.agentConfig?.harness??'codex',...(s.account||binding&&!s.nativeSessionId?{account:binding?.binding??s.account,...(binding?{managedAccount:binding}:{})}:{}),text:input,...(s.projectId?{projectId:s.projectId,project:projects.project_context({projectId:s.projectId}).settings}:{}),...(gitNetwork(s)?{gitNetwork:true}:{}),setup:s.setup??'run',...(s.workingDirectory?{workingDirectory:s.workingDirectory}:{}),model:e.model,effort:e.effort,...(s.agentConfig?{agentConfig:s.agentConfig}:{}),instructions:s.agentConfig?'':'Follow the user request and its scope. Discuss, answer questions, or review existing results when requested; do not create or modify a website unless requested. When creating a static website, deliver output/index.html and retain all source assets. Do not spawn subagents.',...(s.nativeSessionId?{nativeSessionId:s.nativeSessionId}:{}),...extra.payload},status:'pending'};Object.assign(e,extra.execution);(await agent.decorate(s,e,command));
  await put('execution',e);await put('message',{id:randomUUID(),sessionId:s.id,executionId:e.id,requestId:requestId,text:input});
  await put('command',command);
  // An automatic take (the subscription's take rule listed the project): the holder directs the task from now on (§5).
  if(binding?.taken&&s.deliveryId)await store.tasks.take(s.deliveryId,binding.holder);
  return e;
 });
 // Account for a new project session: the named one, else the project preference, else the first enabled account of the harness with a ready binding (on hostId when given). The binding picks the host.
 // Ranking (§6 order, before dispatch): the director's subscriptions before take-rule ones, then the project's subscriptions list.
 const pickAccount=async (harness:string,projectId:string,hostId?:string,directedBy:string|null=director())=>store.unscoped(async()=>{const order:string[]=subscriptionsOf(projectId);
  const ms=(await all('managed-account')).filter(m=>m.intake!=='paused'&&(m.harness??'codex')===harness&&(m.holder===directedBy||(m.take?.projects??[]).includes(projectId))&&(m.bindings??[]).some((b:any)=>b.readiness==='ready'&&(!hostId||b.hostId===hostId)));
  const rank=(m:any)=>(m.holder===directedBy?0:1e6)+(order.includes(m.name)?order.indexOf(m.name):order.includes(m.id)?order.indexOf(m.id):1e5);
  return ms.sort((a:any,b:any)=>rank(a)-rank(b))[0]?.name});
 const subscriptionsOf=(projectId:string|null)=>{if(!projectId)return [];try{return projects.project_context({projectId}).settings.subscriptions??[]}catch{return []}};
 // Known host: configured, or has polled at least once (a host-observation).
 const knownHost=async(hostId:string)=>store.unscoped(async()=>(await hosts.ids()).includes(hostId)||!!(await all('host-observation',{id:hostId})).find(h=>h.id===hostId));
 agent=agentTools(store,{knownHost,browserOrigin:config.browserOrigin,directory:config.profilesDirectory||'config/agents',hostId:config.hostId,enqueue,pickAccount,deliver:async (b:any)=>(await deliveryRuntime.operations.session_deliver(b)),project:(projectId:string)=>projects.project_context({projectId}).settings,resolveAccount:async(account:string,harness:string)=>{let last:any;for(const h of new Set([config.hostId,...(await hosts.executionIds())])){try{const r=await managed.resolve(account,harness,h);if(r)return r}catch(e){last=e}}if(last)throw last}});
 // Executions bound to a host, account or environment nothing registered are marked with a problem (design §1).
 const detectProblems=async()=>{const environments:Record<string,string[]>={};for(const id of projects.project_list?(await projects.project_list()).projects.map((p:any)=>p.id):[]){try{environments[id]=projects.project_context({projectId:id}).settings.environments.map((e:any)=>e.hostId)}catch{}}
  return store.executions.detectProblems({hosts:await hosts.ids(),environments})};
 const delegates=delegation(store,config.profilesDirectory||'config/agents',enqueue,(id:string)=>artifacts.readBuffer(id,128000));
 app.get('/api/executions/:id/delegations',async(req:any)=>{(await get(req.params.id,'execution'));return {delegations:(await map((await all('delegation')).filter(d=>d.parentExecutionId===req.params.id||d.childExecutionId===req.params.id||d.continuationExecutionId===req.params.id),related))}});
 app.post('/api/sessions/:id/messages',async(req:any)=>(await store.tx(async ()=>{
  const s=(await get(req.params.id,'session')),b=req.body; if(!b||Array.isArray(b)||typeof b!=='object'||Object.keys(b).some(k=>!['requestId','text','delivery'].includes(k))||!Object.hasOwn(b,'requestId')||!Object.hasOwn(b,'text'))fail(400,'Expected requestId, text and optional delivery');text(b.requestId,200);text(b.text);return (await agent.submit({sessionId:s.id,requestId:b.requestId,text:b.text,delivery:b.delivery}));
 })));
 // Session finish/archive: one teardown command on the last execution's host (W2 executor removes the worktree).
 const session_archive=async (b:any)=>{const s=(await get(b?.sessionId,'session')),es=(await all('execution',{sessionId:s.id})).filter(x=>x.sessionId===s.id);if(es.some(e=>!['completed','failed','stopped','unknown'].includes(e.state)))fail(409,'Execution active');
  const old=(await all('command',{sessionId:s.id,kind:'teardown'})).find(c=>c.sessionId===s.id&&c.kind==='teardown');if(old)return old;s.archivedAt=Date.now();await put('session',s);const last=es.filter(e=>e.hostId).at(-1);if(!last)return {sessionId:s.id,archivedAt:s.archivedAt,teardown:null};
  const c={id:randomUUID(),executionId:last.id,sessionId:s.id,hostId:last.hostId,kind:'teardown',payload:{...(s.projectId?{projectId:s.projectId,project:projects.project_context({projectId:s.projectId}).settings}:{}),...(s.workingDirectory?{workingDirectory:s.workingDirectory}:{})},status:'pending'};await put('command',c);return c};
 const execution_stop=async (b:any)=>{const e=(await get(b?.executionId,'execution'));let c=(await all('command',{executionId:e.id,kind:'stop'})).find(x=>x.executionId===e.id&&x.kind==='stop');if(!c){c={id:randomUUID(),executionId:e.id,sessionId:e.sessionId,hostId:e.hostId,kind:'stop',payload:{},status:'pending'};await put('command',c)}e.stopRequested=true;
  // A host that is neither configured nor ever polled has fetched none of this execution's commands, so no host can confirm
  // the stop: settle it here as stopped (its pending commands too) instead of leaving it open forever (example: 6b00f0fb).
  if(e.hostId&&!(await knownHost(e.hostId))&&!['completed','failed','stopped'].includes(e.state)){for(const x of (await all('command',{executionId:e.id})).filter(x=>x.executionId===e.id&&x.status==='pending')){x.status='stopped';await put('command',x)}e.state='stopped';e.stopDetail='host never registered';c=(await get(c.id,'command'))}
  await put('execution',e);return {...c,stop:(await agent.stopOutcome(e))}};
 app.post('/api/executions/:id/stop',async(req:any)=>(await store.tx(async ()=>(await execution_stop({executionId:req.params.id})))));
 app.post('/api/hosts/agent-tools',async(req:any)=>{const b=req.body;(await owned(b));const c=(await get(b.commandId,'command'));if(c.executionId!==b.executionId||c.hostId!==b.hostId)fail(403,'Command rejected');return (await agent.mcp(b.request,async (n:string,a:any)=>READ_OPERATIONS.has(n)?store.read(()=>agent.bound(n,a,b.executionId)):store.tx(()=>agent.bound(n,a,b.executionId)),'fleet-agent'))});
 app.get('/api/reconciliation',async()=>({reconciliation:(await all('reconciliation')).at(-1)??null}));
 // Host problems the executor reports with each poll (edge-redirect: the edge answers one of its routes with a login page or redirect); the list is
 // replaced on every poll, so a recovered route clears itself.
 const HOST_STALE_MS=30000;
 const hostProblems=(v:PollRequest['problems'])=>Array.isArray(v)?{problems:v.filter(p=>p?.kind==='edge-redirect'&&typeof p.route==='string'&&Number.isSafeInteger(p.status)).slice(0,20).map(p=>({kind:'edge-redirect',route:p.route.slice(0,200),status:p.status,location:typeof p.location==='string'?p.location.slice(0,500):null,detail:typeof p.detail==='string'?p.detail.slice(0,500):null}))}:{};
 app.post<{Body:PollRequest}>('/api/hosts/poll',async(req)=>(await store.tx(async ()=>{(await owned(req.body));await stores.observe(req.body.hostId,req.body.stores);await put('host-observation',{id:req.body.hostId,holder:req.fleetHost.holder,lastSeenAt:Date.now(),...( ['darwin','win32','linux'].includes(req.body.platform)?{platform:req.body.platform}:{} ),...(Array.isArray(req.body.capabilities)&&req.body.capabilities.every((x:any)=>typeof x==='string')?{capabilities:req.body.capabilities}:{}),...hostProblems(req.body.problems)});(await delegates.reconcile());await detectProblems();(await agent.promote());const pending=await filter(await all('command',{hostId:req.body.hostId,status:'pending'}),(x:any)=>x.hostId===req.body.hostId&&x.status==='pending');
  // ponytail: executions read once for all pending commands; delegates.ready only rewrites the execution of its own start command.
  const executions=await store.many(pending.map((c:any)=>c.executionId),'execution');
  const ready=async (command:any)=>{if(!(await agent.ready(command)))return false;const execution=executions.get(command.executionId)??(await get(command.executionId,'execution'));// Held-host trust (§3): work that carries project content goes only to a host whose holder may see the project;
  // control commands (stop, steer, teardown, inspections) of the host's own executions always pass.
  if(['start','script','delivery-step'].includes(command.kind)&&!hosts.maySee(req.fleetHost,execution.projectId??command.payload?.projectId))return false;if(command.kind!=='stop'&&!(await delegates.ready(execution)))return false;return true};const chosen=await filter(pending,ready),fresh=await store.many(chosen.map((x:any)=>x.id),'command');const reply:PollReply={commands:await map(chosen,async (x:any)=>fresh.get(x.id)??(await get(x.id,'command')))};return reply})));
 app.post('/api/hosts/receipts',async(req:any)=>{if(req.body?.accountObservation||req.body?.accountCatalogue)return store.atomic(async()=>{const b=req.body;(await owned(b));const c=(await get(b.commandId,'command')),v=b.accountObservation??b.accountCatalogue;if(c.hostId!==b.hostId||c.executionId!==b.executionId||c.kind!=='account-inspect'||c.payload.accountId!==v.accountId||c.payload.binding!==(v.binding??null))fail(403,'Account inspection ownership rejected');return (b.accountCatalogue?managed.catalogue:managed.report)({...v,hostId:c.payload.nativeHostId})});if(req.body?.delegation?.operation){const b=req.body;(await owned(b));const c=(await get(b.commandId,'command')),session=(await get(c.sessionId,'session')),q=b.delegation;if(c.executionId!==b.executionId||c.hostId!==b.hostId||!session.projectId||q.projectId!==session.projectId||!['project_context','catalogue','pipeline_run','direct_search','pipeline_history','pipeline_get','question_ask','question_history','account_list'].includes(q.operation))fail(403,'Project grant rejected');let started:(run:any)=>void=()=>{};const begun=new Promise<any>(r=>started=r);const call=()=>projects[q.operation]({...q.arguments,projectId:session.projectId},{kind:'native-agent',executionId:b.executionId,sessionId:session.id},started);const result=LONG_OPERATIONS.has(q.operation)?call():store.atomic(call);if(['pipeline_run','direct_search'].includes(q.operation)){const run=await Promise.race([begun,result.then(()=>null,()=>null)]);if(!run)return result;result.catch(async ()=>{const current=(await get(run.id,'pipeline-run'));if(current.state==='running'){current.state='unknown';current.error={code:'dispatch-unknown',message:'Native tool dispatch outcome unconfirmed; inspect original evidence, do not replay'};current.steps[0].state='unknown';current.steps[0].error=current.error;current.durationMs=null;await put('pipeline-run',current)}});return run}return result}return (await store.tx(async ()=>{const b:Receipt=req.body;(await owned(b));const c=(await get(b.commandId,'command'));if(c.executionId!==b.executionId||c.hostId!==b.hostId)fail(403,'Command rejected');if(req.body.delegation)return (await delegates.request(b,c));if(!['accepted','started','completed','failed','stopped','unknown'].includes(b.status))fail(400,'Invalid status');const ranks:any={pending:0,accepted:1,started:2,unknown:2,completed:3,failed:3,stopped:3}; if(ranks[b.status]<ranks[c.status]||(ranks[c.status]===3&&b.status!==c.status))fail(409,'Receipt regression'); // A repeated terminal receipt (an outbox retry) changes nothing: the gate and onReceipt ran once.
if(ranks[c.status]===3&&b.status===c.status)return {ok:true}; if(b.nativeSessionId){const known=(await agent.knownNative((await get(b.executionId,'execution'))));if(known&&known!==b.nativeSessionId)fail(409,'Native identity conflict')} c.status=b.status;await put('command',c);const e=(await get(b.executionId,'execution'));const wasFinal=['completed','failed','stopped'].includes(e.state);if(!wasFinal&&(c.kind!=='steer'&&(c.kind!=='stop'||b.status==='stopped')))e.state=b.status;e.updatedAt=Date.now();if(b.status==='started'&&!e.startedAt)e.startedAt=e.updatedAt;if(c.kind!=='steer'&&['completed','failed','stopped'].includes(b.status)&&!e.finishedAt)e.finishedAt=e.updatedAt;if(b.error)e.error=b.error;if(['start','script'].includes(c.kind)&&b.status==='completed'&&!wasFinal)Object.assign(e,await artifactGate(e,Array.isArray(b.artifacts)?b.artifacts.slice(0,10000):[]));await put('execution',e);if(b.status!=='unknown')await store.executions.markObservation(e.id,'confirmed');if(b.nativeSessionId&&c.kind==='start')(await agent.captureNative(e,text(b.nativeSessionId,300)));if(e.state!=='waiting'){(await agent.onReceipt(c,e,e.state==='failed'&&b.status==='completed'?'failed':b.status));if(['completed','failed','stopped'].includes(e.state))(await agent.promote());(await deliveryRuntime?.advance())}if(['completed','failed','stopped','unknown'].includes(e.state))setImmediate(()=>runner.onExecution(e.id).catch(()=>{}));return {ok:true}}))});
 const transportRecovery=recovery(app,{...store,transaction:store.tx},owned);
 app.post<{Body:HostEvents}>('/api/hosts/events',async(req)=>(await transportRecovery.ingest(req.body)));
// Artifact routes between executors (or program workers) and the coordinator (design §3 Write protocol). Long routes: each
 // operation takes short transactions around its store calls.
 const hostArtifact=(fn:(b:any)=>Promise<any>)=>async(req:any)=>{const b=req.body??{};(await owned({hostId:b.hostId}));return fn(b)};
 app.post('/api/hosts/artifacts/reserve',hostArtifact(async b=>{(await owned(b));return artifacts.reserve(b)}));
 app.post('/api/hosts/artifacts/parts',hostArtifact(b=>artifacts.parts(b)));
 app.post('/api/hosts/artifacts/commit',hostArtifact(b=>artifacts.commit(b)));
 app.post('/api/hosts/artifacts/verified',hostArtifact(b=>artifacts.verified(b)));
 app.post('/api/hosts/artifacts/renew',hostArtifact(b=>artifacts.renew(b)));
 app.post('/api/hosts/artifacts/failed',hostArtifact(b=>artifacts.refused(b)));
 // artifact_save required=true: recorded through the outbox before the completion receipt, independent of the upload (P3-D3).
 app.post('/api/hosts/artifacts/declare',hostArtifact(async b=>{(await owned(b));return artifacts.declare(b)}));
 // A host reads an artifact only for its own command that names it (delivery files, copies), or for its own execution in
 // the artifact's project (agent artifact_get).
 const hostMayRead=async(b:any)=>{const a=await artifacts.get(text(b.artifactId,200));
  if(a.projectId){const host=await hosts.byId(b.hostId);if(!host||!hosts.maySee(host,a.projectId))fail(403,'Artifact not readable by this host')}
  if(typeof b.commandId==='string'){const c=await get(b.commandId,'command').catch(()=>null);
   const named=c?.kind==='delivery-step'?(c.payload?.files??[]).map((f:any)=>f?.artifactId):['artifact-copy','artifact-verify'].includes(c?.kind)?[c.payload?.artifactId]:[];
   if(c&&c.hostId===b.hostId&&['pending','accepted','started'].includes(c.status)&&named.some((x:any)=>typeof x==='string'&&(x===a.id||x===a.legacyId)))return a}
  // Lab examples hold reference results: no agent reads them through the host routes (Stage 2 hands a trial its inputs only).
  if(typeof b.executionId==='string'){(await owned(b));const e=await store.executions.get(b.executionId);if((a as any).role!=='example'&&(a.executionId===e.id||(e.projectId&&a.projectId===e.projectId)))return a
   // Pipeline step inputs: the artifacts this execution's own start or script command names in payload.inputs.
   const named=(await all('command',{executionId:e.id})).filter((c:any)=>c.executionId===e.id&&c.hostId===b.hostId&&['start','script'].includes(c.kind)).flatMap((c:any)=>(c.payload?.inputs??[]).map((i:any)=>i?.artifactId));
   if((a as any).role!=='example'&&named.includes(a.id))return a}
  return fail(403,'Artifact not readable by this host')};
 app.post('/api/hosts/artifacts/get',hostArtifact(async b=>artifacts.get((await hostMayRead(b)).id,b.hostId)));
 app.post('/api/hosts/artifacts/read',async(req:any,r:any)=>{const b=req.body??{};(await owned({hostId:b.hostId}));const a=await hostMayRead(b);return r.type(a.mediaType||'application/octet-stream').send(await artifacts.read(a.id))});
 // artifact_list for an agent: artifacts of its own project (or its own execution when it has no project).
 app.post('/api/hosts/artifacts/list',async(req:any)=>{const b=req.body??{};(await owned(b));const e=await store.executions.get(text(b.executionId,200));const f=b.filter??{};
  return {artifacts:await artifacts.list({executionId:!e.projectId?e.id:typeof f.executionId==='string'?f.executionId:undefined,taskId:typeof f.taskId==='string'?f.taskId:undefined,mediaType:typeof f.mediaType==='string'?f.mediaType:undefined,projectId:e.projectId??undefined,noExamples:true})}});
 app.post('/api/hosts/stores',async(req:any)=>{const b=req.body??{};(await owned({hostId:b.hostId}));return store.tx(()=>stores.register(b.hostId,b.store,req.fleetHost.holder))});
 // Results stream from a verified copy with the same sandbox as before (no size limit; bytes never sit in the coordinator).
 app.get('/api/results/:id',async(req:any,r)=>{const a=await artifacts.get(req.params.id);if(a.state!=='stored')fail(404,'Not found');r.header('Content-Security-Policy',"sandbox allow-scripts; default-src 'none'; script-src 'unsafe-inline'; style-src 'unsafe-inline'; img-src data:");return r.type(a.mediaType||'application/octet-stream').send(await artifacts.read(a.id))});
 // Pipeline v2 runner (docs/designs/fleet-lab.md Stage 2): api steps call connections from this host; agent steps are
 // ordinary agent executions with the step's profile (variant overrides applied); script steps are executor script commands.
 const configDir=config.projectConfigDirectory||'config',profilesDir=config.profilesDirectory||'config/agents';
 let lab:ReturnType<typeof labExperiments>|undefined;
 const runner=pipelineRunner({store:store as any,directory:configDir,connectionsDir:config.connectionsDir,client:config.connectionClient,
  // A Lab run's end moves its experiment; a completed draft-examples run proposes its drafted examples.
  onFinish:run=>{if(!lab)return;if(run.experimentId)lab.reconcile(run.experimentId).catch(e=>app.log.error(e));if(run.pipelineId==='draft-examples'&&run.state==='completed')lab.importDrafts(run,b=>examples.add(b)).catch(e=>app.log.error(e))},
  save:o=>artifacts.saveObject(o),read:id=>artifacts.read(id).then(async (b:any)=>Buffer.isBuffer(b)?b:Buffer.from(await new Response(b).arrayBuffer())),
  artifactsOf:async id=>(await artifacts.list({executionId:id})).filter((a:any)=>a.state==='stored').map((a:any)=>({id:a.id,path:a.name,sha256:a.sha256})),
  unsettled:async id=>(await artifacts.list({executionId:id})).filter((a:any)=>a.state==='pending').length,
  startAgent:async({run,step,stepExecutionId,prompt,inputs,overrides})=>{const profile:any=readTrustedProfile(profilesDir,overrides.profile??step.profile);
   if(overrides.model!==undefined)profile.model=overrides.model;if(overrides.skills!==undefined)profile.skills=overrides.skills;
   if(overrides.web_route!==undefined&&!['connections','builtin'].includes(overrides.web_route))fail(400,'web_route must be connections or builtin');
   const skills=(profile.skills??[]).map((id:string)=>{if(!/^[a-z0-9-]{1,80}$/.test(id))fail(400,'Invalid skill');return {id,text:readFileSync(resolve(configDir,'skills',id,'SKILL.md'),'utf8')}});
   const s={id:randomUUID(),createdAt:Date.now(),agentConfig:profile,...(overrides.account?{account:overrides.account}:{}),...(run.projectId?{projectId:run.projectId}:{}),pipelineRunId:run.id};await put('session',s);
   const e=await store.atomic(()=>enqueue(s,prompt,'pipeline:'+stepExecutionId,{...(overrides.host!==undefined?{host:String(overrides.host)}:{}),payload:{inputs,skills,webRoute:overrides.web_route??'connections',connectionLabel:stepExecutionId},execution:{pipelineStepId:stepExecutionId,pipelineRunId:run.id,...(run.experimentId?{experimentId:run.experimentId}:{})}}));return e.id},
  startScript:async({run,stepExecutionId,command,timeoutMs,hostId,inputs})=>store.atomic(async()=>{const s={id:randomUUID(),createdAt:Date.now(),...(run.projectId?{projectId:run.projectId}:{}),pipelineRunId:run.id};await put('session',s);
   const e:any={id:randomUUID(),executionId:'',sessionId:s.id,hostId:hostId??config.hostId,state:'queued',createdAt:Date.now(),pipelineStepId:stepExecutionId,pipelineRunId:run.id,...(run.experimentId?{experimentId:run.experimentId}:{})};e.executionId=e.id;
   await put('execution',e);await put('command',{id:randomUUID(),executionId:e.id,sessionId:s.id,hostId:e.hostId,kind:'script',payload:{command,inputs,...(timeoutMs?{timeout_ms:timeoutMs}:{})},status:'pending'});return e.id})});
 const projects=(registerProjects(app,store,{directory:configDir,runner}));
 // Generic v2 run: an example (an approved example artifact, references withheld, or an inline example) and variant overrides.
 app.post('/api/pipelines/:id/runs',async(req:any)=>{const b=req.body??{};if(typeof b.requestId!=='string'||!b.requestId||b.requestId.length>200||Object.keys(b).some(k=>!['requestId','projectId','exampleArtifactId','example','overrides'].includes(k)))fail(400,'Expected requestId and optional projectId, exampleArtifactId or example, overrides');
  const file=String(req.params.id)+'.json';runner.load(file);
  const signature=createHash('sha256').update(JSON.stringify(b)).digest('hex');
  const created=await store.atomic(async()=>{const old=(await all('pipeline-run',{requestId:b.requestId,pipeline:file})).find((r:any)=>r.requestId===b.requestId&&r.pipeline===file);if(old){if(old.signature!==signature)fail(409,'Request identity conflict');return {old}}
  let example=b.example??{};if(b.exampleArtifactId){const ex=(await examples.get(text(b.exampleArtifactId,200)));if(ex.state!=='approved')fail(409,'Example not approved');example=ex.example}
  // References are known-good answers: a run never carries them (Stage 1 rule, agents never see references).
  const {references:_,...withoutReferences}=example;example=withoutReferences;
  return {run:await runner.start({file,projectId:b.projectId??null,requestId:b.requestId,example,overrides:b.overrides??{},extra:{signature}})}});
  if('old' in created)return created.old;runner.advance(created.run.id).catch(()=>{});return created.run});
 app.addHook('onReady',async()=>{pipelineTimer=setInterval(()=>{if(ticking)return;ticking=true;runner.tick().then(()=>lab?.reconcileRunning()).catch(()=>{}).finally(()=>{ticking=false})},config.pipelineTickMs??5000);pipelineTimer.unref?.()});
 app.addHook('onClose',async()=>{clearInterval(pipelineTimer)});
 Object.assign(projects,managed.operations);
 // API key connections (fleet-connections.md, C0). Key values: FLEET_SECRET_DIR in production, a temporary folder otherwise.
 const connections=connectionRegistry(store,{secretDir:config.secretDir??mkdtempSync(join(tmpdir(),'fleet-secrets-')),gatewayDir:config.gatewayDir});
 Object.assign(projects,connections.operations);
 app.addHook('onReady',async()=>{await store.atomic(()=>connections.sync());await connections.writeSnapshot()});
 app.decorate('connections',connections);
 // Success rule (design §3, P3-D2): a start command reported completed waits for the execution's required artifacts; a
 // missing or failed one fails it. settle() finishes a waiting execution once its artifacts are settled or the window passed.
 const artifactGate=async(e:any,declared:string[]=[])=>{const r=await artifacts.requirement(e.id,declared);
  if(r.missing.length||r.failed.length)return {state:'failed',error:{code:'artifact-required',message:'Required artifacts missing or failed',missing:r.missing,failed:r.failed}};
  if(r.pending.length)return {state:'waiting',waitReason:'artifacts',waitingSince:Date.now(),pendingArtifacts:r.pending,declaredArtifacts:declared};return {}};
 const settle=async(executionId:string,expired?:string)=>{const e=await get(executionId,'execution').catch(()=>null);if(!e||e.state!=='waiting'||e.waitReason!=='artifacts')return;
  const r=await artifacts.requirement(e.id,e.declaredArtifacts??[]);if(!expired&&r.pending.length&&!r.failed.length&&!r.missing.length)return;
  const failed=!!expired||r.failed.length>0||r.missing.length>0||r.pending.length>0;
  delete e.waitReason;delete e.waitingSince;delete e.pendingArtifacts;delete e.declaredArtifacts;e.state=failed?'failed':'completed';e.updatedAt=Date.now();
  if(failed)e.error={code:'artifact-required',message:expired??'Required artifacts missing or failed',missing:r.missing,failed:r.failed,pending:r.pending};
  await put('execution',e);const c=(await all('command',{executionId:e.id,kind:'start'}))[0];
  if(c){(await agent.onReceipt(c,e,e.state));(await agent.promote());(await deliveryRuntime?.advance())}setImmediate(()=>runner.onExecution(e.id).catch(()=>{}))};
 // Artifact store (design §3): registry, operations, placement. Locations come from the project settings, the coordinator
 // default otherwise (PDS18 unless configured).
 // Store keys: FLEET_STORE_SECRETS in production; next to a file database, or a temporary folder, otherwise.
 const stores=storeRegistry(store,{endpointHosts:config.storeEndpointHosts,secretsDir:config.storeSecrets??(/^(postgres(ql)?:|memory:)/.test(config.database)?mkdtempSync(join(tmpdir(),'fleet-store-secrets-')):join(dirname(config.database),'stores'))});
 const locations=(projectId:string|null)=>{if(projectId){try{const l=projects.project_context({projectId}).settings.artifact_locations;if(l)return l}catch{}}return config.defaultArtifactLocations??[config.hostId]};
 const rulesOf=(projectId:string|null)=>{if(!projectId)return [];try{return projects.project_context({projectId}).settings.artifact_rules??[]}catch{return []}};
 // Tool commands for a host's executor (copy, verify): a program-worker execution of its own, like the other host tools.
 const queueCommand=async(hostId:string,kind:string,payload:any)=>{const sid=randomUUID(),id=randomUUID();
  await put('session',{id:sid,createdAt:Date.now(),actor:'program-worker'});
  await put('execution',{id,executionId:id,sessionId:sid,hostId,state:'queued',createdAt:Date.now(),actor:'program-worker',artifactId:payload.artifactId});
  await put('command',{id:randomUUID(),executionId:id,sessionId:sid,hostId,kind,status:'pending',payload})};
 artifacts=artifactModel(store,{stores,locations,access:()=>accessToken(config.cfAccessFile),retryWindowMs:()=>config.artifactRetryWindowMs??86400000,queueCommand:(h,k,p)=>store.tx(()=>queueCommand(h,k,p)),onSettled:(id:string)=>store.tx(()=>settle(id))});
 store.registerKind('artifact',artifacts.adapter);
 // Lab examples (docs/designs/fleet-lab.md Stage 1): owner import, list, read. Experiments and the screen come in Stage 3.
 const examples=labExamples(store,artifacts,config.labDirectory||'config/lab');
 app.post('/api/lab/examples',async(req:any)=>examples.add(req.body??{}));
 app.get('/api/lab/examples',async(req:any)=>examples.list({state:req.query?.state}));
 app.get('/api/lab/examples/:id',async(req:any)=>examples.get(req.params.id));
 // Lab experiments, scores and ratings (Stage 3). Rating and budget answers arrive through tasks.answer.
 lab=labExperiments({store,runner,examples,labDirectory:config.labDirectory||'config/lab',read:id=>artifacts.readBuffer(id,64*1024*1024),
  stopExecution:executionId=>store.tx(()=>execution_stop({executionId})),catalogue:projectId=>projects.catalogue({projectId}),recommend:(projectId,methodId)=>store.tx(()=>projects.recommendation_set({projectId,methodId}))});
 store.tasks.onAnswer(lab.answers);const labOps=labOperations(lab,examples);
 // Experiments reconcile on start, and on the pipeline tick as a backstop for answers committed after their hook ran.
 app.addHook('onReady',async()=>{lab!.reconcileAll().catch(e=>app.log.error(e));
  // A finish hook lost to a restart: completed draft runs are imported again (idempotent).
  for(const r of await all('pipeline-run',{pipelineId:'draft-examples',state:'completed'}))lab!.importDrafts(r,b=>examples.add(b)).catch(e=>app.log.error(e))});
 const placement=placementWorker(store,{stores,artifacts,locations,rules:rulesOf,queueCommand:(h,k,p)=>store.tx(()=>queueCommand(h,k,p)),retryWindowMs:()=>config.artifactRetryWindowMs??86400000,onExpired:(id:string)=>store.tx(()=>settle(id,'artifact retry window passed'))});
 Object.assign(projects,{
  artifact_list:async(b:any)=>({artifacts:await artifacts.list({executionId:b?.executionId,taskId:b?.taskId,mediaType:b?.mediaType,projectId:b?.projectId})}),
  artifact_get:(b:any)=>artifacts.get(text(b?.artifactId,200)),
  artifact_purge:(b:any)=>artifacts.purge({contentHash:b?.contentHash,projectId:b?.projectId,scope:b?.scope,reason:b?.reason}),
  artifact_rule_preview:(b:any)=>placement.previewRule(b?.projectId??null,b?.rule),
  artifact_rule_confirm:(b:any)=>placement.confirmRule(b?.projectId??null,b?.rule,actor('operator')),
  stores_list:async()=>{const all=await stores.list();return {stores:all,problems:[...all.filter(s=>s.state==='full').map(s=>({kind:'store-full',storeId:s.id,hostId:s.host_id,capacityBytes:s.capacity_bytes,freeSpaceWarningBytes:s.free_space_warning_bytes,actions:['store_set_free_space_warning','project artifact_locations','artifact_rule_confirm']})),...placement.problems().filter((x:any)=>{const p=requestPerson.getStore();return !p||p.owner||(!!x.projectId&&mayAccessProject(p,x.projectId))})]}},
  store_set_free_space_warning:(b:any)=>stores.setFreeSpaceWarning(b?.storeId,b?.bytes),
  artifact_copy_retry:(b:any)=>placement.retryCopy(text(b?.artifactId,200),text(b?.storeId,200)),
 });
 deliveryRuntime=delivery(store,{...config,hosts},enqueue,projects,managed.resolve,async(id:string)=>subscriptionsOf(id)[0],(id:string)=>artifacts.readBuffer(id,1024*1024));
 Object.assign(projects,deliveryRuntime.operations);
 app.addHook('onReady',()=>store.atomic(async()=>{await deliveryRuntime.advance();await agent.reconcile()}));
 // Placement runs on its own timer (placement_interval_seconds, default 60 s); 0 turns the timer off (tests run it directly).
 let placementTimer:any;const placementMs=config.placementIntervalMs??60000;
 app.addHook('onReady',async()=>{if(placementMs>0){let running=false;placementTimer=setInterval(()=>{if(running)return;running=true;/* single flight: a slow pass never overlaps the next */stores.sweep().then(()=>artifacts.backfill()).then(()=>placement.runPlacement()).catch(e=>app.log.error(e)).finally(()=>{running=false})},placementMs);placementTimer.unref()}});
 app.addHook('onClose',async()=>{clearInterval(placementTimer)});
 app.decorate('artifacts',artifacts);app.decorate('placement',placement);app.decorate('stores',stores);
 // One operation table: served as MCP tools (/api/tools) and as POST /api/ops/<name>; the web UI calls the same names.
 // ponytail: per-feature routes stay as aliases of the operations.
 // Tasks, questions and executions (design §1-2). Operator answers carry the person's e-mail; agents answer through reply.
 const str=(v:any,name:string,max=4000)=>typeof v==='string'&&v.length>0&&v.length<=max?v:fail(400,'Expected '+name);
 const typedOps={
  task_create:async(b:any)=>{const id=await store.tasks.create({projectId:b.projectId??null,title:str(b.title,'title',500),expectedOutcome:b.expectedOutcome??null,priority:b.priority,completionRequirements:b.completionRequirements,resultDestination:b.resultDestination??null,createdBy:actor('operator'),directedBy:director(),dependsOn:Array.isArray(b.dependsOn)?b.dependsOn.map((x:any)=>str(x,'dependsOn',200)):[]});return store.tasks.get(id)},
  task_update:(b:any)=>store.tasks.update({...b,taskId:str(b?.taskId,'taskId',200)}),
  task_get:(b:any)=>store.tasks.get(str(b?.taskId,'taskId',200)),
  // Asks the operator to accept a task's stored results (design §2 Acceptance); the default artifacts are the task's stored originals.
  task_request_acceptance:async(b:any)=>{const t=await store.tasks.get(str(b?.taskId,'taskId',200));if(t.state!=='in_progress')fail(409,'Task is not in progress');
   const ids=Array.isArray(b.artifactIds)?b.artifactIds:(await artifacts.list({taskId:t.id})).filter((a:any)=>a.state==='stored'&&a.role==='original').map((a:any)=>a.id);
   const id=await store.tasks.ask({kind:'acceptance',taskId:t.id,projectId:t.projectId,text:typeof b.text==='string'?b.text.slice(0,2000):'Accept the result of: '+t.title,options:['accepted','rejected'],askedBy:actor('operator'),data:{artifactIds:ids}});
   return (await store.tasks.questions({taskId:t.id})).find((q:any)=>q.id===id)},
  task_list:async(b:any)=>({tasks:await store.tasks.list({projectId:b?.projectId,state:b?.state})}),
  // Take (§5): the person directs the task from now on; they need a grant on its project.
  task_take:async(b:any)=>{const t=await store.tasks.get(str(b?.taskId,'taskId',200));const p=requestPerson.getStore();
   if(!p||!mayAccessProject(p,t.projectId))fail(403,'Take needs a grant on the task project');
   if(t.directedBy!==p!.email){await store.tasks.take(t.id,p!.email);await put('setting-change',{id:randomUUID(),target:'task:'+t.id,projectId:t.projectId,field:'directedBy',previous:t.directedBy,value:p!.email,actor:p!.email,at:Date.now()})}return store.tasks.get(t.id)},
  questions_list:async(b:any)=>({questions:await store.tasks.questions({taskId:b?.taskId,state:b?.state,kind:b?.kind})}),
  questions_answer:async(b:any)=>{const q=await store.tasks.answer(str(b?.questionId,'questionId',200),{answer:str(b.answer,'answer'),by:actor('operator'),artifactIds:Array.isArray(b.artifactIds)?b.artifactIds:undefined});return (await store.tasks.questions({})).find(x=>x.id===q.id)},
  executions_list:async(b:any)=>({executions:await store.executions.list({taskId:b?.taskId,projectId:b?.projectId,parentExecutionId:b?.parentExecutionId,problem:b?.problem===true})}),
  // Operator app reads (docs/designs/fleet-operator-app.md §6); agents can call them too.
  changes_since:(b:any)=>{const c=b?.cursor;if(c!==null&&c!==undefined&&(!Number.isSafeInteger(c)||c<0))fail(400,'Invalid cursor');return store.changes.since(c??null,b?.limit??500)},
  task_page:(b:any)=>store.tasks.page({filter:b?.filter,group:b?.group,groupCounts:b?.groupCounts,limit:b?.limit,after:b?.after}),
  sessions_list:(b:any)=>sessionList.page({filter:b?.filter,limit:b?.limit,after:b?.after}),
  list_membership:async(b:any)=>{const ids=Array.isArray(b?.ids)&&b.ids.length<=1000?b.ids:fail(400,'Expected at most 1000 ids');if(!Array.isArray(b?.lists)||b.lists.length<1||b.lists.length>2)fail(400,'Expected one or two lists');
   return {lists:await map(b.lists,async(l:any)=>l?.name==='tasks'?{name:'tasks',rows:await store.tasks.membership(l.filter??{},l.group,ids),...(await store.tasks.summary(l.filter??{},l.group))}
    :l?.name==='sessions'?{name:'sessions',rows:await sessionList.membership(l.filter??{},ids),counts:(await sessionList.page({filter:l.filter,limit:1})).counts}:fail(400,'Unknown list'))}},
  hosts_list:async()=>{const configured=await hosts.ids();
   const observed=await all('host-observation'),ids=[...new Set([...configured,...observed.map((h:any)=>h.id)])];
   const running=await store.query("SELECT host_id,harness,count(*)::int AS n FROM execution WHERE state IN ('preparing','running') AND host_id IS NOT NULL GROUP BY 1,2");
   const queued=await store.query("SELECT data->>'hostId' AS host_id,count(*)::int AS n FROM document WHERE kind='command' AND data->>'status'='pending' GROUP BY 1");
   return {staleAfterMs:HOST_STALE_MS,serverNow:Date.now(),hosts:ids.map(id=>{const o:any=observed.find((h:any)=>h.id===id);const by:Record<string,number>={};for(const r of running)if(r.host_id===id)by[r.harness??'unknown']=r.n;
    return {id,configured:configured.includes(id),platform:o?.platform??null,lastSeenAt:o?.lastSeenAt??null,capabilities:o?.capabilities??[],// A host that stopped polling shows no problems: its staleness is the signal, not a report that may be days old.
    problems:o?.lastSeenAt&&Date.now()-o.lastSeenAt<HOST_STALE_MS?o?.problems??[]:[],runningByHarness:by,running:Object.values(by).reduce((a,n)=>a+n,0),slots:null,queued:queued.find((q:any)=>q.host_id===id)?.n??0,stores:'unavailable'}})}},
  task_derived_check:async()=>store.tasks.derivedDrift(),
  hosts_register:(b:any)=>hosts.operations.hosts_register(b,requestPerson.getStore()??null),
  hosts_update:(b:any)=>hosts.operations.hosts_update(b,requestPerson.getStore()??null),
  hosts_rotate_secret:(b:any)=>hosts.operations.hosts_rotate_secret(b,requestPerson.getStore()??null),
  me:async()=>{const p=requestPerson.getStore();return {email:p?.email??null,name:p?.name??null,roles:p?.roles??[],owner:p?.owner??false,grantsVersion:p?.grantsVersion??null,changeLogRetentionDays:retentionDays}},
 };
 const ops:Record<string,(b:any)=>any>={...typedOps,...projects,...transportRecovery.operations,...agent.operations,session_create,session_profile,session_archive,execution_stop,session_get,thread_entries,thread_blocks,thread_search,thread_export,execution_differences,execution_events,profiles_list,...labOps};
 registerStream(app,store,{retentionDays});
 const runOp=async (n:string,b:any)=>{const op=ops[n];if(!op)fail(404,'Unknown operation');return READ_OPERATIONS.has(n)?store.read(()=>op(b??{})):LONG_OPERATIONS.has(n)?op(b??{}):op===projects[n]||op===transportRecovery.operations[n]?store.atomic(()=>op(b??{})):(await store.tx(()=>op(b??{})))};
 // Per-op timing: the Server-Timing header (browser devtools, curl -i) carries the operation's own time, not the transfer.
 app.post('/api/ops/:name',async(req:any,reply:any)=>{const t=performance.now();try{return await runOp(req.params.name,req.body)}finally{if(Object.hasOwn(ops,req.params.name))reply.header('server-timing',`op;desc="${req.params.name}";dur=${(performance.now()-t).toFixed(1)}`)}});
 registerLab(app,labOps,runOp);
 app.post('/api/tools',async(req:any)=>{const q=req.body;if(q?.method==='initialize')return {jsonrpc:'2.0',id:q.id,result:{protocolVersion:'2025-06-18',capabilities:{tools:{}},serverInfo:{name:'fleet',version:'1'}}};if(q?.method==='tools/list')return {jsonrpc:'2.0',id:q.id,result:{tools:Object.keys(ops).map(name=>({name,...(agent.descriptions[name]?{description:agent.descriptions[name]}:{}),inputSchema:{type:'object'}}))}};if(q?.method!=='tools/call'||!ops[q.params?.name])fail(400,'Unknown tool');return {jsonrpc:'2.0',id:q.id,result:{content:[{type:'text',text:JSON.stringify(await runOp(q.params.name,q.params.arguments))}]}}});
 app.addHook('onClose',async()=>{if(ownStore)await store.close()});return app;
}
