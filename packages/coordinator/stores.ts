// Store registry (design §3, Phase 3 item 22). A store's own executor registers it through the outbox; the coordinator keeps
// the S3 key of its role in a 0600 file under secretsDir, never in rows. Each poll reports reachability and capacity.
import {mkdirSync,readFileSync,writeFileSync,chmodSync,existsSync} from 'node:fs';
import {join} from 'node:path';
import type {Store} from './store.js';
const fail=(code:number,message:string):never=>{throw Object.assign(Error(message),{statusCode:code})};
const id=(v:any,name:string)=>typeof v==='string'&&/^[a-z0-9][a-z0-9-]{0,62}$/.test(v)?v:fail(400,'Invalid '+name);
const url=(v:any,name:string)=>{try{const u=new URL(v);if(!['http:','https:'].includes(u.protocol))throw 0;return u.toString().replace(/\/$/,'')}catch{return fail(400,'Invalid '+name)}};
const LOOPBACK=new Set(['127.0.0.1','localhost','[::1]','::1']);
export const isLoopback=(u:string)=>LOOPBACK.has(new URL(u).hostname);
// Tunnel endpoints must be on an operator-allowed host (FLEET_STORE_ENDPOINT_HOSTS: exact hosts or .suffixes); a registering
// host cannot point the coordinator, its S3 signatures or the Access token at anything else.
export const allowedTunnel=(u:string,hosts:string[])=>{const h=new URL(u).hostname;return hosts.some(x=>x.startsWith('.')?h.endsWith(x):h===x)};
export type StoreRow={id:string,host_id:string,bucket:string,endpoint:string,local_endpoint:string|null,state:string,capacity_bytes:number|null,used_bytes:number|null,free_space_warning_bytes:number|null,requires_logon:boolean,zone:string|null};
// Cloudflare Access service token from an env file (CF_ACCESS_CLIENT_ID, CF_ACCESS_CLIENT_SECRET), read when used.
export function accessToken(file?:string):{id:string,secret:string}|null{if(!file||!existsSync(file))return null;
 const v=Object.fromEntries(readFileSync(file,'utf8').split('\n').map(l=>l.replace(/^export /,'').trim()).filter(l=>l.includes('=')).map(l=>{const i=l.indexOf('=');return [l.slice(0,i),l.slice(i+1).replace(/^['"]|['"]$/g,'')]}));
 return v.CF_ACCESS_CLIENT_ID&&v.CF_ACCESS_CLIENT_SECRET?{id:v.CF_ACCESS_CLIENT_ID,secret:v.CF_ACCESS_CLIENT_SECRET}:null}
export function storeRegistry(store:Store,config:{secretsDir:string,unseenMs?:number,endpointHosts?:string[]}){
 const unseen=config.unseenMs??120000;
 const keyFile=(storeId:string)=>join(config.secretsDir,storeId+'.key');
 // Registration: only the executor of the store's host may create or replace it.
 async function register(hostId:string,b:any){const storeId=id(b?.id,'store id'),bucket=id(b?.bucket,'bucket');
  if(typeof b.accessKeyId!=='string'||!b.accessKeyId||typeof b.secretAccessKey!=='string'||!b.secretAccessKey)fail(400,'Expected store credentials');
  const endpoint=url(b.endpoint,'endpoint'),local=b.localEndpoint?url(b.localEndpoint,'localEndpoint'):null,hosts=config.endpointHosts??[];
  if(!allowedTunnel(endpoint,hosts))fail(403,'Store endpoint host is not allowed');if(local&&!isLoopback(local))fail(400,'localEndpoint must be a loopback URL');
  const row=(await store.query('SELECT host_id FROM store WHERE id=$1',[storeId]))[0];if(row&&row.host_id!==hostId)fail(403,'Store belongs to another host');
  mkdirSync(config.secretsDir,{recursive:true,mode:0o700});writeFileSync(keyFile(storeId),JSON.stringify({accessKeyId:b.accessKeyId,secretAccessKey:b.secretAccessKey}),{mode:0o600});chmodSync(keyFile(storeId),0o600);
  await store.query(`INSERT INTO store(id,host_id,zone,bucket,endpoint,local_endpoint,capacity_bytes,used_bytes,requires_logon,state,observed_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,'reachable',now())
   ON CONFLICT(id) DO UPDATE SET zone=excluded.zone,bucket=excluded.bucket,endpoint=excluded.endpoint,local_endpoint=excluded.local_endpoint,capacity_bytes=excluded.capacity_bytes,used_bytes=excluded.used_bytes,requires_logon=excluded.requires_logon,state='reachable',observed_at=now(),registered_at=now()`,
   [storeId,hostId,typeof b.zone==='string'?b.zone:null,bucket,endpoint,local,Number.isSafeInteger(b.capacityBytes)?b.capacityBytes:null,Number.isSafeInteger(b.usedBytes)?b.usedBytes:null,b.requiresLogon===true]);
  return {ok:true,storeId}}
 // Poll report: reachable stores get fresh capacity (capacityBytes is the capacity left, §3) and used bytes; a store is full
 // when the capacity left falls below its store.free_space_warning (operator setting; unset: never full, no built-in cap).
 async function observe(hostId:string,stores:any){if(!Array.isArray(stores))return;
  for(const s of stores){if(typeof s?.id!=='string')continue;const cap=Number.isSafeInteger(s.capacityBytes)?s.capacityBytes:null,used=Number.isSafeInteger(s.usedBytes)?s.usedBytes:null;
   await store.query("UPDATE store SET state=CASE WHEN NOT $5 THEN 'unreachable' WHEN free_space_warning_bytes IS NOT NULL AND coalesce($3,capacity_bytes) < free_space_warning_bytes THEN 'full' ELSE 'reachable' END,capacity_bytes=coalesce($3,capacity_bytes),used_bytes=coalesce($4,used_bytes),observed_at=CASE WHEN $5 THEN now() ELSE observed_at END WHERE id=$1 AND host_id=$2",[s.id,hostId,cap,used,s.reachable===true])}}
 // Operator setting store.free_space_warning (bytes, or null to clear); the state follows at once.
 async function setFreeSpaceWarning(storeId:string,bytes:any){const v=bytes===null?null:Number.isSafeInteger(bytes)&&bytes>=0?bytes:fail(400,'Expected bytes or null');
  const r=await store.query("UPDATE store SET free_space_warning_bytes=$2,state=CASE WHEN state='unreachable' THEN state WHEN $2::bigint IS NOT NULL AND capacity_bytes < $2::bigint THEN 'full' ELSE 'reachable' END WHERE id=$1 RETURNING id,state",[id(storeId,'store id'),v]);
  return r[0]??fail(404,'Unknown store '+storeId)}
 // A store its host has not reported for `unseen` ms is unreachable.
 const sweep=()=>store.query("UPDATE store SET state='unreachable' WHERE state<>'unreachable' AND (observed_at IS NULL OR observed_at < now() - ($1::text||' milliseconds')::interval) RETURNING id",[String(unseen)]);
 const num=(v:any)=>v===null?null:Number(v);
 const list=async():Promise<StoreRow[]>=>(await store.query('SELECT * FROM store ORDER BY id')).map(r=>({...r,capacity_bytes:num(r.capacity_bytes),used_bytes:num(r.used_bytes),free_space_warning_bytes:num(r.free_space_warning_bytes)}));
 const get=async(storeId:string):Promise<StoreRow>=>(await list()).find(s=>s.id===storeId)??fail(404,'Unknown store '+storeId);
 const credentials=(storeId:string):{accessKeyId:string,secretAccessKey:string}=>{try{return JSON.parse(readFileSync(keyFile(storeId),'utf8'))}catch{return fail(503,'No credentials for store '+storeId)}};
 return {register,observe,setFreeSpaceWarning,sweep,list,get,credentials,endpointHosts:config.endpointHosts??[]};
}
export type Stores=ReturnType<typeof storeRegistry>;
