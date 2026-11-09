import {AsyncLocalStorage} from 'node:async_hooks';
import {readFileSync} from 'node:fs';
import pg from 'pg';

type Query=(sql:string,params?:any[])=>Promise<{rows:any[]}>;
type Backend={query:Query,exec:(sql:string)=>Promise<unknown>,tx:<T>(fn:(q:Query)=>Promise<T>)=>Promise<T>,close:()=>Promise<void>};
export type Store=ReturnType<typeof createStore>;

// Postgres when the URL is postgres:// or postgresql://, otherwise an embedded PGlite data directory (tests; 'memory://' for no files).
const backend=(url:string):Backend=>{
 if(/^postgres(ql)?:\/\//.test(url)){
  const pool=new pg.Pool({connectionString:url});
  return {query:(s,p)=>pool.query(s,p),exec:s=>pool.query(s),close:()=>pool.end(),tx:async fn=>{const c=await pool.connect();try{await c.query('BEGIN ISOLATION LEVEL SERIALIZABLE');const r=await fn((s,p)=>c.query(s,p));await c.query('COMMIT');return r}catch(e){await c.query('ROLLBACK').catch(()=>{});throw e}finally{c.release()}}};
 }
 let db:any;const ready=import('@electric-sql/pglite').then(m=>db=new m.PGlite(url));
 return {query:async(s,p)=>(await ready,db.query(s,p)),exec:async s=>(await ready,db.exec(s)),close:async()=>{await ready;await db.close()},tx:async fn=>(await ready,db.transaction(async(t:any)=>{await t.query('SET TRANSACTION ISOLATION LEVEL SERIALIZABLE');return fn((s,p)=>t.query(s,p))}))};
};

const retryable=(e:any)=>e?.code==='40001'||e?.code==='40P01';
const notFound=()=>{throw Object.assign(Error('Not found'),{statusCode:404})};

export function createStore(url:string){
 const db=backend(url);
 // The current transaction follows the async call chain, so get/put/all inside tx(fn) join it without passing a client around.
 const current=new AsyncLocalStorage<{q:Query,done:boolean}>();
 const q:Query=(s,p)=>{const t=current.getStore();return t&&!t.done?t.q(s,p):db.query(s,p)};
 return {
  async get(id:string,kind:string):Promise<any>{const r=(await q('SELECT data FROM document WHERE id=$1 AND kind=$2',[id,kind])).rows[0];return r?r.data:notFound()},
  // An id that already exists under another kind is left unchanged, as with the SQLite records table.
  async put(kind:string,v:any):Promise<void>{await q('INSERT INTO document(kind,id,data,project_id) VALUES($1,$2,$3,$4) ON CONFLICT(id) DO UPDATE SET data=excluded.data,project_id=excluded.project_id,updated_at=now() WHERE document.kind=excluded.kind',[kind,v.id,JSON.stringify(v),typeof v.projectId==='string'?v.projectId:null])},
  // Insertion order (seq) as SQLite rowid gave it; filter is a jsonb containment match.
  async all(kind:string,filter?:object):Promise<any[]>{return (await q(`SELECT data FROM document WHERE kind=$1${filter?' AND data @> $2':''} ORDER BY seq`,filter?[kind,JSON.stringify(filter)]:[kind])).rows.map(r=>r.data)},
  // SERIALIZABLE; fn runs again after a serialization failure, so it must be safe to repeat. Nested tx calls join the outer one.
  // ponytail: retries until it commits; serialization failures clear once the competing transaction ends.
  async tx<T>(fn:()=>Promise<T>|T):Promise<T>{
   if(current.getStore()&&!current.getStore()!.done)return fn();
   for(;;){const t={q:null as any as Query,done:false};
    try{return await db.tx(async tq=>{t.q=tq;try{return await current.run(t,fn)}finally{t.done=true}})}catch(e){if(!retryable(e))throw e}}
  },
  query:(sql:string,params:any[]=[])=>q(sql,params).then(r=>r.rows),
  async migrate(){
   await db.exec('CREATE TABLE IF NOT EXISTS schema_migration(version int PRIMARY KEY,applied_at timestamptz NOT NULL DEFAULT now())');
   const done=new Set((await db.query('SELECT version FROM schema_migration')).rows.map(r=>r.version));
   const file=readFileSync(new URL('./db/schema.sql',import.meta.url),'utf8');
   for(const part of file.split(/^-- migration (?=\d+)/m).slice(1)){const version=Number(part.match(/^\d+/)![0]);if(done.has(version))continue;
    await db.tx(async tq=>{for(const s of part.replace(/^\d+.*\n/,'').split(/;\s*$/m).map(x=>x.trim()).filter(Boolean))await tq(s);await tq('INSERT INTO schema_migration(version) VALUES($1)',[version])});}
  },
  close:()=>db.close(),
 };
}
export type Docs=Pick<Store,'get'|'put'|'all'>;
// Sequential async forms of the array methods, for callbacks that read the store; order of reads and writes stays as in the sync code.
export const every=async<T>(xs:T[],f:(x:T)=>unknown)=>{for(const x of xs)if(!await f(x))return false;return true};
export const some=async<T>(xs:T[],f:(x:T)=>unknown)=>{for(const x of xs)if(await f(x))return true;return false};
export const find=async<T>(xs:T[],f:(x:T)=>unknown)=>{for(const x of xs)if(await f(x))return x;return undefined};
export const filter=async<T>(xs:T[],f:(x:T)=>unknown)=>{const r:T[]=[];for(const x of xs)if(await f(x))r.push(x);return r};
export const map=async<T,U>(xs:T[],f:(x:T,i:number)=>U|Promise<U>)=>{const r:U[]=[];for(const [i,x] of xs.entries())r.push(await f(x,i));return r};
