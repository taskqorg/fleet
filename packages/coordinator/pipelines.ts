// Pipeline format version 2 runner (docs/designs/fleet-lab.md, "Pipeline format version 2", Stage 2 item 20).
// A run is an ordered chain of api, agent and script steps. The run is the pipeline_run execution (legacy record kind
// 'pipeline-run', whose steps are the pipeline_step rows `<run>:step:<i>`); each provider call is a tool_run child of its
// step with connection_cost. Every step leaves an output.json artifact; agent and script steps get the earlier steps'
// artifacts in FLEET_INPUT_DIR/<step id>/ (and the example inputs in FLEET_INPUT_DIR/example/).
// advance(runId) moves a run forward and is safe to call any time: on creation, on every receipt of a step's execution,
// on coordinator start, and on the job poll. A sync api call interrupted by a restart is unknown and never replayed.
import {readFileSync,mkdtempSync} from 'node:fs';
import {readFile,rm} from 'node:fs/promises';
import {tmpdir} from 'node:os';
import {resolve,join} from 'node:path';
import {spawn} from 'node:child_process';
import {fileURLToPath} from 'node:url';
import {createHash,randomUUID} from 'node:crypto';

const fail=(statusCode:number,message:string):never=>{throw Object.assign(Error(message),{statusCode})};
const STEP_ID=/^[a-z][a-z0-9-]{0,62}$/;
// Pipe reasons that are infrastructure, not the pipeline's fault: the trial is left out of comparison (problem infra:<code>).
const INFRA=/^(queue-full|no-usable-key|http-429|http-432|http-433|key-disabled-\d+|provider-unreachable|unreachable|unauthenticated|connection-unavailable|client-hash-mismatch)$/;
export const problemOf=(outcome:string,code:string|null)=>outcome==='unknown'?'unknown:'+(code??'outcome'):code&&INFRA.test(code)?'infra:'+code:'step:'+(code??'failed');

export function validatePipeline(d:any){
 const bad=(m:string):never=>fail(500,'Invalid pipeline: '+m);
 if(!d||d.version!==2)bad('version must be 2');if(typeof d.id!=='string'||!STEP_ID.test(d.id))bad('id');
 if(!Array.isArray(d.steps)||!d.steps.length)bad('steps');const seen=new Set<string>();
 for(const s of d.steps){if(!s||typeof s.id!=='string'||!STEP_ID.test(s.id)||seen.has(s.id)||s.id==='example')bad('step id '+s?.id);seen.add(s.id);
  const allowed:any=({api:['id','kind','connection','tool','arguments','mode'],agent:['id','kind','profile','prompt'],script:['id','kind','command','timeout_ms','host']} as any)[s.kind]??bad('step kind '+s.kind);
  for(const k of Object.keys(s))if(!allowed.includes(k))bad(`step ${s.id} key ${k}`);
  if(s.kind==='api'&&(typeof s.connection!=='string'||typeof s.tool!=='string'||!s.arguments||typeof s.arguments!=='object'||s.mode!==undefined&&!['sync','job'].includes(s.mode)))bad('api step '+s.id);
  if(s.kind==='agent'&&(typeof s.profile!=='string'||typeof s.prompt!=='string'))bad('agent step '+s.id);
  if(s.kind==='script'&&(typeof s.command!=='string'||!s.command||s.timeout_ms!==undefined&&!(Number.isSafeInteger(s.timeout_ms)&&s.timeout_ms>0)))bad('script step '+s.id)}
 return d}

