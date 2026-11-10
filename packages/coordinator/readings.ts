// Readings (docs/designs/fleet-people-access.md §8, slice 7): the latest provider limit value Fleet has seen per subscription
// and limit period, from the limit_reading events the executor forwards (Codex: percent; Claude: status, no percent). A reading
// is only as fresh as the subscription's last run: it carries its time. A reading whose resetsAt has passed counts as unknown,
// and no reading is unknown, never zero. Read from the events, so host ingest writes nothing outside its execution.
import type {Store} from './store.ts';

export type Reading={period:string,pct:number|null,status:string|null,resetsAt:number|null,at:number,hostId:string|null,state:'known'|'unknown'};
export function readings(store:Store){
 // Latest reading per (subscription, period).
 const latest=async():Promise<Map<string,Reading[]>>=>{
  const rows=await store.query(`SELECT DISTINCT ON (x.account_id, r->>'period') x.account_id, x.host_id, r AS reading, d.updated_at AS at
   FROM document d JOIN execution x ON x.id = d.data->>'executionId' CROSS JOIN LATERAL jsonb_array_elements(coalesce(d.data->'data'->'readings','[]'::jsonb)) r
   WHERE d.kind='event' AND d.data->>'type'='limit_reading' AND x.account_id IS NOT NULL
   ORDER BY x.account_id, r->>'period', d.seq DESC`);
  const out=new Map<string,Reading[]>();
  for(const r of rows){const v=r.reading,resetsAt=typeof v.resetsAt==='number'?v.resetsAt:null;
   const reading:Reading={period:v.period??'unknown',pct:typeof v.pct==='number'&&v.pct>=0&&v.pct<=100?v.pct:null,status:typeof v.status==='string'?v.status:null,resetsAt,at:new Date(r.at).getTime(),hostId:r.host_id,
    state:resetsAt!==null&&resetsAt<=Date.now()?'unknown':'known'};
   out.set(r.account_id,[...(out.get(r.account_id)??[]),reading])}
  return out};
 // Skips (§6): the provider rejected (status, Claude), or a percent reading at or over the switchPct. Only readings that know
 // when their limit period ends count: one without resetsAt could otherwise block the subscription forever.
 const overSwitch=(a:any,list:Reading[]|undefined)=>{const live=(list??[]).filter(x=>x.state==='known'&&x.resetsAt!==null),old=(r:Reading)=>`${Math.max(0,Math.round((Date.now()-r.at)/60000))} min old`;
  const rejected=live.find(x=>x.status==='rejected');if(rejected)return `provider limit reached (${rejected.period}), ${old(rejected)}`;
  if(typeof a.switchPct!=='number')return null;
  const r=live.filter(x=>x.pct!==null&&x.pct>=a.switchPct).sort((x,y)=>y.pct!-x.pct!)[0];
  return r?`switch point ${a.switchPct} %; reading ${r.pct} % (${r.period}), ${old(r)}`:null};
 const operations={
  // Limits view: every visible subscription with its readings; none is "unknown".
  subscription_limits:async()=>{const map=await latest();return {serverNow:Date.now(),subscriptions:(await store.all('managed-account')).map((a:any)=>({id:a.id,name:a.name,harness:a.harness,holder:a.holder,intake:a.intake,switchPct:a.switchPct??null,readings:map.get(a.id)??[]}))}},
 };
 return {latest,overSwitch,operations};
}
