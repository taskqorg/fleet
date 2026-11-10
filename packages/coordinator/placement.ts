// Placement worker (design §3, Phase 3 item 25): every stored artifact gets a verified copy on each reachable store of its
// project's artifact_locations, through copy commands to the target store's own executor; copies outside the list are deleted
// only behind a surviving verified copy, never the last one; pending purges are reconciled first; confirmed retention rules
// run through the same copy-then-delete path.
import {createHash} from 'node:crypto';
import {CreateMultipartUploadCommand,DeleteObjectCommand,GetObjectCommand,HeadObjectCommand} from '@aws-sdk/client-s3';
import {getSignedUrl} from '@aws-sdk/s3-request-presigner';
import type {Store} from './store.ts';
import type {Stores} from './stores.ts';
import type {Artifacts} from './artifacts.ts';
const fail=(code:number,message:string):never=>{throw Object.assign(Error(message),{statusCode:code})};
export type Rule={scope:'lab'|'prod',older_than_days:number,action:'move'|'delete',to?:string[]};
export const ruleHash=(r:Rule)=>createHash('sha256').update(JSON.stringify({scope:r.scope,older_than_days:r.older_than_days,action:r.action,to:r.to??null})).digest('hex');
// storeHosts: hosts with registered stores; omitted where stores are not known (project settings load checks the shape only).
export function validRules(rules:any,storeHosts?:Set<string>):Rule[]{if(rules===undefined)return [];if(!Array.isArray(rules))fail(400,'artifact_rules must be a list');
 return rules.map((r:any)=>{if(!r||!['lab','prod'].includes(r.scope)||!Number.isSafeInteger(r.older_than_days)||r.older_than_days<0||!['move','delete'].includes(r.action))fail(400,'Invalid artifact rule');
  if(r.action==='move'&&(!Array.isArray(r.to)||!r.to.length||r.to.some((h:any)=>typeof h!=='string'||storeHosts&&!storeHosts.has(h))))fail(400,'A move rule needs to: hosts with registered stores');
  if(r.action==='delete'&&r.to!==undefined)fail(400,'A delete rule takes no to');return {scope:r.scope,older_than_days:r.older_than_days,action:r.action,...(r.to?{to:r.to}:{})}})}

