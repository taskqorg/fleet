// Dispatch (docs/designs/fleet-people-access.md §6-7): the one late choice point. On every host poll, each queued agent execution
// (no host yet, oldest first) gets a ranked list of candidates (subscription, host) over all hosts. The best unskipped candidate
// wins; the poll claims it when the winner is the polling host, otherwise the execution waits for that host's poll. With no
// candidate left the execution waits with the first-ranked skip reason, and one needs-you question is open per cause.
// Nothing here is a number Fleet invents: online is the existing 30 s poll rule, every other skip is the owner's or a holder's
// setting, a reading, a budget or a provider refusal.
import type {Store} from './store.ts';
import type {Host} from './hosts.ts';

export type Candidate={subscription:{id:string,name:string,holder:string}|null,host:string,skipped?:string,cause?:string,warnings:string[],binding?:any};
type Deps={store:Store,hosts:{list:()=>Promise<Host[]>,maySee:(h:Host,p:string|null|undefined)=>boolean},
 resolve:(selection:string|undefined,harness:string,hostId:string,director?:string|null,projectId?:string|null)=>Promise<any>,
 subscriptionsOf:(projectId:string|null)=>string[],defaultBinding?:string,defaultHost:string,
 budgetsReached:(e:any)=>Promise<{id:string,action:string,reason:string}[]>,claim:(e:any,hostId:string,binding:any)=>Promise<any>};
const ONLINE_MS=30000; // accounts.ts connectivity: a poll within the last 30 s (existing rule, not a new number)

