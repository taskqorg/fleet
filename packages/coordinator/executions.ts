// Execution model (design Technical Plan §1, Phase 2 item 17). One table holds every run Fleet launches or a harness reports:
// agent runs, pipeline runs and their steps, delivery and tool steps, and harness-reported child agents.
// Each row keeps typed columns and, in data, the record shape the coordinator API returns. Until callers move to the typed
// fields, the legacy kinds 'execution', 'delegation' and 'pipeline-run' are served from this table through the adapters below.
export type Raw={query:(sql:string,params?:any[])=>Promise<any[]>,get:(id:string,kind:string)=>Promise<any>};
export type Adapter={get:(id:string)=>Promise<any>,put:(v:any)=>Promise<void>,all:(filter?:any,omit?:string[])=>Promise<any[]>,many:(ids:string[])=>Promise<Map<string,any>>};
export const EXECUTION_STATES=['preparing','running','waiting','succeeded','failed','cancelled'] as const;
export type State=typeof EXECUTION_STATES[number];
export const TERMINAL=new Set<State>(['succeeded','failed','cancelled']);
const notFound=()=>{throw Object.assign(Error('Not found'),{statusCode:404})};
const ts=(ms:any)=>Number.isFinite(ms)?new Date(ms).toISOString():null;
const clean=(v:any)=>JSON.stringify(v);

// Legacy state mapping (§1, §7): unknown keeps the last known state and lowers the observation to unconfirmed.
export function mapLegacyState(legacy:string|undefined,previous?:State):{state:State,observation:'confirmed'|'unconfirmed'}{
 switch(legacy){
  case 'queued':case 'accepted':case 'pending':return {state:'preparing',observation:'confirmed'};
  case 'started':case 'running':return {state:'running',observation:'confirmed'};
  case 'waiting':return {state:'waiting',observation:'confirmed'};
  case 'completed':return {state:'succeeded',observation:'confirmed'};
  case 'failed':case 'quota':case 'unavailable':return {state:'failed',observation:'confirmed'};
  case 'stopped':case 'cancelled':return {state:'cancelled',observation:'confirmed'};
  default:return {state:previous??'preparing',observation:'unconfirmed'};
 }
}

