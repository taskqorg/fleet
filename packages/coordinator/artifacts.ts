// Artifact operations (design §3, Phase 3 item 24). Bytes go straight between executors and the per-host stores; the
// coordinator holds rows, chooses stores, presigns, and promotes a staging object to its content-hash key only after the
// store host's executor has re-hashed it over loopback (P3-D1). A verified object is never overwritten.
import {randomUUID} from 'node:crypto';
import type {Readable} from 'node:stream';
import {PutObjectCommand,S3Client,CreateMultipartUploadCommand,UploadPartCommand,CompleteMultipartUploadCommand,AbortMultipartUploadCommand,HeadObjectCommand,GetObjectCommand,CopyObjectCommand,UploadPartCopyCommand,DeleteObjectCommand} from '@aws-sdk/client-s3';
import {getSignedUrl} from '@aws-sdk/s3-request-presigner';
import type {Store} from './store.js';
import {type Stores,type StoreRow,allowedTunnel} from './stores.js';
const fail=(code:number,message:string,extra:any={}):never=>{throw Object.assign(Error(message),{statusCode:code,...extra})};
export const PART_SIZE=32*1024*1024;
const COPY_LIMIT=5*1024*1024*1024,COPY_PART=1024*1024*1024,URL_SECONDS=900,LEASE_MS=3600000;
const sha=(v:any)=>typeof v==='string'&&/^[a-f0-9]{64}$/.test(v)?v:fail(400,'Expected sha256');
const name=(v:any)=>typeof v==='string'&&v.length>0&&v.length<=1000&&!v.split('/').some((p:string)=>p===''||p==='.'||p==='..')?v:fail(400,'Invalid artifact name');
const projectKey=(projectId:string|null)=>projectId??'_';
export type ArtifactRow={id:string,project_id:string|null,scope:'lab'|'prod',content_hash:string,size:number,media_type:string,name:string,execution_id:string|null,task_id:string|null,required:boolean,state:string,legacy_id:string|null,role:string,derived_from_artifact_id:string|null,previous_artifact_id:string|null,last_error:string|null,created_at:any,seq:any};

