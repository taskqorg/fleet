// Store registry (design §3, Phase 3 item 22). A store's own executor registers it through the outbox; the coordinator keeps
// the S3 key of its role in a 0600 file under secretsDir, never in rows. Each poll reports reachability and capacity.
import {mkdirSync,readFileSync,writeFileSync,chmodSync,existsSync} from 'node:fs';
import {join} from 'node:path';
import type {Store} from './store.js';
const fail=(code:number,message:string):never=>{throw Object.assign(Error(message),{statusCode:code})};
const id=(v:any,name:string)=>typeof v==='string'&&/^[a-z0-9][a-z0-9-]{0,62}$/.test(v)?v:fail(400,'Invalid '+name);
const url=(v:any,name:string)=>{try{const u=new URL(v);if(!['http:','https:'].includes(u.protocol))throw 0;return u.toString().replace(/\/$/,'')}catch{return fail(400,'Invalid '+name)}};
export type StoreRow={id:string,host_id:string,bucket:string,endpoint:string,local_endpoint:string|null,state:string,capacity_bytes:number|null,used_bytes:number|null,requires_logon:boolean,zone:string|null};
// Cloudflare Access service token from an env file (CF_ACCESS_CLIENT_ID, CF_ACCESS_CLIENT_SECRET), read when used.
export function accessToken(file?:string):{id:string,secret:string}|null{if(!file||!existsSync(file))return null;
 const v=Object.fromEntries(readFileSync(file,'utf8').split('\n').map(l=>l.replace(/^export /,'').trim()).filter(l=>l.includes('=')).map(l=>{const i=l.indexOf('=');return [l.slice(0,i),l.slice(i+1).replace(/^['"]|['"]$/g,'')]}));
 return v.CF_ACCESS_CLIENT_ID&&v.CF_ACCESS_CLIENT_SECRET?{id:v.CF_ACCESS_CLIENT_ID,secret:v.CF_ACCESS_CLIENT_SECRET}:null}
export function storeRegistry(store:Store,config:{secretsDir:string,freeSpaceWarningBytes?:number,unseenMs?:number}){
 const unseen=config.unseenMs??120000,warning=config.freeSpaceWarningBytes??0;
 const keyFile=(storeId:string)=>join(config.secretsDir,storeId+'.key');
 // Registration: only the executor of the store's host may create or replace it.
 async function register(hostId:string,b:any){const storeId=id(b?.id,'store id'),bucket=id(b?.bucket,'bucket');
  if(typeof b.accessKeyId!=='string'||!b.accessKeyId||typeof b.secretAccessKey!=='string'||!b.secretAccessKey)fail(400,'Expected store credentials');
  const row=(await store.query('SELECT host_id FROM store WHERE id=$1',[storeId]))[0];if(row&&row.host_id!==hostId)fail(403,'Store belongs to another host');
  mkdirSync(config.secretsDir,{recursive:true,mode:0o700});writeFileSync(keyFile(storeId),JSON.stringify({accessKeyId:b.accessKeyId,secretAccessKey:b.secretAccessKey}),{mode:0o600});chmodSync(keyFile(storeId),0o600);
  await store.query(`INSERT INTO store(id,host_id,zone,bucket,endpoint,local_endpoint,capacity_bytes,used_bytes,requires_logon,state,observed_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,'reachable',now())
   ON CONFLICT(id) DO UPDATE SET zone=excluded.zone,bucket=excluded.bucket,endpoint=excluded.endpoint,local_endpoint=excluded.local_endpoint,capacity_bytes=excluded.capacity_bytes,used_bytes=excluded.used_bytes,requires_logon=excluded.requires_logon,state='reachable',observed_at=now(),registered_at=now()`,
   [storeId,hostId,typeof b.zone==='string'?b.zone:null,bucket,url(b.endpoint,'endpoint'),b.localEndpoint?url(b.localEndpoint,'localEndpoint'):null,Number.isSafeInteger(b.capacityBytes)?b.capacityBytes:null,Number.isSafeInteger(b.usedBytes)?b.usedBytes:null,b.requiresLogon===true]);
  return {ok:true,storeId}}
 // Poll report: reachable stores get fresh capacity; full below the free-space warning.
 async function observe(hostId:string,stores:any){if(!Array.isArray(stores))return;
  for(const s of stores){if(typeof s?.id!=='string')continue;const cap=Number.isSafeInteger(s.capacityBytes)?s.capacityBytes:null,used=Number.isSafeInteger(s.usedBytes)?s.usedBytes:null;
   const state=s.reachable!==true?'unreachable':cap!==null&&cap-(used??0)<warning?'full':'reachable';
   await store.query("UPDATE store SET state=$3,capacity_bytes=coalesce($4,capacity_bytes),used_bytes=coalesce($5,used_bytes),observed_at=CASE WHEN $6 THEN now() ELSE observed_at END WHERE id=$1 AND host_id=$2",[s.id,hostId,state,cap,used,s.reachable===true])}}
 // A store its host has not reported for `unseen` ms is unreachable.
 const sweep=()=>store.query("UPDATE store SET state='unreachable' WHERE state<>'unreachable' AND (observed_at IS NULL OR observed_at < now() - ($1::text||' milliseconds')::interval) RETURNING id",[String(unseen)]);
 const list=async():Promise<StoreRow[]>=>(await store.query('SELECT * FROM store ORDER BY id')).map(r=>({...r,capacity_bytes:r.capacity_bytes===null?null:Number(r.capacity_bytes),used_bytes:r.used_bytes===null?null:Number(r.used_bytes)}));
 const get=async(storeId:string):Promise<StoreRow>=>(await list()).find(s=>s.id===storeId)??fail(404,'Unknown store '+storeId);
 const credentials=(storeId:string):{accessKeyId:string,secretAccessKey:string}=>{try{return JSON.parse(readFileSync(keyFile(storeId),'utf8'))}catch{return fail(503,'No credentials for store '+storeId)}};
 return {register,observe,sweep,list,get,credentials};
}
export type Stores=ReturnType<typeof storeRegistry>;
