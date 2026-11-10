// Budgets (docs/designs/fleet-people-access.md §9, slice 6 shape): one shape for person, project, experiment and connection
// scopes. The owner sets them; dispatch applies the reached ones (pause, stop) and shows warn ones as warnings. Spend is measured
// here only for units Fleet measures per execution (wall_ms, tokens); other units are shown as unverifiable and never enforced.
// Money units and connection spend come with the connections stages (fleet-connections.md); Lab experiment budgets stay in Lab.
import {randomUUID} from 'node:crypto';
import type {Store} from './store.ts';
import type {Person} from './people.ts';
const fail=(code:number,message:string):never=>{throw Object.assign(Error(message),{statusCode:code})};
const SCOPES=['person','project','experiment','connection'],ACTIONS=['warn','pause','stop'],PERIODS=['day','week','month'];
const MEASURED:Record<string,string>={wall_ms:'coalesce(t.wall_ms,0)',tokens:'coalesce(t.tokens_input,0)+coalesce(t.tokens_output,0)'};
const view=(r:any)=>({id:r.id,scope:r.scope,scopeId:r.scope_id,unit:r.unit,limit:Number(r.limit),period:r.period,action:r.action,setBy:r.set_by,setAt:r.set_at});

export function budgets(store:Store){
 const spend=async(b:any):Promise<{value:number|null,basis:string}>=>{
  // A connection budget counts that connection's tool_run rows: calls, or the amount in its own unit (credits) from the spool.
  if(b.scope==='connection'){const since=b.period?`AND (e.data->>'startedAt')::bigint >= (extract(epoch from now() - interval '1 ${b.period}')*1000)::bigint`:'';
   const v=b.unit==='calls'?'count(*)':`coalesce(sum((t.connection_cost->>'amount')::numeric) FILTER (WHERE t.connection_cost->>'unit'=$2),0)`;
   const r=(await store.query(`SELECT ${v}::float8 AS v FROM execution e LEFT JOIN execution_totals t ON t.execution_id=e.id WHERE e.kind='tool_run' AND e.data->>'connection'=$1 ${since}`,b.unit==='calls'?[b.scope_id]:[b.scope_id,b.unit]))[0];
   return {value:Number(r?.v??0),basis:'measured'}}
  const expr=MEASURED[b.unit];if(!expr||!['person','project'].includes(b.scope))return {value:null,basis:'unverifiable'};
  const since=b.period?`AND e.started_at >= now() - interval '1 ${b.period}'`:'';
  const col=b.scope==='project'?'e.project_id':'e.directed_by';
  const r=(await store.query(`SELECT coalesce(sum(${expr}),0)::float8 AS v FROM execution e JOIN execution_totals t ON t.execution_id=e.id WHERE ${col}=$1 ${since}`,[b.scope_id]))[0];
  return {value:Number(r?.v??0),basis:'measured'}};
 const operations={
  // Owner only: create or change a budget ({id?} updates). Default action: warn (§9; Lab keeps its own pause).
  budget_set:async(b:any,p:Person|null)=>{if(!p?.owner)fail(403,'Only the owner sets budgets');
   if(!SCOPES.includes(b?.scope)||typeof b.scopeId!=='string'||!b.scopeId||typeof b.unit!=='string'||!b.unit||!(typeof b.limit==='number'&&b.limit>=0))fail(400,'Expected scope, scopeId, unit, limit');
   const action=b.action??'warn',period=b.period??null;if(!ACTIONS.includes(action)||(period!==null&&!PERIODS.includes(period)))fail(400,'action warn|pause|stop; period day|week|month');
   const id=typeof b.id==='string'?b.id:randomUUID();
   await store.query(`INSERT INTO budget(id,scope,scope_id,unit,"limit",period,action,set_by) VALUES($1,$2,$3,$4,$5,$6,$7,$8)
    ON CONFLICT(id) DO UPDATE SET scope=excluded.scope,scope_id=excluded.scope_id,unit=excluded.unit,"limit"=excluded."limit",period=excluded.period,action=excluded.action,set_by=excluded.set_by,set_at=now()`,[id,b.scope,b.scopeId,b.unit,b.limit,period,action,p!.email]);
   return view((await store.query('SELECT * FROM budget WHERE id=$1',[id]))[0])},
  // Ask for more (§9): anyone who sees the budget asks; the owner's 'approve' writes the asked limit, audited with both people.
  budget_ask:async(b:any,p:Person|null,ask:(q:any)=>Promise<string>)=>{const r=(await store.query('SELECT * FROM budget WHERE id=$1',[b?.budgetId]))[0]??fail(404,'Unknown budget');
   if(!(typeof b.limit==='number'&&b.limit>Number(r.limit)))fail(400,'Expected a limit above the current one');
   const id=await ask({kind:'operator_decision',projectId:r.scope==='project'?r.scope_id:null,text:`${p?.email??'fleet'} asks to raise the ${r.scope} ${r.scope_id} budget from ${Number(r.limit)} to ${b.limit} ${r.unit}${typeof b.reason==='string'&&b.reason?': '+b.reason.slice(0,500):''}.`,options:['approve','decline'],askedBy:p?.email??'fleet',data:{budgetAsk:{budgetId:r.id,limit:b.limit,askedBy:p?.email??null},cause:`budget:${r.id}:ask`}});
   return {questionId:id}},
  budgets_list:async()=>({budgets:await Promise.all((await store.query('SELECT * FROM budget ORDER BY set_at')).map(async(r:any)=>({...view(r),spend:await spend(r)})))}),
 };
 // The budgets that cover an execution (its project and its director) and are reached by measured spend.
 const reached=async(e:any)=>{const rows=await store.query("SELECT * FROM budget WHERE (scope='project' AND scope_id=$1) OR (scope='person' AND scope_id=$2)",[e.dispatch?.projectId??'',e.directedBy??'']);
  const out=[];for(const r of rows){const s=await spend(r);if(s.value!==null&&s.value>=Number(r.limit))out.push({id:r.id,action:r.action,reason:`${r.scope} ${r.scope_id} budget reached: ${r.unit} ${s.value} of ${Number(r.limit)}`})}return out};
 // Connection budgets reached with action stop: the gateway refuses that connection's calls (fleet-connections.md §6). The
 // default action is warn; warn and pause only show (owner rule: refusing calls is the owner's explicit setting).
 const blockedConnections=async():Promise<Set<string>>=>{const out=new Set<string>();
  for(const r of await store.query("SELECT * FROM budget WHERE scope='connection' AND action='stop'")){const s=await spend(r);if(s.value!==null&&s.value>=Number(r.limit))out.add(r.scope_id)}return out};
 // Answer hook for asks: only the owner approves; an approval sets the limit.
 const answers={check:async(q:any,answer:string,p:Person|null)=>{if(!q.data?.budgetAsk)return;if(!['approve','decline'].includes(answer))fail(400,'Answer approve or decline');if(answer==='approve'&&p&&!p.owner)fail(403,'Only the owner raises a budget')},
  after:async(q:any)=>{const a=q.data?.budgetAsk;if(!a||q.answer!=='approve')return;
   await store.query(`UPDATE budget SET "limit"=$2,set_by=$3,set_at=now() WHERE id=$1`,[a.budgetId,a.limit,q.answered_by??'owner']);
   await store.put('setting-change',{id:randomUUID(),target:'budget:'+a.budgetId,field:'limit',value:a.limit,askedBy:a.askedBy,actor:q.answered_by,at:Date.now()})}};
 return {operations,reached,blockedConnections,answers};
}
