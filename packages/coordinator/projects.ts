import {LONG_OPERATIONS} from './store.js';
import {readFileSync,realpathSync,readdirSync,existsSync} from 'node:fs';
import {resolve,sep} from 'node:path';
import {fileURLToPath} from 'node:url';
import {createHash,randomUUID} from 'node:crypto';
import {spawn} from 'node:child_process';
import type {FastifyInstance} from 'fastify';
import {projectUi} from './project-ui.js';
type Store={all:(kind:string,filter?:object,omit?:string[])=>Promise<any[]>,get:(id:string,kind:string)=>Promise<any>,put:(kind:string,value:any)=>Promise<unknown>,atomic:<T>(fn:()=>Promise<T>|T)=>Promise<T>};
const digest=(v:any)=>createHash('sha256').update(JSON.stringify(v)).digest('hex');
const fail=(statusCode:number,message:string):never=>{throw Object.assign(Error(message),{statusCode})};
function pipe(connection:string|undefined,run:string,args:any):Promise<any>{
 if(!connection)return Promise.resolve({isError:true,content:[{type:'text',text:'Pipe connection unavailable: host connection is not configured'}]});
 return new Promise(resolveResult=>{const child=spawn('python3',[fileURLToPath(new URL('./pipe_search.py',import.meta.url)),connection,run],{env:{PATH:process.env.PATH},stdio:['pipe','pipe','ignore']});let output='',settled=false;const finish=(v:any)=>{if(settled)return;settled=true;clearTimeout(timer);resolveResult(v)};const timer=setTimeout(()=>{child.kill('SIGKILL');finish({isError:true,content:[{type:'text',text:'Pipe outcome unknown: client timeout; do not replay'}]})},50000);child.stdout.on('data',b=>{output+=b;if(output.length>4*1024*1024){child.kill('SIGKILL');finish({isError:true,content:[{type:'text',text:'Pipe outcome unknown: response exceeds retained limit; do not replay'}]})}});child.on('error',()=>finish({isError:true,content:[{type:'text',text:'Pipe connection unavailable: client could not start'}]}));child.on('close',()=>{try{finish(JSON.parse(output))}catch{finish({isError:true,content:[{type:'text',text:'Pipe outcome unknown: invalid client response; do not replay'}]})}});child.stdin.on('error',()=>{});child.stdin.end(JSON.stringify(args));});
}
// Project definition subset (docs/prototype-implementation-plan.md W1). Absent key = default below; unknown keys are reported in unknownKeys, never dropped silently.
const SETTINGS_DEFAULTS:any={repos:[],environments:[],roles:{},worktree:{envMode:'worktree',baseRef:null,startFromOrigin:true,branchPrefix:'fleet',linkPaths:[],sharedDirectories:[]},scripts:{setup:null,teardown:null},setupStart:'wait',integration:{mode:'pr',mergeMethod:'squash',autoMerge:true,syncCheckoutAfterMerge:true},cleanup:{keepWhen:['uncommitted','teardown-failed']},identity:null};
const TOP_KEYS=['id','name','capability','recommendedMethod','contextFiles',...Object.keys(SETTINGS_DEFAULTS)];
const ITEM_KEYS:any={repos:['id','remote','forge','defaultBranch','connection','path'],environments:['hostId','checkoutPath','repo'],identity:['name','email']};
const ENUMS:any={'worktree.envMode':['worktree','local'],setupStart:['wait','background'],'integration.mode':['pr','direct'],'integration.mergeMethod':['squash','merge','rebase'],'repos.forge':['github','gitlab']};
export function projectSettings(p:any){
 const unknownKeys:string[]=[],bad=(k:string):never=>fail(500,'Invalid project setting '+k);
 const isObj=(v:any)=>v&&typeof v==='object'&&!Array.isArray(v);
 const check=(k:string,v:any,d:any)=>{if(ENUMS[k]&&!ENUMS[k].includes(v))bad(k);if(d!==null&&v!==null&&typeof d!==typeof v||Array.isArray(d)!==Array.isArray(v))bad(k)};
 for(const k of Object.keys(p))if(!TOP_KEYS.includes(k))unknownKeys.push(k);
 const out:any={};
 for(const [k,d] of Object.entries(SETTINGS_DEFAULTS) as [string,any][]){
  const v=p[k];if(v===undefined){out[k]=structuredClone(d);continue}
  if(k==='roles'){if(!isObj(v)||Object.values(v).some(x=>typeof x!=='string'))bad(k);out[k]=v;continue}
  if(Array.isArray(d)){if(!Array.isArray(v))bad(k);out[k]=v.map((item:any,i:number)=>{if(!isObj(item))bad(k);for(const x of Object.keys(item)){if(!ITEM_KEYS[k].includes(x))unknownKeys.push(`${k}[${i}].${x}`);else if(typeof item[x]!=='string')bad(`${k}[${i}].${x}`)}if(k==='repos'){if(!item.remote)bad(k+'.remote');check('repos.forge',item.forge??'github',null);return {forge:'github',defaultBranch:'main',...item}}if(!item.hostId||!item.checkoutPath)bad(k);return item});continue}
  if(k==='identity'){if(!isObj(v))bad(k);for(const x of Object.keys(v))if(!ITEM_KEYS.identity.includes(x))unknownKeys.push('identity.'+x);out[k]=v;continue}
  if(isObj(d)){if(!isObj(v))bad(k);out[k]={...d};for(const [x,y] of Object.entries(v)){if(!(x in d)){unknownKeys.push(k+'.'+x);continue}check(k+'.'+x,y,d[x]);out[k][x]=y}continue}
  check(k,v,d);out[k]=v;
 }
 return {settings:out,unknownKeys};
}
export function registerProjects(app:FastifyInstance,store:Store,config:{directory:string,pipeConnectionFile?:string,proxy?:boolean,searchHostId?:string}){
 const {all,get,put,atomic}=store;
 const read=(path:string)=>JSON.parse(readFileSync(path,'utf8'));
 const ids=()=>readdirSync(resolve(config.directory,'projects')).filter(n=>existsSync(resolve(config.directory,'projects',n,'project.json'))).sort();
 const project=(id:string)=>{if(typeof id!=='string'||!ids().includes(id))fail(404,'Unknown project');const root=resolve(config.directory,'projects',id),p=read(resolve(root,'project.json'));if(p.id!==id||!Array.isArray(p.contextFiles))fail(500,'Invalid project definition');return {...p,...projectSettings(p),context:p.contextFiles.map((name:any)=>{if(typeof name!=='string'||name.includes('..')||name.startsWith('/'))fail(500,'Invalid project context path');const path=realpathSync(resolve(root,name));if(!path.startsWith(realpathSync(root)+sep))fail(500,'Project context escapes repository');const text=readFileSync(path,'utf8');return {path:name,text,sha256:digest(text)}})}};
 const catalogue=async (id:string)=>{const p=project(id),c=read(resolve(config.directory,'catalogue.json'));return {projectId:id,capability:c.capability,sharedDefault:c.sharedDefault,recommendedMethod:(await all('project-recommendation',{projectId:id})).find(r=>r.projectId===id)?.methodId??p.recommendedMethod??c.sharedDefault,recommendationSource:(await all('project-recommendation',{projectId:id})).some(r=>r.projectId===id)?'project-selected':p.recommendedMethod?'project':'shared',methods:c.methods.filter((m:any)=>m.scope==='shared'||m.scope===id)}};
 app.addHook('onReady',async()=>{for(const run of (await all('pipeline-run',{state:'running'})).filter(r=>r.state==='running')){run.state='unknown';run.steps[0].state='unknown';run.steps[0].error={code:'interrupted','message':'Original dispatch outcome unconfirmed; no replay'};run.durationMs=null;await put('pipeline-run',run)}});
 async function runPipeline(b:any,actor:any={kind:'operator'},started?:(run:any)=>void){
  const pre=await atomic(async()=>{if(!b||Array.isArray(b)||Object.keys(b).some(k=>!['projectId','requestId','query','methodId','invocation'].includes(k))||typeof b.requestId!=='string'||!b.requestId||b.requestId.length>200||typeof b.query!=='string'||!b.query.trim()||b.query.length>2000||b.methodId!==undefined&&typeof b.methodId!=='string')fail(400,'Expected projectId, requestId, query and optional methodId');
  const signature=digest({projectId:b.projectId,query:b.query,methodId:b.methodId??null,invocation:b.invocation??'pipeline'});const old=(await all('pipeline-run',{projectId:b.projectId,requestId:b.requestId})).find(r=>r.projectId===b.projectId&&r.requestId===b.requestId);if(old){if(old.signature!==signature)fail(409,'Request identity conflict');return {old}}
  if(b.invocation!==undefined&&!['direct','pipeline'].includes(b.invocation))fail(400,'Invalid invocation');
  const p=project(b.projectId),c=(await catalogue(b.projectId)),method=c.methods.find((m:any)=>m.id===(b.methodId??c.recommendedMethod));if(!method)fail(400,'Selected method unavailable for project');if(!/^[a-z0-9-]+\.json$/.test(method.pipeline))fail(500,'Invalid pipeline reference');const definition=read(resolve(config.directory,'pipelines',method.pipeline));
  if(definition.version!==1||definition.steps?.length!==1||definition.steps[0].tool!=='web_search'||definition.steps[0].connection!=='pipe-read'||definition.steps[0].arguments?.query!=='$input.query'||!Number.isInteger(definition.steps[0].arguments.max_results)||definition.steps[0].arguments.max_results<1||definition.steps[0].arguments.max_results>20||!['basic','advanced'].includes(definition.steps[0].arguments.search_depth))fail(500,'Unsupported pipeline subset');
  const startedAt=Date.now(),args={query:b.query,max_results:definition.steps[0].arguments.max_results,search_depth:definition.steps[0].arguments.search_depth};const run:any={id:randomUUID(),projectId:b.projectId,requestId:b.requestId,signature,requestedBy:actor,invocation:b.invocation??'pipeline',selectedMethod:method.id,selectionSource:b.methodId?'explicit':c.recommendationSource,context:p,definition,definitionSha256:digest(definition),state:'running',startedAt,durationMs:null,usage:null,steps:[{id:definition.steps[0].id,tool:'web_search',connection:'pipe-read',arguments:args,state:'running',startedAt,durationMs:null,usage:null}],result:null};
  await put('pipeline-run',run);return {run,args,startedAt}});if('old' in pre)return pre.old;const {run,args,startedAt}=pre;started?.(run);return (async()=>{let response:any;
  if(config.searchHostId){
   const sid=randomUUID(),eid=randomUUID(),cid=randomUUID();await put('session',{id:sid,createdAt:Date.now(),actor:'program-worker',projectId:b.projectId});await put('execution',{id:eid,executionId:eid,sessionId:sid,hostId:config.searchHostId,state:'queued',createdAt:Date.now(),actor:'program-worker',pipelineRunId:run.id});await put('command',{id:cid,executionId:eid,sessionId:sid,hostId:config.searchHostId,kind:'pipe-search',status:'pending',payload:{runId:run.id,arguments:args}});run.executionId=eid;await put('pipeline-run',run);
   const deadline=Date.now()+50000;
   while(Date.now()<deadline){const artifact=(await all('artifact',{executionId:eid,path:'pipe-response.json'})).find(a=>a.executionId===eid&&a.path==='pipe-response.json');if(artifact){try{response=JSON.parse(Buffer.from(artifact.contentBase64,'base64').toString())}catch{}break}const e=(await get(eid,'execution'));if(['failed','unknown','stopped'].includes(e.state)){response={isError:true,content:[{type:'text',text:'Pipe outcome unknown: host worker '+e.state+'; inspect execution before replay'}]};break}await new Promise(r=>setTimeout(r,200))}
   response??={isError:true,content:[{type:'text',text:'Pipe outcome unknown: host acknowledgement timeout; do not replay'}]};
  }else response=await pipe(config.pipeConnectionFile,run.id,args);const step=run.steps[0];step.finishedAt=Date.now();step.durationMs=step.finishedAt-startedAt;
  const text=response.content?.filter((c:any)=>c.type==='text').map((c:any)=>c.text).join('\n')??'Invalid Pipe response';
  if(response.isError){const status=Number(/HTTP (\d+)/.exec(text)?.[1]);const state=[429,432,433].includes(status)||/quota/i.test(text)?'quota':/connection unavailable|HTTP (401|403|503)/i.test(text)?'unavailable':/unknown|unreachable|HTTP (502|504)/i.test(text)?'unknown':'failed';run.state=step.state=state;step.error={code:state,message:text};run.result={sources:[],error:step.error};}
  else{let value:any;try{value=JSON.parse(text)}catch{value={text}}step.result=value;step.usage=value.usage??null;run.usage=step.usage;run.result={sources:Array.isArray(value.results)?value.results.map((s:any)=>({url:s.url,title:s.title??s.url,content:s.content??null})):[],native:value};run.state=step.state='completed';}
  run.finishedAt=step.finishedAt;run.durationMs=step.durationMs;await put('pipeline-run',run);return run;})();
 }
 const operations:any={
  direct_search:async (b:any,actor:any,started?:(run:any)=>void)=>(await runPipeline({...b,invocation:'direct'},actor,started)),
  recommendation_set:async (b:any)=>{const c=(await catalogue(b.projectId));if(!c.methods.some((m:any)=>m.id===b.methodId))fail(400,'Method unavailable');const r={id:'recommendation:'+b.projectId,projectId:b.projectId,methodId:b.methodId,selectedAt:Date.now(),actor:'operator'};await put('project-recommendation',r);return r},
  question_ask:async (b:any)=>{project(b.projectId);if(typeof b.requestId!=='string'||!b.requestId||typeof b.text!=='string'||!b.text||b.text.length>2000)fail(400,'Expected question text and requestId');const old=(await all('project-question',{projectId:b.projectId,requestId:b.requestId})).find(q=>q.projectId===b.projectId&&q.requestId===b.requestId);if(old){if(old.text!==b.text)fail(409,'Question identity conflict');return old}const q={id:randomUUID(),projectId:b.projectId,requestId:b.requestId,text:b.text,state:'waiting',createdAt:Date.now(),actor:'operator'};await put('project-question',q);return q},
  question_answer:async (b:any)=>{const q=(await get(b.questionId,'project-question'));if(typeof b.answer!=='string'||!b.answer||b.answer.length>4000)fail(400,'Expected answer');if(q.state==='answered'){if(q.answer!==b.answer)fail(409,'Answer already recorded');return q}Object.assign(q,{state:'answered',answer:b.answer,answeredAt:Date.now(),answeredBy:'operator'});await put('project-question',q);return q},
  question_history:async (b:any)=>{project(b.projectId);return {questions:(await all('project-question',{projectId:b.projectId})).filter(q=>q.projectId===b.projectId)}},project_context:(b:any)=>project(b.projectId),catalogue:async (b:any)=>(await catalogue(b.projectId)),pipeline_run:runPipeline,pipeline_get:async (b:any)=>{if(typeof b.runId!=='string'||!b.runId||b.runId.length>200)fail(400,'Expected runId');const r=(await get(b.runId,'pipeline-run'));if(b.projectId&&r.projectId!==b.projectId)fail(403,'Pipeline belongs to another project');return r},pipeline_history:async (b:any)=>{project(b.projectId);return {runs:(await all('pipeline-run',{projectId:b.projectId})).filter(r=>r.projectId===b.projectId)}}};
 app.get('/projects',async(_,r)=>r.type('text/html').send(projectUi(config.proxy)));
 operations.project_list=()=>({projects:ids().map(project)});
 app.get('/api/projects',async()=>operations.project_list());
 app.get('/api/projects/:id',async(req:any)=>operations.project_context({projectId:req.params.id}));
 app.get('/api/projects/:id/catalogue',async(req:any)=>(await operations.catalogue({projectId:req.params.id})));
 app.get('/api/projects/:id/runs',async(req:any)=>(await operations.pipeline_history({projectId:req.params.id})));
 app.post('/api/pipeline-runs',async(req:any)=>(await operations.pipeline_run(req.body)));
 app.get('/api/pipeline-runs/:id',async(req:any)=>(await operations.pipeline_get({runId:req.params.id})));
 app.post('/api/project-operations/:name',async(req:any)=>{const op=operations[req.params.name];if(!op)fail(404,'Unknown operation');return LONG_OPERATIONS.has(req.params.name)?op(req.body):atomic(()=>op(req.body))});
 app.post('/api/project-tools',async(req:any)=>{const q=req.body;if(q?.method==='initialize')return {jsonrpc:'2.0',id:q.id,result:{protocolVersion:'2025-06-18',capabilities:{tools:{}},serverInfo:{name:'fleet-projects',version:'1'}}};if(q?.method==='tools/list')return {jsonrpc:'2.0',id:q.id,result:{tools:Object.keys(operations).map(name=>({name,description:'Fleet shared '+name,inputSchema:{type:'object'}}))}};if(q?.method!=='tools/call'||!operations[q.params?.name])fail(400,'Unknown project operation');return {jsonrpc:'2.0',id:q.id,result:{content:[{type:'text',text:JSON.stringify(await (LONG_OPERATIONS.has(q.params.name)?operations[q.params.name](q.params.arguments??{}):atomic(()=>operations[q.params.name](q.params.arguments??{}))))}]}}});
 return operations;
}
