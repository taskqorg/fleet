// API key connections (docs/designs/fleet-connections.md §1-2, stage C0): a list separate from subscriptions, one ownership
// model (holder, access, intake). Key values live only in <secretDir>/connections/<connection>/<fingerprint> (0600); rows
// hold the fingerprint and state. Every change republishes <gatewayDir>/snapshot.json (no key values) after commit.
import {mkdirSync,readdirSync,readFileSync,renameSync,rmSync,writeFileSync,existsSync,chmodSync,statSync} from 'node:fs';
import {createHash,randomUUID} from 'node:crypto';
import {join} from 'node:path';
import {providers} from './providers.ts';
import {requestPerson,actor,director} from './people.ts';
import type {Store} from './store.ts';
const fail=(code:number,message:string):never=>{throw Object.assign(Error(message),{statusCode:code})};
const NAME=/^[a-z][a-z0-9-]{0,62}$/;
const STATES=['active','draining','disabled','retired'];
export const fingerprint=(value:string)=>createHash('sha256').update(value).digest('hex').slice(0,12);

export function connectionRegistry(store:Store,config:{secretDir?:string,gatewayDir?:string}){
 const {all,get,put}=store;
 const dirOf=(name:string)=>join(config.secretDir??fail(409,'FLEET_SECRET_DIR is not set on the coordinator'),'connections',name);
 const owner=()=>{const p=requestPerson.getStore();if(p&&!p.owner)fail(403,'Only the owner handles key values')};
 const holderOrOwner=(c:any)=>{const p=requestPerson.getStore();if(p&&!p.owner&&p.email!==c.holder)fail(403,'Only the holder or the owner changes this API key')};
 const byName=async(name:any)=>typeof name==='string'&&NAME.test(name)?get('connection:'+name,'api-connection').catch(()=>fail(404,'Unknown API key '+name)):fail(400,'Expected connection name');
 const change=(c:any,field:string,previous:any,value:any)=>put('setting-change',{id:randomUUID(),target:'connection:'+c.name,holder:c.holder,field,previous:previous??null,value,actor:actor('operator'),at:Date.now()});
 const keysOf=async(name:string)=>(await all('connection-key',{connectionId:name})).filter((k:any)=>k.connectionId===name).sort((a:any,b:any)=>a.addedAt-b.addedAt);
 const after=(fn:()=>void)=>store.afterCommit(fn);

 // Snapshot (§3): written atomically (temp file plus rename) after the change committed. C1 adds tokens; C3 adds budget flags.
 async function snapshot(){
  const connections:any={};
  for(const c of await all('api-connection'))connections[c.name]={provider:c.provider,tools:c.tools,intake:c.intake,keys:(await keysOf(c.name)).filter((k:any)=>k.state!=='retired').map((k:any)=>({id:k.id.slice(4),state:k.state})),blocked:[]};
  return {schema:2,generated_ms:Date.now(),providers,connections,tokens:[]}}
 async function writeSnapshot(){if(!config.gatewayDir)return;const s=await store.read(snapshot);
  mkdirSync(config.gatewayDir,{recursive:true,mode:0o750});const file=join(config.gatewayDir,'snapshot.json'),tmp=file+'.'+process.pid+'.tmp';
  writeFileSync(tmp,JSON.stringify(s),{mode:0o640});renameSync(tmp,file)}
 let pending:Promise<void>|null=null;
 const publish=()=>after(()=>{pending??=new Promise<void>(r=>setImmediate(r)).then(()=>{pending=null;return writeSnapshot()}).catch(e=>{pending=null;console.error('snapshot',e)})});

 // Key files present without a row (import, restore) are registered; a directory without a connection becomes one, held by
 // the owner, usable by every project (the Tavily import: scripts/connections-import.sh). Files join only an owner-held
 // connection: the directory is the owner's, so a connection another person created under that name never receives them.
 async function sync(){if(!config.secretDir||!existsSync(join(config.secretDir,'connections')))return;
  for(const name of readdirSync(join(config.secretDir,'connections'))){if(!NAME.test(name))continue;
   try{if(!statSync(dirOf(name)).isDirectory())continue}catch{continue}
   let c=await get('connection:'+name,'api-connection').catch(()=>null);
   if(c&&c.holder!==director()){console.error(`secret directory ${name}: connection is held by ${c.holder}, files not registered`);continue}
   if(!c){if(!providers[name])continue;c={id:'connection:'+name,name,provider:name,holder:director(),access:{people:[],projects:['*'],experiments:[]},intake:'all',tools:Object.keys(providers[name].tools),comment:'',createdAt:Date.now()};await put('api-connection',c)}
   for(const file of readdirSync(dirOf(name))){if(!/^[0-9a-f]{12}$/.test(file))continue;let value:string;try{value=readFileSync(join(dirOf(name),file),'utf8').trim()}catch(e){console.error('secret file',name,file,e);continue}
    if(fingerprint(value)!==file)continue;// a renamed or edited file never registers under a wrong fingerprint
    if(!(await get('key:'+file,'connection-key').catch(()=>null)))await put('connection-key',{id:'key:'+file,connectionId:name,fingerprint:file,state:'active',addedAt:Date.now(),addedBy:'import'})}}}

 const list=(strings:any,field:string)=>Array.isArray(strings)&&strings.every((x:any)=>typeof x==='string'&&x.length<=320)?[...new Set(strings as string[])]:fail(400,field+': list of ids');
 const operations:any={
  connections_list:async()=>{const keys=await all('connection-key'),history=(await all('setting-change')).filter((h:any)=>typeof h.target==='string'&&h.target.startsWith('connection:'));
   return {providers,connections:(await all('api-connection')).map((c:any)=>({...c,keys:keys.filter((k:any)=>k.connectionId===c.name).sort((a:any,b:any)=>a.addedAt-b.addedAt),spend:{basis:'unavailable'},history:history.filter((h:any)=>h.target==='connection:'+c.name)}))}},
  connections_create:async(b:any)=>{if(!b||!NAME.test(b.name)||!providers[b.provider]||Object.keys(b).some(k=>!['name','provider','holder','comment'].includes(k)))fail(400,'Expected name and a known provider');
   if(await get('connection:'+b.name,'api-connection').catch(()=>null))fail(409,'API key name exists');
   const p=requestPerson.getStore(),holder=typeof b.holder==='string'?b.holder.toLowerCase():director();if(p&&!p.owner&&holder!==p.email)fail(403,'Only the owner creates an API key for another person');
   const c={id:'connection:'+b.name,name:b.name,provider:b.provider,holder,access:{people:[],projects:[],experiments:[]},intake:'all',tools:Object.keys(providers[b.provider].tools),comment:typeof b.comment==='string'?b.comment.slice(0,2000):'',createdAt:Date.now()};
   await put('api-connection',c);publish();return c},
  // Owner only. Writes the value file and the row in one step and answers with the fingerprint only.
  connections_key_add:async(b:any)=>{owner();const c=await byName(b?.connection);const value=b?.value;
   if(typeof value!=='string'||!/^\S{8,4096}$/.test(value))fail(400,'Expected one key value without spaces');
   const fp=fingerprint(value);if(await get('key:'+fp,'connection-key').catch(()=>null))fail(409,'Key already added');
   const dir=dirOf(c.name);// the file follows the committed row; a rolled-back add leaves no file for sync() to adopt
   after(()=>{mkdirSync(dir,{recursive:true,mode:0o700});writeFileSync(join(dir,fp),value+'\n',{mode:0o600});chmodSync(join(dir,fp),0o600)});
   const k={id:'key:'+fp,connectionId:c.name,fingerprint:fp,state:'active',addedAt:Date.now(),addedBy:actor('operator')};await put('connection-key',k);publish();return k},
  // Rotation (§2): active ↔ draining, any → retired (the value file is deleted; the row stays). disabled → active re-enables
  // a key the gateway disabled, owner only (G3).
  connections_key_state:async(b:any)=>{const k=await get(typeof b?.keyId==='string'?(b.keyId.startsWith('key:')?b.keyId:'key:'+b.keyId):'','connection-key').catch(()=>fail(404,'Unknown key'));
   const c=await byName(k.connectionId);holderOrOwner(c);const to=b.state;
   if(!STATES.includes(to)||to==='disabled')fail(400,'state: active, draining or retired');if(k.state==='retired')fail(409,'A retired key stays retired');
   if(k.state==='disabled'&&to!=='retired')owner();// a disabled key comes back only through the owner (G3)
   if(to===k.state)return k;await change(c,'key:'+k.fingerprint,k.state,to);
   if(to==='retired'){const file=join(dirOf(c.name),k.fingerprint);after(()=>rmSync(file,{force:true}))}
   Object.assign(k,{state:to,updatedAt:Date.now()});if(to==='active'){delete k.disabledReason;delete k.disabledAt}await put('connection-key',k);publish();return k},
  connections_set_access:async(b:any)=>{const c=await byName(b?.connection);holderOrOwner(c);const a=b.access??{};
   if(Object.keys(a).some(k=>!['people','projects','experiments'].includes(k)))fail(400,'access: {people, projects, experiments}');
   const next={people:list(a.people??[],'people').map(e=>e.toLowerCase()),projects:list(a.projects??[],'projects'),experiments:list(a.experiments??[],'experiments')};
   if(JSON.stringify(next)!==JSON.stringify(c.access)){await change(c,'access',c.access,next);c.access=next;c.updatedAt=Date.now();await put('api-connection',c);publish()}return c},
  connections_set_intake:async(b:any)=>{const c=await byName(b?.connection);holderOrOwner(c);if(!['all','paused'].includes(b.intake))fail(400,'intake: all or paused');
   if(c.intake!==b.intake){await change(c,'intake',c.intake,b.intake);c.intake=b.intake;c.updatedAt=Date.now();await put('api-connection',c);publish()}return c},
 };
 return {operations,sync,snapshot,writeSnapshot,publish};
}