export function executionModel(raw:Raw){
 const one=async(sql:string,params:any[])=>(await raw.query(sql,params))[0];
 const optionalDoc=async(id:any,kind:string)=>{if(typeof id!=='string')return undefined;try{return await raw.get(id,kind)}catch(e:any){if(e?.statusCode===404)return undefined;throw e}};
 const previousState=async(id:string)=>(await one('SELECT state FROM execution WHERE id=$1',[id]))?.state as State|undefined;

 // Kind of a legacy execution record: program workers either run a delivery step or a tool (inspection, search, sync).
 const kindOf=(e:any)=>e.actor==='program-worker'?(e.deliveryId?'pipeline_step':'tool_run'):'agent_run';
 // Links that the legacy record keeps elsewhere: the session's project and delivery, the handoff or fork source.
 const links=async(e:any)=>{const s=await optionalDoc(e.sessionId,'session');
  const handoff=e.handoffId?await optionalDoc(e.handoffId,'handoff'):undefined;
  // Parent: the delegating, continued or pipeline execution (the pipeline step for a step's agent or script run), the handoff source, the agent execution that started the session
  // (session_start from a host agent), or the source of a fork for the fork's first execution.
  const parent=e.parentExecutionId??e.continuationOf??(e.pipelineStepId??e.pipelineRunId)??handoff?.turns?.at?.(-1)??s?.createdByExecutionId??(s?.forkOf?.executionId&&!(await one('SELECT 1 AS x FROM execution WHERE session_id=$1 AND id<>$2 AND legacy_kind=$3 LIMIT 1',[s.id,e.id,'execution']))?s.forkOf.executionId:undefined);
  return {projectId:e.projectId??s?.projectId??null,taskId:e.deliveryId??s?.deliveryId??null,parent:parent??null}};

 // Upsert of a legacy execution record. Typed-only columns (task_id once set, harness_version, problem, last_seen_at)
 // belong to their own writers and are not overwritten here.
 async function putExecution(e:any){
  const prev=await previousState(e.id),{state,observation}=mapLegacyState(e.state,prev),l=await links(e);
  const harness=e.agentConfig?.harness??(kindOf(e)==='agent_run'?'codex':null);
  const config={profileId:e.agentConfig?.id??null,profileSha256:e.profileSha256??null,effort:e.effort??null};
  await raw.query(`INSERT INTO execution(id,kind,state,observation,project_id,task_id,experiment_id,variant,parent_execution_id,session_id,account_id,host_id,harness,model,config_ref,started_at,ended_at,legacy_kind,legacy_id,data)
   VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,'execution',$1,$18)
   ON CONFLICT(id) DO UPDATE SET kind=excluded.kind,state=excluded.state,observation=excluded.observation,project_id=excluded.project_id,task_id=coalesce(execution.task_id,excluded.task_id),
    experiment_id=excluded.experiment_id,variant=excluded.variant,parent_execution_id=excluded.parent_execution_id,session_id=excluded.session_id,account_id=excluded.account_id,host_id=excluded.host_id,
    harness=excluded.harness,model=excluded.model,config_ref=excluded.config_ref,started_at=excluded.started_at,ended_at=excluded.ended_at,data=excluded.data,
    problem=CASE WHEN excluded.state='succeeded' THEN NULL ELSE execution.problem END`,
   [e.id,kindOf(e),state,observation,l.projectId,l.taskId,e.experimentId??null,e.experimentId?e.agentConfig?.id??null:null,l.parent,e.sessionId??null,e.effectiveAccount?.accountId??e.accountId??null,e.hostId??null,harness,e.model??null,clean(config),ts(e.startedAt),ts(e.finishedAt),clean(e)]);
  // Separate statement: migration 6 replays legacy records before migration 17 adds the column; they carry no director.
  if(e.directedBy)await raw.query('UPDATE execution SET directed_by=$2 WHERE id=$1 AND directed_by IS DISTINCT FROM $2',[e.id,e.directedBy]);
  // A record written already ended (migration replay) has no live end to place in its thread.
  if(TERMINAL.has(state)&&!(prev&&TERMINAL.has(prev))){await writeTotals(e.id);if(prev)await writeEnded(e.id)}
 }

 // Pipeline run with one pipeline_step row per step (§7); steps link to the run by parent_execution_id.
 async function putPipeline(run:any){
  const {steps=[],...rest}=run,{state,observation}=mapLegacyState(run.state==='running'?'started':run.state,await previousState(run.id));
  // A Lab trial's row is inserted first (createExecution with request_id, variant and the variant's config_ref); a put keeps them.
  await raw.query(`INSERT INTO execution(id,kind,state,observation,project_id,task_id,started_at,ended_at,legacy_kind,legacy_id,data,config_ref,experiment_id,variant) VALUES($1,'pipeline_run',$2,$3,$4,$5,$6,$7,'pipeline-run',$1,$8,$9,$10,$11)
   ON CONFLICT(id) DO UPDATE SET state=excluded.state,observation=excluded.observation,project_id=excluded.project_id,task_id=coalesce(execution.task_id,excluded.task_id),started_at=excluded.started_at,ended_at=excluded.ended_at,data=excluded.data,config_ref=CASE WHEN execution.variant IS NULL THEN excluded.config_ref ELSE execution.config_ref END,legacy_kind='pipeline-run',legacy_id=excluded.legacy_id,experiment_id=coalesce(execution.experiment_id,excluded.experiment_id),variant=coalesce(execution.variant,excluded.variant)`,
   [run.id,state,observation,run.projectId??null,run.requestedBy?.deliveryId??null,ts(run.startedAt),ts(run.finishedAt),clean(rest),clean({method:run.selectedMethod??null,definitionSha256:run.definitionSha256??null}),run.experimentId??null,run.variant??null]);
  for(const [i,step] of steps.entries()){const s=mapLegacyState(step.state==='running'?'started':step.state);
   await raw.query(`INSERT INTO execution(id,kind,state,observation,project_id,parent_execution_id,started_at,legacy_kind,legacy_id,data) VALUES($1,'pipeline_step',$2,$3,$4,$5,$6,'pipeline-step',$7,$8)
    ON CONFLICT(id) DO UPDATE SET state=excluded.state,observation=excluded.observation,project_id=excluded.project_id,started_at=excluded.started_at,data=excluded.data,problem=CASE WHEN excluded.state='succeeded' THEN NULL ELSE execution.problem END`,
    [`${run.id}:step:${i}`,s.state,s.observation,run.projectId??null,run.id,ts(step.startedAt),step.id??String(i),clean({index:i,...step})])}
 }
 const withSteps=async(rows:any[])=>{if(!rows.length)return [];const steps=await raw.query("SELECT parent_execution_id AS run,data FROM execution WHERE legacy_kind='pipeline-step' AND parent_execution_id=ANY($1) ORDER BY (data->>'index')::int",[rows.map(r=>r.data.id)]);
  return rows.map(r=>({...r.data,steps:steps.filter(s=>s.run===r.data.id).map(({data:{index,...step}})=>step)}))};

 // Filters on legacy records: indexed columns where the legacy field has one, jsonb containment on data for the rest.
 const COLUMNS:Record<string,string>={sessionId:'session_id',hostId:'host_id'};
 const where=(filter:any,table='execution',column='data')=>{const params:any[]=[],clauses:string[]=[],rest:any={};
  for(const [k,v] of Object.entries(filter??{})){if(v===undefined)continue;if(column==='data'&&COLUMNS[k]&&typeof v==='string'){params.push(v);clauses.push(`${COLUMNS[k]}=$${params.length}`)}else if(k==='id'&&typeof v==='string'){params.push(v);clauses.push(`${table==='execution'&&column==='delegation'?"delegation->>'id'":'id'}=$${params.length}`)}else rest[k]=v}
  if(Object.keys(rest).length){params.push(clean(rest));clauses.push(`${column} @> $${params.length}`)}
  return {params,clauses}};
 const select=async(legacy:string,filter:any,omit?:string[])=>{const {params,clauses}=where(filter);params.push(legacy);
  return (await raw.query(`SELECT data FROM execution WHERE legacy_kind=$${params.length}${clauses.map(c=>' AND '+c).join('')} ORDER BY seq`,params)).map(r=>omit?Object.fromEntries(Object.entries(r.data).filter(([k])=>!omit.includes(k))):r.data)};

 const adapters:Record<string,Adapter>={
  execution:{
   get:async id=>(await one("SELECT data FROM execution WHERE id=$1 AND legacy_kind='execution'",[id]))?.data??notFound(),
   put:putExecution,
   all:(f,omit)=>select('execution',f,omit),
   many:async ids=>new Map((await raw.query("SELECT data FROM execution WHERE legacy_kind='execution' AND id=ANY($1)",[ids])).map(r=>[r.data.id,r.data])),
  },
  // A delegation is the record of a child execution: it lives on the child's row (parent_execution_id is the delegating execution).
  delegation:{
   get:async id=>(await one("SELECT delegation FROM execution WHERE delegation->>'id'=$1",[id]))?.delegation??notFound(),
   put:async d=>{const r=await raw.query('UPDATE execution SET delegation=$2,parent_execution_id=coalesce(parent_execution_id,$3) WHERE id=$1 RETURNING id',[d.childExecutionId,clean(d),d.parentExecutionId??null]);
    if(!r.length)throw Object.assign(Error('Delegation child execution not found: '+d.childExecutionId),{statusCode:409})},
   all:async(f)=>{const {params,clauses}=where(f,'execution','delegation');return (await raw.query(`SELECT delegation FROM execution WHERE delegation IS NOT NULL${clauses.map(c=>' AND '+c).join('')} ORDER BY seq`,params)).map(r=>r.delegation)},
   many:async ids=>new Map((await raw.query("SELECT delegation FROM execution WHERE delegation->>'id'=ANY($1)",[ids])).map(r=>[r.delegation.id,r.delegation])),
  },
  'pipeline-run':{
   get:async id=>(await withSteps(await raw.query("SELECT data FROM execution WHERE id=$1 AND legacy_kind='pipeline-run'",[id])))[0]??notFound(),
   put:putPipeline,
   all:async(f)=>{const {params,clauses}=where(f);return withSteps(await raw.query(`SELECT data FROM execution WHERE legacy_kind='pipeline-run'${clauses.map(c=>' AND '+c).join('')} ORDER BY seq`,params))},
   many:async ids=>new Map((await withSteps(await raw.query("SELECT data FROM execution WHERE legacy_kind='pipeline-run' AND id=ANY($1)",[ids]))).map(r=>[r.id,r])),
  },
 };

 // Open delegations (delegates.reconcile on every poll), through a partial index.
 const openDelegations=async()=>(await raw.query("SELECT delegation FROM execution WHERE delegation IS NOT NULL AND coalesce(delegation->>'continuationExecutionId','')='' AND coalesce(delegation->>'state','')<>'suppressed' ORDER BY seq")).map(r=>r.delegation);

 // Typed operations.
 async function createExecution(v:{id:string,kind:'agent_run'|'pipeline_run'|'pipeline_step'|'child_agent'|'tool_run',state?:State,projectId?:string|null,taskId?:string|null,parentExecutionId?:string|null,sessionId?:string|null,hostId?:string|null,harness?:string|null,model?:string|null,configRef?:any,data?:any,experimentId?:string|null,variant?:string|null,accountId?:string|null,requestId?:string|null}):Promise<boolean>{
  // A row whose id or request_id already exists is left as it is; the result says whether this call inserted one.
  const rows=await raw.query(`INSERT INTO execution(id,kind,state,project_id,task_id,parent_execution_id,session_id,host_id,harness,model,config_ref,data,experiment_id,variant,account_id,request_id) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16) ON CONFLICT DO NOTHING RETURNING id`,
   [v.id,v.kind,v.state??'preparing',v.projectId??null,v.taskId??null,v.parentExecutionId??null,v.sessionId??null,v.hostId??null,v.harness??null,v.model??null,v.configRef?clean(v.configRef):null,clean(v.data??{}),v.experimentId??null,v.variant??null,v.accountId??null,v.requestId??null]);
  return rows.length>0}
 const ORDER:Record<State,number>={preparing:0,waiting:1,running:1,succeeded:2,failed:2,cancelled:2};
 // A state moves forward only: terminal states are final, and running/waiting never go back to preparing.
 async function transition(id:string,state:State){if(!EXECUTION_STATES.includes(state))throw Object.assign(Error('Unknown execution state '+state),{statusCode:400});
  const row=await one('SELECT state FROM execution WHERE id=$1',[id]);if(!row)notFound();
  if(row.state===state)return row;if(TERMINAL.has(row.state)||ORDER[state]<ORDER[row.state as State])throw Object.assign(Error(`Execution ${row.state} cannot become ${state}`),{statusCode:409});
  await raw.query(`UPDATE execution SET state=$2,problem=CASE WHEN $2::execution_state='succeeded' THEN NULL ELSE problem END,ended_at=CASE WHEN $3 THEN coalesce(ended_at,now()) ELSE ended_at END,started_at=CASE WHEN $2::execution_state IN ('running','waiting') THEN coalesce(started_at,now()) ELSE started_at END WHERE id=$1`,[id,state,TERMINAL.has(state)]);
  if(TERMINAL.has(state)){await writeTotals(id);await writeEnded(id)}return {state}}
 // The end of an agent run in its thread (docs/designs/fleet-conversations.md): one event with no blocks whose seq places the
 // result block. The fixed id keeps it to one row however often the run is settled.
 async function writeEnded(id:string){const r=await one("SELECT session_id,project_id,ended_at FROM execution WHERE id=$1 AND kind='agent_run'",[id]);if(!r)return;
  await raw.query("INSERT INTO document(kind,id,data,project_id) VALUES('event',$1,$2,$3) ON CONFLICT(id) DO NOTHING",['ended:'+id,clean({id:'ended:'+id,executionId:id,sessionId:r.session_id,type:'execution.ended',timestamp:r.ended_at?new Date(r.ended_at).getTime():Date.now(),data:{fleetNormalized:{blocks:[]}}}),r.project_id])}
 // Observation confidence: confirmed whenever the host reports for the execution, unconfirmed when contact is lost.
 async function markObservation(id:string,observation:'confirmed'|'unconfirmed'){await raw.query('UPDATE execution SET observation=$2::observation,last_seen_at=CASE WHEN $2::text=\'confirmed\' THEN now() ELSE last_seen_at END WHERE id=$1',[id,observation])}
 const setHarnessVersion=(id:string,version:string)=>raw.query('UPDATE execution SET harness_version=$2 WHERE id=$1 AND harness_version IS DISTINCT FROM $2',[id,version]);
 // Child agents a harness reports (Codex collab, executor d3802cc): one child_agent row per child thread, under the parent.
 async function recordChildren(parentId:string,children:any[]){const p=await one('SELECT project_id,task_id,session_id,host_id,harness FROM execution WHERE id=$1',[parentId]);if(!p)return;
  for(const c of children){if(typeof c?.threadId!=='string')continue;
   await createExecution({id:`${parentId}:child:${c.threadId}`,kind:'child_agent',state:'running',projectId:p.project_id,taskId:p.task_id,parentExecutionId:parentId,sessionId:p.session_id,hostId:p.host_id,harness:p.harness,model:typeof c.binding?.model==='string'?c.binding.model:null,configRef:c.binding??null,data:{threadId:c.threadId,parentThreadId:c.parentThreadId??null,tool:c.tool??null}})}}
 // Totals before Phase 4 (ClickHouse): wall-clock time from started_at and ended_at is measured; every other measure is unavailable.
 async function writeTotals(id:string){await raw.query(`INSERT INTO execution_totals(execution_id,project_id,wall_ms,wall_ms_basis,computed_at)
   SELECT id,project_id,CASE WHEN started_at IS NOT NULL AND ended_at IS NOT NULL THEN (extract(epoch FROM ended_at-started_at)*1000)::bigint END,CASE WHEN started_at IS NOT NULL AND ended_at IS NOT NULL THEN 'measured' ELSE 'unavailable' END,now() FROM execution WHERE id=$1
   ON CONFLICT(execution_id) DO UPDATE SET project_id=excluded.project_id,wall_ms=excluded.wall_ms,wall_ms_basis=excluded.wall_ms_basis,computed_at=excluded.computed_at`,[id])}
 const ended=async(id:string)=>!!(await one('SELECT 1 AS x FROM execution WHERE id=$1 AND ended_at IS NOT NULL',[id]));
 // Executions bound to a host, account or environment that nothing registered (example: 6b00f0fb waiting for host 'pds18').
 // Rule (design §1): a problem explains why an open execution cannot run. It is set and cleared only while the execution is open;
 // reaching succeeded clears it; failed and cancelled keep the last problem as the explanation.
 // One statement: registered hosts are the configured ones plus every host that has polled (host-observation), accounts are
 // the managed accounts, environments the hosts of each project's environments.
 async function detectProblems(registered:{hosts:string[],environments:Record<string,string[]>}){
  return (await raw.query(`WITH found AS (SELECT e.id,CASE
    WHEN e.host_id IS NOT NULL AND NOT (e.host_id=ANY($1) OR EXISTS (SELECT 1 FROM document h WHERE h.kind='host-observation' AND h.id=e.host_id)) THEN 'unregistered host '||e.host_id
    WHEN e.account_id IS NOT NULL AND NOT EXISTS (SELECT 1 FROM document a WHERE a.kind='managed-account' AND a.id=e.account_id) THEN 'unregistered account '||e.account_id
    WHEN e.project_id IS NOT NULL AND e.host_id IS NOT NULL AND jsonb_array_length(coalesce($2::jsonb->e.project_id,'[]'))>0 AND NOT ($2::jsonb->e.project_id) ? e.host_id THEN 'no registered environment of project '||e.project_id||' on host '||e.host_id
   END AS problem FROM execution e WHERE e.state NOT IN ('succeeded','failed','cancelled') AND e.kind<>'child_agent')
   UPDATE execution x SET problem=found.problem FROM found WHERE x.id=found.id AND x.problem IS DISTINCT FROM found.problem RETURNING x.id`,[registered.hosts,clean(registered.environments)])).length}
 // Typed views for new readers.
 const row=(r:any)=>r&&{id:r.id,kind:r.kind,state:r.state,observation:r.observation,lastSeenAt:r.last_seen_at,projectId:r.project_id,taskId:r.task_id,experimentId:r.experiment_id,variant:r.variant,parentExecutionId:r.parent_execution_id,sessionId:r.session_id,accountId:r.account_id,hostId:r.host_id,environmentId:r.environment_id,harness:r.harness,harnessVersion:r.harness_version,model:r.model,configRef:r.config_ref,problem:r.problem,startedAt:r.started_at,endedAt:r.ended_at,legacyKind:r.legacy_kind,legacyId:r.legacy_id};
 const COLS='id,kind,state,observation,last_seen_at,project_id,task_id,experiment_id,variant,parent_execution_id,session_id,account_id,host_id,environment_id,harness,harness_version,model,config_ref,problem,started_at,ended_at,legacy_kind,legacy_id';
 const getTyped=async(id:string)=>row(await one(`SELECT ${COLS} FROM execution WHERE id=$1`,[id]))??notFound();
 const list=async(f:{taskId?:string,projectId?:string,problem?:boolean,parentExecutionId?:string}={})=>{const c:string[]=[],p:any[]=[];
  if(f.taskId){p.push(f.taskId);c.push(`task_id=$${p.length}`)}if(f.projectId){p.push(f.projectId);c.push(`project_id=$${p.length}`)}if(f.parentExecutionId){p.push(f.parentExecutionId);c.push(`parent_execution_id=$${p.length}`)}if(f.problem)c.push('problem IS NOT NULL');
  return (await raw.query(`SELECT ${COLS} FROM execution${c.length?' WHERE '+c.join(' AND '):''} ORDER BY seq`,p)).map(row)};
 return {adapters,openDelegations,createExecution,insertIfAbsent:createExecution,transition,markObservation,setHarnessVersion,recordChildren,writeTotals,ended,detectProblems,get:getTyped,list};
}
export type Executions=ReturnType<typeof executionModel>;