export function placementWorker(store:Store,deps:{stores:Stores,artifacts:Artifacts,locations:(projectId:string|null)=>string[],rules:(projectId:string|null)=>Rule[],queueCommand:(hostId:string,kind:string,payload:any)=>Promise<void>,retryWindowMs:()=>number,onExpired?:(executionId:string)=>Promise<void>}){
 const A=deps.artifacts;
 // Problems of the last pass (shown by stores_list): invalid rules (skipped, never crash the pass) and artifacts it could not place.
 let problems:any[]=[];
 // Valid rules of a project; each invalid rule is reported and ignored.
 const rulesOf=(projectId:string|null,hosts:Set<string>)=>{const out:Rule[]=[];const raw=deps.rules(projectId);
  for(const [i,r] of (Array.isArray(raw)?raw:[raw]).entries()){try{out.push(...validRules([r],hosts))}catch(e:any){problems.push({kind:'artifact-rule-invalid',projectId,rule:i,error:e.message})}}return out};
 // Rule in effect for an artifact: a confirmed move rule that matches replaces the project list for it; a delete rule ends it.
 async function effective(a:any,hosts:Set<string>){const rules=rulesOf(a.project_id,hosts),age=(Date.now()-new Date(a.created_at).getTime())/86400000;
  for(const r of rules.filter(r=>r.scope===a.scope&&age>=r.older_than_days).sort((x,y)=>y.older_than_days-x.older_than_days)){
   if(!(await store.query('SELECT 1 FROM artifact_rule_confirmation WHERE project_id=$1 AND rule_hash=$2',[a.project_id??'_',ruleHash(r)])).length)continue;
   return r.action==='delete'?{delete:r}:{locations:r.to!}}
  return {locations:deps.locations(a.project_id)}}

 // One pass. Store calls run between short transactions (§12).
 async function runPlacement(){const stores=await deps.stores.list(),byHost=new Map(stores.map(s=>[s.host_id,s])),hosts=new Set(stores.map(s=>s.host_id));let queued=0,deleted=0;
  problems=[];
  await reconcilePurges();
  for(const a of await store.query("SELECT * FROM artifact WHERE state='stored' ORDER BY seq"))try{a.size=Number(a.size);
   const copies=await store.query('SELECT *,(next_attempt_at IS NULL OR next_attempt_at<=now()) AS due FROM artifact_copy WHERE artifact_id=$1',[a.id]),verified=copies.filter((c:any)=>c.state==='verified');
   const plan=await effective(a,hosts);
   if('delete' in plan){await deleteArtifact(a,`rule:${ruleHash(plan.delete!).slice(0,12)}`);continue}
   const wanted=plan.locations.map(h=>byHost.get(h)).filter(Boolean) as typeof stores;
   for(const s of wanted){if(s.state!=='reachable')continue;const c=copies.find((x:any)=>x.store_id===s.id);if(c&&['verified','pending','copying','purge_pending'].includes(c.state))continue;
    // A failed copy waits for its backoff (or an operator retry); its failure stays visible meanwhile.
    if(c?.state==='failed'){problems.push({kind:'artifact-copy-failed',artifactId:a.id,storeId:s.id,error:c.last_error,attempts:c.attempts,nextAttemptAt:c.next_attempt_at,actions:['artifact_copy_retry']});if(!c.due)continue}
    const from=verified.map((v:any)=>stores.find(x=>x.id===v.store_id)).find(x=>x&&x.state!=='unreachable');if(!from)continue;
    const staging=A.stagingOf(a,s.id),up=await A.s3(s).send(new CreateMultipartUploadCommand({Bucket:s.bucket,Key:staging,ContentType:a.media_type}));
    await store.tx(()=>store.query(`INSERT INTO artifact_copy(artifact_id,store_id,project_id,state,upload_id,staging_key,lease_expires_at) VALUES($1,$2,$3,'copying',$4,$5,now()+interval '6 hours')
     ON CONFLICT(artifact_id,store_id) DO UPDATE SET state='copying',next_attempt_at=NULL,upload_id=excluded.upload_id,staging_key=excluded.staging_key,lease_expires_at=excluded.lease_expires_at,last_error=NULL`,[a.id,s.id,a.project_id,up.UploadId,staging]));
    const url=await getSignedUrl(A.s3(from,s.host_id===from.host_id&&from.local_endpoint?'local':'tunnel'),new GetObjectCommand({Bucket:from.bucket,Key:A.keyOf(a)}),{expiresIn:6*3600});
    await deps.queueCommand(s.host_id,'artifact-copy',{artifactId:a.id,storeId:s.id,size:a.size,partSize:A.PART_SIZE,source:{storeId:from.id,url}});queued++}
   // Copies on stores outside the list: delete only behind a verified copy on a reachable listed store that HEAD confirms.
   for(const c of verified){const s=stores.find(x=>x.id===c.store_id);if(!s||plan.locations.includes(s.host_id))continue;
    if(await removeCopy(a,s,verified.filter((v:any)=>v.store_id!==s.id).map((v:any)=>stores.find(x=>x.id===v.store_id)).filter((x:any)=>x&&plan.locations.includes(x.host_id))))deleted++}}
  catch(e:any){problems.push({kind:'artifact-placement-error',artifactId:a.id,error:String(e?.message??e)})}
  await A.expireLeases();await expireWaiting();
  return {queued,deleted}}

 // Deletes the object on a store only when no other stored row of the same project, scope and hash needs that store.
 async function removeCopy(a:any,s:any,survivors:any[]){const alive=[];for(const x of survivors){if(x.state==='unreachable')continue;
   const ok=await A.s3(x).send(new HeadObjectCommand({Bucket:x.bucket,Key:A.keyOf(a)})).then(h=>Number(h.ContentLength)===a.size,()=>false);if(ok)alive.push(x)}
  if(!alive.length)return false;
  const needed=(await store.query("SELECT a.project_id FROM artifact a JOIN artifact_copy c ON c.artifact_id=a.id WHERE a.id<>$1 AND a.project_id IS NOT DISTINCT FROM $2 AND a.scope=$3 AND a.content_hash=$4 AND a.state='stored' AND c.store_id=$5 AND c.state='verified'",[a.id,a.project_id,a.scope,a.content_hash,s.id])).some((r:any)=>deps.locations(r.project_id).includes(s.host_id));
  if(!needed)await A.s3(s).send(new DeleteObjectCommand({Bucket:s.bucket,Key:A.keyOf(a)}));
  await store.tx(()=>store.query("UPDATE artifact_copy SET state='deleted' WHERE artifact_id=$1 AND store_id=$2",[a.id,s.id]));return true}

 // A delete rule ends the artifact: every copy goes (objects only where no other stored row needs them), the row stays a tombstone.
 async function deleteArtifact(a:any,by:string){const stores=await deps.stores.list();
  if((await store.query("SELECT 1 FROM artifact_copy c JOIN store s ON s.id=c.store_id WHERE c.artifact_id=$1 AND c.state='verified' AND s.state='unreachable' LIMIT 1",[a.id])).length)return;
  for(const c of await store.query("SELECT * FROM artifact_copy WHERE artifact_id=$1 AND state='verified'",[a.id])){const s=stores.find(x=>x.id===c.store_id)!;
   const shared=(await store.query("SELECT 1 FROM artifact x JOIN artifact_copy y ON y.artifact_id=x.id WHERE x.id<>$1 AND x.project_id IS NOT DISTINCT FROM $2 AND x.scope=$3 AND x.content_hash=$4 AND x.state='stored' AND y.store_id=$5 AND y.state='verified' LIMIT 1",[a.id,a.project_id,a.scope,a.content_hash,s.id])).length;
   if(!shared)await A.s3(s).send(new DeleteObjectCommand({Bucket:s.bucket,Key:A.keyOf(a)}));
   await store.tx(()=>store.query("UPDATE artifact_copy SET state='deleted' WHERE artifact_id=$1 AND store_id=$2",[a.id,s.id]))}
  await store.tx(()=>store.query("UPDATE artifact SET state='deleted',deleted_at=now(),deleted_by=$2 WHERE id=$1",[a.id,by]))}

 // Pending purges: deleted as soon as their store is reachable again, before it serves anything.
 async function reconcilePurges(){const stores=await deps.stores.list();
  for(const c of await store.query("SELECT c.*,a.project_id AS p,a.scope,a.content_hash,a.size FROM artifact_copy c JOIN artifact a ON a.id=c.artifact_id WHERE c.state='purge_pending'")){const s=stores.find(x=>x.id===c.store_id);if(!s||s.state==='unreachable')continue;
   await A.s3(s).send(new DeleteObjectCommand({Bucket:s.bucket,Key:A.keyOf({project_id:c.p,scope:c.scope,content_hash:c.content_hash} as any)})).catch(()=>{});
   await store.tx(()=>store.query("UPDATE artifact_copy SET state='deleted' WHERE artifact_id=$1 AND store_id=$2",[c.artifact_id,c.store_id]))}}

 // Executions waiting for their artifacts longer than artifact_retry_window fail (P3-D2).
 async function expireWaiting(){const rows=await store.query("SELECT id FROM execution WHERE legacy_kind='execution' AND data->>'state'='waiting' AND data->>'waitReason'='artifacts' AND coalesce((data->>'waitingSince')::bigint,0) < $1",[Date.now()-deps.retryWindowMs()]);
  for(const r of rows)if(deps.onExpired)await deps.onExpired(r.id)}

 // Retention rules: the operator previews the effect, then confirms; editing a rule changes its hash and needs a new confirmation.
 async function previewRule(projectId:string|null,rule:Rule){const rows=await store.query("SELECT id,name,size FROM artifact WHERE state='stored' AND project_id IS NOT DISTINCT FROM $1 AND scope=$2 AND created_at <= now() - ($3::text||' days')::interval ORDER BY seq",[projectId,rule.scope,String(rule.older_than_days)]);
  return {ruleHash:ruleHash(rule),count:rows.length,bytes:rows.reduce((n:number,r:any)=>n+Number(r.size),0),sample:rows.slice(0,10).map((r:any)=>({id:r.id,name:r.name,size:Number(r.size)}))}}
 // An operator retry of a failed copy: placement queues it on its next pass.
 async function retryCopy(artifactId:string,storeId:string){const r=await store.tx(()=>store.query("UPDATE artifact_copy SET next_attempt_at=now(),attempts=0 WHERE artifact_id=$1 AND store_id=$2 AND state='failed' RETURNING artifact_id",[artifactId,storeId]));
  return r.length?{retrying:true}:fail(409,'No failed copy for this artifact and store')}
 async function confirmRule(projectId:string|null,rule:Rule,by:string){validRules([rule],new Set((await deps.stores.list()).map(s=>s.host_id)));if(!deps.rules(projectId).some(r=>ruleHash(r)===ruleHash(rule)))fail(404,'Rule is not in the project settings');
  await store.tx(()=>store.query('INSERT INTO artifact_rule_confirmation(project_id,rule_hash,confirmed_by) VALUES($1,$2,$3) ON CONFLICT(project_id,rule_hash) DO UPDATE SET confirmed_by=excluded.confirmed_by,confirmed_at=now()',[projectId??'_',ruleHash(rule),by]));return {ruleHash:ruleHash(rule),confirmed:true}}
 return {runPlacement,previewRule,confirmRule,retryCopy,reconcilePurges,problems:()=>problems};
}
export type Placement=ReturnType<typeof placementWorker>;
