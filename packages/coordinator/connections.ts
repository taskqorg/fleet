// API key connections (docs/designs/fleet-connections.md §1-2, stage C0): a list separate from subscriptions, one ownership
// model (holder, access, intake). Key values live only in <secretDir>/connections/<connection>/<fingerprint> (0600); rows
// hold the fingerprint and state. Every change republishes <gatewayDir>/snapshot.json (no key values) after commit.
import {mkdirSync,readdirSync,readFileSync,renameSync,rmSync,writeFileSync,existsSync,chmodSync,statSync} from 'node:fs';
import {createHash,randomUUID,randomBytes} from 'node:crypto';
import {join} from 'node:path';
import {providers} from './providers.ts';
import {problemOf} from './pipelines.ts';
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

 // Snapshot (§3): written atomically (temp file plus rename) after the change committed. C3 adds budget flags.
 async function snapshot(){
  const connections:any={};
  for(const c of await all('api-connection'))connections[c.name]={provider:c.provider,tools:c.tools,intake:c.intake,keys:(await keysOf(c.name)).filter((k:any)=>k.state!=='retired').map((k:any)=>({id:k.id.slice(4),state:k.state,...(k.enabledAt?{enabledAt:k.enabledAt}:{})})),blocked:[]};
  const tokens=(await all('run-token')).filter((t:any)=>!t.revokedAt).map((t:any)=>({sha256:t.sha256,executionId:t.executionId,directedBy:t.directedBy,projectId:t.projectId,experimentId:t.experimentId,connections:t.connections}));
  return {schema:2,generated_ms:Date.now(),providers,connections,tokens}}
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
   Object.assign(k,{state:to,updatedAt:Date.now()});if(to==='active'){delete k.disabledReason;delete k.disabledAt;k.enabledAt=Date.now()}await put('connection-key',k);publish();return k},
  connections_set_access:async(b:any)=>{const c=await byName(b?.connection);holderOrOwner(c);const a=b.access??{};
   if(Object.keys(a).some(k=>!['people','projects','experiments'].includes(k)))fail(400,'access: {people, projects, experiments}');
   const next={people:list(a.people??[],'people').map(e=>e.toLowerCase()),projects:list(a.projects??[],'projects'),experiments:list(a.experiments??[],'experiments')};
   if(JSON.stringify(next)!==JSON.stringify(c.access)){await change(c,'access',c.access,next);c.access=next;c.updatedAt=Date.now();await put('api-connection',c);publish()}return c},
  connections_set_intake:async(b:any)=>{const c=await byName(b?.connection);holderOrOwner(c);if(!['all','paused'].includes(b.intake))fail(400,'intake: all or paused');
   if(c.intake!==b.intake){await change(c,'intake',c.intake,b.intake);c.intake=b.intake;c.updatedAt=Date.now();await put('api-connection',c);publish()}return c},
 };
 // Per-run tokens (§4). The run's connections are the requested ones (profile grants, api step connection) that exist, take
 // intake and whose access list names the run's director, project or experiment (projects ['*']: all work, with or without a project; an empty list:
 // only the holder's own work). Only the SHA-256 is kept with the run; the value goes into the start command.
 const allowed=(c:any,run:{directedBy?:string|null,projectId?:string|null,experimentId?:string|null})=>{const a=c.access??{};
  const none=!a.people?.length&&!a.projects?.length&&!a.experiments?.length;
  return none?run.directedBy===c.holder:(!!run.directedBy&&a.people?.includes(run.directedBy))||a.projects?.includes('*')||(!!run.projectId&&a.projects?.includes(run.projectId))||(!!run.experimentId&&a.experiments?.includes(run.experimentId))};
 async function mint(run:{executionId:string,directedBy?:string|null,projectId?:string|null,experimentId?:string|null,pipelineRunId?:string|null,step?:boolean},requested:string[]){
  const granted:any[]=[];
  for(const name of [...new Set(requested)]){const c=await get('connection:'+name,'api-connection').catch(()=>null);if(c&&c.intake==='all'&&allowed(c,run))granted.push(c)}
  if(!granted.length)return {token:null,connections:[],refused:[...new Set(requested)]};
  const token=randomBytes(32).toString('base64url'),sha256=createHash('sha256').update(token).digest('hex');
  await put('run-token',{id:'run-token:'+sha256,sha256,executionId:run.executionId,directedBy:run.directedBy??null,projectId:run.projectId??null,experimentId:run.experimentId??null,pipelineRunId:run.pipelineRunId??null,...(run.step?{step:true}:{}),connections:granted.map(c=>c.name),createdAt:Date.now()});publish();
  return {token,refused:[...new Set(requested)].filter(n=>!granted.some(c=>c.name===n)),connections:granted.map(c=>({name:c.name,instructions:providers[c.provider].instructions,tools:c.tools.filter((t:string)=>providers[c.provider].tools[t]).map((t:string)=>({name:t,description:providers[c.provider].tools[t].description,inputSchema:providers[c.provider].tools[t].inputSchema}))}))}}
 async function revoke(executionId:string){let changed=false;for(const t of await all('run-token',{executionId}))if(t.executionId===executionId&&!t.revokedAt){t.revokedAt=Date.now();await put('run-token',t);changed=true}if(changed)publish()}
 const ENDED=['completed','failed','stopped'];
 // Spool ingest (§3): every gateway line becomes one tool_run under the token's execution with connection_cost, idempotent by
 // callId. A past day's file is deleted once read. A line naming a disabled key disables its row and asks the owner once.
 const spoolDir=()=>config.gatewayDir?join(config.gatewayDir,'state','spool'):null;
 async function ingest(){const dir=spoolDir();if(!dir||!existsSync(dir))return 0;let added=0;const yesterday=new Date(Date.now()-86400000).toISOString().slice(0,10);
  for(const file of readdirSync(dir).filter(f=>/^\d{4}-\d\d-\d\d\.jsonl$/.test(f)).sort()){
   // ponytail: the whole file is read each pass (one day of lines); keep a read offset if a day's file gets large.
   const lines=readFileSync(join(dir,file),'utf8').split('\n').filter(Boolean).flatMap(l=>{try{return [JSON.parse(l)]}catch{return []}});
   for(const l of lines)if(await record(l))added++;
   // Deleted after commit, and only once a whole day has passed since it ended: a call that started before midnight may still append.
   if(file.slice(0,10)<yesterday){const f=join(dir,file);after(()=>rmSync(f,{force:true}))}}
  return added}
 async function record(l:any){if(typeof l?.callId!=='string'||!l.tokenSha256||!l.executionId)return false;
  const t=await get('run-token:'+l.tokenSha256,'run-token').catch(()=>null);if(!t)return false;
  const id='call:'+l.callId,amount=Number.isFinite(l.usage?.amount)?l.usage.amount:null;
  const inserted=await store.executions.createExecution({id,kind:'tool_run',state:l.outcome==='ok'?'succeeded':'failed',projectId:t.projectId,parentExecutionId:t.executionId,experimentId:t.experimentId,
   data:{tool:l.tool,connection:l.connection,...(t.pipelineRunId?{pipelineRunId:t.pipelineRunId}:{}),outcome:l.outcome,errorCode:l.errorCode??null,status:l.status,keyFingerprint:l.keyFingerprint??null,usage:l.usage??null,directedBy:t.directedBy,startedAt:l.startedAt,ms:l.ms}});
  if(!inserted)return false;
  if(l.outcome!=='ok')await store.query('UPDATE execution SET problem=$2 WHERE id=$1 AND problem IS NULL',[id,problemOf(l.outcome,l.errorCode??null)]);
  await store.query(`INSERT INTO execution_totals(execution_id,project_id,connection_cost,connection_cost_basis) VALUES($1,$2,$3,$4)
   ON CONFLICT(execution_id) DO UPDATE SET connection_cost=excluded.connection_cost,connection_cost_basis=excluded.connection_cost_basis,computed_at=now()`,
   [id,t.projectId,amount===null?null:JSON.stringify({unit:l.usage.unit,[l.usage.unit]:amount,amount,connection:l.connection}),amount===null?'unavailable':'measured']);
  if(l.disabled?.key)await disable(l.disabled.key,l.disabled.status);
  if(l.keyProblem?.key){const k=await get('key:'+l.keyProblem.key,'connection-key').catch(()=>null);if(k){k.problem={status:l.keyProblem.status,message:String(l.keyProblem.message??'').slice(0,500),at:l.startedAt??Date.now()};await put('connection-key',k)}}
  return true}
 async function disable(fp:string,status:number){const k=await get('key:'+fp,'connection-key').catch(()=>null);if(!k||k.state==='retired'||k.state==='disabled')return;
  Object.assign(k,{state:'disabled',disabledReason:`provider answered HTTP ${status}`,disabledAt:Date.now()});await put('connection-key',k);publish();
  const cause=`key:${fp}:disabled`;if((await store.tasks.questions({kind:'operator_decision'})).some((q:any)=>q.cause===cause&&q.state!=='answered'))return;
  await store.tasks.ask({kind:'operator_decision',text:`API key ${fp} of ${k.connectionId} was refused by the provider (HTTP ${status}) and is disabled. Re-enable it on the API keys screen once it works again, or retire it.`,options:['seen'],askedBy:'fleet',data:{cause}})}
 // One pass: ingest, then revoke tokens of ended runs (a run that ended while the coordinator was down is settled here).
 async function tick(){await ingest();
  const open=(await all('run-token')).filter((t:any)=>!t.revokedAt);if(!open.length)return;
  const runs=await store.many(open.map((t:any)=>t.executionId),'execution');
  // An api step's token is revoked by its caller (and all of them at start, below); any other token goes when its execution
  // ended or is unknown.
  for(const t of open){if(t.step)continue;const e=runs.get(t.executionId);if(!e||ENDED.includes(e.state))await revoke(t.executionId)}}
 // At start no api step call of an earlier process is in flight: their tokens are revoked.
 const started=Date.now();
 async function revokeSteps(){for(const t of await all('run-token'))if(t.step&&!t.revokedAt&&t.createdAt<started){t.revokedAt=Date.now();await put('run-token',t)}}
 return {operations,sync:async()=>{await revokeSteps();await sync()},snapshot,writeSnapshot,publish,mint,revoke,ingest,tick};
}
