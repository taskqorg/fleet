// Lab examples (docs/designs/fleet-lab.md, "Use-case registry and examples"; Stage 1). An example is one runnable Lab input
// drawn from a use case of config/lab/use-cases/*.json. It is stored as a lab-scope artifact with role 'example': the
// canonical example JSON, named examples/<id>@<version>.json; its hash is the artifact's content hash. approved_at is set
// when the owner approves it (an owner import approves directly; agent-drafted examples arrive proposed in Stage 2).
import {createHash,randomUUID} from 'node:crypto';
import {readdirSync,readFileSync} from 'node:fs';
import {join} from 'node:path';
import type {Store} from '../store.js';
import type {Artifacts} from '../artifacts.js';
const fail=(code:number,message:string):never=>{throw Object.assign(Error(message),{statusCode:code})};

export type ExampleFile={path:string,text:string};
export type Example={id:string,use_case_id:string,version:number,origin:string,fixture:{repo:string,commit:string}|null,prompt:string,inputs:ExampleFile[],references:ExampleFile[],checks:{id:string,run:string}[],split:'tune'|'held_out'};

const str=(v:any,what:string,max=100000)=>typeof v==='string'&&v.length>0&&v.length<=max?v:fail(400,`Invalid ${what}`);
const ident=(v:any,what:string)=>typeof v==='string'&&/^[A-Za-z0-9][A-Za-z0-9._:-]{0,99}$/.test(v)?v:fail(400,`Invalid ${what}`);
const relPath=(v:any)=>typeof v==='string'&&v.length<=300&&!v.startsWith('/')&&!v.split('/').some(p=>p===''||p==='.'||p==='..')?v:fail(400,'Invalid file path');
const files=(v:any,what:string):ExampleFile[]=>{if(v===undefined)return [];if(!Array.isArray(v))fail(400,`Invalid ${what}`);const seen=new Set<string>();
 return v.map((f:any)=>{const path=relPath(f?.path);if(seen.has(path))fail(400,`Duplicate ${what} path ${path}`);seen.add(path);if(typeof f.text!=='string')fail(400,`Invalid ${what} text`);return {path,text:f.text}})};

// The registry's use-case IDs: an example must point at an indexed use case.
export function useCaseIds(labDirectory:string){const dir=join(labDirectory,'use-cases'),ids=new Set<string>();
 for(const f of readdirSync(dir).filter(f=>f.endsWith('.json')))for(const e of JSON.parse(readFileSync(join(dir,f),'utf8')))ids.add(e.id);return ids}

// Validates and normalizes one example into its canonical shape (fixed key order, so equal content gives an equal hash).
export function parseExample(v:any,useCases:Set<string>):Example{
 if(!v||typeof v!=='object'||Array.isArray(v))fail(400,'Expected an example object');
 const id=ident(v.id,'example id'),use_case_id=str(v.use_case_id,'use_case_id',200);if(!useCases.has(use_case_id))fail(400,`Unknown use case ${use_case_id}`);
 const version=Number.isSafeInteger(v.version)&&v.version>=1?v.version:fail(400,'Invalid version');
 const origin=v.origin==='synthetic'||typeof v.origin==='string'&&/^task:[A-Za-z0-9-]{1,80}$/.test(v.origin)?v.origin:fail(400,'Invalid origin');
 const fixture=v.fixture==null?null:{repo:ident(v.fixture.repo,'fixture repo'),commit:typeof v.fixture.commit==='string'&&/^[0-9a-f]{7,40}$/.test(v.fixture.commit)?v.fixture.commit:fail(400,'Invalid fixture commit')};
 const checks=Array.isArray(v.checks)?v.checks.map((c:any)=>({id:ident(c?.id,'check id'),run:str(c?.run,'check command',10000)})):fail(400,'Invalid checks');
 if(new Set(checks.map((c:any)=>c.id)).size!==checks.length)fail(400,'Duplicate check id');
 const split=v.split==='tune'||v.split==='held_out'?v.split:fail(400,'Invalid split');
 return {id,use_case_id,version,origin,fixture,prompt:str(v.prompt,'prompt'),inputs:files(v.inputs,'input'),references:files(v.references,'reference'),checks,split}}