export function dispatcher(d:Deps){
 const {store}=d;
 // A skip reason from a refusal, with its stall cause key (one open question per cause).
 const causeOf=(a:any,host:string,message:string)=>/Paused/.test(message)?`subscription:${a.id}:paused`:/not allowed on host/.test(message)?`placement:${host}:${a.harness}`:/held by/.test(message)?`subscription:${a.id}:not-taken`:/ready binding/.test(message)?`subscription:${a.id}:not-ready`:`subscription:${a.id}:refused`;
 async function rank(e:any):Promise<Candidate[]>{
  const x=e.dispatch,director=e.directedBy??null,projectId=x.projectId??null;
  const hosts=(await d.hosts.list()).filter(h=>!h.roles.length),byId=new Map(hosts.map(h=>[h.id,h]));
  const seen=new Map((await store.all('host-observation')).map((o:any)=>[o.id,o.lastSeenAt]));
  const order=x.host?[x.host]:[...new Set([x.last,d.defaultHost,...hosts.map(h=>h.id)])].filter(h=>byId.has(h));
  const accounts=(await store.all('managed-account')).filter((a:any)=>(a.harness??'codex')===x.harness);
  // An explicit subscription ("this one for now") is a list of one; the codex default binding is the owner's configured default.
  const requested=x.account??(x.harness==='codex'?d.defaultBinding:undefined);
  let subs:any[];
  if(requested){const a=accounts.find((a:any)=>a.id===requested||a.name===requested||(a.bindings??[]).some((b:any)=>b.binding===requested));subs=a?[a]:[null]}
  else{const list=d.subscriptionsOf(projectId),pos=(a:any)=>{const i=list.indexOf(a.name)>=0?list.indexOf(a.name):list.indexOf(a.id);return i<0?1e5:i};
   subs=[...accounts.filter((a:any)=>a.holder===director||(a.take?.projects??[]).includes(projectId)).sort((a:any,b:any)=>(a.holder===director?0:1e6)+pos(a)-((b.holder===director?0:1e6)+pos(b))),null]}
  const out:Candidate[]=[];
  for(const a of subs)for(const id of order){const h=byId.get(id)!;
   if(a&&!(a.bindings??[]).some((b:any)=>b.hostId===id))continue;
   const c:Candidate={subscription:a?{id:a.id,name:a.name,holder:a.holder}:null,host:id,warnings:[]};out.push(c);
   const skip=(reason:string,cause:string)=>{c.skipped=reason;c.cause=cause};
   if(!d.hosts.maySee(h,projectId)){skip(`${id} is held by ${h.holder}, who has no grant on ${projectId}`,`host:${id}:trust`);continue}
   if(h.intake==='paused'){skip(`${id} is Paused`,`host:${id}:paused`);continue}
   if(a){try{c.binding=await d.resolve(a.name,x.harness,id,director,projectId)}catch(err:any){skip(err.message,causeOf(a,id,err.message));continue}
    if(a.harness==='claude'&&a.holder!==h.holder)c.warnings.push(`${h.holder} can read this login (Claude on a host held by someone else)`)}
   else{
    if(x.harness!=='codex'&&!x.account){skip(`no ${x.harness} subscription of ${director} applies`,`director:${director}:${x.harness}`);continue}
    if(director&&h.holder!==director){skip(`${id} login belongs to ${h.holder}`,`host:${id}:holder`);continue}
    if(h.harnesses?.[x.harness]?.allowed===false){skip(`${x.harness} is not allowed on host ${id}`,`placement:${id}:${x.harness}`);continue}
    if(x.account)c.binding=undefined}
   if(!(Date.now()-(seen.get(id)??0)<ONLINE_MS)){skip(`${id} is offline`,`host:${id}:offline`);continue}
   if(x.harness==='antigravity'||x.harness==='acp')c.warnings.push('ACP-driven harness: provider terms note (terms research §2)');
  }
  // Two personal subscriptions of one provider and one holder in the same order list (terms research §1).
  const live=out.filter(c=>c.subscription&&!c.skipped),holders=live.map(c=>c.subscription!.holder);
  for(const c of live)if(holders.filter(h=>h===c.subscription!.holder).length>1&&new Set(live.filter(o=>o.subscription!.holder===c.subscription!.holder).map(o=>o.subscription!.id)).size>1)c.warnings.push('two personal subscriptions of one holder in one order list (provider rate-limit terms)');
  const reached=await d.budgetsReached(e);
  for(const b of reached)if(b.action!=='warn')for(const c of out)if(!c.skipped){c.skipped=b.reason;c.cause=`budget:${b.id}:reached`}else{}
  for(const b of reached)if(b.action==='warn')for(const c of out)c.warnings.push(b.reason);
  return out;
 }
 const record=(e:any,pollingHost:string,candidates:Candidate[],winner:Candidate|null)=>store.query('INSERT INTO route_decision(execution_id,project_id,host_id,candidates,winner) VALUES($1,$2,$3,$4,$5)',
  [e.id,e.dispatch.projectId??null,pollingHost,JSON.stringify(candidates.map(({binding,...c})=>c)),winner?JSON.stringify({subscription:winner.subscription,host:winner.host}):null]);
 async function run(pollingHost:string){
  // One query per poll for the idle case: queued executions and open stall questions.
  const [q0]=await store.query("SELECT (SELECT coalesce(array_agg(id ORDER BY seq),'{}') FROM execution WHERE host_id IS NULL AND state='preparing' AND kind='agent_run') AS ids,(SELECT coalesce(json_agg(json_build_object('id',id,'cause',cause)),'[]') FROM question WHERE state='open' AND cause IS NOT NULL) AS open");
  const ids:string[]=q0.ids,open:any[]=typeof q0.open==='string'?JSON.parse(q0.open):q0.open;
  const causes=new Map<string,{reason:string,director:string|null,ask:string|null,projectId:string|null}>();
  for(const id of ids){const e=await store.get(id,'execution');if(!e.dispatch||e.hostId||e.state!=='queued'||e.stopRequested)continue;
   const candidates=await rank(e),best=candidates.find(c=>!c.skipped);
   if(best){if(best.host!==pollingHost)continue;
    await d.claim(e,best.host,best.binding);await record(e,pollingHost,candidates,best);
    await store.query('UPDATE execution SET wait_reason=NULL WHERE id=$1',[e.id]);continue}
   const first=candidates[0],reason=first?.skipped??'no host can run this work',cause=first?.cause??`execution:${e.id}:no-candidate`;
   if(e.waitReason!==reason){e.waitReason=reason;await store.put('execution',e);await store.query('UPDATE execution SET wait_reason=$2 WHERE id=$1',[e.id,reason]);await record(e,pollingHost,candidates,null)}
   const holder=/^host:([^:]+)/.exec(cause)?(await d.hosts.list()).find(h=>h.id===/^host:([^:]+)/.exec(cause)![1])?.holder??null:first?.subscription?.holder??null;
   if(!causes.has(cause))causes.set(cause,{reason,director:e.directedBy??null,ask:holder,projectId:e.dispatch.projectId??null})}
  // One open question per cause; a cause that is gone closes its question.
  for(const q of open)if(!causes.has(q.cause))await store.query("UPDATE question SET state='withdrawn' WHERE id=$1",[q.id]);
  for(const [cause,c] of causes)if(!open.some((q:any)=>q.cause===cause)){
   const qid=await store.tasks.ask({kind:'operator_decision',text:`Work is waiting: ${c.reason}.${c.ask&&c.ask!==c.director?` Ask ${c.ask}, or change the setting.`:' Change the setting to let it start.'}`,options:['ok'],askedBy:'fleet',projectId:c.projectId,data:{cause}});
   await store.query('UPDATE question SET cause=$2,addressed_to=$3 WHERE id=$1',[qid,cause,c.director])}
 }
 return {run,rank};
}
