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
 budgetsReached:(e:any)=>Promise<{id:string,action:string,reason:string}[]>,
 readings:{latest:()=>Promise<Map<string,any[]>>,overSwitch:(a:any,list:any[]|undefined)=>string|null},claim:(e:any,hostId:string,binding:any)=>Promise<any>};
const ONLINE_MS=30000; // the existing online rule (accounts.ts connectivity, app.ts HOST_STALE_MS): a poll within the last 30 s

export function dispatcher(d:Deps){
 const {store}=d;
 // A skip reason from a refusal, with its stall cause key (one question per cause). ponytail: keyed on resolve's messages;
 // structured refusal codes if those messages start to change.
 const causeOf=(a:any,host:string,message:string)=>/Paused/.test(message)?`subscription:${a.id}:paused`:/not allowed on host/.test(message)?`placement:${host}:${a.harness}`:/held by/.test(message)?`subscription:${a.id}:not-taken`:/ready binding/.test(message)?`subscription:${a.id}:not-ready`:`subscription:${a.id}:refused`;
 // What every candidate list of one poll reads, loaded once per run.
 const context=async()=>{const hosts=(await d.hosts.list()).filter(h=>!h.roles.length);
  return {hosts,byId:new Map(hosts.map(h=>[h.id,h])),seen:new Map((await store.all('host-observation')).map((o:any)=>[o.id,o.lastSeenAt])),accounts:await store.all('managed-account'),readings:await d.readings.latest()}};
 async function rank(e:any,k?:Awaited<ReturnType<typeof context>>):Promise<Candidate[]>{
  k??=await context();const {byId,seen}=k;
  const x=e.dispatch,director=e.directedBy??null,projectId=x.projectId??null;
  const order=x.host?[x.host]:[...new Set([x.last,d.defaultHost,...k.hosts.map(h=>h.id)])].filter(h=>byId.has(h));
  const named=(v:string)=>(a:any)=>a.id===v||a.name===v||(a.bindings??[]).some((b:any)=>b.binding===v);
  const accounts=k.accounts.filter((a:any)=>(a.harness??'codex')===x.harness),out:Candidate[]=[];
  let subs:any[];
  if(x.account){
   // An explicit subscription ("this one for now") is a list of one; a name no subscription has is a host binding name (legacy).
   const a=k.accounts.find(named(x.account));
   if(a&&(a.harness??'codex')!==x.harness)return [{subscription:{id:a.id,name:a.name,holder:a.holder},host:order[0]??d.defaultHost,warnings:[],skipped:`${a.name} is a ${a.harness} subscription, not ${x.harness}`,cause:`subscription:${a.id}:harness`}];
   subs=[a??null]}
  else{
   // ponytail: the same order as pickAccount in app.ts (director's, then take-rule, then the project list); one helper if they drift.
   const list=d.subscriptionsOf(projectId),pos=(a:any)=>{const i=list.indexOf(a.name)>=0?list.indexOf(a.name):list.indexOf(a.id);return i<0?1e5:i};
   const mine=(a:any)=>a.holder===director||(a.take?.projects??[]).includes(projectId);
   const ranked=accounts.filter(mine).sort((a:any,b:any)=>(a.holder===director?0:1e6)+pos(a)-((b.holder===director?0:1e6)+pos(b)));
   // The codex default binding (the owner's configured default) goes first when it applies to this director; for anyone else it
   // does not apply and never blocks.
   const def=x.harness==='codex'&&d.defaultBinding?accounts.find(named(d.defaultBinding)):undefined;
   // The host's own login is the last resort only when no default subscription applies (as before dispatch).
   subs=[...(def&&mine(def)?[def]:[]),...ranked.filter(a=>a!==def),...(def&&mine(def)?[]:[null])]}
  for(const a of subs)for(const id of order){const h=byId.get(id)!;
   if(a&&!(a.bindings??[]).some((b:any)=>b.hostId===id))continue;
   const c:Candidate={subscription:a?{id:a.id,name:a.name,holder:a.holder}:null,host:id,warnings:[]};out.push(c);
   const skip=(reason:string,cause:string)=>{c.skipped=reason;c.cause=cause};
   if(!d.hosts.maySee(h,projectId)){skip(`${id} is held by ${h.holder}, who has no grant on ${projectId}`,`host:${id}:trust`);continue}
   if(h.intake==='paused'){skip(`${id} is Paused`,`host:${id}:paused`);continue}
   if(a){try{c.binding=await d.resolve(a.name,x.harness,id,director,projectId)}catch(err:any){skip(err.message,causeOf(a,id,err.message));continue}
    const over=d.readings.overSwitch(a,k.readings.get(a.id));if(over){skip(over,`subscription:${a.id}:limit`);continue}
    if(a.harness==='claude'&&a.holder!==h.holder)c.warnings.push(`${h.holder} can read this login (Claude on a host held by someone else)`)}
   else{
    if(x.harness!=='codex'&&!x.account){skip(`no ${x.harness} subscription of ${director} applies`,`director:${director}:${x.harness}`);continue}
    if(director&&h.holder!==director){skip(`${id} login belongs to ${h.holder}`,`host:${id}:holder`);continue}
    if(h.harnesses?.[x.harness]?.allowed===false){skip(`${x.harness} is not allowed on host ${id}`,`placement:${id}:${x.harness}`);continue}}
   if(!(Date.now()-(seen.get(id)??0)<ONLINE_MS)){skip(`${id} is offline`,`host:${id}:offline`);continue}
   if(x.harness==='antigravity'||x.harness==='acp')c.warnings.push('ACP-driven harness: provider terms note (terms research §2)');
  }
  // Two personal subscriptions of one provider and one holder in the same order list (terms research §1).
  const live=out.filter(c=>c.subscription&&!c.skipped);
  for(const c of live)if(new Set(live.filter(o=>o.subscription!.holder===c.subscription!.holder).map(o=>o.subscription!.id)).size>1)c.warnings.push('two personal subscriptions of one holder in one order list (provider rate-limit terms)');
  // A stop budget wins over a pause budget: it is applied first.
  for(const b of (await d.budgetsReached(e)).sort((x,y)=>(x.action==='stop'?0:1)-(y.action==='stop'?0:1))){if(b.action==='warn'){for(const c of out)c.warnings.push(b.reason);continue}
   for(const c of out)if(!c.skipped){c.skipped=b.reason;c.cause=`budget:${b.id}:reached`;(c as any).stop=b.action==='stop'}}
  return out;
 }
 const record=(e:any,pollingHost:string,candidates:Candidate[],winner:Candidate|null)=>store.query('INSERT INTO route_decision(execution_id,project_id,host_id,candidates,winner) VALUES($1,$2,$3,$4,$5)',
  [e.id,e.dispatch.projectId??null,pollingHost,JSON.stringify(candidates.map(({binding,...c})=>c)),winner?JSON.stringify({subscription:winner.subscription,host:winner.host}):null]);
 async function run(pollingHost:string){
  // One query per poll for the idle case: queued executions and the stall questions that hold a cause (open or answered).
  const [q0]=await store.query("SELECT (SELECT coalesce(array_agg(id ORDER BY seq),'{}') FROM execution WHERE host_id IS NULL AND state='preparing' AND kind='agent_run') AS ids,(SELECT coalesce(json_agg(json_build_object('id',id,'cause',cause,'state',state)),'[]') FROM question WHERE state IN ('open','answered') AND cause IS NOT NULL) AS held");
  const ids:string[]=q0.ids,held:any[]=typeof q0.held==='string'?JSON.parse(q0.held):q0.held;
  const causes=new Map<string,{reason:string,director:string|null,ask:string|null,projectId:string|null}>();
  const k=ids.length?await context():null;
  for(const id of ids){const e=await store.get(id,'execution');if(!e.dispatch||e.hostId||e.state!=='queued'||e.stopRequested)continue;
   const candidates=await rank(e,k!),best=candidates.find(c=>!c.skipped);
   if(best){if(best.host!==pollingHost)continue;
    delete e.waitReason;await d.claim(e,best.host,best.binding);await record(e,pollingHost,candidates,best);
    await store.query('UPDATE execution SET wait_reason=NULL WHERE id=$1',[e.id]);continue}
   const first=candidates[0],reason=first?.skipped??'no host can run this work',cause=first?.cause??`execution:${e.id}:no-candidate`;
   // A reached stop budget ends the work instead of keeping it waiting (§9: stop only when the owner sets it).
   if((first as any)?.stop){Object.assign(e,{state:'stopped',stopDetail:reason,waitReason:reason});await store.put('execution',e);await record(e,pollingHost,candidates,null);continue}
   if(e.waitReason!==reason){e.waitReason=reason;await store.put('execution',e);await store.query('UPDATE execution SET wait_reason=$2 WHERE id=$1',[e.id,reason]);await record(e,pollingHost,candidates,null)}
   const hostOfCause=/^host:([^:]+)/.exec(cause)?.[1],holder=hostOfCause?k!.byId.get(hostOfCause)?.holder??null:first?.subscription?.holder??null;
   if(!causes.has(cause))causes.set(cause,{reason,director:e.directedBy??null,ask:holder,projectId:e.dispatch.projectId??null})}
  // One question per cause while it lasts (an answered one is not asked again); a cause that is gone closes and releases it.
  for(const q of held)if(!causes.has(q.cause))await store.query(q.state==='open'?"UPDATE question SET state='withdrawn',cause=NULL WHERE id=$1":'UPDATE question SET cause=NULL WHERE id=$1',[q.id]);
  for(const [cause,c] of causes)if(!held.some((q:any)=>q.cause===cause)){
   const qid=await store.tasks.ask({kind:'operator_decision',text:`Work is waiting: ${c.reason}.${c.ask&&c.ask!==c.director?` Ask ${c.ask}, or change the setting.`:' Change the setting to let it start.'}`,options:['ok'],askedBy:'fleet',projectId:c.projectId,data:{cause}});
   await store.query('UPDATE question SET cause=$2,addressed_to=$3 WHERE id=$1',[qid,cause,c.director])}
 }
 return {run,rank};
}
