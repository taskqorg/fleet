// Lab example format (moved from packages/coordinator/lab/examples.ts; the Go coordinator validates imports itself) (docs/designs/fleet-lab.md "Use-case registry and examples"). An example is one runnable Lab input
// drawn from a use case of config/lab/use-cases/*.json. It is stored as a lab-scope artifact with role 'example': the
// canonical example JSON, named examples/<id>@<version>.json; its hash is the artifact's content hash. approved_at is set
// when the owner approves it (an owner import approves directly; agent-drafted examples arrive proposed in Stage 2).
import {readdirSync,readFileSync} from 'node:fs';
import {join} from 'node:path';
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
 for(const f of readdirSync(dir).filter(f=>f.endsWith('.json')&&!f.startsWith('.')))for(const e of JSON.parse(readFileSync(join(dir,f),'utf8')))ids.add(e.id);return ids}

// Validates and normalizes one example into its canonical shape (fixed key order, so equal content gives an equal hash).
export function parseExample(v:any,useCases:Set<string>):Example{
 if(!v||typeof v!=='object'||Array.isArray(v))fail(400,'Expected an example object');
 const id=ident(v.id,'example id'),use_case_id=str(v.use_case_id,'use_case_id',200);if(!useCases.has(use_case_id))fail(400,`Unknown use case ${use_case_id}`);
 const version=Number.isSafeInteger(v.version)&&v.version>=1?v.version:fail(400,'Invalid version');
 const origin=v.origin==='synthetic'||typeof v.origin==='string'&&/^(task|export):[A-Za-z0-9._-]{1,80}$/.test(v.origin)?v.origin:fail(400,'Invalid origin');
 const fixture=v.fixture==null?null:{repo:ident(v.fixture.repo,'fixture repo'),commit:typeof v.fixture.commit==='string'&&/^[0-9a-f]{7,40}$/.test(v.fixture.commit)?v.fixture.commit:fail(400,'Invalid fixture commit')};
 const checks=Array.isArray(v.checks)?v.checks.map((c:any)=>({id:ident(c?.id,'check id'),run:str(c?.run,'check command',10000)})):fail(400,'Invalid checks');
 if(new Set(checks.map((c:any)=>c.id)).size!==checks.length)fail(400,'Duplicate check id');
 const split=v.split==='tune'||v.split==='held_out'?v.split:fail(400,'Invalid split');
 return {id,use_case_id,version,origin,fixture,prompt:str(v.prompt,'prompt'),inputs:files(v.inputs,'input'),references:files(v.references,'reference'),checks,split}}