export const exampleName=(e:{id:string,version:number})=>`examples/${e.id}@${e.version}.json`;

export function labExamples(store:Store,artifacts:Artifacts,labDirectory:string){
 // Import: an identical example (same id, version and content) returns the stored artifact; other content under the same id
 // and version is refused, since a version never changes. A new version is a new artifact.
 async function add(b:{examples:any[],projectId?:string|null,approved?:boolean}){
  if(!Array.isArray(b?.examples)||!b.examples.length)fail(400,'Expected examples');
  const projectId=b.projectId==null?null:ident(b.projectId,'projectId'),useCases=useCaseIds(labDirectory),list=b.examples.map(x=>parseExample(x,useCases));
  const out=[];
  for(const e of list){const bytes=Buffer.from(JSON.stringify(e,null,1)+'\n'),name=exampleName(e);
   const existing=async()=>(await store.query("SELECT id,content_hash,approved_at FROM artifact WHERE role='example' AND name=$1 AND coalesce(project_id,'')=$2",[name,projectId??'']))[0];
   const hash=createHash('sha256').update(bytes).digest('hex');let old=await existing();
   if(!old){const w=await artifacts.writeObject(projectId,'lab',bytes,'application/json')??fail(409,'No reachable store in the project locations'),artifactId=randomUUID();
    // A concurrent import of the same name inserts nothing here (unique index) and is then compared like an existing one.
    const inserted=await store.tx(async()=>{const r=await store.query("INSERT INTO artifact(id,project_id,scope,content_hash,size,media_type,name,role,state,approved_at) VALUES($1,$2,'lab',$3,$4,'application/json',$5,'example','stored',$6) ON CONFLICT (coalesce(project_id,''),name) WHERE role='example' DO NOTHING RETURNING id",[artifactId,projectId,w.hash,bytes.length,name,b.approved?new Date():null]);
     if(r.length)await store.query("INSERT INTO artifact_copy(artifact_id,store_id,project_id,state,verified_at) VALUES($1,$2,$3,'verified',now())",[artifactId,w.target.id,projectId]);return r.length>0});
    if(inserted){out.push({artifactId,id:e.id,version:e.version,hash,created:true});continue}
    old=await existing()??fail(500,'Example row missing after conflict')}
   if(old.content_hash!==hash)fail(409,`${name} exists with other content; raise version`);
   if(b.approved&&!old.approved_at)await store.tx(()=>store.query("UPDATE artifact SET approved_at=now() WHERE id=$1",[old.id]));
   out.push({artifactId:old.id,id:e.id,version:e.version,hash,created:false})}
  return {examples:out}}
 async function list(q:{state?:string}={}){if(q.state!==undefined&&!['proposed','approved'].includes(q.state))fail(400,'Invalid state');
  const rows=await store.query(`SELECT id,project_id,name,content_hash,approved_at,created_at FROM artifact WHERE role='example' AND state='stored'${q.state==='approved'?' AND approved_at IS NOT NULL':q.state==='proposed'?' AND approved_at IS NULL':''} ORDER BY name`);
  return {examples:rows.map((r:any)=>({artifactId:r.id,projectId:r.project_id,name:r.name,hash:r.content_hash,state:r.approved_at?'approved':'proposed',approvedAt:r.approved_at,createdAt:r.created_at}))}}
 async function get(artifactId:string){const a=(await store.query("SELECT id,approved_at FROM artifact WHERE id=$1 AND role='example'",[artifactId]))[0]??fail(404,'Unknown example');
  return {artifactId:a.id,state:a.approved_at?'approved':'proposed',example:JSON.parse((await artifacts.readBuffer(a.id,8*1024*1024)).toString('utf8'))}}
 return {add,list,get}}
