import {AsyncLocalStorage} from 'node:async_hooks';
import {readFileSync} from 'node:fs';
import pg from 'pg';

type Query=(sql:string,params?:any[])=>Promise<{rows:any[]}>;
type Backend={query:Query,exec:(sql:string)=>Promise<unknown>,tx:<T>(fn:(q:Query)=>Promise<T>,lock?:string)=>Promise<T>,close:()=>Promise<void>};
export type Store=ReturnType<typeof createStore>;

// Postgres when the URL is postgres:// or postgresql://, otherwise an embedded PGlite data directory (tests; 'memory://' for no files).
const backend=(url:string):Backend=>{
 if(/^postgres(ql)?:\/\//.test(url)){
  const pool=new pg.Pool({connectionString:url});
  return {query:(s,p)=>pool.query(s,p),exec:s=>pool.query(s),close:()=>pool.end(),tx:async(fn,lock)=>{const c=await pool.connect();let broken=false;
   // The lock is a session lock taken before BEGIN, so the SERIALIZABLE snapshot starts after the wait.
   try{if(lock)await c.query('SELECT pg_advisory_lock(hashtext($1))',[lock]);try{await c.query('BEGIN ISOLATION LEVEL SERIALIZABLE');const r=await fn((s,p)=>c.query(s,p));await c.query('COMMIT');return r}catch(e){await c.query('ROLLBACK').catch(()=>{});throw e}
    finally{if(lock)await c.query('SELECT pg_advisory_unlock(hashtext($1))',[lock]).catch(()=>{broken=true})}}finally{c.release(broken)}}};
 }
 let db:any;const ready=import('@electric-sql/pglite').then(m=>db=new m.PGlite(url));
 return {query:async(s,p)=>(await ready,db.query(s,p)),exec:async s=>(await ready,db.exec(s)),close:async()=>{await ready;await db.close()},tx:async fn=>(await ready,db.transaction(async(t:any)=>{await t.query('SET TRANSACTION ISOLATION LEVEL SERIALIZABLE');return fn((s,p)=>t.query(s,p))}))};
};

const retryable=(e:any)=>e?.code==='40001'||e?.code==='40P01';
// Short randomized backoff before a retry, growing with the attempt (5 ms .. 200 ms).
const backoff=(attempt:number)=>new Promise(r=>setTimeout(r,Math.random()*Math.min(200,5*2**attempt)));
// jsonb @> for the values filters use: scalars equal, objects by key, arrays contain each element.
const contains=(a:any,b:any):boolean=>b===null||typeof b!=='object'?a===b:Array.isArray(b)?Array.isArray(a)&&b.every(x=>a.some((y:any)=>contains(y,x))):!!a&&typeof a==='object'&&!Array.isArray(a)&&Object.entries(b).every(([k,v])=>contains(a[k],v));
const notFound=()=>{throw Object.assign(Error('Not found'),{statusCode:404})};

