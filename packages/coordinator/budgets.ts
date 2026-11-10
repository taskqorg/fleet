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
 const spend=async(b:any):Promise<{value:number|null,basis:string}>=>{const expr=MEASURED[b.unit];if(!expr||!['person','project'].includes(b.scope))return {value:null,basis:'unverifiable'};
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
  budgets_list:async()=>({budgets:await Promise.all((await store.query('SELECT * FROM budget ORDER BY set_at')).map(async(r:any)=>({...view(r),spend:await spend(r)})))}),
 };
 // The budgets that cover an execution (its project and its director) and are reached by measured spend.
 const reached=async(e:any)=>{const rows=await store.query("SELECT * FROM budget WHERE (scope='project' AND scope_id=$1) OR (scope='person' AND scope_id=$2)",[e.dispatch?.projectId??'',e.directedBy??'']);
  const out=[];for(const r of rows){const s=await spend(r);if(s.value!==null&&s.value>=Number(r.limit))out.push({id:r.id,action:r.action,reason:`${r.scope} ${r.scope_id} budget reached: ${r.unit} ${s.value} of ${Number(r.limit)}`})}return out};
 return {operations,reached};
}
