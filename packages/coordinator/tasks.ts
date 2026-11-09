// Tasks and questions (design Technical Plan §2, Phase 2 item 18). A task is a production work item; its executions, results
// and questions are linked by task_id. Only state and wait_reason are stored, and they change only through transition() here.
// A legacy delivery is a task row (field and state mapping in §2); the legacy 'delivery', 'question' and 'project-question'
// records are served from the typed tables through the adapters below.
import {randomUUID} from 'node:crypto';
import type {Adapter,Raw,Executions} from './executions.js';
export const TASK_STATES=['queued','in_progress','waiting','paused','completed','failed','cancelled'] as const;
export type TaskState=typeof TASK_STATES[number];
const OPEN=new Set<TaskState>(['queued','in_progress','waiting','paused']);
const fail=(code:number,message:string):never=>{throw Object.assign(Error(message),{statusCode:code})};
const notFound=()=>fail(404,'Not found');
const clean=(v:any)=>JSON.stringify(v);
const ts=(ms:any)=>Number.isFinite(ms)?new Date(ms).toISOString():null;

// The transitions of §2, and nowhere else. queued -> waiting is the case of a task blocked before its first execution
// (a delivery waiting for the operator's answer to its first question).
type Trigger='first_execution'|'blocked'|'unblocked'|'accepted'|'rejected'|'operator_fail'|'pause'|'resume'|'cancel';
const TRANSITIONS:Record<Trigger,{from:TaskState[],to:TaskState|'previous'}>={
 first_execution:{from:['queued'],to:'in_progress'},
 blocked:{from:['queued','in_progress'],to:'waiting'},
 unblocked:{from:['waiting'],to:'in_progress'},
 accepted:{from:['in_progress'],to:'completed'},
 rejected:{from:['in_progress'],to:'in_progress'},
 operator_fail:{from:['in_progress','waiting'],to:'failed'},
 pause:{from:['queued','in_progress','waiting'],to:'paused'},
 resume:{from:['paused'],to:'previous'},
 cancel:{from:['queued','in_progress','waiting','paused'],to:'cancelled'},
};
// Legacy delivery state -> task state (§2 mapping). merged and published stay in_progress until acceptance.
const DELIVERY_TARGET:Record<string,TaskState>={waiting:'waiting',researching:'in_progress',generating:'in_progress',integrating:'in_progress',publishing:'in_progress','pr-open':'in_progress',blocked:'in_progress',merged:'in_progress',published:'in_progress',unknown:'in_progress',failed:'failed',quota:'failed',unavailable:'failed'};

