// Lab experiments (docs/designs/fleet-lab.md "Experiment reconcile", "Budgets", "Ratings"; Stage 3 items 26-29).
// An experiment is one comparison: pinned approved examples × variants × repeats of one v2 pipeline. A trial is a
// pipeline_run execution with experiment_id, variant and request_id set; a judge run is one with variant null.
// reconcile(id) is the one function that moves an experiment: it runs under the experiment's scope lock (store.tx with
// key experiment:<id>, which takes pg_advisory_xact_lock), so calls for one experiment are serial; trials are inserted by
// request_id with ON CONFLICT DO NOTHING and only inserted rows launch; spend is derived from rows, so it is counted once.
import {createHash,randomUUID} from 'node:crypto';
import {readdirSync,readFileSync} from 'node:fs';
import {join} from 'node:path';
import {labScores,runChecks,parseRating,checkValue,shuffle,agreement,summarize,type Form} from './scores.ts';
const fail=(code:number,message:string):never=>{throw Object.assign(Error(message),{statusCode:code})};

const KINDS=['single','ab'];// 'loop' arrives with strategies in Stage 4
const UNITS=['trials','wall_ms','pipe_calls','pipe_credits'],ACTIONS=['warn','pause','stop'];
const OVERRIDES=['profile','model','skills','prompt','web_route','account','host'];
const FINAL=new Set(['completed','failed','unknown','cancelled']);
const ident=(v:any,what:string)=>typeof v==='string'&&/^[A-Za-z0-9][A-Za-z0-9._:-]{0,199}$/.test(v)?v:fail(400,'Invalid '+what);
const sha=(x:any)=>createHash('sha256').update(typeof x==='string'?x:JSON.stringify(x)).digest('hex');
// Canonical JSON (sorted keys): equal overrides give an equal config_ref.
const canon=(v:any):string=>Array.isArray(v)?'['+v.map(canon).join(',')+']':v&&typeof v==='object'?'{'+Object.keys(v).sort().map(k=>JSON.stringify(k)+':'+canon(v[k])).join(',')+'}':JSON.stringify(v);
export const evaluatorName=(e:{kind:string,ref:string,form:string})=>`${e.kind}:${e.ref}:${e.form}`;
export const cellKey=(example:string,variant:string,repeat:number)=>`${example}|${variant}|${repeat}`;

type Deps={store:any,runner:any,examples:{get:(id:string)=>Promise<any>},labDirectory:string,
 read:(artifactId:string)=>Promise<Buffer>,stopExecution:(executionId:string)=>Promise<unknown>,
 catalogue:(projectId:string)=>Promise<any>,recommend:(projectId:string,methodId:string)=>Promise<any>};

