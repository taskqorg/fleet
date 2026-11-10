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

// Changes only when what the person may do changes (roles, owner flag, grants), not on a name edit.
const version=(p:Person)=>createHash('sha256').update(JSON.stringify([[...p.roles].sort(),p.owner,p.grants.map(g=>g.kind+':'+g.id).sort()])).digest('hex').slice(0,16);
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
   grantsVersion:''});
  const x=out.get(email)!;x.grantsVersion=version(x);
 }
 if([...out.values()].filter(p=>p.owner).length!==1)throw Error('Exactly one person must have owner: true');
 return out;
}

// The person of the current request. Set around each route handler (app.ts); null for host and program-worker traffic.
export const requestPerson=new AsyncLocalStorage<Person|null>();
// Who acted, for audit rows: the request person's e-mail, else the given fallback (an execution, 'program-worker').
// The one project-grant rule: the owner sees every project, others those of their project grants.
export const mayAccessProject=(p:Person|null|undefined,projectId:string|null|undefined)=>!!p&&(p.owner||!!projectId&&p.grants.some(g=>g.kind==='project'&&g.id===projectId));
// The director of new work (people-access §5): the request person, else the owner (timers, Lab, program workers).
let ownerEmail:string|null=null;
export const director=()=>requestPerson.getStore()?.email??ownerEmail;
export const actor=(fallback='system')=>requestPerson.getStore()?.email??fallback;
// requestedBy of a run: the request person, else the operator role (timers, Lab reconcile).
export const requester=()=>{const p=requestPerson.getStore();return p?{kind:'person',email:p.email}:{kind:'operator'}};

export function peopleRegistry(file:string|undefined,store:Store){
 let loaded=new Map<string,Person>(),seen='';
 const write=async(p:Map<string,Person>)=>store.atomic(async()=>{
  await store.query('DELETE FROM person',[]);
  for(const x of p.values()){await store.query('INSERT INTO person(email,name,roles,owner) VALUES($1,$2,$3,$4)',[x.email,x.name,x.roles,x.owner]);
   for(const g of x.grants)await store.query('INSERT INTO person_grant(email,object_kind,object_id) VALUES($1,$2,$3) ON CONFLICT DO NOTHING',[x.email,g.kind,g.id])}});
 // After a restart with a broken file, the rows of the last valid load are that load.
 const fromRows=async()=>{const m=new Map<string,Person>();
  for(const r of (await store.query('SELECT email,name,roles,owner FROM person',[])))m.set(r.email,{email:r.email,name:r.name,roles:r.roles,owner:r.owner,grants:[],grantsVersion:''});
  for(const g of (await store.query('SELECT email,object_kind,object_id FROM person_grant',[])))m.get(g.email)?.grants.push({kind:g.object_kind,id:g.object_id});
  for(const p of m.values())p.grantsVersion=version(p);return m};
 // One open question for a broken file, kept across restarts; answering it after the fix closes it.
 const problem=async(message:string)=>store.atomic(async()=>{
  if((await store.query("SELECT 1 FROM question WHERE state='open' AND data->>'peopleFile'='true'",[])).length)return;
  await store.tasks.ask({kind:'operator_decision',text:`people.json is not valid: ${message}. Fleet keeps the last valid list of people until the file is fixed.`,options:['fixed'],askedBy:'fleet',data:{peopleFile:true}})});
 // Re-read when mtime, size or inode change; the state is recorded only after the load is applied.
 const refresh=async()=>{
  if(!file)return;let key:string,next:Map<string,Person>|null=null,error='';
  try{const st=statSync(file);key=[st.mtimeMs,st.size,st.ino].join(':')}catch(e:any){key='missing';error='cannot read the file ('+e.code+')'}
  if(key===seen)return;
  if(!error)try{next=parsePeople(readFileSync(file,'utf8'))}catch(e:any){error=e.message}
  if(next){await write(next);loaded=next}else{if(!loaded.size)loaded=await fromRows();await problem(error)}
  ownerEmail=[...loaded.values()].find(p=>p.owner)?.email??null;
  seen=key;
 };
 return {refresh,
  personFor:(email:string|undefined)=>email?loaded.get(email.toLowerCase())??null:null};
}
