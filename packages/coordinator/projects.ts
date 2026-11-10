import {LONG_OPERATIONS,READ_OPERATIONS} from './store.ts';
import {readFileSync,realpathSync,readdirSync,existsSync} from 'node:fs';
import {resolve,sep} from 'node:path';
import {createHash,randomUUID} from 'node:crypto';
import type {pipelineRunner} from './pipelines.ts';
import type {FastifyInstance} from 'fastify';
type Store={all:(kind:string,filter?:object,omit?:string[])=>Promise<any[]>,get:(id:string,kind:string)=>Promise<any>,put:(kind:string,value:any)=>Promise<unknown>,atomic:<T>(fn:()=>Promise<T>|T)=>Promise<T>,read:<T>(fn:()=>Promise<T>|T)=>Promise<T>,query:(sql:string,params?:any[])=>Promise<any[]>,tasks?:any};
const digest=(v:any)=>createHash('sha256').update(JSON.stringify(v)).digest('hex');
const fail=(statusCode:number,message:string):never=>{throw Object.assign(Error(message),{statusCode})};
// Project definition subset (docs/prototype-implementation-plan.md W1). Absent key = default below; unknown keys are reported in unknownKeys, never dropped silently.
const SETTINGS_DEFAULTS:any={repos:[],environments:[],roles:{},worktree:{envMode:'worktree',baseRef:null,startFromOrigin:true,branchPrefix:'fleet',linkPaths:[],sharedDirectories:[]},scripts:{setup:null,teardown:null},setupStart:'wait',integration:{mode:'pr',mergeMethod:'squash',autoMerge:true,syncCheckoutAfterMerge:true},cleanup:{keepWhen:['uncommitted','teardown-failed']},identity:null};
const TOP_KEYS=['id','name','capability','recommendedMethod','contextFiles','artifact_locations','artifact_rules',...Object.keys(SETTINGS_DEFAULTS)];
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
 // Artifact placement (design §3): host IDs that must hold the project's artifacts (null: the coordinator default), and the
 // optional retention rules; whether each host has a registered store is checked where stores are known (artifacts.ts).
 const loc=p.artifact_locations;if(loc!==undefined&&(!Array.isArray(loc)||!loc.length||loc.some((h:any)=>typeof h!=='string'||!h)||new Set(loc).size!==loc.length))bad('artifact_locations');
 out.artifact_locations=loc??null;
 const rules=p.artifact_rules;if(rules!==undefined&&(!Array.isArray(rules)||rules.some((r:any)=>!r||typeof r!=='object')))bad('artifact_rules');out.artifact_rules=rules??[];
 return {settings:out,unknownKeys};
}
export function registerProjects(app:FastifyInstance,store:Store,config:{directory:string,runner:ReturnType<typeof pipelineRunner>}){
 const {all,get,put,atomic}=store,snapshot=store.read;
 const read=(path:string)=>JSON.parse(readFileSync(path,'utf8'));
 const ids=()=>readdirSync(resolve(config.directory,'projects')).filter(n=>existsSync(resolve(config.directory,'projects',n,'project.json'))).sort();
 const project=(id:string)=>{if(typeof id!=='string'||!ids().includes(id))fail(404,'Unknown project');const root=resolve(config.directory,'projects',id),p=read(resolve(root,'project.json'));if(p.id!==id||!Array.isArray(p.contextFiles))fail(500,'Invalid project definition');return {...p,...projectSettings(p),context:p.contextFiles.map((name:any)=>{if(typeof name!=='string'||name.includes('..')||name.startsWith('/'))fail(500,'Invalid project context path');const path=realpathSync(resolve(root,name));if(!path.startsWith(realpathSync(root)+sep))fail(500,'Project context escapes repository');const text=readFileSync(path,'utf8');return {path:name,text,sha256:digest(text)}})}};
 const catalogue=async (id:string)=>{const p=project(id),c=read(resolve(config.directory,'catalogue.json'));return {projectId:id,capability:c.capability,sharedDefault:c.sharedDefault,recommendedMethod:(await all('project-recommendation',{projectId:id})).find(r=>r.projectId===id)?.methodId??p.recommendedMethod??c.sharedDefault,recommendationSource:(await all('project-recommendation',{projectId:id})).some(r=>r.projectId===id)?'project-selected':p.recommendedMethod?'project':'shared',methods:c.methods.filter((m:any)=>m.scope==='shared'||m.scope===id)}};
 // On start: v2 runs move on (an interrupted sync call becomes unknown, never replayed); any other running run is unknown.
 app.addHook('onReady',async()=>{for(const run of (await all('pipeline-run',{state:'running'})).filter(r=>r.state==='running')){if(run.definition?.version===2){config.runner.advance(run.id).catch(()=>{});continue}run.state='unknown';for(const s of run.steps)if(s.state==='running'){s.state='unknown';s.error={code:'interrupted',message:'Original dispatch outcome unconfirmed; no replay'}}run.durationMs=null;await put('pipeline-run',run)}});
 // pipeline_run and direct_search: the selected method's v2 pipeline with the query as the example ({query, prompt}).
 async function runPipeline(b:any,actor:any={kind:'operator'},started?:(run:any)=>void){
  const pre=await atomic(async()=>{if(!b||Array.isArray(b)||Object.keys(b).some(k=>!['projectId','requestId','query','methodId','invocation'].includes(k))||typeof b.requestId!=='string'||!b.requestId||b.requestId.length>200||typeof b.query!=='string'||!b.query.trim()||b.query.length>2000||b.methodId!==undefined&&typeof b.methodId!=='string')fail(400,'Expected projectId, requestId, query and optional methodId');
  const signature=digest({projectId:b.projectId,query:b.query,methodId:b.methodId??null,invocation:b.invocation??'pipeline'});const old=(await all('pipeline-run',{projectId:b.projectId,requestId:b.requestId})).find(r=>r.projectId===b.projectId&&r.requestId===b.requestId);if(old){if(old.signature!==signature)fail(409,'Request identity conflict');return {old}}
  if(b.invocation!==undefined&&!['direct','pipeline'].includes(b.invocation))fail(400,'Invalid invocation');
  const p=project(b.projectId),c=(await catalogue(b.projectId)),method=c.methods.find((m:any)=>m.id===(b.methodId??c.recommendedMethod));if(!method)fail(400,'Selected method unavailable for project');
  return {run:await config.runner.start({file:method.pipeline,projectId:b.projectId,requestId:b.requestId,example:{query:b.query,prompt:b.query},requestedBy:actor,extra:{signature,invocation:b.invocation??'pipeline',selectedMethod:method.id,selectionSource:b.methodId?'explicit':c.recommendationSource,context:p}})}});
  if('old' in pre)return pre.old;started?.(pre.run);await config.runner.advance(pre.run.id);return config.runner.wait(pre.run.id);
 }
 const operations:any={
  direct_search:async (b:any,actor:any,started?:(run:any)=>void)=>(await runPipeline({...b,invocation:'direct'},actor,started)),
  recommendation_set:async (b:any)=>{const c=(await catalogue(b.projectId));if(!c.methods.some((m:any)=>m.id===b.methodId))fail(400,'Method unavailable');const r={id:'recommendation:'+b.projectId,projectId:b.projectId,methodId:b.methodId,selectedAt:Date.now(),actor:'operator'};await put('project-recommendation',r);return r},
  question_ask:async (b:any)=>{project(b.projectId);if(typeof b.requestId!=='string'||!b.requestId||typeof b.text!=='string'||!b.text||b.text.length>2000)fail(400,'Expected question text and requestId');const old=(await all('project-question',{projectId:b.projectId,requestId:b.requestId})).find(q=>q.projectId===b.projectId&&q.requestId===b.requestId);if(old){if(old.text!==b.text)fail(409,'Question identity conflict');return old}const q={id:randomUUID(),projectId:b.projectId,requestId:b.requestId,text:b.text,state:'waiting',createdAt:Date.now(),actor:'operator'};await put('project-question',q);return q},
  // Legacy operator decisions keep their record shape; any other question (acceptance, typed) answers through tasks.answer.
  question_answer:async (b:any)=>{if(typeof b?.questionId==='string'&&store.tasks&&!(await store.query("SELECT 1 FROM question WHERE id=$1 AND legacy_kind='project-question'",[b.questionId])).length){if(typeof b.answer!=='string'||!b.answer||b.answer.length>4000)fail(400,'Expected answer');const answered=await store.tasks.answer(b.questionId,{answer:b.answer,by:'operator',artifactIds:Array.isArray(b.artifactIds)?b.artifactIds:undefined});return (await store.tasks.questions({taskId:answered.task_id??undefined})).find((x:any)=>x.id===answered.id)??{id:answered.id,state:answered.state,answer:answered.answer}}
   const q=(await get(b.questionId,'project-question'));if(typeof b.answer!=='string'||!b.answer||b.answer.length>4000)fail(400,'Expected answer');if(q.state==='answered'){if(q.answer!==b.answer)fail(409,'Answer already recorded');return q}Object.assign(q,{state:'answered',answer:b.answer,answeredAt:Date.now(),answeredBy:'operator'});await put('project-question',q);return q},
  question_history:async (b:any)=>{project(b.projectId);return {questions:(await all('project-question',{projectId:b.projectId})).filter(q=>q.projectId===b.projectId)}},project_context:(b:any)=>project(b.projectId),catalogue:async (b:any)=>(await catalogue(b.projectId)),pipeline_run:runPipeline,pipeline_get:async (b:any)=>{if(typeof b.runId!=='string'||!b.runId||b.runId.length>200)fail(400,'Expected runId');const r=(await get(b.runId,'pipeline-run'));if(b.projectId&&r.projectId!==b.projectId)fail(403,'Pipeline belongs to another project');return r},pipeline_history:async (b:any)=>{project(b.projectId);return {runs:(await all('pipeline-run',{projectId:b.projectId})).filter(r=>r.projectId===b.projectId)}}};
 operations.project_list=()=>({projects:ids().map(project)});
 app.get('/api/projects',async()=>operations.project_list());
 app.get('/api/projects/:id',async(req:any)=>operations.project_context({projectId:req.params.id}));
 app.get('/api/projects/:id/catalogue',async(req:any)=>(await operations.catalogue({projectId:req.params.id})));
 app.get('/api/projects/:id/runs',async(req:any)=>(await operations.pipeline_history({projectId:req.params.id})));
 app.post('/api/pipeline-runs',async(req:any)=>(await operations.pipeline_run(req.body)));
 app.get('/api/pipeline-runs/:id',async(req:any)=>(await operations.pipeline_get({runId:req.params.id})));
 app.post('/api/project-operations/:name',async(req:any)=>{const op=operations[req.params.name];if(!op)fail(404,'Unknown operation');return READ_OPERATIONS.has(req.params.name)?snapshot(()=>op(req.body)):LONG_OPERATIONS.has(req.params.name)?op(req.body):atomic(()=>op(req.body))});
 app.post('/api/project-tools',async(req:any)=>{const q=req.body;if(q?.method==='initialize')return {jsonrpc:'2.0',id:q.id,result:{protocolVersion:'2025-06-18',capabilities:{tools:{}},serverInfo:{name:'fleet-projects',version:'1'}}};if(q?.method==='tools/list')return {jsonrpc:'2.0',id:q.id,result:{tools:Object.keys(operations).map(name=>({name,description:'Fleet shared '+name,inputSchema:{type:'object'}}))}};if(q?.method!=='tools/call'||!operations[q.params?.name])fail(400,'Unknown project operation');return {jsonrpc:'2.0',id:q.id,result:{content:[{type:'text',text:JSON.stringify(await (READ_OPERATIONS.has(q.params.name)?snapshot(()=>operations[q.params.name](q.params.arguments??{})):LONG_OPERATIONS.has(q.params.name)?operations[q.params.name](q.params.arguments??{}):atomic(()=>operations[q.params.name](q.params.arguments??{}))))}]}}});
 return operations;
}