// References: ${example.<path>} and ${steps.<id>.output.<path>}. A string that is one whole reference takes the value as it
// is (an array stays an array); inside a longer string the value is inlined as text. A missing value fails with ref:<path>.
export function resolveRefs(v:any,ctx:{example:any,outputs:Record<string,any>}):any{
 const look=(path:string)=>{const parts=path.split('.');let cur:any;
  if(parts[0]==='example'){cur=ctx.example;parts.shift()}else if(parts[0]==='steps'&&parts[2]==='output'){cur=ctx.outputs[parts[1]];parts.splice(0,3)}else throw Object.assign(Error('ref:'+path),{ref:path});
  for(const p of parts){if(cur==null||typeof cur!=='object'||!(p in cur))throw Object.assign(Error('ref:'+path),{ref:path});cur=cur[p]}
  if(cur===undefined)throw Object.assign(Error('ref:'+path),{ref:path});return cur};
 if(typeof v==='string'){const whole=/^\$\{([^}]+)\}$/.exec(v);if(whole)return look(whole[1]);return v.replace(/\$\{([^}]+)\}/g,(_,p)=>{const x=look(p);return typeof x==='string'?x:JSON.stringify(x)})}
 if(Array.isArray(v))return v.map(x=>resolveRefs(x,ctx));
 if(v&&typeof v==='object')return Object.fromEntries(Object.entries(v).map(([k,x])=>[k,resolveRefs(x,ctx)]));
 return v}

type Deps={
 store:{get:(id:string,kind:string)=>Promise<any>,put:(kind:string,v:any)=>Promise<unknown>,all:(kind:string,f?:any)=>Promise<any[]>,query:(sql:string,p?:any[])=>Promise<any[]>,atomic:<T>(fn:()=>Promise<T>|T)=>Promise<T>,outside:<T>(fn:()=>T)=>T,executions:any},
 directory:string,
 // Host directory of connection records <dir>/<connection>/connection.json (FLEET_CONNECTIONS_DIR of the coordinator host).
 connectionsDir?:string,
 save:(o:{projectId:string|null,scope:'lab'|'prod',executionId:string,name:string,bytes:Buffer,mediaType:string})=>Promise<{id:string}>,
 artifactsOf:(executionId:string)=>Promise<{id:string,path:string,sha256:string}[]>,
 read:(id:string)=>Promise<Buffer>,
 startAgent:(o:{run:any,step:any,stepExecutionId:string,prompt:string,inputs:{artifactId:string,path:string}[],overrides:any})=>Promise<string>,
 startScript:(o:{run:any,step:any,stepExecutionId:string,command:string,timeoutMs?:number,hostId?:string,inputs:{artifactId:string,path:string}[]})=>Promise<string>,
 // Test seam: the connection client command (default python3 connection_call.py).
 client?:string[]};