export function labExperiments(d:Deps){
 const {store,runner}=d,q=(sql:string,p:any[]=[])=>store.query(sql,p),scores=labScores(q,(fn,lock)=>store.tx(fn,lock));
 const row=async(id:string)=>(await q('SELECT * FROM experiment WHERE id=$1',[id]))[0]??fail(404,'Unknown experiment');
 const view=(r:any)=>({id:r.id,projectId:r.project_id,kind:r.kind,pipelineId:r.pipeline_id,examples:r.examples,variants:r.variants,repeats:r.repeats,evaluators:r.evaluators,primaryEvaluator:r.primary_evaluator,budgets:r.budgets,state:r.state,stopReason:r.stop_reason,createdAt:r.created_at});

 async function create(b:any){
  const known=['kind','projectId','pipelineId','examples','variants','repeats','evaluators','budgets','primaryEvaluator','strategy'];
  if(!b||typeof b!=='object'||Object.keys(b).some(k=>!known.includes(k)))fail(400,'Expected '+known.join(', '));
  if(b.kind==='loop')fail(400,'Loop experiments need strategies (Stage 4)');if(!KINDS.includes(b.kind))fail(400,'Invalid kind');
  if(b.strategy!==undefined)fail(400,'strategy is for loop experiments');
  const projectId=ident(b.projectId,'projectId'),pipelineId=ident(b.pipelineId,'pipelineId');runner.load(pipelineId+'.json');
  // Agent steps start only in a configured project: an unknown one would fail every trial at its first agent step.
  if(!await d.catalogue(projectId).then(()=>true,()=>false))fail(400,'Unknown project '+projectId);
  if(!Array.isArray(b.examples)||!b.examples.length)fail(400,'Expected examples (approved example artifact IDs)');
  const examples:any[]=[];for(const id of b.examples){const ex=await d.examples.get(ident(id,'example artifact')).catch((e:any)=>e.statusCode===404?fail(400,'Unknown example '+id):Promise.reject(e));
   if(ex.state!=='approved')fail(409,`Example ${id} is not approved`);const hash=(await q('SELECT content_hash FROM artifact WHERE id=$1',[id]))[0].content_hash;
   examples.push({id:`${ex.example.id}@${ex.example.version}`,artifact_id:id,hash,split:ex.example.split})}
  if(new Set(examples.map(e=>e.id)).size!==examples.length)fail(400,'Duplicate example');
  if(!Array.isArray(b.variants)||!b.variants.length)fail(400,'Expected variants');
  const variants=b.variants.map((v:any)=>{const label=typeof v?.label==='string'&&/^[A-Za-z0-9][A-Za-z0-9 ._+-]{0,59}$/.test(v.label)?v.label:fail(400,'Invalid variant label');
   const overrides=v.overrides??{};if(typeof overrides!=='object'||Array.isArray(overrides)||Object.keys(overrides).some(k=>!OVERRIDES.includes(k)))fail(400,'Variant overrides are '+OVERRIDES.join(', '));
   if(overrides.web_route!==undefined&&!['connections','builtin'].includes(overrides.web_route))fail(400,'web_route must be connections or builtin');
   return {label,overrides,config_ref:sha(canon({pipeline:pipelineId,overrides}))}});
  if(new Set(variants.map((v:any)=>v.label)).size!==variants.length)fail(400,'Duplicate variant label');
  if(b.kind==='single'&&variants.length!==1)fail(400,'A single experiment has one variant');if(b.kind==='ab'&&variants.length<2)fail(400,'An ab experiment has two or more variants');
  const repeats=b.repeats??1;if(!Number.isSafeInteger(repeats)||repeats<1)fail(400,'Invalid repeats');
  const evaluators=(b.evaluators??[]).map((e:any)=>{const kind=e?.kind,form=e?.form;
   if(kind==='script'){if(e.ref!=='example.checks'||!['pass_fail','metric'].includes(form))fail(400,'A script evaluator is {kind: script, ref: example.checks, form: pass_fail|metric}')}
   else if(kind==='judge'){runner.load(ident(e.ref,'judge pipeline')+'.json');if(!['pass_fail','scale'].includes(form))fail(400,'A judge form is pass_fail or scale')}
   else if(kind==='human'){if(e.ref!=='owner'||!['pass_fail','pick_best','scale'].includes(form))fail(400,'A human evaluator is {kind: human, ref: owner, form: pass_fail|pick_best|scale}')}
   else fail(400,'Evaluator kind is script, judge or human');return {kind,ref:e.ref,form}});
  if(b.primaryEvaluator!==undefined&&!evaluators.some((e:any)=>evaluatorName(e)===b.primaryEvaluator))fail(400,'primaryEvaluator names none of the evaluators');
  // Default action when unset: pause (R3.7).
  const budgets=(b.budgets??[]).map((x:any)=>{if(!UNITS.includes(x?.unit))fail(400,'Budget unit is '+UNITS.join(', '));if(!(Number.isFinite(x.limit)&&x.limit>0))fail(400,'Invalid budget limit');
   const action=x.action??'pause';if(!ACTIONS.includes(action))fail(400,'Budget action is warn, pause or stop');return {unit:x.unit,limit:x.limit,action}});
  const id=randomUUID();
  await store.tx(()=>q('INSERT INTO experiment(id,project_id,kind,pipeline_id,examples,variants,repeats,evaluators,primary_evaluator,budgets) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)',
   [id,projectId,b.kind,pipelineId,JSON.stringify(examples),JSON.stringify(variants),repeats,JSON.stringify(evaluators),b.primaryEvaluator??null,JSON.stringify(budgets)]));
  return get(id)}

 // Spend of the experiment, from rows: trials counts trial executions; wall time sums the experiment's pipeline runs (trials
 // and judges); calls and credits sum its tool_run rows (every caller, coordinator-side or agent-side).
 async function spend(id:string){
  const [t]=await q(`SELECT count(*) FILTER (WHERE variant IS NOT NULL)::int AS trials,
   coalesce(sum(extract(epoch FROM coalesce(ended_at,now())-started_at)*1000) FILTER (WHERE started_at IS NOT NULL),0)::float8 AS wall_ms
   FROM execution WHERE experiment_id=$1 AND kind='pipeline_run'`,[id]);
  const [c]=await q(`SELECT count(*)::int AS calls,coalesce(sum((t.connection_cost->>'credits')::numeric),0)::float8 AS credits,count(*) FILTER (WHERE t.connection_cost_basis IS DISTINCT FROM 'measured')::int AS unmeasured
   FROM execution e LEFT JOIN execution_totals t ON t.execution_id=e.id WHERE e.experiment_id=$1 AND e.kind='tool_run'`,[id]);
  return {trials:{value:t.trials,basis:'measured'},wall_ms:{value:Math.round(t.wall_ms),basis:'measured'},pipe_calls:{value:c.calls,basis:'measured'},pipe_credits:{value:c.credits,basis:c.unmeasured?'partial':'measured',unmeasured:c.unmeasured}}}

 // Lab runs of the experiment (trials and judges), as run records with their steps, plus the problem column.
 const runsOf=async(id:string):Promise<any[]>=>{const rows=await q(`SELECT id,variant,problem FROM execution WHERE experiment_id=$1 AND kind='pipeline_run' AND legacy_kind='pipeline-run' ORDER BY seq`,[id]);
  const m=await store.many(rows.map((r:any)=>r.id),'pipeline-run');return rows.filter((r:any)=>m.has(r.id)).map((r:any)=>({...m.get(r.id),variant:r.variant,problem:r.problem}))};
 const trialsOf=(runs:any[])=>runs.filter(r=>r.cell);
 // Latest attempt per cell.
 const latest=(trials:any[])=>{const m=new Map<string,any>();for(const t of trials){const k=cellKey(t.cell.example,t.cell.variant,t.cell.repeat);const o=m.get(k);if(!o||t.cell.attempt>o.cell.attempt)m.set(k,t)}return m};
 // Comparable: completed; infrastructure failures and cancelled trials are left out (they keep their reason).
 const comparable=(t:any)=>t.state==='completed';

 // Inserts one Lab pipeline run by request_id; launches only when this call inserted it. Returns the run id or null.
 async function insertRun(exp:any,o:{requestId:string,file:string,example:any,variant?:any,extra:any}){
  const id=randomUUID();
  const inserted=await store.executions.createExecution({id,kind:'pipeline_run',state:'preparing',projectId:exp.project_id,experimentId:exp.id,variant:o.variant?.label??null,requestId:o.requestId,configRef:o.variant?{config_ref:o.variant.config_ref}:null});
  if(!inserted)return null;
  await runner.start({id,file:o.file,projectId:exp.project_id,requestId:o.requestId,example:o.example,overrides:o.variant?.overrides??{},experimentId:exp.id,variant:o.variant?.label,requestedBy:{kind:'lab',experimentId:exp.id},extra:{priority:'background',...o.extra}});
  return id}
 const exampleOf=async(pin:any)=>{const ex=await d.examples.get(pin.artifact_id);const hash=(await q('SELECT content_hash FROM artifact WHERE id=$1',[pin.artifact_id]))[0]?.content_hash;
  if(hash!==pin.hash)fail(409,`Example ${pin.id} changed since it was pinned`);const {references:_,...rest}=ex.example;return {full:ex.example,run:rest}};
 async function launchTrial(exp:any,pin:any,variant:any,repeat:number,attempt:number){
  const cell={iteration:0,example:pin.id,variant:variant.label,repeat,attempt};
  return insertRun(exp,{requestId:'lab:'+sha([exp.id,0,pin.id,variant.label,repeat,attempt]),file:exp.pipeline_id+'.json',example:(await exampleOf(pin)).run,variant,extra:{cell}})}

 // A trial's files for evaluation: example inputs, then each step's files at the top level (later steps win), as an
 // example's checks expect them (they run in the trial's output directory).
 async function trialFiles(trial:any,example:any){const files=new Map<string,Buffer>();
  for(const f of example.inputs??[])files.set(f.path,Buffer.from(f.text));
  for(const o of await runner.outputsOf(trial)){const rel=o.path.split('/').slice(1).join('/');if(rel)files.set(rel,await d.read(o.artifactId))}
  return [...files].map(([path,bytes])=>({path,bytes}))}

 // Automatic evaluators over completed trials. Returns true when every completed trial has a score or a failed run of every
 // automatic evaluator.
 async function evaluate(exp:any,trials:any[],runs:any[],current:any[],launched:string[],checked:Map<string,any>){
  let complete=true;const evs=exp.evaluators.filter((e:any)=>e.kind!=='human');
  for(const t of trials.filter(comparable))for(const e of evs){const name=evaluatorName(e);
   if(current.some(s=>s.evaluator===name&&s.trialIds[0]===t.id))continue;
   const pin=exp.examples.find((p:any)=>p.id===t.cell.example);
   if(e.kind==='script'){const ex=(await exampleOf(pin)).full,r=checked.get(t.id);if(!r){complete=false;continue}
    await scores.add({experimentId:exp.id,projectId:exp.project_id,trialIds:[t.id],evaluator:name,evaluatorVersion:sha(ex.checks).slice(0,16),form:e.form as Form,value:e.form==='metric'?(r.total?r.passed/r.total:0):r.failed.length===0,rationale:r.failed.length?'failed: '+r.failed.join(', '):`${r.passed} of ${r.total} checks pass`});continue}
   // Judge: one pipeline run per (evaluator, trial); its last step writes {value, rationale}. It sees the example's prompt and
   // the trial's files, never the variant.
   const run=runs.find(r=>r.judge?.evaluator===name&&r.judge.trialId===t.id);
   // Judges launch only while the experiment runs: a paused or stopped experiment spends nothing new.
   if(!run&&exp.state!=='running'){complete=false;continue}
   if(!run){const ex=(await exampleOf(pin)).full,files=await trialFiles(t,ex);
    const jid=await insertRun(exp,{requestId:'lab:'+sha([exp.id,'judge',name,t.id]),file:e.ref+'.json',example:{prompt:ex.prompt,form:e.form,inputs:files.filter(f=>f.bytes.length<1048576).map(f=>({path:'trial/'+f.path,text:f.bytes.toString('utf8')}))},extra:{judge:{evaluator:name,trialId:t.id}}});if(jid)launched.push(jid);complete=false;continue}
   if(!FINAL.has(run.state)){complete=false;continue}
   if(run.state!=='completed')continue;// a failed judge run counts as evaluated; it has no score
   const last=run.steps.at(-1)?.output;let value:any;try{value=checkValue(e.form,last?.value)}catch{continue}
   await scores.add({experimentId:exp.id,projectId:exp.project_id,trialIds:[t.id],evaluator:name,evaluatorVersion:run.definitionSha256?.slice(0,16)??'unknown',form:e.form,value,sourceExecutionId:run.id,rationale:typeof last.rationale==='string'?last.rationale:null})}
  return complete}

 async function reconcileOnce(id:string){
  const launched:string[]=[];
  // Script checks and their file reads run before the transaction, so a slow check never holds the experiment lock.
  const checked=new Map<string,any>();
  {const exp=await row(id);if(exp.state==='draft')return;const cur=await scores.current(id);
   if(exp.evaluators.some((e:any)=>e.kind==='script'))for(const t of trialsOf(await runsOf(id)).filter(comparable)){
    if(cur.some(x=>x.evaluator.startsWith('script:')&&x.trialIds[0]===t.id))continue;
    const ex=(await exampleOf(exp.examples.find((p:any)=>p.id===t.cell.example))).full;checked.set(t.id,await runChecks(ex.checks,await trialFiles(t,ex)))}}
  await store.tx(async()=>{const exp=await row(id);if(exp.state==='draft')return;
   const runs=await runsOf(id),trials=trialsOf(runs),cur=await scores.current(id);
   const evaluated=await evaluate(exp,trials,runs,cur,launched,checked);
   if(exp.state!=='running')return;
   // Stop rules, first match wins: (a) owner stop is the stop action itself; (b) a budget with action stop. Then pause and warn.
   // Each budget action fires once: the budget keeps firedAt, so a resumed experiment does not pause again on the same budget.
   const last=latest(trials),missing=exp.examples.length*exp.variants.length*exp.repeats-last.size;
   // A budget reached with nothing left to launch changes nothing: the experiment finishes.
   const s:any=await spend(id),budgets=missing>0?exp.budgets:[];let state='running',reason:string|null=null,changed=false;
   for(const b of budgets.filter((b:any)=>b.action==='stop'))if(!b.firedAt&&s[b.unit].value>=b.limit){b.firedAt=Date.now();changed=true;state='stopped';reason='budget:'+b.unit;break}
   if(state==='running')for(const b of budgets.filter((b:any)=>b.action!=='stop'))if(!b.firedAt&&s[b.unit].value>=b.limit){b.firedAt=Date.now();changed=true;
    if(b.action==='pause'){state='paused';
     await store.tasks.ask({kind:'operator_decision',projectId:exp.project_id,text:`Lab budget reached: ${b.unit} ${s[b.unit].value} of ${b.limit}. Running trials finish; resume launches or stop the experiment?`,options:['resume','stop'],askedBy:'lab',data:{experimentId:id,budget:b.unit}});break}}
   if(changed||state!=='running')await q('UPDATE experiment SET budgets=$2,state=$3,stop_reason=coalesce($4,stop_reason) WHERE id=$1',[id,JSON.stringify(budgets),state,reason]);
   if(state!=='running')return;
   // Missing cells of iteration 0: examples × variants × repeats, attempt 0 unless a retry raised it.
   // A trials budget that has not fired caps this pass's launches, so the budget is reached exactly, not overshot.
   let room=Math.min(...budgets.filter((b:any)=>b.unit==='trials'&&b.action!=='warn'&&!b.firedAt).map((b:any)=>b.limit-s.trials.value));
   launch:for(const pin of exp.examples)for(const v of exp.variants)for(let r=0;r<exp.repeats;r++){if(last.has(cellKey(pin.id,v.label,r)))continue;if(room<=0)break launch;
    const runId=await launchTrial(exp,pin,v,r,0);if(runId){launched.push(runId);room--}}
   const cells=[...last.values()];
   if(!launched.length&&cells.length===exp.examples.length*exp.variants.length*exp.repeats&&cells.every(t=>FINAL.has(t.state))&&evaluated)
    await q("UPDATE experiment SET state='done',stop_reason='complete' WHERE id=$1 AND state='running'",[id])},'experiment:'+id);
  for(const r of launched)runner.advance(r).catch(()=>{})}
 // Serial per experiment in this process too, so a burst of finish events runs one reconcile after another.
 const chains=new Map<string,Promise<void>>();
 const reconcile=(id:string):Promise<void>=>store.outside(()=>{const prev=chains.get(id)??Promise.resolve();const next=prev.catch(()=>{}).then(()=>reconcileOnce(id));chains.set(id,next);next.finally(()=>{if(chains.get(id)===next)chains.delete(id)}).catch(()=>{});return next});
 const reconcileRunning=async()=>{for(const r of await q("SELECT id FROM experiment WHERE state='running'"))if(!chains.has(r.id))await reconcile(r.id).catch(()=>{})};
 const reconcileAll=async()=>{for(const r of await q("SELECT id FROM experiment WHERE state IN ('running','paused','stopped')"))await reconcile(r.id).catch(()=>{})};

 // Owner actions. Each ends with a reconcile.
 async function setState(id:string,from:string[],to:string,reason?:string){const r=await q(`UPDATE experiment SET state=$2,stop_reason=coalesce($3,stop_reason) WHERE id=$1 AND state=ANY($4) RETURNING id`,[id,to,reason??null,from]);
  if(!r.length){const e=await row(id);fail(409,`Experiment is ${e.state}`)}
  // A budget question answered by the buttons instead is withdrawn, so a late answer never moves a later pause.
  await q("UPDATE question SET state='withdrawn' WHERE state='open' AND kind='operator_decision' AND data->>'experimentId'=$1",[id])}
 async function launch(id:string){await store.tx(()=>setState(id,['draft'],'running'));await reconcile(id);const out:any=await get(id);
  // Drift warns on launch, never refuses (registry source_status of the pinned examples' use cases).
  const ucs=new Set<string>();for(const p of out.examples){const ex=await d.examples.get(p.artifact_id);ucs.add(ex.example.use_case_id)}
  out.warnings=registry().filter(u=>ucs.has(u.id)&&u.source_status!=='current').map(u=>`${u.id}: source ${u.source_status}`);return out}
 const stop=async(id:string)=>{await store.tx(()=>setState(id,['running','paused'],'stopped','owner'));await reconcile(id);return get(id)};
 const resume=async(id:string)=>{await store.tx(()=>setState(id,['paused'],'running'));await reconcile(id);return get(id)};
 async function cellAction(id:string,cell:string,action:'cancel'|'retry'){
  const exp=await row(id),t=latest(trialsOf(await runsOf(id))).get(cell)??fail(404,'Unknown cell');
  if(action==='cancel'){if(FINAL.has(t.state))fail(409,'Trial already finished');const own=await runner.cancel(t.id);if(own)await d.stopExecution(own).catch(()=>{})}
  else{if(!FINAL.has(t.state))fail(409,'Trial still running; cancel it first');if(exp.state==='draft'||exp.state==='stopped')fail(409,`Experiment is ${exp.state}`);
   const pin=exp.examples.find((p:any)=>p.id===t.cell.example),v=exp.variants.find((x:any)=>x.label===t.cell.variant);
   const runId=await store.tx(async()=>{await q("UPDATE experiment SET state='running' WHERE id=$1 AND state='done'",[id]);return launchTrial(exp,pin,v,t.cell.repeat,t.cell.attempt+1)},'experiment:'+id);
   if(runId)runner.advance(runId).catch(()=>{})}
  await reconcile(id);return get(id)}

 // Promote: a catalogue method entry as a JSON patch for config/catalogue.json for the owner to commit (the coordinator never
 // writes tracked config); once a method with this config_ref exists, the project recommendation is set to it.
 async function promote(id:string,label:string){const exp=await row(id),v=exp.variants.find((x:any)=>x.label===label)??fail(400,'Unknown variant');
  const method={id:`${exp.pipeline_id}-${label.toLowerCase().replace(/[^a-z0-9]+/g,'-').replace(/^-|-$/g,'')}`,name:`${exp.pipeline_id} ${label}`,scope:exp.project_id,pipeline:exp.pipeline_id+'.json',overrides:v.overrides,configRef:v.config_ref,sourceExperiment:id};
  const c=await d.catalogue(exp.project_id).catch(()=>null),existing=c?.methods?.find((m:any)=>m.configRef===v.config_ref);
  return {cataloguePatch:existing?null:[{op:'add',path:'/methods/-',value:method}],method:existing??method,recommendation:existing?await d.recommend(exp.project_id,existing.id):null}}

 // Owner ratings: one rating question; pass_fail and scale rate one trial, pick_best two or more (shown blind, seeded order).
 async function rate(b:any){const exp=await row(ident(b?.experimentId,'experimentId')),form=b.form;
  if(!['pass_fail','pick_best','scale'].includes(form))fail(400,'form is pass_fail, pick_best or scale');
  const ids=Array.isArray(b.trialIds)?b.trialIds.map((x:any)=>ident(x,'trial')):fail(400,'Expected trialIds');
  if(form==='pick_best'?ids.length<2:ids.length!==1)fail(400,form==='pick_best'?'pick_best rates two or more trials':`${form} rates one trial`);
  const trials=trialsOf(await runsOf(exp.id));for(const t of ids){const x=trials.find(y=>y.id===t)??fail(400,'Unknown trial '+t);if(!comparable(x))fail(409,'Trial '+t+' has no comparable result')}
  const seed=randomUUID();
  const qid=await store.tx(()=>store.tasks.ask({kind:'rating',projectId:exp.project_id,text:form==='pick_best'?`Pick the best of ${ids.length} outputs`:form==='pass_fail'?'Pass or fail this output':'Rate this output from 1 to 5',options:{form,experiment_id:exp.id,trial_ids:ids,seed},askedBy:'operator',data:{experimentId:exp.id}}));
  return rating(qid)}
 // The rating as the screen shows it: outputs in the seeded order, labelled A, B, …; never the variant.
 async function rating(qid:string){const r=(await q('SELECT * FROM question WHERE id=$1',[qid]))[0]??fail(404,'Unknown question');const o=r.options??{};
  const runs=await runsOf(o.experiment_id);
  const items=[];for(const [i,tid] of shuffle(o.trial_ids??[],o.seed??'').entries()){const t=runs.find(x=>x.id===tid);items.push({label:String.fromCharCode(65+i),trialId:tid,files:t?await runner.outputsOf(t):[]})}
  return {id:r.id,kind:r.kind,form:o.form,experimentId:o.experiment_id??null,exampleArtifactId:o.example_artifact_id??null,state:r.state,answer:r.answer,text:r.text,items}}
 const ratings=async(b:any)=>{const rows=await q("SELECT id FROM question WHERE kind='rating' AND ($1::text IS NULL OR options->>'experiment_id'=$1) AND ($2::text IS NULL OR state::text=$2) ORDER BY seq",[b?.experimentId??null,b?.state??null]);
  const out=[];for(const r of rows)out.push(await rating(r.id));return {ratings:out}};

 // Answer hook (tasks.answer): rating answers become scores; a pass on an example's rating approves the example; a budget
 // question resumes or stops its experiment.
 const answers={check:async(qn:any,answer:string)=>{
  if(qn.kind==='rating'){const o=qn.options??{};parseRating(o.form,answer,o.trial_ids??[])}
  else if(qn.data?.experimentId&&qn.data?.budget&&!['resume','stop'].includes(answer))fail(400,'Answer resume or stop')},
 after:async(qn:any)=>{const o=qn.options??{};
  if(qn.kind==='rating'&&o.experiment_id){const p=parseRating(o.form,qn.answer,o.trial_ids);const exp=await row(o.experiment_id);
   await scores.add({experimentId:exp.id,projectId:exp.project_id,trialIds:o.trial_ids,evaluator:'human:owner:'+o.form,evaluatorVersion:'1',form:o.form,value:p.value,questionId:qn.id,rationale:p.rationale})}
  else if(qn.kind==='rating'&&o.example_artifact_id){if(parseRating('pass_fail',qn.answer,[]).value)await q("UPDATE artifact SET approved_at=now() WHERE id=$1 AND role='example' AND approved_at IS NULL",[o.example_artifact_id])}
  else if(qn.data?.experimentId&&qn.data?.budget){await q(`UPDATE experiment SET state=$2,stop_reason=CASE WHEN $2='stopped' THEN 'owner' ELSE stop_reason END WHERE id=$1 AND state='paused'`,[qn.data.experimentId,qn.answer==='resume'?'running':'stopped'])}
  else return;
  const eid=o.experiment_id??qn.data?.experimentId;if(eid)setImmediate(()=>{reconcile(eid).catch(()=>{})})}};

 // Drafted examples (R3.6): when a draft-examples run completes, each examples/*.json file of its steps becomes a proposed
 // example artifact with one pass_fail rating question; the owner's pass approves it. A file that is not a valid example is
 // skipped (the run's check step reports shape problems).
 async function importDrafts(run:any,add:(b:any)=>Promise<any>){
  for(const f of await runner.outputsOf(run)){if(!/\/examples\/[^/]+\.json$/.test(f.path))continue;let ex:any;
   try{ex=JSON.parse((await d.read(f.artifactId)).toString('utf8'))}catch{continue}
   let stored:any;try{stored=(await add({examples:[ex],projectId:run.projectId??null,approved:false})).examples[0]}catch{continue}
   await store.tx(async()=>{if((await q("SELECT 1 FROM question WHERE kind='rating' AND options->>'example_artifact_id'=$1",[stored.artifactId])).length)return;
    await store.tasks.ask({kind:'rating',projectId:run.projectId??null,text:`Approve drafted example ${stored.id}@${stored.version}? Approving lets the coordinator run its checks:\n${(await d.examples.get(stored.artifactId)).example.checks.map((c:any)=>`${c.id}: ${c.run}`).join('\n')}`,options:{form:'pass_fail',example_artifact_id:stored.artifactId,seed:stored.artifactId},askedBy:'lab',data:{exampleArtifactId:stored.artifactId}})})}}

 // Use-case registry for the screen: entries with drift status, fit and maturity.
 function registry(){const dir=join(d.labDirectory,'use-cases'),out:any[]=[];
  for(const f of readdirSync(dir).filter(f=>f.endsWith('.json')&&!f.startsWith('.')).sort())for(const e of JSON.parse(readFileSync(join(dir,f),'utf8')))
   out.push({id:e.id,goal:e.goal,source:e.source,source_status:e.source_status,lab_fit:e.lab_fit,maturity:e.maturity,checks:e.checks});return out}

 async function get(id:string){const exp=await row(id),runs=await runsOf(id),trials=trialsOf(runs),cur=await scores.current(id),s=await spend(id);
  const stepRows=await q(`SELECT data->>'pipelineRunId' AS run,host_id,harness,harness_version,model FROM execution WHERE experiment_id=$1 AND data ? 'pipelineStepId' AND data ? 'agentConfig'`,[id]);
  const credits=await q(`SELECT split_part(e.parent_execution_id,':step:',1) AS run,count(*)::int AS calls,coalesce(sum((t.connection_cost->>'credits')::numeric),0)::float8 AS credits,count(*) FILTER (WHERE t.connection_cost_basis IS DISTINCT FROM 'measured')::int AS unmeasured FROM execution e LEFT JOIN execution_totals t ON t.execution_id=e.id WHERE e.experiment_id=$1 AND e.kind='tool_run' GROUP BY 1`,[id]);
  const trialView=(t:any)=>{const st=stepRows.filter((x:any)=>x.run===t.id),c=credits.find((x:any)=>x.run===t.id);
   return {id:t.id,cell:t.cell,state:t.state,problem:t.problem,comparable:comparable(t),startedAt:t.startedAt,finishedAt:t.finishedAt??null,durationMs:t.durationMs,
    spend:{calls:c?.calls??0,credits:c?.credits??0,basis:c?.unmeasured?'partial':'measured'},
    confounders:{hosts:[...new Set(st.map((x:any)=>x.host_id).filter(Boolean))],harnesses:[...new Set(st.map((x:any)=>[x.harness,x.harness_version].filter(Boolean).join(' ')).filter(Boolean))],models:[...new Set(st.map((x:any)=>x.model).filter(Boolean))],webRoute:t.overrides?.web_route??'connections'},
    scores:cur.filter(x=>x.trialIds.includes(t.id))}};
  const last=latest(trials);
  const cells=exp.examples.flatMap((p:any)=>exp.variants.map((v:any)=>{const ts=[...last.values()].filter(t=>t.cell.example===p.id&&t.cell.variant===v.label).sort((a,b)=>a.cell.repeat-b.cell.repeat);
   return {example:p.id,variant:v.label,trials:ts.map(t=>cellKey(p.id,v.label,t.cell.repeat)),summary:summarize(cur,ts.filter(comparable).map(t=>t.id))}}));
  // Spread of each variant across repeats per evaluator (the baseline's spread is the noise floor of a comparison).
  const variants=exp.variants.map((v:any)=>({...v,summary:summarize(cur,[...last.values()].filter(t=>t.cell.variant===v.label&&comparable(t)).map(t=>t.id))}));
  return {...view(exp),variants,spend:s,trials:trials.map(trialView),cells,scores:cur,agreement:agreement(cur),
   judges:runs.filter(r=>r.judge).map(r=>({id:r.id,state:r.state,evaluator:r.judge.evaluator,trialId:r.judge.trialId,problem:r.problem})),
   priority:{value:'background',note:'No effect until the scheduler exists'}}}
 const list=async()=>({experiments:(await q('SELECT * FROM experiment ORDER BY seq DESC')).map(view)});
 return {create,get,list,launch,stop,resume,cellAction,promote,rate,rating,ratings,answers,reconcile,reconcileAll,reconcileRunning,importDrafts,registry,scores}}
