// Hosts (docs/designs/fleet-people-access.md §3). A host is a 'host' document {id, name, holder, platform, roles, secretHash,
// intake, harnesses}. Each host has its own secret; only its SHA-256 is stored. The coordinator derives the host from the
// secret on every /api/hosts/* request and never trusts a hostId in the body.
// roles: 'inspector-for:<hostId>' (runs account inspections and integration for that host), 'publication' (runs delivery).
import {createHash,randomBytes} from 'node:crypto';
import type {Store} from './store.ts';
import type {Person} from './people.ts';

export type Host={id:string,name:string,holder:string,platform:string,roles:string[],secretHash:string,intake:'all'|'paused',harnesses:Record<string,{allowed:boolean}>,createdAt:number};
export const secretHash=(secret:string)=>createHash('sha256').update(secret).digest('hex');
const fail=(code:number,message:string):never=>{throw Object.assign(Error(message),{statusCode:code})};
const ID=/^[a-z][a-z0-9-]{0,79}$/,HASH=/^[0-9a-f]{64}$/;

export function hostRegistry(store:Store,people:{personFor:(e:string|undefined)=>Person|null},browserSecret:string){
 // Read through on every call: fewer than 10 hosts, one indexed kind scan. ponytail: cache if polls ever show up in profiles.
 // Document ids are unique across kinds, and host-observation uses the bare host id: a host row is stored as 'host:<id>'.
 const list=async():Promise<Host[]>=>(await store.all('host')).map(({hostId,...x}:any)=>({...x,id:hostId}));
 const save=(h:Host)=>store.put('host',{...h,id:'host:'+h.id,hostId:h.id});
 const byId=async(id:string)=>(await list()).find(h=>h.id===id)??null;
 // A host whose holder is no longer in people.json is refused.
 const fromSecret=async(secret:string)=>{if(!secret)return null;const h=secretHash(secret),x=(await list()).find(x=>x.secretHash===h);return x&&people.personFor(x.holder)?x:null};
 // A host held by the owner sees every project; a host held by someone else sees only projects its holder has a grant on.
 const maySee=(host:Host,projectId:string|null|undefined)=>{const p=people.personFor(host.holder);if(!p)return false;if(p.owner)return true;
  return !!projectId&&p.grants.some(g=>g.kind==='project'&&g.id===projectId)};
 const owner=(p:Person|null|undefined)=>{if(!p?.owner)fail(403,'Only the owner manages hosts')};
 // The two credential kinds never match (§1).
 const distinct=(hash:string)=>{if(hash===secretHash(browserSecret))fail(400,'A host secret must differ from the browser secret')};
 const operations={
  // Owner only. With secretSha256 (the hash of a secret generated on the host itself, which never leaves it) the coordinator
  // stores that hash; without it, the coordinator mints a secret and returns it once.
  hosts_register:async(b:any,p:Person|null)=>{owner(p);
   if(!ID.test(b?.id??''))fail(400,'Expected host id');if(await byId(b.id))fail(409,'Host exists');
   const holder=typeof b.holder==='string'?b.holder.toLowerCase():p!.email;if(!people.personFor(holder))fail(400,'Holder is not a person');
   if(b.secretSha256!==undefined&&!HASH.test(b.secretSha256))fail(400,'secretSha256 must be 64 hex characters');
   const roles=b.roles??[];if(!Array.isArray(roles)||roles.some((r:any)=>typeof r!=='string'||!/^(publication|inspector-for:[a-z][a-z0-9-]{0,79})$/.test(r)))fail(400,'roles: publication or inspector-for:<hostId>');
   const harnesses=b.harnesses??{};if(typeof harnesses!=='object'||Object.values(harnesses).some((v:any)=>typeof v?.allowed!=='boolean'))fail(400,'harnesses: {<name>: {allowed}}');
   const secret=b.secretSha256?null:randomBytes(32).toString('hex');
   const h:Host={id:b.id,name:typeof b.name==='string'?b.name:b.id,holder,platform:typeof b.platform==='string'?b.platform:'unknown',roles,secretHash:b.secretSha256??secretHash(secret!),intake:'all',harnesses,createdAt:Date.now()};
   distinct(h.secretHash);if((await list()).some(x=>x.secretHash===h.secretHash))fail(409,'Secret already used by another host');
   await save(h);return {host:view(h),...(secret?{secret}:{})}},
  // Replaces the hash; the old secret stops at once.
  hosts_rotate_secret:async(b:any,p:Person|null)=>{owner(p);const h=await byId(b?.id)??fail(404,'Unknown host');
   if(b.secretSha256!==undefined&&!HASH.test(b.secretSha256))fail(400,'secretSha256 must be 64 hex characters');
   const secret=b.secretSha256?null:randomBytes(32).toString('hex');h.secretHash=b.secretSha256??secretHash(secret!);
   distinct(h.secretHash);if((await list()).some(x=>x.id!==h.id&&x.secretHash===h.secretHash))fail(409,'Secret already used by another host');
   await save(h);return {host:view(h),...(secret?{secret}:{})}},
 };
 const view=({secretHash,...h}:Host)=>h;
 return {list,byId,fromSecret,maySee,operations,view,
  ids:async()=>(await list()).map(h=>h.id),
  // Hosts that run agent work: every host without a program-worker role.
  executionIds:async()=>(await list()).filter(h=>!h.roles.length).map(h=>h.id),
  inspectorFor:async(id:string)=>(await list()).find(h=>h.roles.includes('inspector-for:'+id))?.id,
  publication:async()=>(await list()).find(h=>h.roles.includes('publication'))?.id,
  // The integration worker: the inspector of the coordinator's default host.
  integration:async(defaultHost:string)=>(await list()).find(h=>h.roles.includes('inspector-for:'+defaultHost))?.id};
}