export function pipelineRunner(d:Deps){
 const {store}=d;
 const load=(file:string)=>{if(!/^[a-z0-9-]+\.json$/.test(file))fail(500,'Invalid pipeline reference');return validatePipeline(JSON.parse(readFileSync(resolve(d.directory,'pipelines',file),'utf8')))};
 const stepRow=(run:any,i:number)=>`${run.id}:step:${i}`;
 const client=d.client??['python3',fileURLToPath(new URL('./connection_call.py',import.meta.url))];
 const inflight=new Set<string>();const chains=new Map<string,Promise<any>>();

 // One provider call through the connection client; resolves to the call line (with output) the client printed.
 function call(connection:string,label:string,callsDir:string,tool:string,args:any):Promise<any>{
  if(!d.connectionsDir||!/^[a-z0-9-]+$/.test(connection))return Promise.resolve({tool,outcome:'error',error_code:'connection-unavailable',usage:null,output:null});
  return new Promise(res=>{const child=spawn(client[0],[...client.slice(1),'call',resolve(d.connectionsDir!,connection,'connection.json'),label,callsDir,tool],{env:{PATH:process.env.PATH},stdio:['pipe','pipe','ignore']});
   let out='';child.stdout.on('data',b=>out+=b);child.on('error',()=>res({tool,outcome:'error',error_code:'client-unavailable',usage:null,output:null}));
   child.on('close',()=>{try{res(JSON.parse(out))}catch{res({tool,outcome:'unknown',error_code:'invalid-response',usage:null,output:null})}});child.stdin.on('error',()=>{});child.stdin.end(JSON.stringify(args))})}

 // Calls file lines become tool_run children of the step with connection_cost (basis measured when Pipe reported usage).
 async function recordCalls(run:any,stepId:string,text:string,connection:string|null){
  const lines=text.split('\n').filter(Boolean).flatMap(l=>{try{return [JSON.parse(l)]}catch{return []}});
  for(const [i,l] of lines.entries()){const id=`${stepId}:call:${i}`,credits=Number.isFinite(l.usage?.credits)?l.usage.credits:null;
   await d.store.executions.createExecution({id,kind:'tool_run',state:l.outcome==='ok'?'succeeded':'failed',projectId:run.projectId??null,parentExecutionId:stepId,experimentId:run.experimentId??null,variant:run.variant??null,data:{tool:l.tool,requestId:l.request_id,outcome:l.outcome,errorCode:l.error_code,usage:l.usage??null,connection}});
   if(l.outcome!=='ok')await store.query('UPDATE execution SET problem=$2 WHERE id=$1 AND problem IS NULL',[id,problemOf(l.outcome,l.error_code)]);
   await store.query(`INSERT INTO execution_totals(execution_id,project_id,connection_cost,connection_cost_basis) VALUES($1,$2,$3,$4)
    ON CONFLICT(execution_id) DO UPDATE SET connection_cost=excluded.connection_cost,connection_cost_basis=excluded.connection_cost_basis,computed_at=now()`,
    [id,run.projectId??null,credits===null?null:JSON.stringify({unit:'credits',credits,connection}),credits===null?'unavailable':'measured'])}
  return lines}

 async function start(o:{file:string,projectId:string|null,requestId:string,example?:any,overrides?:any,requestedBy?:any,experimentId?:string,variant?:string,extra?:any}){
  const definition=load(o.file),now=Date.now();
  const run:any={id:randomUUID(),projectId:o.projectId,requestId:o.requestId,requestedBy:o.requestedBy??{kind:'operator'},definition,definitionSha256:createHash('sha256').update(JSON.stringify(definition)).digest('hex'),
   pipelineId:definition.id,pipeline:o.file,example:o.example??{},overrides:o.overrides??{},experimentId:o.experimentId??undefined,variant:o.variant??undefined,state:'running',startedAt:now,durationMs:null,usage:null,result:null,
   steps:definition.steps.map((s:any)=>({id:s.id,kind:s.kind,...(s.kind==='api'?{tool:s.tool,connection:s.connection}:{}),state:'pending'})),...o.extra};
  await store.put('pipeline-run',run);return run}

 const finishRun=async(run:any,state:'completed'|'failed'|'unknown',problem?:string)=>{run.state=state;run.finishedAt=Date.now();run.durationMs=run.finishedAt-run.startedAt;
  const credits=run.steps.reduce((a:number,s:any)=>a+(s.usage?.credits??0),0);run.usage=run.steps.some((s:any)=>s.usage)?{credits}:null;
  const outputs=Object.fromEntries(run.steps.filter((s:any)=>s.output!==undefined).map((s:any)=>[s.id,s.output]));
  const search=run.steps.find((s:any)=>Array.isArray(s.output?.results));
  run.result={outputs,sources:search?search.output.results.map((x:any)=>({url:x.url,title:x.title??x.url,content:x.content??null})):[],...(search?{native:search.output}:{}),...(problem?{error:run.steps.find((s:any)=>s.error)?.error??{code:problem}}:{})};
  await store.put('pipeline-run',run);if(problem)await store.query('UPDATE execution SET problem=$2 WHERE id=$1',[run.id,problem])};
 const failStep=async(run:any,i:number,problem:string,message:string)=>{const s=run.steps[i];s.state=problem.startsWith('unknown:')?'unknown':'failed';s.problem=problem;s.error={code:problem,message};s.finishedAt=Date.now();
  // Later steps never start (failed step stops later steps); the run keeps the failed step's reason.
  await store.put('pipeline-run',run);await store.query('UPDATE execution SET problem=$2 WHERE id=$1',[stepRow(run,i),problem]);await finishRun(run,s.state==='unknown'?'unknown':'failed',problem)};
 const completeStep=async(run:any,i:number,output:any,extra:any={})=>{const s=run.steps[i];
  if(!run.steps[i].outputArtifactId){const a=await d.save({projectId:run.projectId,scope:run.experimentId?'lab':'prod',executionId:stepRow(run,i),name:'output.json',bytes:Buffer.from(JSON.stringify(output)),mediaType:'application/json'});s.outputArtifactId=a.id}
  Object.assign(s,extra,{output,state:'completed',finishedAt:Date.now()});s.durationMs=s.finishedAt-(s.startedAt??s.finishedAt);await store.put('pipeline-run',run)};

 // Inputs of an agent or script step: example inputs, then every earlier step's artifacts under <step id>/.
 async function inputsFor(run:any,i:number){const out:{artifactId:string,path:string}[]=[];
  if(Array.isArray(run.example?.inputs)&&run.example.inputs.length){if(!run.exampleInputs){run.exampleInputs=[];for(const f of run.example.inputs){const a=await d.save({projectId:run.projectId,scope:run.experimentId?'lab':'prod',executionId:run.id,name:'example/'+f.path,bytes:Buffer.from(String(f.text)),mediaType:'text/plain'});run.exampleInputs.push({artifactId:a.id,path:'example/'+f.path})}await store.put('pipeline-run',run)}out.push(...run.exampleInputs)}
  for(let j=0;j<i;j++){const s=run.steps[j];const ids=[stepRow(run,j),...(s.executionId?[s.executionId]:[])];
   for(const id of ids)for(const a of await d.artifactsOf(id)){const rel=a.path.replace(/^output\//,'');if(rel.split('/').some(p=>p.startsWith('.')))continue;out.push({artifactId:a.id,path:s.id+'/'+rel})}}
  return out}

 async function step(run:any,i:number):Promise<'again'|'wait'>{
  const s=run.steps[i],def=run.definition.steps[i],ctx={example:run.example,outputs:Object.fromEntries(run.steps.slice(0,i).map((x:any)=>[x.id,x.output]))},row=stepRow(run,i);
  if(s.state==='pending'){
   let resolved:any;
   try{resolved=def.kind==='api'?resolveRefs(def.arguments,ctx):def.kind==='agent'?resolveRefs(run.overrides?.prompt??def.prompt,ctx):resolveRefs(def.command,ctx)}
   catch(e:any){if(!e.ref)throw e;s.startedAt=Date.now();await failStep(run,i,'ref:'+e.ref,'Missing reference '+e.ref);return 'wait'}
   s.state='running';s.startedAt=Date.now();
   if(def.kind==='api'){s.arguments=resolved;await store.put('pipeline-run',run);inflight.add(row);
    try{const tool=def.tool,dir=mkdtempSync(join(tmpdir(),'fleet-step-'));
     const line=await call(def.connection,row,dir,tool,resolved);const text=await readFile(dir+'/.connection-calls.jsonl','utf8').catch(()=>'');
     await recordCalls(run,row,text,def.connection);await rm(dir,{recursive:true,force:true});
     if(line.outcome!=='ok'){await failStep(run,i,problemOf(line.outcome,line.error_code),'Connection call '+line.outcome+': '+(line.error_code??''));return 'wait'}
     if(def.mode==='job'){if(typeof line.output?.jobId!=='string'){await failStep(run,i,'step:no-job-id','Job submit returned no jobId');return 'wait'}s.jobId=line.output.jobId;s.polls=0;await store.put('pipeline-run',run);await store.query("UPDATE execution SET data=data||jsonb_build_object('jobId',$2::text) WHERE id=$1",[row,s.jobId]);return 'wait'}
     await completeStep(run,i,line.output,{usage:line.usage??null});return 'again'}finally{inflight.delete(row)}}
   const inputs=await inputsFor(run,i);
   s.executionId=def.kind==='agent'?await d.startAgent({run,step:def,stepExecutionId:row,prompt:resolved,inputs,overrides:run.overrides??{}}):await d.startScript({run,step:def,stepExecutionId:row,command:resolved,timeoutMs:def.timeout_ms,hostId:run.overrides?.host??def.host,inputs});
   await store.put('pipeline-run',run);return 'wait'}
  // Running.
  if(def.kind==='api'&&def.mode!=='job'){if(inflight.has(row))return 'wait';await failStep(run,i,'unknown:interrupted','Coordinator restarted during the call; outcome unknown, not replayed');return 'wait'}
  if(def.kind==='api'){if(inflight.has(row))return 'wait';inflight.add(row);
   try{const dir=mkdtempSync(join(tmpdir(),'fleet-poll-'));const line=await call(def.connection,row+':poll',dir,'job_status',{jobId:s.jobId});
    await rm(dir,{recursive:true,force:true});s.polls=(s.polls??0)+1;
    if(line.outcome!=='ok'){if(line.outcome==='unknown')return 'wait';await failStep(run,i,problemOf(line.outcome,line.error_code),'Job poll failed: '+line.error_code);return 'wait'}
    const st=line.output?.state;if(st==='succeeded'){await completeStep(run,i,line.output.output??{},{usage:line.output.usage??null});
     if(line.output.usage)await recordCalls(run,row,JSON.stringify({tool:def.tool,request_id:s.jobId,outcome:'ok',error_code:null,usage:line.output.usage})+'\n',def.connection);return 'again'}
    if(st==='failed'){await failStep(run,i,problemOf('error',line.output.error??'job-failed'),'Job failed');return 'wait'}
    await store.put('pipeline-run',run);return 'wait'}finally{inflight.delete(row)}}
  const e=await store.get(s.executionId,'execution').catch(()=>null);if(!e)return 'wait';
  if(!['completed','failed','stopped','unknown'].includes(e.state))return 'wait';
  if(e.state!=='completed'){await failStep(run,i,e.state==='unknown'?'unknown:execution':e.state==='stopped'?'step:cancelled':'step:'+(e.error?.code??'failed'),e.error?.message??('Step execution '+e.state));return 'wait'}
  const arts=await d.artifactsOf(s.executionId),calls=arts.find(a=>/(^|\/)\.connection-calls\.jsonl$/.test(a.path)),own=arts.find(a=>/^(output\/)?output\.json$/.test(a.path));
  if(calls)await recordCalls(run,row,(await d.read(calls.id)).toString('utf8'),null);
  let output:any;if(own){try{output=JSON.parse((await d.read(own.id)).toString('utf8'))}catch{output=undefined}}
  output??={files:arts.map(a=>a.path.replace(/^output\//,'')).filter(p=>!p.split('/').some(x=>x.startsWith('.')))};
  const lines=calls?(await d.read(calls.id)).toString('utf8').split('\n').filter(Boolean).map(l=>{try{return JSON.parse(l)}catch{return {}}}):[];
  const credits=lines.reduce((a:number,l:any)=>a+(Number.isFinite(l.usage?.credits)?l.usage.credits:0),0);
  await completeStep(run,i,output,{usage:lines.length?{credits}:null});return 'again'}

 async function advanceOnce(runId:string){
  for(;;){const run=await store.get(runId,'pipeline-run').catch(()=>null);if(!run||run.state!=='running'||run.definition?.version!==2)return run;
   const i=run.steps.findIndex((s:any)=>s.state!=='completed');if(i<0){await finishRun(run,'completed');return run}
   if(await step(run,i)==='wait')return await store.get(runId,'pipeline-run')}}
 // Calls for one run are serial within this coordinator process, and run outside any caller's transaction.
 const advance=(runId:string):Promise<any>=>store.outside(()=>{const prev=chains.get(runId)??Promise.resolve();const next=prev.catch(()=>{}).then(()=>advanceOnce(runId));chains.set(runId,next);next.finally(()=>{if(chains.get(runId)===next)chains.delete(runId)}).catch(()=>{});return next});
 const running=async()=>(await store.all('pipeline-run',{state:'running'})).filter(r=>r.state==='running'&&r.definition?.version===2);
 // Run whose step is this execution (receipt hook).
 const onExecution=async(executionId:string)=>{for(const r of await running())if(r.steps.some((s:any)=>s.executionId===executionId))await advance(r.id)};
 // Job steps poll on each tick; agent and script steps also advance here in case a receipt hook was missed.
 const tick=async()=>{for(const r of await running())await advance(r.id).catch(()=>{})};
 const wait=async(runId:string)=>{for(;;){const r=await store.get(runId,'pipeline-run');if(r.state!=='running')return r;await new Promise(x=>setTimeout(x,200))}};
 return {load,start,advance,onExecution,tick,wait,recordCalls}}