export function artifactModel(store:Store,deps:{stores:Stores,locations:(projectId:string|null)=>string[],access:()=>{id:string,secret:string}|null,onSettled?:(executionId:string)=>Promise<void>,queueCommand:(hostId:string,kind:string,payload:any)=>Promise<void>}){
 // One S3 client per store and route. Tunnel requests get the Access headers after signing, so they are never signed headers.
 const clients=new Map<string,S3Client>();
 const s3=(s:StoreRow,via:'tunnel'|'local'='tunnel')=>{const k=[s.id,via,s.endpoint,s.local_endpoint].join('|');let c=clients.get(k);if(c)return c;
  const cred=deps.stores.credentials(s.id);
  c=new S3Client({endpoint:via==='local'&&s.local_endpoint?s.local_endpoint:s.endpoint,region:'garage',forcePathStyle:true,credentials:cred,requestChecksumCalculation:'WHEN_REQUIRED',responseChecksumValidation:'WHEN_REQUIRED'});
  // Access headers only to an allowed tunnel host, never to loopback or an unlisted host.
  if(via==='tunnel'&&allowedTunnel(s.endpoint,deps.stores.endpointHosts))c.middlewareStack.addRelativeTo((next:any)=>async(args:any)=>{const a=deps.access();if(a&&args.request?.headers){args.request.headers['cf-access-client-id']=a.id;args.request.headers['cf-access-client-secret']=a.secret}return next(args)},{relation:'after',toMiddleware:'httpSigningMiddleware',name:'fleetCloudflareAccess',override:true});
  clients.set(k,c);return c};
 // URLs for a host: the store's loopback endpoint when that host is the store's own, its tunnel otherwise (store protocol rules).
 const viaFor=(s:StoreRow,hostId:string)=>hostId===s.host_id&&s.local_endpoint?'local' as const:'tunnel' as const;
 const presign=(s:StoreRow,hostId:string,cmd:any)=>getSignedUrl(s3(s,viaFor(s,hostId)),cmd,{expiresIn:URL_SECONDS});
 const keyOf=(a:ArtifactRow)=>`${projectKey(a.project_id)}/${a.scope}/${a.content_hash}`;
 const stagingOf=(a:ArtifactRow,storeId:string)=>`${projectKey(a.project_id)}/${a.scope}/.pending/${a.id}.${storeId}`;
 const row=async(id:string):Promise<ArtifactRow>=>{const r=(await store.query('SELECT * FROM artifact WHERE id=$1 OR legacy_id=$1',[id]))[0];return r?{...r,size:Number(r.size)}:fail(404,'Unknown artifact')};
 const copy=async(artifactId:string,storeId:string)=>(await store.query('SELECT * FROM artifact_copy WHERE artifact_id=$1 AND store_id=$2',[artifactId,storeId]))[0];

 // Store selection (the coordinator decides): the writer's host when listed, else the first listed reachable store not full.
 async function select(locations:string[],hostId:string){const all=(await deps.stores.list()).filter(s=>locations.includes(s.host_id)&&s.state==='reachable');
  return all.find(s=>s.host_id===hostId)??locations.map(h=>all.find(s=>s.host_id===h)).find(Boolean)}

 async function reserve(b:{executionId:string,hostId:string,name:string,mediaType?:string,size:number,sha256:string,required?:boolean,artifactId?:string,derivedFrom?:string,role?:string,previousArtifactId?:string}){
  const artifactId=typeof b.artifactId==='string'&&/^[a-zA-Z0-9-]{8,80}$/.test(b.artifactId)?b.artifactId:randomUUID();
  const old=(await store.query('SELECT id,state FROM artifact WHERE id=$1',[artifactId]))[0];
  if(old){const c=(await store.query("SELECT store_id FROM artifact_copy WHERE artifact_id=$1 AND state IN ('pending','copying','verified') ORDER BY state LIMIT 1",[artifactId]))[0];return {artifactId,state:old.state,storeId:c?.store_id,partSize:PART_SIZE}}
  const size=Number.isSafeInteger(b.size)&&b.size>=0?b.size:fail(400,'Expected size'),hash=sha(b.sha256),file=name(b.name);
  const e=await store.executions.get(b.executionId);
  const projectId=e.projectId??null,scope=e.experimentId?'lab' as const:'prod' as const;
  if((await store.query('SELECT 1 FROM purged_hash WHERE project_id=$1 AND scope=$2 AND content_hash=$3',[projectKey(projectId),scope,hash])).length)fail(409,'Content was purged',{code:'purged'});
  const target=await select(deps.locations(projectId),b.hostId)??fail(409,'No reachable store in the project locations',{code:'no_store'});
  const media=typeof b.mediaType==='string'&&b.mediaType.length<200?b.mediaType:'application/octet-stream';
  const required=b.required===true,role=['thumbnail','poster'].includes(b.role as string)?b.role:'original';
  const insert=(state:string)=>store.query('INSERT INTO artifact(id,project_id,scope,content_hash,size,media_type,name,execution_id,task_id,required,role,derived_from_artifact_id,previous_artifact_id,state) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)',
   [artifactId,projectId,scope,hash,size,media,file,b.executionId,e.taskId??null,required,role,b.derivedFrom??null,b.previousArtifactId??null,state]);
  // The same content of this project and scope, already verified on the chosen store: nothing to upload.
  const same=(await store.query("SELECT 1 FROM artifact a JOIN artifact_copy c ON c.artifact_id=a.id WHERE a.project_id IS NOT DISTINCT FROM $1 AND a.scope=$2 AND a.content_hash=$3 AND c.store_id=$4 AND c.state='verified' LIMIT 1",[projectId,scope,hash,target.id])).length;
  if(same){await store.tx(async()=>{await insert('stored');await store.query("INSERT INTO artifact_copy(artifact_id,store_id,project_id,state,verified_at) VALUES($1,$2,$3,'verified',now())",[artifactId,target.id,projectId])});
   if(deps.onSettled)await deps.onSettled(b.executionId);return {artifactId,state:'stored',storeId:target.id}}
  const a={id:artifactId,project_id:projectId,scope,content_hash:hash} as ArtifactRow,staging=stagingOf(a,target.id);
  const up=await s3(target).send(new CreateMultipartUploadCommand({Bucket:target.bucket,Key:staging,ContentType:media}));
  await store.tx(async()=>{await insert('pending');await store.query("INSERT INTO artifact_copy(artifact_id,store_id,project_id,state,upload_id,staging_key,lease_expires_at) VALUES($1,$2,$3,'pending',$4,$5,now()+($6::text||' milliseconds')::interval)",[artifactId,target.id,projectId,up.UploadId,staging,String(LEASE_MS)])});
  return {artifactId,state:'pending',storeId:target.id,partSize:PART_SIZE}}

 // The copy row an upload or copy works on: the given store, else the artifact's one copy in progress.
 async function working(artifactId:string,storeId?:string){const c=storeId?await copy(artifactId,storeId):(await store.query("SELECT * FROM artifact_copy WHERE artifact_id=$1 AND state IN ('pending','copying') LIMIT 1",[artifactId]))[0];
  if(!c||!['pending','copying'].includes(c.state)||!c.upload_id)fail(409,'No upload in progress for this artifact');return c}
 // Only the writer (the execution's host) or the target store's host may drive an upload.
 async function allowed(a:ArtifactRow,s:StoreRow,hostId:string){if(hostId===s.host_id)return;const e=a.execution_id?(await store.query('SELECT host_id FROM execution WHERE id=$1',[a.execution_id]))[0]:null;if(e?.host_id!==hostId)fail(403,'Upload belongs to another host')}

 async function parts(b:{artifactId:string,storeId?:string,hostId:string,partNumbers:number[]}){const a=await row(b.artifactId),c=await working(a.id,b.storeId),s=await deps.stores.get(c.store_id);await allowed(a,s,b.hostId);
  const numbers=Array.isArray(b.partNumbers)?b.partNumbers.filter(n=>Number.isSafeInteger(n)&&n>=1&&n<=10000).slice(0,64):fail(400,'Expected partNumbers');
  await store.tx(()=>store.query("UPDATE artifact_copy SET lease_expires_at=now()+($3::text||' milliseconds')::interval WHERE artifact_id=$1 AND store_id=$2",[a.id,s.id,String(LEASE_MS)]));
  return {storeId:s.id,parts:await Promise.all(numbers.map(async n=>({partNumber:n,url:await presign(s,b.hostId,new UploadPartCommand({Bucket:s.bucket,Key:c.staging_key,UploadId:c.upload_id,PartNumber:n}))})))}}

 async function commit(b:{artifactId:string,storeId?:string,hostId:string,parts:{partNumber:number,etag:string}[]}){const a=await row(b.artifactId);
  if(a.state==='stored'&&!b.storeId)return {state:'stored'};
  const c=await working(a.id,b.storeId),s=await deps.stores.get(c.store_id);await allowed(a,s,b.hostId);
  const list=Array.isArray(b.parts)?b.parts.filter(p=>Number.isSafeInteger(p?.partNumber)&&typeof p.etag==='string').sort((x,y)=>x.partNumber-y.partNumber):fail(400,'Expected parts');
  await s3(s).send(new CompleteMultipartUploadCommand({Bucket:s.bucket,Key:c.staging_key,UploadId:c.upload_id,MultipartUpload:{Parts:list.map(p=>({PartNumber:p.partNumber,ETag:p.etag}))}}));
  const head=await s3(s).send(new HeadObjectCommand({Bucket:s.bucket,Key:c.staging_key}));
  if(Number(head.ContentLength)!==a.size){await reject(a,s,c.staging_key,`size ${head.ContentLength} differs from reserved ${a.size}`);return {state:'failed'}}
  await store.tx(()=>store.query("UPDATE artifact_copy SET upload_id=NULL,lease_expires_at=now()+($3::text||' milliseconds')::interval WHERE artifact_id=$1 AND store_id=$2",[a.id,s.id,String(LEASE_MS)]));
  // P3-D1: the store host's executor re-hashes the staging object over loopback. When the writer is that host it does so now.
  const verifyUrl=await getSignedUrl(s3(s,s.local_endpoint?'local':'tunnel'),new GetObjectCommand({Bucket:s.bucket,Key:c.staging_key}),{expiresIn:URL_SECONDS});
  if(b.hostId===s.host_id)return {state:'verifying',verify:{artifactId:a.id,storeId:s.id,url:verifyUrl}};
  await deps.queueCommand(s.host_id,'artifact-verify',{artifactId:a.id,storeId:s.id,url:verifyUrl});return {state:'verifying'}}

 async function reject(a:ArtifactRow,s:StoreRow,key:string,why:string){await s3(s).send(new DeleteObjectCommand({Bucket:s.bucket,Key:key})).catch(()=>{});
  await store.tx(async()=>{await store.query("UPDATE artifact_copy SET state='failed',last_error=$3,upload_id=NULL,lease_expires_at=NULL WHERE artifact_id=$1 AND store_id=$2",[a.id,s.id,why]);
   await store.query("UPDATE artifact SET state='failed',last_error=$2 WHERE id=$1 AND state='pending' AND NOT EXISTS (SELECT 1 FROM artifact_copy WHERE artifact_id=$1 AND state='verified')",[a.id,why])});
  if(a.execution_id&&deps.onSettled)await deps.onSettled(a.execution_id)}

 // Promotion: the staging object becomes the content-hash key only when that key is absent; the staging key is then removed.
 async function verified(b:{artifactId:string,storeId:string,hostId:string,sha256:string}){const a=await row(b.artifactId),s=await deps.stores.get(b.storeId);
  if(b.hostId!==s.host_id)fail(403,'Only the store host verifies its copies');const c=await copy(a.id,s.id);if(!c||!['pending','copying'].includes(c.state))return {state:c?.state??'unknown'};
  if(b.sha256!==a.content_hash){await reject(a,s,c.staging_key,'content hash differs from the reserved hash');return {state:'failed'}}
  const final=keyOf(a);const exists=await s3(s).send(new HeadObjectCommand({Bucket:s.bucket,Key:final})).then(()=>true,(e:any)=>e?.$metadata?.httpStatusCode===404||e?.name==='NotFound'?false:Promise.reject(e));
  if(!exists){if(a.size<=COPY_LIMIT)await s3(s).send(new CopyObjectCommand({Bucket:s.bucket,Key:final,CopySource:`${s.bucket}/${c.staging_key}`}));
   else{const up=await s3(s).send(new CreateMultipartUploadCommand({Bucket:s.bucket,Key:final}));const done:any[]=[];
    for(let i=0,n=1;i<a.size;i+=COPY_PART,n++){const r=await s3(s).send(new UploadPartCopyCommand({Bucket:s.bucket,Key:final,UploadId:up.UploadId,PartNumber:n,CopySource:`${s.bucket}/${c.staging_key}`,CopySourceRange:`bytes=${i}-${Math.min(a.size,i+COPY_PART)-1}`}));done.push({PartNumber:n,ETag:r.CopyPartResult?.ETag})}
    await s3(s).send(new CompleteMultipartUploadCommand({Bucket:s.bucket,Key:final,UploadId:up.UploadId,MultipartUpload:{Parts:done}}))}}
  await s3(s).send(new DeleteObjectCommand({Bucket:s.bucket,Key:c.staging_key}));
  await store.tx(async()=>{await store.query("UPDATE artifact_copy SET state='verified',verified_at=now(),upload_id=NULL,staging_key=NULL,lease_expires_at=NULL,last_error=NULL WHERE artifact_id=$1 AND store_id=$2",[a.id,s.id]);
   await store.query("UPDATE artifact SET state='stored',last_error=NULL WHERE id=$1 AND state='pending'",[a.id])});
  if(a.execution_id&&deps.onSettled)await deps.onSettled(a.execution_id);return {state:'stored'}}

 // A verified copy to read from: the reader host's own store first, then any reachable store.
 async function source(a:ArtifactRow,hostId?:string){const copies=await store.query("SELECT c.store_id FROM artifact_copy c WHERE c.artifact_id=$1 AND c.state='verified'",[a.id]);
  // A store with a purge still pending serves nothing until the purge is reconciled.
  const blocked=new Set((await store.query("SELECT DISTINCT store_id FROM artifact_copy WHERE state='purge_pending'")).map((r:any)=>r.store_id));
  const stores=(await deps.stores.list()).filter(s=>copies.some((c:any)=>c.store_id===s.id)&&s.state!=='unreachable'&&!blocked.has(s.id));
  return stores.find(s=>s.host_id===hostId)??stores[0]}
 // Coordinator reads of the bytes (delivery results, website sources, differences): streamed from a verified copy.
 async function read(id:string):Promise<Readable>{const legacyDoc=await legacyDocument(id);if(legacyDoc)return (await import('node:stream')).Readable.from([Buffer.from(legacyDoc.contentBase64??'','base64')]);const a=await row(id);if(a.state!=='stored')fail(409,'Artifact not stored');const s=await source(a)??fail(503,'Artifact unavailable: no reachable store holds a verified copy');
  return (await s3(s).send(new GetObjectCommand({Bucket:s.bucket,Key:keyOf(a)}))).Body as Readable}
 // Coordinator code reads bounded content only: an artifact larger than maxBytes is refused before any byte is read.
 const readBuffer=async(id:string,maxBytes:number)=>{const doc=await legacyDocument(id),size=doc?Number(doc.size??0):(await row(id)).size;if(size>maxBytes)fail(413,`Artifact larger than ${maxBytes} bytes`);
  const chunks:Buffer[]=[];let n=0;for await(const c of await read(id)){n+=c.length;if(n>maxBytes)fail(413,`Artifact larger than ${maxBytes} bytes`);chunks.push(Buffer.from(c))}return Buffer.concat(chunks)};
 const view=(a:ArtifactRow)=>({id:a.id,legacyId:a.legacy_id,projectId:a.project_id,scope:a.scope,executionId:a.execution_id,taskId:a.task_id,name:a.name,mediaType:a.media_type,size:a.size,sha256:a.content_hash,required:a.required,role:a.role,derivedFromArtifactId:a.derived_from_artifact_id,previousArtifactId:a.previous_artifact_id,state:a.state,error:a.last_error});
 // Metadata and a presigned GET for the given host (§3 Reading); unavailable when no reachable store holds a verified copy.
 async function get(id:string,hostId?:string){const legacyDoc=await legacyDocument(id);if(legacyDoc)return {id:legacyDoc.id,legacyId:legacyDoc.id,projectId:null as string|null,executionId:legacyDoc.executionId,name:legacyDoc.path,mediaType:legacyDoc.mediaType,size:legacyDoc.size,sha256:legacyDoc.sha256,state:'stored',availability:'available',copies:[]};const a=await row(id),copies=await store.query('SELECT store_id,state,verified_at FROM artifact_copy WHERE artifact_id=$1 ORDER BY store_id',[a.id]);
  const s=a.state==='stored'?await source(a,hostId):undefined;
  return {...view(a),copies:copies.map((c:any)=>({storeId:c.store_id,state:c.state})),availability:a.state!=='stored'?a.state:s?'available':'unavailable',...(s?{url:await presign(s,hostId??'',new GetObjectCommand({Bucket:s.bucket,Key:keyOf(a),ResponseContentType:a.media_type}))}:{})}}
 async function list(f:{executionId?:string,taskId?:string,mediaType?:string,projectId?:string}={}){const c:string[]=[],p:any[]=[];
  for(const [k,col] of [['executionId','execution_id'],['taskId','task_id'],['mediaType','media_type'],['projectId','project_id']] as const)if(typeof f[k]==='string'){p.push(f[k]);c.push(`${col}=$${p.length}`)}
  return (await store.query(`SELECT * FROM artifact${c.length?' WHERE '+c.join(' AND '):''} ORDER BY seq`,p)).map((r:any)=>view({...r,size:Number(r.size)}))}

 // Forced purge (operator): every store, now where reachable, pending where not; the hash is refused from then on.
 async function purge(b:{contentHash:string,projectId?:string|null,scope?:string,reason:string}){const hash=sha(b.contentHash),reason=typeof b.reason==='string'&&b.reason?b.reason.slice(0,500):fail(400,'Expected reason');
  const rows=await store.query('SELECT * FROM artifact WHERE content_hash=$1 AND ($2::text IS NULL OR project_id IS NOT DISTINCT FROM $2) AND ($3::text IS NULL OR scope=$3)',[hash,b.projectId??null,b.scope??null]);
  const stores=await deps.stores.list();let deleted=0,pending=0;
  for(const a of rows){await store.tx(()=>store.query("INSERT INTO purged_hash(project_id,scope,content_hash,reason) VALUES($1,$2,$3,$4) ON CONFLICT DO NOTHING",[projectKey(a.project_id),a.scope,hash,reason]));
   for(const c of await store.query("SELECT * FROM artifact_copy WHERE artifact_id=$1 AND state<>'deleted'",[a.id])){const s=stores.find(x=>x.id===c.store_id);
    if(s&&s.state!=='unreachable'){await s3(s).send(new DeleteObjectCommand({Bucket:s.bucket,Key:keyOf({...a,size:Number(a.size)})})).catch(()=>{});if(c.staging_key)await s3(s).send(new DeleteObjectCommand({Bucket:s.bucket,Key:c.staging_key})).catch(()=>{});
     await store.tx(()=>store.query("UPDATE artifact_copy SET state='deleted' WHERE artifact_id=$1 AND store_id=$2",[a.id,c.store_id]));deleted++}
    else{await store.tx(()=>store.query("UPDATE artifact_copy SET state='purge_pending' WHERE artifact_id=$1 AND store_id=$2",[a.id,c.store_id]));pending++}}
   await store.tx(()=>store.query("UPDATE artifact SET state='deleted',deleted_at=now(),deleted_by=$2 WHERE id=$1",[a.id,'purge:'+reason]))}
  return {artifacts:rows.length,deleted,pending}}

 // Reservations whose lease ran out: the upload is aborted and the copy fails (the artifact too, when it has no other copy).
 async function expireLeases(){const stale=await store.query("SELECT * FROM artifact_copy WHERE state IN ('pending','copying') AND lease_expires_at < now()");const stores=await deps.stores.list();
  for(const c of stale){const s=stores.find(x=>x.id===c.store_id),a=await row(c.artifact_id);if(s&&c.upload_id&&s.state!=='unreachable')await s3(s).send(new AbortMultipartUploadCommand({Bucket:s.bucket,Key:c.staging_key,UploadId:c.upload_id})).catch(()=>{});
   await reject(a,s??({} as StoreRow),c.staging_key??'','upload lease expired').catch(async()=>{await store.tx(()=>store.query("UPDATE artifact_copy SET state='failed',last_error='upload lease expired' WHERE artifact_id=$1 AND store_id=$2",[c.artifact_id,c.store_id]))})}
  return stale.length}

 // Required artifacts of an execution (P3-D3): the task's completion_requirements.artifacts (names or glob patterns) set the
 // minimum; artifacts saved with required=true add to it.
 async function requirement(executionId:string,declared:string[]=[]){const e=(await store.query('SELECT task_id FROM execution WHERE id=$1',[executionId]))[0];
  const patterns:string[]=e?.task_id?((await store.query('SELECT completion_requirements FROM task WHERE id=$1',[e.task_id]))[0]?.completion_requirements?.artifacts??[]).filter((x:any)=>typeof x==='string'):[];
  const rows:ArtifactRow[]=await store.query("SELECT * FROM artifact WHERE execution_id=$1 AND state<>'deleted' AND role='original' ORDER BY seq",[executionId]);
  const glob=(p:string)=>new RegExp('^'+p.replace(/[.+^${}()|[\]\\]/g,'\\$&').replace(/\*\*/g,'\u0000').replace(/\*/g,'[^/]*').replace(/\u0000/g,'.*').replace(/\?/g,'[^/]')+'$');
  const needed=rows.filter(r=>r.required||patterns.some(p=>glob(p).test(r.name)));
  // Names the executor reported as collected but not yet reserved count as pending, not missing.
  const unreserved=declared.filter(n=>typeof n==='string'&&!rows.some(r=>r.name===n)&&patterns.some(p=>glob(p).test(n)));
  const missing=patterns.filter(p=>!rows.some(r=>glob(p).test(r.name))&&!unreserved.some(n=>glob(p).test(n)));
  return {pending:[...needed.filter(r=>r.state==='pending').map(r=>r.name),...unreserved],failed:needed.filter(r=>r.state==='failed').map(r=>({name:r.name,error:r.last_error})),missing}}

 // Legacy record shape for the existing readers of kind 'artifact' (session results, lab trials, recovery, delegation).
 const legacy=(r:any)=>({id:r.legacy_id??r.id,artifactId:r.id,executionId:r.execution_id,sessionId:r.session_id??undefined,path:r.name,mediaType:r.media_type,size:Number(r.size),sha256:r.content_hash});
 // Base64 documents not yet moved by the backfill keep serving their readers (metadata here, bytes through read()).
 const legacyDocument=async(id:string)=>(await store.query("SELECT data FROM document WHERE kind='artifact' AND id=$1",[id]))[0]?.data;
 const legacySelect=async(where:string,params:any[],docWhere='',docParams:any[]=[])=>{const rows=(await store.query(`SELECT a.*,e.session_id,a.seq AS order_seq FROM artifact a LEFT JOIN execution e ON e.id=a.execution_id WHERE a.state='stored' AND a.role='original'${where}`,params)).map((r:any)=>({...legacy(r),order:Number(r.order_seq)}));
  const docs=(await store.query(`SELECT data - 'contentBase64' AS data,seq FROM document WHERE kind='artifact'${docWhere}`,docParams)).map((r:any)=>({...r.data,artifactId:r.data.id,order:Number(r.seq)}));
  return [...rows,...docs].sort((x,y)=>x.order-y.order).map(({order,...r})=>r)};
 const adapter={
  get:async(id:string)=>(await legacySelect(' AND (a.id=$1 OR a.legacy_id=$1)',[id],' AND id=$1',[id]))[0]??fail(404,'Not found'),
  // A legacy record (seed data, migration tools) is kept as a document; the backfill moves it to a store.
  put:async(v:any)=>{await store.query("INSERT INTO document(kind,id,data,project_id) VALUES('artifact',$1,$2,$3) ON CONFLICT(id) DO UPDATE SET data=excluded.data WHERE document.kind='artifact'",[v.id,JSON.stringify(v),typeof v.projectId==='string'?v.projectId:null])},
  all:async(f:any={})=>{const c:string[]=[],p:any[]=[];for(const [k,col] of [['executionId','a.execution_id'],['sessionId','e.session_id'],['path','a.name']] as const)if(typeof f?.[k]==='string'){p.push(f[k]);c.push(` AND ${col}=$${p.length}`)}
   if(typeof f?.id==='string'){p.push(f.id);c.push(` AND (a.id=$${p.length} OR a.legacy_id=$${p.length})`)}
   const d:string[]=[],dp:any[]=[];for(const k of ['executionId','sessionId','path','id'])if(typeof f?.[k]==='string'){dp.push(f[k]);d.push(k==='id'?` AND id=$${dp.length}`:` AND data->>'${k}'=$${dp.length}`)}
   return legacySelect(c.join(''),p,d.join(''),dp)},
  many:async(ids:string[])=>new Map((await legacySelect(' AND (a.id=ANY($1) OR a.legacy_id=ANY($1))',[ids],' AND id=ANY($1)',[ids])).map(r=>[r.id,r])),
 };
 // Backfill (Phase 3 item 26c): base64 documents move to a store of their project's locations, one at a time, idempotently.
 // The coordinator computed the hash itself, so the object is written straight to its content key when that key is absent.
 async function backfill(limit=50){let moved=0;
  for(const r of await store.query("SELECT data FROM document WHERE kind='artifact' ORDER BY seq LIMIT $1",[limit])){const d=r.data,bytes=Buffer.from(d.contentBase64??'','base64');
   const e=d.executionId?await store.executions.get(d.executionId).catch(()=>null):null,projectId=e?.projectId??null,scope=e?.experimentId?'lab' as const:'prod' as const;
   const target=await select(deps.locations(projectId),'');if(!target)break;
   const hash=(await import('node:crypto')).createHash('sha256').update(bytes).digest('hex'),a={project_id:projectId,scope,content_hash:hash} as ArtifactRow,key=keyOf(a);
   const exists=await s3(target).send(new HeadObjectCommand({Bucket:target.bucket,Key:key})).then(()=>true,()=>false);if(!exists)await s3(target).send(new PutObjectCommand({Bucket:target.bucket,Key:key,Body:bytes,ContentType:d.mediaType??'application/octet-stream'}));
   const id=randomUUID();await store.tx(async()=>{await store.query("INSERT INTO artifact(id,project_id,scope,content_hash,size,media_type,name,execution_id,task_id,state,legacy_id) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,'stored',$10) ON CONFLICT(legacy_id) DO NOTHING",[id,projectId,scope,hash,bytes.length,d.mediaType??'application/octet-stream',d.path??d.id,d.executionId??null,e?.taskId??null,d.id]);
    await store.query("INSERT INTO artifact_copy(artifact_id,store_id,project_id,state,verified_at) SELECT id,$2,$3,'verified',now() FROM artifact WHERE legacy_id=$1 ON CONFLICT DO NOTHING",[d.id,target.id,projectId]);
    await store.query("DELETE FROM document WHERE kind='artifact' AND id=$1",[d.id])});moved++}
  return moved}
 return {backfill,reserve,parts,commit,verified,read,readBuffer,get,list,purge,expireLeases,requirement,adapter,s3,presign,keyOf,stagingOf,row,select,PART_SIZE};
}
export type Artifacts=ReturnType<typeof artifactModel>;
