import {actor as who} from './people.ts';
// Tasks and questions (design Technical Plan §2, Phase 2 item 18). A task is a production work item; its executions, results
// and questions are linked by task_id. Only state and wait_reason are stored, and they change only through transition() here.
// A legacy delivery is a task row (field and state mapping in §2); the legacy 'delivery', 'question' and 'project-question'
// records are served from the typed tables through the adapters below.
import {randomUUID} from 'node:crypto';
import type {Adapter,Raw,Executions} from './executions.ts';
// The task state list is defined once, in the view URL codec, which the browser and the coordinator share.
import {TASK_STATES,type TaskState} from '../view-url/view-url.ts';
export {TASK_STATES,type TaskState};
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

// Priority classes; p0/p1/p2 are accepted as TaskQ synonyms for urgent/normal/background.
const PRIORITY:Record<string,string>={urgent:'urgent',normal:'normal',background:'background',p0:'urgent',p1:'normal',p2:'background'};
export const priorityClass=(v:unknown)=>typeof v==='string'&&PRIORITY[v]?PRIORITY[v]:(()=>{throw Object.assign(Error('Invalid priority: use urgent, normal or background'),{statusCode:400})})();
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

 // Take (people-access §5): the taker directs the task from now on; requested_by never changes; executions not yet ended follow.
 async function take(id:string,taker:string){
  const t=await one('SELECT directed_by FROM task WHERE id=$1',[id])??notFound();if(t.directed_by===taker)return;
  await raw.query('UPDATE task SET directed_by=$2,taken_from=$3,updated_at=now() WHERE id=$1',[id,taker,t.directed_by]);
  await raw.query("UPDATE execution SET directed_by=$2 WHERE task_id=$1 AND state IN ('preparing','running','waiting')",[id,taker])}
 // Questions: one typed table, one answer path.
 async function ask(v:{kind:'operator_decision'|'agent_question'|'acceptance'|'rating',text:string,projectId?:string|null,taskId?:string|null,executionId?:string|null,sessionId?:string|null,options?:any,askedBy?:string,id?:string,legacyKind?:string,data?:any,askedAt?:number}){
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
  // Lab questions (ratings, budget decisions) are checked and applied by lab/scores.ts through the answer hook, in this transaction.
  if(answerHook)await answerHook.check(q,v.answer);
  // Only one answer wins: the read above is not locked, so a concurrent answer may have landed since. The loser rereads.
  const won=await raw.query("UPDATE question SET answer=$2,answered_by=$3,answered_at=coalesce($4::timestamptz,now()),state='answered' WHERE id=$1 AND state='open' RETURNING id",[id,v.answer,v.by,ts(v.at)]);
  if(!won.length){const now=await one('SELECT * FROM question WHERE id=$1',[id]);if(now?.state==='answered'&&now.answer===v.answer)return now;fail(409,now?.state==='answered'?'Question already answered differently':'Question withdrawn')}
  if(q.task_id){if(q.kind==='acceptance'){const artifacts=v.artifactIds??q.data?.artifactIds??[];
    if(v.answer==='accepted'){await transition(q.task_id,'accepted');await raw.query('UPDATE task SET accepted_by=$2,accepted_at=now(),accepted_artifact_ids=$3 WHERE id=$1',[q.task_id,v.by,artifacts])}
    else await transition(q.task_id,'rejected')}
   else await settle(q.task_id)}
  const done=await one('SELECT * FROM question WHERE id=$1',[id]);if(answerHook)await answerHook.after(done);return done}
 let answerHook:{check:(q:any,answer:string)=>Promise<void>|void,after:(q:any)=>Promise<void>}|null=null;
 const onAnswer=(h:typeof answerHook)=>{answerHook=h};
 const questionView=(q:any)=>({id:q.id,kind:q.kind,projectId:q.project_id,taskId:q.task_id,executionId:q.execution_id,sessionId:q.session_id,text:q.text,options:q.options,askedBy:q.asked_by,askedAt:q.asked_at,answer:q.answer,answeredBy:q.answered_by,answeredAt:q.answered_at,state:q.state,artifactIds:q.data?.artifactIds??undefined,cause:q.data?.cause??undefined});
 const questions=async(f:{taskId?:string,state?:string,kind?:string}={})=>{const c:string[]=[],p:any[]=[];for(const [k,col] of [['taskId','task_id'],['state','state'],['kind','kind']] as const)if(f[k]){p.push(f[k]);c.push(`${col}=$${p.length}`)}
  return (await raw.query(`SELECT * FROM question${c.length?' WHERE '+c.join(' AND '):''} ORDER BY seq`,p)).map(questionView)};

 // Tasks.
 async function create(v:{id?:string,projectId?:string|null,title:string,expectedOutcome?:string|null,priority?:string,completionRequirements?:any,resultDestination?:string|null,deliveryMethod?:any,createdBy?:string,directedBy?:string|null,dependsOn?:string[],legacyKind?:string,data?:any,createdAt?:number}){
  if(typeof v.title!=='string'||!v.title.trim()||v.title.length>500)fail(400,'Expected task title');
  const id=v.id??randomUUID(),priority=priorityClass(v.priority??'normal');
  // priority is left to the column default ('normal'); before migration 8 (migration 6 replays legacy records) it is an integer.
  await raw.query('INSERT INTO task(id,project_id,title,expected_outcome,completion_requirements,result_destination,delivery_method,created_by,created_at,legacy_kind,legacy_id,data) VALUES($1,$2,$3,$4,$5,$6,$7,$8,coalesce($9::timestamptz,now()),$10,$11,$12)',
   [id,v.projectId??null,v.title,v.expectedOutcome??null,clean(v.completionRequirements??{}),v.resultDestination??null,v.deliveryMethod?clean(v.deliveryMethod):null,v.createdBy??null,ts(v.createdAt),v.legacyKind??null,v.legacyKind?id:null,clean(v.data??{})]);
  // Separate statement: migration 6 replays legacy tasks before migration 17 adds the columns; they carry no director.
  if(v.directedBy)await raw.query('UPDATE task SET directed_by=$2,requested_by=$2 WHERE id=$1',[id,v.directedBy]);
  if(priority!=='normal')await raw.query('UPDATE task SET priority=$2 WHERE id=$1',[id,priority]);
  for(const d of v.dependsOn??[])await depend(id,d,'blocks');
  return id}
 async function depend(id:string,on:string,kind:'blocks'|'revision_of'){const t=await one('SELECT project_id FROM task WHERE id=$1',[id]);
  await raw.query('INSERT INTO task_dependency(task_id,depends_on_task_id,kind,project_id) VALUES($1,$2,$3,$4) ON CONFLICT DO NOTHING',[id,on,kind,t?.project_id??null])}
 // Operator changes: title, outcome and priority directly; state only through transitions.
 async function update(b:any){const t=await taskRow(b?.taskId);
  const action=b.action;if(action!==undefined){const map:Record<string,Trigger>={pause:'pause',resume:'resume',cancel:'cancel',fail:'operator_fail'};if(!map[action])fail(400,'Unknown task action');await transition(t.id,map[action],{by:who('operator')})}
  for(const [k,col] of [['title','title'],['expectedOutcome','expected_outcome'],['priority','priority'],['resultDestination','result_destination']] as const)if(b[k]!==undefined){
   if(k!=='priority'&&(typeof b[k]!=='string'||b[k].length>4000))fail(400,'Invalid '+k);await raw.query(`UPDATE task SET ${col}=$2,updated_at=now() WHERE id=$1`,[t.id,k==='priority'?priorityClass(b[k]):b[k]])}
  return get(t.id)}
 const view=(t:any)=>({id:t.id,projectId:t.project_id,title:t.title,expectedOutcome:t.expected_outcome,state:t.state,waitReason:t.wait_reason,priority:t.priority,completionRequirements:t.completion_requirements,resultDestination:t.result_destination,deliveryMethod:t.delivery_method,acceptedBy:t.accepted_by,acceptedAt:t.accepted_at,acceptedArtifactIds:t.accepted_artifact_ids,createdBy:t.created_by,directedBy:t.directed_by,requestedBy:t.requested_by,takenFrom:t.taken_from,createdAt:t.created_at,updatedAt:t.updated_at,legacyKind:t.legacy_kind,legacyId:t.legacy_id});
 // tasks.get: the stored task with its derived progress, cost and latest result (task_view), executions, dependencies, questions.
 async function get(id:string){const t=await taskRow(id),d=await one('SELECT * FROM task_view WHERE task_id=$1',[id]);
  return {...view(t),progress:{executions:Number(d.execution_count),byState:d.executions_by_state},cost:d.totals,latestResult:d.latest_result,openQuestions:d.open_questions,
   executions:await executions.list({taskId:id}),dependencies:(await raw.query('SELECT depends_on_task_id AS id,kind FROM task_dependency WHERE task_id=$1',[id])),questions:await questions({taskId:id})}}
 async function list(f:{projectId?:string,state?:string}={}){const c:string[]=[],p:any[]=[];if(f.projectId){p.push(f.projectId);c.push(`t.project_id=$${p.length}`)}if(f.state){p.push(f.state);c.push(`t.state=$${p.length}`)}
  return (await raw.query(`SELECT t.*,v.execution_count,v.executions_by_state,v.open_questions FROM task t JOIN task_view v ON v.task_id=t.id${c.length?' WHERE '+c.join(' AND '):''} ORDER BY t.seq`,p)).map(t=>({...view(t),progress:{executions:Number(t.execution_count),byState:t.executions_by_state},openQuestions:t.open_questions}))}

 // Operator app list (docs/designs/fleet-operator-app.md §6): one WHERE builder serves the page, the counts and membership, so the
 // client never decides membership. Order is (group key, newest first, id); the cursor is that triple. Grouping by priority puts
 // urgent, normal, background.
 // Group expression per grouping; derived says whether it reads task_derived (then the page must join it before ordering).
 const GROUPS:Record<string,{sql:string,derived:boolean}>={none:{sql:"left(t.id,0)",derived:false},priority:{sql:"(CASE t.priority WHEN 'urgent' THEN '1' WHEN 'normal' THEN '2' ELSE '3' END)",derived:false},
  project:{sql:"coalesce(t.project_id,'~')",derived:false},agent:{sql:"coalesce(d.agent,'~')",derived:true},host:{sql:"coalesce(d.host,'~')",derived:true},
  state:{sql:"(CASE t.state WHEN 'waiting' THEN '1' WHEN 'failed' THEN '2' WHEN 'in_progress' THEN '3' WHEN 'queued' THEN '4' WHEN 'paused' THEN '5' WHEN 'completed' THEN '6' ELSE '7' END)",derived:false}};
 // Group label column per grouping (from this fixed map only; nothing from the request is interpolated).
 const LABELS:Record<string,string>={priority:'t.priority',state:'t.state::text',project:'t.project_id',agent:'d.agent',host:'d.host'};
 const STATES:string[]=[...TASK_STATES];
 // projectId: one id, null (no project), or a list of both (view URL project=a,-).
 type PageFilter={projectId?:string|null|(string|null)[],state?:string[],search?:string};
 // Derived values live in task_derived (migration 10), kept by triggers; the page joins it 1:1.
 const base='FROM task t LEFT JOIN task_derived d ON d.task_id=t.id';
 const where=(f:PageFilter,p:any[],withState=true)=>{const c:string[]=[];
  if(f.projectId!==undefined){const ps=Array.isArray(f.projectId)?f.projectId:[f.projectId];if(!ps.every(x=>x===null||typeof x==='string'))fail(400,'Invalid projectId');
   const ids=ps.filter((x):x is string=>typeof x==='string'),o:string[]=[];if(ids.length){p.push(ids);o.push(`t.project_id=ANY($${p.length})`)}if(ps.includes(null))o.push('t.project_id IS NULL');if(o.length)c.push(`(${o.join(' OR ')})`)}
  if(withState&&Array.isArray(f.state)&&f.state.length){if(!f.state.every(x=>STATES.includes(x)))fail(400,'Invalid state');p.push(f.state);c.push(`t.state::text=ANY($${p.length})`)}
  if(typeof f.search==='string'&&f.search.trim()){p.push('%'+f.search.trim().toLowerCase().replace(/[%_\\]/g,m=>'\\'+m)+'%');c.push(`(lower(t.title) LIKE $${p.length} OR lower(t.id) LIKE $${p.length})`)}
  return c};
 // A task without a derived row (only possible if a trigger was skipped) still lists, with empty values; the drift check reports it.
 const cols='t.id,t.project_id,t.title,t.state,t.wait_reason,t.priority,t.created_at,t.updated_at,t.seq,d.agent,d.host,d.started_at,coalesce(d.open_questions,0) AS open_questions';
 const listRow=(x:any)=>({id:x.id,projectId:x.project_id,title:x.title,state:x.state,waitReason:x.wait_reason,priority:x.priority,agent:x.agent,host:x.host,startedAt:x.started_at,createdAt:x.created_at,updatedAt:x.updated_at,openQuestions:Number(x.open_questions),sortKey:[x.g,-Number(x.seq),x.id]});
 const groupOf=(g:any)=>{if(g!==undefined&&!(typeof g==='string'&&Object.hasOwn(GROUPS,g)))fail(400,'Invalid group');return GROUPS[g??'none']};
 const W=(c:string[])=>c.length?' WHERE '+c.join(' AND '):'';
 async function page(b:{filter?:PageFilter,group?:string,groupCounts?:boolean,limit?:number,after?:[string,number,string]}={}){
  const f=b.filter??{},G=groupOf(b.group),g=G.sql,limit=Math.max(1,Math.min(500,Math.floor(b.limit??100)));
  const p:any[]=[],c=where(f,p);
  if(b.after){if(!Array.isArray(b.after)||b.after.length!==3)fail(400,'Invalid after');p.push(String(b.after[0]),Number(b.after[1]),String(b.after[2]));c.push(`(${g},-t.seq,t.id) > ($${p.length-2},$${p.length-1},$${p.length})`)}
  // Join task_derived only where needed: agent/host grouping orders by it; otherwise the page is chosen on task alone and
  // only its rows are joined (the planner would otherwise join every matching task before the LIMIT). Counts never join.
  const rows=await raw.query(G.derived?`SELECT ${cols},${g} AS g ${base}${W(c)} ORDER BY ${g},t.seq DESC,t.id LIMIT ${limit+1}`
   :`SELECT ${cols},t.g FROM (SELECT t.*,${g} AS g FROM task t${W(c)} ORDER BY ${g},t.seq DESC,t.id LIMIT ${limit+1}) t LEFT JOIN task_derived d ON d.task_id=t.id ORDER BY t.g,t.seq DESC,t.id`,p);
  const {counts:byState,groups}=await summary(f,b.group,b.groupCounts===true);
  const page=rows.slice(0,limit).map(listRow);
  return {tasks:page,nextAfter:rows.length>limit?page.at(-1)!.sortKey:null,counts:byState,total:Object.values(byState).reduce((a:number,n:any)=>a+n,0),groups}}
 // Counts and group summaries only (list_membership after a change): no page rows.
 // Per-state counts leave the state filter out, so every counter keeps its number while one state is selected.
 async function summary(f:PageFilter,group?:string,groupCounts=false){const G=groupOf(group);
  const cp:any[]=[],cc=where(f,cp,false),byState=Object.fromEntries(STATES.map(s=>[s,0]));
  for(const r of await raw.query(`SELECT t.state::text AS state,count(*) AS n FROM task t${W(cc)} GROUP BY 1`,cp))byState[r.state]=Number(r.n);
  const gp:any[]=[],gc=where(f,gp);
  const groups=group&&group!=='none'?(await raw.query(`SELECT ${G.sql} AS g,min(${LABELS[group]}) AS label,count(*) AS n ${G.derived?base:'FROM task t'}${W(gc)} GROUP BY 1 ORDER BY 1`,gp)).map(r=>({key:r.g,label:r.label,count:Number(r.n)})):[];
  if(!groupCounts||!groups.length)return {counts:byState,groups};
  // groupCounts (Overview): each group's per-state counts, like the top-level counts without the state filter, in one query.
  const byGroup=new Map(groups.map(g=>[g.key,Object.fromEntries(STATES.map(s=>[s,0]))]));
  for(const r of await raw.query(`SELECT ${G.sql} AS g,t.state::text AS state,count(*) AS n ${G.derived?base:'FROM task t'}${W(cc)} GROUP BY 1,2`,cp)){const c=byGroup.get(r.g);if(c)c[r.state]=Number(r.n)}
  return {counts:byState,groups:groups.map(g=>({...g,counts:byGroup.get(g.key)!}))}}
 // The ids among `ids` that match the filter, with the same row shape and sort key as page().
 async function membership(f:PageFilter,group:string|undefined,ids:string[]){const g=groupOf(group).sql,p:any[]=[],c=where(f,p);p.push(ids.filter(x=>typeof x==='string').slice(0,1000));c.push(`t.id=ANY($${p.length})`);
  return (await raw.query(`SELECT ${cols},${g} AS g ${base}${W(c)}`,p)).map(listRow)}
 // Drift check: task_derived_drift() recomputes values with all projects visible (no false drift for a scoped caller) and
 // returns only tasks the caller may see; checked counts the same tasks (row-level security on task, same rule).
 async function derivedDrift(){const [{n}]=await raw.query('SELECT count(*)::int AS n FROM task');
  const drifted=(await raw.query("SELECT task_id,stored,expected FROM task_derived_drift()")).map((r:any)=>({taskId:r.task_id,stored:r.stored,expected:r.expected}));
  return {checked:Number(n),drifted}}

 // Legacy delivery record <-> task row (§2 field mapping). The record stays whole in data for the delivery API.
 async function putDelivery(r:any){
  const exists=await one('SELECT id,state FROM task WHERE id=$1',[r.id]);
  const title=(r.task??(r.sourceSessionId?'Deliver session '+r.sourceSessionId:'Delivery '+r.id)).split('\n')[0].slice(0,200);
  const req={requestId:r.requestId??null,signature:r.signature??null},method={methodId:r.methodId??null,profileId:r.profileId??null,account:r.account??null,invocation:r.invocation??null,branch:r.branch??null};
  if(!exists)await create({id:r.id,projectId:r.projectId??null,title,expectedOutcome:r.task??null,completionRequirements:req,deliveryMethod:method,createdBy:r.sourceSessionId?'session:'+r.sourceSessionId:who('operator'),legacyKind:'delivery',data:r,createdAt:r.createdAt});
  // Write only when something changed: the delivery advance re-saves every record on each coordinator start.
  else await raw.query('UPDATE task SET project_id=$2,title=$3,expected_outcome=$4,completion_requirements=$5,delivery_method=$6,data=$7,updated_at=now() WHERE id=$1 AND (project_id,title,expected_outcome,completion_requirements,delivery_method,data) IS DISTINCT FROM ($2,$3,$4,$5::jsonb,$6::jsonb,$7::jsonb)',[r.id,r.projectId??null,title,r.task??null,clean(req),clean(method),clean(r)]);
  if(r.previousDeliveryId&&await one('SELECT 1 AS x FROM task WHERE id=$1',[r.previousDeliveryId]))await depend(r.id,r.previousDeliveryId,'revision_of');
  if(r.questionId)await raw.query('UPDATE question SET task_id=$2 WHERE id=$1 AND task_id IS NULL',[r.questionId,r.id]);
  const steps=(r.steps??[]).map((s:any)=>s.executionId).filter((x:any)=>typeof x==='string');
  const runs=[r.researchRunId,r.sourceRunId].filter((x:any)=>typeof x==='string');
  if(steps.length||runs.length)await raw.query('UPDATE execution SET task_id=$1 WHERE id=ANY($2) AND task_id IS NULL',[r.id,[...steps,...runs]]);
  const target=DELIVERY_TARGET[r.state];if(target)await settle(r.id,target);
  // A merged or published result waits for the operator's acceptance; one acceptance question per round.
  if(['merged','published'].includes(r.state)&&!(await one("SELECT 1 AS x FROM question WHERE task_id=$1 AND kind='acceptance' AND state='open'",[r.id]))&&(await one('SELECT state FROM task WHERE id=$1',[r.id])).state==='in_progress'&&!(await one("SELECT 1 AS x FROM question WHERE task_id=$1 AND kind='acceptance' AND data->>'round'=$2",[r.id,r.state]))){
   // Before migration 9 (artifact table) only documents hold artifacts.
   const typed=(await raw.query("SELECT to_regclass('artifact') IS NOT NULL AS t"))[0].t;
   const artifactIds=(await raw.query(typed?"SELECT coalesce(legacy_id,id) AS id,seq FROM artifact WHERE state='stored' AND role='original' AND execution_id=ANY($1) UNION ALL SELECT id,seq FROM document WHERE kind='artifact' AND data->>'executionId'=ANY($1) ORDER BY seq":"SELECT id FROM document WHERE kind='artifact' AND data->>'executionId'=ANY($1) ORDER BY seq",[steps])).map(a=>a.id);
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
 return {adapters,create,update,get,list,page,summary,membership,derivedDrift,transition,settle,depend,take,ask,answer,questions,onAnswer};
}
export type Tasks=ReturnType<typeof taskModel>;