export function createStore(url:string){
 const db=backend(url);
 // The current transaction follows the async call chain, so get/put/all inside tx(fn) join it without passing a client around.
 type Frame={sp:string|null,parent:Frame|null};
 // doomed: the first serialization failure seen in this transaction. Later queries rethrow it and the transaction never commits,
 // even when handler code caught the error (Postgres then answers 25P02 to everything until rollback).
 type Tx={q:Query,done:boolean,doomed?:unknown};
 const current=new AsyncLocalStorage<{t:Tx,frame?:Frame}>();
 // Query counts by SQL text, for profiling and the poll query budget test.
 const stats=new Map<string,number>();
 const q:Query=async(s,p)=>{stats.set(s,(stats.get(s)??0)+1);const c=current.getStore(),t=c?.t;if(!t||t.done)return db.query(s,p);
  // A savepoint is opened lazily, just before the first write inside a nested tx: reads never pay for one, and the state
  // before the first write equals the state at the start of the nested call. Outer frames get theirs first.
  if(c!.frame&&!/^\s*SELECT/i.test(s)){const chain:Frame[]=[];for(let f:Frame|null=c!.frame;f&&!f.sp;f=f.parent)chain.unshift(f);for(const f of chain){f.sp='s'+(++savepoints);stats.set('SAVEPOINT',(stats.get('SAVEPOINT')??0)+1);await t.q('SAVEPOINT '+f.sp)}}
  return t.q(s,p)};
 // SERIALIZABLE; fn runs again after a serialization failure, so it must be safe to repeat.
 // tx rolls back when fn throws. atomic commits what fn wrote before it threw: the isolation of a transaction with the
 // write-as-you-go behaviour of a handler that ran outside a transaction on synchronous SQLite.
 // A nested tx undoes its own writes on error, as nested better-sqlite3 transactions did (savepoint opened on first write); atomic keeps them.
 // ponytail: retries until it commits; serialization failures clear once the competing transaction ends.
 let savepoints=0;
 const run=async<T>(fn:()=>Promise<T>|T,commitOnError:boolean,lock?:string):Promise<T>=>{
  const c0=current.getStore(),t0=c0?.t;
  if(t0&&!t0.done){if(commitOnError)return fn();const frame:Frame={sp:null,parent:c0!.frame??null};
   try{const v=await current.run({t:t0,frame},fn);if(frame.sp)await t0.q('RELEASE SAVEPOINT '+frame.sp);return v}catch(e){if(t0.doomed)throw t0.doomed;if(frame.sp)await t0.q('ROLLBACK TO SAVEPOINT '+frame.sp);throw e}}
  for(let attempt=0;;attempt++){const t:Tx={q:null as any as Query,done:false};
   try{const out:{v?:T,e?:unknown}=await db.tx(async tq=>{
     t.q=async(s,p)=>{if(t.doomed)throw t.doomed;try{return await tq(s,p)}catch(e){if(retryable(e))t.doomed=e;throw t.doomed??e}};
     try{const v=await current.run({t},fn);if(t.doomed)throw t.doomed;return {v}}
     catch(e){if(t.doomed)throw t.doomed;if(commitOnError&&!retryable(e))return {e};throw e}finally{t.done=true}},lock);
    if('e' in out)throw out.e;return out.v as T}
   catch(e){if(!retryable(e))throw e;stats.set('RETRY',(stats.get('RETRY')??0)+1);await backoff(attempt)}}
 };
 return {
  async get(id:string,kind:string):Promise<any>{const r=(await q('SELECT data FROM document WHERE id=$1 AND kind=$2',[id,kind])).rows[0];return r?r.data:notFound()},
  // Several documents of one kind in one query, keyed by id; missing ids are absent (callers fall back to get for its 404).
  async many(ids:string[],kind:string):Promise<Map<string,any>>{const rows=(await q('SELECT data FROM document WHERE kind=$1 AND id=ANY($2)',[kind,[...new Set(ids.filter(x=>typeof x==='string'))]])).rows;return new Map(rows.map(r=>[r.data.id,r.data]))},
  // An id that already exists under another kind is left unchanged, as with the SQLite records table.
  async put(kind:string,v:any):Promise<void>{await q('INSERT INTO document(kind,id,data,project_id) VALUES($1,$2,$3,$4) ON CONFLICT(id) DO UPDATE SET data=excluded.data,project_id=excluded.project_id,updated_at=now() WHERE document.kind=excluded.kind',[kind,v.id,JSON.stringify(v),typeof v.projectId==='string'?v.projectId:null])},
  // Insertion order (seq) as SQLite rowid gave it; filter is a jsonb containment match ({} matches all).
  // omit drops top-level keys in SQL, for reads that never use them (artifact bodies).
  // A filter with id, executionId or sessionId reads through btree indexes only (primary key, migration 4); the other keys are
  // matched in JS. GIN predicate locks sit on shared entry pages, so under SERIALIZABLE they conflict with unrelated inserts.
  async all(kind:string,filter?:object,omit?:string[]):Promise<any[]>{const params:any[]=[kind],where=['kind=$1'],rest:any={};
   for(const [k,v] of Object.entries(filter??{})){if(v===undefined)continue;if(['id','executionId','sessionId'].includes(k)&&typeof v==='string'){params.push(v);where.push(k==='id'?`id=$${params.length}`:`data->>'${k}'=$${params.length}`)}else rest[k]=v}
   const narrow=where.length>1,inJs=narrow&&Object.keys(rest).length>0;
   if(!narrow&&Object.keys(rest).length){params.push(JSON.stringify(rest));where.push(`data @> $${params.length}`)}
   if(omit)params.push(omit);
   const rows=(await q(`SELECT data${omit?` - $${params.length}::text[]`:''} AS data FROM document WHERE ${where.join(' AND ')} ORDER BY seq`,params)).rows.map(r=>r.data);
   return inJs?rows.filter(d=>contains(d,rest)):rows},
  // lock: serialize transactions on one key (e.g. one execution's host traffic); ignored inside an outer transaction and on PGlite.
  tx:<T>(fn:()=>Promise<T>|T,lock?:string)=>run(fn,false,lock),
  atomic:<T>(fn:()=>Promise<T>|T,lock?:string)=>run(fn,true,lock),
  query:(sql:string,params:any[]=[])=>q(sql,params).then(r=>r.rows),
  async migrate(){
   await db.exec('CREATE TABLE IF NOT EXISTS schema_migration(version int PRIMARY KEY,applied_at timestamptz NOT NULL DEFAULT now())');
   const done=new Set((await db.query('SELECT version FROM schema_migration')).rows.map(r=>r.version));
   const file=readFileSync(new URL('./db/schema.sql',import.meta.url),'utf8');
   for(const part of file.split(/^-- migration (?=\d+)/m).slice(1)){const version=Number(part.match(/^\d+/)![0]);if(done.has(version))continue;
    await db.tx(async tq=>{for(const s of part.replace(/^\d+.*\n/,'').split(/;\s*$/m).map(x=>x.trim()).filter(Boolean))await tq(s);await tq('INSERT INTO schema_migration(version) VALUES($1)',[version])});}
  },
  stats,
  close:()=>db.close(),
 };
}
export type Docs=Pick<Store,'get'|'put'|'all'|'tx'|'atomic'>;
// Operations that wait on a pipeline run; they open short transactions around their own synchronous steps.
export const LONG_OPERATIONS=new Set(['pipeline_run','direct_search','delivery_generate']);
// Sequential async forms of the array methods, for callbacks that read the store; order of reads and writes stays as in the sync code.
export const every=async<T>(xs:T[],f:(x:T)=>unknown)=>{for(const x of xs)if(!await f(x))return false;return true};
export const some=async<T>(xs:T[],f:(x:T)=>unknown)=>{for(const x of xs)if(await f(x))return true;return false};
export const find=async<T>(xs:T[],f:(x:T)=>unknown)=>{for(const x of xs)if(await f(x))return x;return undefined};
export const filter=async<T>(xs:T[],f:(x:T)=>unknown)=>{const r:T[]=[];for(const x of xs)if(await f(x))r.push(x);return r};
export const map=async<T,U>(xs:T[],f:(x:T,i:number)=>U|Promise<U>)=>{const r:U[]=[];for(const [i,x] of xs.entries())r.push(await f(x,i));return r};
