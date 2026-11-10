// People (docs/designs/fleet-people-access.md §1, data foundation §10). people.json is the source of truth:
// [{email, name, roles: ['operator'|'architect'], owner?: true, grants: [{kind, id}]}], exactly one entry with owner: true.
// The file is re-read when its mtime changes; a malformed file keeps the last valid load and raises one needs-you question.
// The rows in person and person_grant mirror the last valid load.
import {readFileSync,statSync} from 'node:fs';
import {createHash} from 'node:crypto';
import {AsyncLocalStorage} from 'node:async_hooks';
import type {Store} from './store.ts';

export type Person={email:string,name:string,roles:string[],owner:boolean,grants:{kind:string,id:string}[],grantsVersion:string};
const ROLES=['operator','architect'];

export function parsePeople(textValue:string):Map<string,Person>{
 const list=JSON.parse(textValue);if(!Array.isArray(list))throw Error('people.json must be a list');
 const out=new Map<string,Person>();
 for(const p of list){
  if(!p||typeof p!=='object'||typeof p.email!=='string'||!/^[^@\s]+@[^@\s]+$/.test(p.email))throw Error('Each person needs an e-mail');
  const email=p.email.toLowerCase();if(out.has(email))throw Error('Duplicate person '+email);
  if(!Array.isArray(p.roles)||!p.roles.length||p.roles.some((r:any)=>!ROLES.includes(r)))throw Error(`Roles of ${email} must be operator and/or architect`);
  if(p.owner!==undefined&&typeof p.owner!=='boolean')throw Error(`owner of ${email} must be true or false`);
  const grants=p.grants??[];if(!Array.isArray(grants)||grants.some((g:any)=>!g||typeof g.kind!=='string'||typeof g.id!=='string'))throw Error(`Grants of ${email} must be [{kind, id}]`);
  out.set(email,{email,name:typeof p.name==='string'?p.name:email,roles:[...p.roles],owner:p.owner===true,grants:grants.map((g:any)=>({kind:g.kind,id:g.id})),
   grantsVersion:createHash('sha256').update(JSON.stringify(p)).digest('hex').slice(0,16)});
 }
 if([...out.values()].filter(p=>p.owner).length!==1)throw Error('Exactly one person must have owner: true');
 return out;
}

// The person of the current request. Set around each route handler (app.ts); null for host and program-worker traffic.
export const requestPerson=new AsyncLocalStorage<Person|null>();
// Who acted, for audit rows: the request person's e-mail, else the given fallback (an execution, 'program-worker').
export const actor=(fallback='system')=>requestPerson.getStore()?.email??fallback;

export function peopleRegistry(file:string|undefined,store:Store){
 let loaded=new Map<string,Person>(),mtime=-1,lastError='';
 const write=async(p:Map<string,Person>)=>store.atomic(async()=>{
  await store.query('DELETE FROM person',[]);
  for(const x of p.values()){await store.query('INSERT INTO person(email,name,roles,owner) VALUES($1,$2,$3,$4)',[x.email,x.name,x.roles,x.owner]);
   for(const g of x.grants)await store.query('INSERT INTO person_grant(email,object_kind,object_id) VALUES($1,$2,$3) ON CONFLICT DO NOTHING',[x.email,g.kind,g.id])}});
 const problem=async(message:string)=>{if(message===lastError)return;lastError=message;
  await store.atomic(()=>store.tasks.ask({kind:'operator_decision',text:`people.json is not valid: ${message}. Fleet keeps the last valid list of people until the file is fixed.`,options:['fixed'],askedBy:'fleet',data:{peopleFile:true}}))};
 // Re-read on mtime change. Returns once the rows match the file.
 const refresh=async()=>{
  if(!file)return;let m:number;try{m=statSync(file).mtimeMs}catch(e:any){m=-2;if(mtime!==m){mtime=m;await problem('cannot read the file ('+e.code+')')}return}
  if(m===mtime)return;mtime=m;
  let next:Map<string,Person>;try{next=parsePeople(readFileSync(file,'utf8'))}catch(e:any){await problem(e.message);return}
  loaded=next;lastError='';await write(next);
 };
 return {refresh,
  personFor:(email:string|undefined)=>email?loaded.get(email.toLowerCase())??null:null,
  owner:()=>[...loaded.values()].find(p=>p.owner)??null};
}