export function taskModel(raw:Raw,executions:Executions){
 const one=async(sql:string,params:any[])=>(await raw.query(sql,params))[0];
 const taskRow=async(id:string)=>await one('SELECT * FROM task WHERE id=$1',[id])??notFound();

 // Guards: a task starts when its first execution has started and every blocking dependency is completed; it is blocked
 // while an operator decision or agent question is open, or a blocking dependency is not completed.
 const startedExecution=async(id:string)=>!!(await one("SELECT 1 AS x FROM execution WHERE task_id=$1 AND state<>'preparing' LIMIT 1",[id]));
 const openDependency=async(id:string)=>!!(await one("SELECT 1 AS x FROM task_dependency d JOIN task t ON t.id=d.depends_on_task_id WHERE d.task_id=$1 AND d.kind='blocks' AND t.state<>'completed' LIMIT 1",[id]));
 const openBlockingQuestion=async(id:string)=>!!(await one("SELECT 1 AS x FROM question WHERE task_id=$1 AND state='open' AND kind IN ('operator_decision','agent_question') LIMIT 1",[id]));
 const waitReason=async(id:string)=>await openBlockingQuestion(id)?'question':await openDependency(id)?'dependency':null;

 async function transition(id:string,trigger:Trigger,detail:{waitReason?:string|null,by?:string}={}){
  const t=await taskRow(id),rule=TRANSITIONS[trigger];
  if(!rule.from.includes(t.state))fail(409,`Task ${t.state} cannot ${trigger.replace('_',' ')}`);
  if(trigger==='first_execution'&&(!(await startedExecution(id))||await openDependency(id)))fail(409,'Task has no started execution or a blocking dependency is open');
  if(trigger==='unblocked'&&await waitReason(id))fail(409,'Task is still blocked');
  const to=rule.to==='previous'?t.state_before_pause:rule.to;
  await raw.query('UPDATE task SET state=$2,wait_reason=$3,state_before_pause=$4,updated_at=now() WHERE id=$1',[id,to,to==='waiting'?detail.waitReason??await waitReason(id):null,trigger==='pause'?t.state:null]);
  return to}
 // Moves an open task toward a target state through the allowed transitions whose guards hold; used when a legacy flow
 // (delivery) or an answer changes what the task waits for. A transition whose guard does not hold yet is left for later.
 async function settle(id:string,target?:TaskState){const t=await taskRow(id);if(!OPEN.has(t.state)||t.state==='paused')return t.state;
  const reason=await waitReason(id);let state:TaskState=t.state;
  const step=async(trigger:Trigger,extra:any={})=>{try{state=await transition(id,trigger,extra) as TaskState;return true}catch(e:any){if(e?.statusCode===409)return false;throw e}};
  if(target==='failed'){if(state==='queued')await step('first_execution');if(state==='in_progress'||state==='waiting')await step('operator_fail');return state}
  if(reason||target==='waiting'){if(state!=='waiting')await step('blocked',{waitReason:reason??'question'});return state}
  if(state==='waiting')await step('unblocked');
  if(state==='queued'&&target!=='queued')await step('first_execution');
  return state}

 // Questions: one typed table, one answer path.
 async function ask(v:{kind:'operator_decision'|'agent_question'|'acceptance',text:string,projectId?:string|null,taskId?:string|null,executionId?:string|null,sessionId?:string|null,options?:any,askedBy?:string,id?:string,legacyKind?:string,data?:any,askedAt?:number}){
  let taskId=v.taskId??null,projectId=v.projectId??null;
  if(!taskId&&v.executionId){const e=await one('SELECT task_id,project_id FROM execution WHERE id=$1',[v.executionId]);taskId=e?.task_id??null;projectId??=e?.project_id??null}
  const id=v.id??randomUUID();
  await raw.query('INSERT INTO question(id,kind,project_id,task_id,execution_id,session_id,text,options,asked_by,asked_at,legacy_kind,data) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,coalesce($10::timestamptz,now()),$11,$12)',
   [id,v.kind,projectId,taskId,v.executionId??null,v.sessionId??null,v.text,v.options?clean(v.options):null,v.askedBy??null,ts(v.askedAt),v.legacyKind??null,clean(v.data??{})]);
  if(taskId&&v.kind!=='acceptance')await settle(taskId);
  return id}
 // Answering an acceptance question records who accepted, when and which artifacts, and completes the task (or keeps it in
 // progress, a new execution expected, when rejected). Other answers unblock their task.
 async function answer(id:string,v:{answer:string,by:string,artifactIds?:string[],at?:number}){
  const q=await one('SELECT * FROM question WHERE id=$1',[id])??notFound();
  if(q.state==='answered'){if(q.answer!==v.answer)fail(409,'Question already answered differently');return q}
  if(q.state!=='open')fail(409,'Question withdrawn');
  if(q.kind==='acceptance'&&!['accepted','rejected'].includes(v.answer))fail(400,'Acceptance answer is accepted or rejected');
  await raw.query("UPDATE question SET answer=$2,answered_by=$3,answered_at=coalesce($4::timestamptz,now()),state='answered' WHERE id=$1",[id,v.answer,v.by,ts(v.at)]);
  if(q.task_id){if(q.kind==='acceptance'){const artifacts=v.artifactIds??q.data?.artifactIds??[];
    if(v.answer==='accepted'){await transition(q.task_id,'accepted');await raw.query('UPDATE task SET accepted_by=$2,accepted_at=now(),accepted_artifact_ids=$3 WHERE id=$1',[q.task_id,v.by,artifacts])}
    else await transition(q.task_id,'rejected')}
   else await settle(q.task_id)}
  return await one('SELECT * FROM question WHERE id=$1',[id])}
 const questionView=(q:any)=>({id:q.id,kind:q.kind,projectId:q.project_id,taskId:q.task_id,executionId:q.execution_id,sessionId:q.session_id,text:q.text,options:q.options,askedBy:q.asked_by,askedAt:q.asked_at,answer:q.answer,answeredBy:q.answered_by,answeredAt:q.answered_at,state:q.state,artifactIds:q.data?.artifactIds??undefined});
 const questions=async(f:{taskId?:string,state?:string,kind?:string}={})=>{const c:string[]=[],p:any[]=[];for(const [k,col] of [['taskId','task_id'],['state','state'],['kind','kind']] as const)if(f[k]){p.push(f[k]);c.push(`${col}=$${p.length}`)}
  return (await raw.query(`SELECT * FROM question${c.length?' WHERE '+c.join(' AND '):''} ORDER BY seq`,p)).map(questionView)};

 // Tasks.
 async function create(v:{id?:string,projectId?:string|null,title:string,expectedOutcome?:string|null,priority?:number,completionRequirements?:any,resultDestination?:string|null,deliveryMethod?:any,createdBy?:string,dependsOn?:string[],legacyKind?:string,data?:any,createdAt?:number}){
  if(typeof v.title!=='string'||!v.title.trim()||v.title.length>500)fail(400,'Expected task title');
  const id=v.id??randomUUID();
  await raw.query('INSERT INTO task(id,project_id,title,expected_outcome,priority,completion_requirements,result_destination,delivery_method,created_by,created_at,legacy_kind,legacy_id,data) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,coalesce($10::timestamptz,now()),$11,$12,$13)',
   [id,v.projectId??null,v.title,v.expectedOutcome??null,v.priority??0,clean(v.completionRequirements??{}),v.resultDestination??null,v.deliveryMethod?clean(v.deliveryMethod):null,v.createdBy??null,ts(v.createdAt),v.legacyKind??null,v.legacyKind?id:null,clean(v.data??{})]);
  for(const d of v.dependsOn??[])await depend(id,d,'blocks');
  return id}
 async function depend(id:string,on:string,kind:'blocks'|'revision_of'){const t=await one('SELECT project_id FROM task WHERE id=$1',[id]);
  await raw.query('INSERT INTO task_dependency(task_id,depends_on_task_id,kind,project_id) VALUES($1,$2,$3,$4) ON CONFLICT DO NOTHING',[id,on,kind,t?.project_id??null])}
 // Operator changes: title, outcome and priority directly; state only through transitions.
 async function update(b:any){const t=await taskRow(b?.taskId);
  const action=b.action;if(action!==undefined){const map:Record<string,Trigger>={pause:'pause',resume:'resume',cancel:'cancel',fail:'operator_fail'};if(!map[action])fail(400,'Unknown task action');await transition(t.id,map[action],{by:'operator'})}
  for(const [k,col] of [['title','title'],['expectedOutcome','expected_outcome'],['priority','priority'],['resultDestination','result_destination']] as const)if(b[k]!==undefined){
   if(k==='priority'?!Number.isSafeInteger(b[k]):typeof b[k]!=='string'||b[k].length>4000)fail(400,'Invalid '+k);await raw.query(`UPDATE task SET ${col}=$2,updated_at=now() WHERE id=$1`,[t.id,b[k]])}
  return get(t.id)}
 const view=(t:any)=>({id:t.id,projectId:t.project_id,title:t.title,expectedOutcome:t.expected_outcome,state:t.state,waitReason:t.wait_reason,priority:t.priority,completionRequirements:t.completion_requirements,resultDestination:t.result_destination,deliveryMethod:t.delivery_method,acceptedBy:t.accepted_by,acceptedAt:t.accepted_at,acceptedArtifactIds:t.accepted_artifact_ids,createdBy:t.created_by,createdAt:t.created_at,updatedAt:t.updated_at,legacyKind:t.legacy_kind,legacyId:t.legacy_id});
 // tasks.get: the stored task with its derived progress, cost and latest result (task_view), executions, dependencies, questions.
 async function get(id:string){const t=await taskRow(id),d=await one('SELECT * FROM task_view WHERE task_id=$1',[id]);
  return {...view(t),progress:{executions:Number(d.execution_count),byState:d.executions_by_state},cost:d.totals,latestResult:d.latest_result,openQuestions:d.open_questions,
   executions:await executions.list({taskId:id}),dependencies:(await raw.query('SELECT depends_on_task_id AS id,kind FROM task_dependency WHERE task_id=$1',[id])),questions:await questions({taskId:id})}}
 async function list(f:{projectId?:string,state?:string}={}){const c:string[]=[],p:any[]=[];if(f.projectId){p.push(f.projectId);c.push(`t.project_id=$${p.length}`)}if(f.state){p.push(f.state);c.push(`t.state=$${p.length}`)}
  return (await raw.query(`SELECT t.*,v.execution_count,v.executions_by_state,v.open_questions FROM task t JOIN task_view v ON v.task_id=t.id${c.length?' WHERE '+c.join(' AND '):''} ORDER BY t.seq`,p)).map(t=>({...view(t),progress:{executions:Number(t.execution_count),byState:t.executions_by_state},openQuestions:t.open_questions}))}

 // Legacy delivery record <-> task row (§2 field mapping). The record stays whole in data for the delivery API.
 async function putDelivery(r:any){
  const exists=await one('SELECT id,state FROM task WHERE id=$1',[r.id]);
  const title=(r.task??(r.sourceSessionId?'Deliver session '+r.sourceSessionId:'Delivery '+r.id)).split('\n')[0].slice(0,200);
  const req={requestId:r.requestId??null,signature:r.signature??null},method={methodId:r.methodId??null,profileId:r.profileId??null,account:r.account??null,invocation:r.invocation??null,branch:r.branch??null};
  if(!exists)await create({id:r.id,projectId:r.projectId??null,title,expectedOutcome:r.task??null,completionRequirements:req,deliveryMethod:method,createdBy:r.sourceSessionId?'session:'+r.sourceSessionId:'operator',legacyKind:'delivery',data:r,createdAt:r.createdAt});
  else await raw.query('UPDATE task SET project_id=$2,title=$3,expected_outcome=$4,completion_requirements=$5,delivery_method=$6,data=$7,updated_at=now() WHERE id=$1',[r.id,r.projectId??null,title,r.task??null,clean(req),clean(method),clean(r)]);
  if(r.previousDeliveryId&&await one('SELECT 1 AS x FROM task WHERE id=$1',[r.previousDeliveryId]))await depend(r.id,r.previousDeliveryId,'revision_of');
  if(r.questionId)await raw.query('UPDATE question SET task_id=$2 WHERE id=$1 AND task_id IS NULL',[r.questionId,r.id]);
  const steps=(r.steps??[]).map((s:any)=>s.executionId).filter((x:any)=>typeof x==='string');
  const runs=[r.researchRunId,r.sourceRunId].filter((x:any)=>typeof x==='string');
  if(steps.length||runs.length)await raw.query('UPDATE execution SET task_id=$1 WHERE id=ANY($2) AND task_id IS NULL',[r.id,[...steps,...runs]]);
  const target=DELIVERY_TARGET[r.state];if(target)await settle(r.id,target);
  // A merged or published result waits for the operator's acceptance; one acceptance question per round.
  if(['merged','published'].includes(r.state)&&!(await one("SELECT 1 AS x FROM question WHERE task_id=$1 AND kind='acceptance' AND state='open'",[r.id]))&&(await one('SELECT state FROM task WHERE id=$1',[r.id])).state==='in_progress'&&!(await one("SELECT 1 AS x FROM question WHERE task_id=$1 AND kind='acceptance' AND data->>'round'=$2",[r.id,r.state]))){
   const artifactIds=(await raw.query("SELECT d.id FROM document d WHERE d.kind='artifact' AND d.data->>'executionId'=ANY($1) ORDER BY d.seq",[steps])).map(a=>a.id);
   await ask({kind:'acceptance',taskId:r.id,projectId:r.projectId??null,text:`Accept the ${r.state} result of: ${title}`,options:['accepted','rejected'],askedBy:'coordinator',data:{artifactIds,round:r.state}})}
 }
 const deliveryWhere=(f:any)=>{const rest=Object.fromEntries(Object.entries(f??{}).filter(([,v])=>v!==undefined));return Object.keys(rest).length?[' AND data @> $1',[clean(rest)]] as const:['',[]] as const};

 // Legacy question records: agent questions ('question') and operator decisions ('project-question').
 const LEGACY_STATE:Record<string,Record<string,string>>={question:{open:'open',answered:'answered'},'project-question':{waiting:'open',answered:'answered'}};
 const putQuestion=(legacy:'question'|'project-question')=>async(v:any)=>{
  const state=LEGACY_STATE[legacy][v.state]??'open',exists=await one('SELECT id FROM question WHERE id=$1',[v.id]);
  if(!exists){await ask({id:v.id,kind:legacy==='question'?'agent_question':'operator_decision',text:legacy==='question'?v.question:v.text,projectId:v.projectId??null,executionId:v.executionId??null,sessionId:v.sessionId??null,askedBy:legacy==='question'?(v.executionId?'execution:'+v.executionId:'agent'):v.actor??'operator',legacyKind:legacy,data:v,askedAt:v.createdAt});
   if(state==='answered')await raw.query("UPDATE question SET state='answered',answer=$2,answered_by=$3,answered_at=$4 WHERE id=$1",[v.id,v.answer??null,v.answeredBy??'operator',ts(v.answeredAt)]);return}
  if(state==='answered'){await answer(v.id,{answer:v.answer,by:v.answeredBy??(legacy==='question'?'operator':'operator'),at:v.answeredAt})}
  await raw.query('UPDATE question SET data=$2 WHERE id=$1',[v.id,clean(v)])};
 const questionAdapter=(legacy:'question'|'project-question'):Adapter=>({
  get:async id=>(await one('SELECT data FROM question WHERE id=$1 AND legacy_kind=$2',[id,legacy]))?.data??notFound(),
  put:putQuestion(legacy),
  all:async f=>{const [w,p]=deliveryWhere(f);return (await raw.query(`SELECT data FROM question WHERE legacy_kind=$${p.length+1}${w} ORDER BY seq`,[...p,legacy])).map(r=>r.data)},
  many:async ids=>new Map((await raw.query('SELECT data FROM question WHERE legacy_kind=$2 AND id=ANY($1)',[ids,legacy])).map(r=>[r.data.id,r.data])),
 });
 const adapters:Record<string,Adapter>={
  delivery:{
   get:async id=>(await one("SELECT data FROM task WHERE id=$1 AND legacy_kind='delivery'",[id]))?.data??notFound(),
   put:putDelivery,
   all:async f=>{const [w,p]=deliveryWhere(f);return (await raw.query(`SELECT data FROM task WHERE legacy_kind='delivery'${w} ORDER BY seq`,[...p])).map(r=>r.data)},
   many:async ids=>new Map((await raw.query("SELECT data FROM task WHERE legacy_kind='delivery' AND id=ANY($1)",[ids])).map(r=>[r.data.id,r.data])),
  },
  question:questionAdapter('question'),
  'project-question':questionAdapter('project-question'),
 };
 return {adapters,create,update,get,list,transition,settle,depend,ask,answer,questions};
}
export type Tasks=ReturnType<typeof taskModel>;
