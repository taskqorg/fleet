import {AsyncLocalStorage} from 'node:async_hooks';
import {readFileSync} from 'node:fs';
import pg from 'pg';
import {executionModel,type Adapter} from './executions.ts';
import {taskModel} from './tasks.ts';

type Query=(sql:string,params?:any[])=>Promise<{rows:any[]}>;
export type Mode='read'|'write';
type Backend={query:Query,exec:(sql:string)=>Promise<unknown>,tx:<T>(fn:(q:Query)=>Promise<T>,mode:Mode)=>Promise<T>,close:()=>Promise<void>,listen:(channel:string,fn:()=>void)=>Promise<()=>Promise<void>>};
export type Store=ReturnType<typeof createStore>;
// read: one consistent snapshot, never blocks writers and never fails to serialize. write: each statement sees the latest
// committed data; the scope lock (below) makes writers of one scope run one at a time.
const BEGIN={read:'BEGIN READ ONLY ISOLATION LEVEL REPEATABLE READ',write:'BEGIN ISOLATION LEVEL READ COMMITTED'};

// Postgres when the URL is postgres:// or postgresql://, otherwise an embedded PGlite data directory (tests; 'memory://' for no files).
// scope: the fleet.project_ids row-level security setting of every connection ('*' is the operator scope, see migration 6).
const backend=(url:string,scope:string):Backend=>{
 if(/^postgres(ql)?:\/\//.test(url)){
  // The scope is a startup option, set before the connection runs any query.
  const pool=new pg.Pool({connectionString:url,options:`-c fleet.project_ids=${scope.replace(/[^a-zA-Z0-9_*,-]/g,'')}`});
  // LISTEN holds one dedicated connection for the process; it reconnects and listens again after an error.
  // close() stops every listener first: a LISTEN client still checked out would make pool.end() wait forever.
  const listeners=new Set<()=>Promise<void>>();
  const listen=async(channel:string,fn:()=>void)=>{let client:any=null,stopped=false;
   const open=async():Promise<void>=>{if(stopped)return;try{client=await pool.connect();client.on('notification',()=>fn());client.on('error',()=>{client?.release(true);client=null;setTimeout(()=>{open();fn()},1000)});await client.query('LISTEN '+channel.replace(/\W/g,''))}catch{client?.release(true);client=null;setTimeout(open,1000)}};
   await open();const stop=async()=>{if(stopped)return;stopped=true;listeners.delete(stop);if(client){await client.query('UNLISTEN *').catch(()=>{});client.release();client=null}};listeners.add(stop);return stop};
  return {listen,query:(s,p)=>pool.query(s,p),exec:s=>pool.query(s),close:async()=>{for(const stop of [...listeners])await stop();await pool.end()},tx:async(fn,mode)=>{const c=await pool.connect();
   try{await c.query(BEGIN[mode]);const r=await fn((s,p)=>c.query(s,p));await c.query('COMMIT');return r}catch(e){await c.query('ROLLBACK').catch(()=>{});throw e}finally{c.release()}}};
 }
 let db:any;const ready=import('@electric-sql/pglite').then(async m=>{db=new m.PGlite(url);await db.query("SELECT set_config('fleet.project_ids',$1,false)",[scope])});
 return {listen:async(channel,fn)=>{await ready;const off=await db.listen(channel,()=>fn());return async()=>{await off()}},query:async(s,p)=>(await ready,db.query(s,p)),exec:async s=>(await ready,db.exec(s)),close:async()=>{await ready;await db.close()},
  tx:async(fn,mode)=>(await ready,db.transaction(async(t:any)=>{await t.query(BEGIN[mode].replace('BEGIN','SET TRANSACTION'));return fn((s,p)=>t.query(s,p))}))};
};

// Scope keys. Lock order: GLOBAL before any execution scope; an execution-scope writer never takes GLOBAL.
export const GLOBAL='fleet:write';
export const executionScope=(id:string)=>'execution:'+id;
const retryable=(e:any)=>e?.code==='40001'||e?.code==='40P01';
// Short randomized backoff before a retry, growing with the attempt (5 ms .. 200 ms).
const backoff=(attempt:number)=>new Promise(r=>setTimeout(r,Math.random()*Math.min(200,5*2**attempt)));
// jsonb @> for the values filters use: scalars equal, objects by key, arrays contain each element.
const contains=(a:any,b:any):boolean=>b===null||typeof b!=='object'?a===b:Array.isArray(b)?Array.isArray(a)&&b.every(x=>a.some((y:any)=>contains(y,x))):!!a&&typeof a==='object'&&!Array.isArray(a)&&Object.entries(b).every(([k,v])=>contains(a[k],v));
const notFound=()=>{throw Object.assign(Error('Not found'),{statusCode:404})};

// Per-person scope (docs/designs/fleet-people-access.md §2): setScope gives a function that returns the scope of the current
// call, or null for the coordinator's own '*' scope (the owner, hosts, timers). A scoped transaction sets fleet.project_ids and
// fleet.person for itself (SET LOCAL); a scoped query outside a transaction runs in one. role: a NOBYPASSRLS role to switch to
// in scoped transactions, for PGlite tests (PGlite runs as a superuser, which bypasses row-level security).
export type Scope={projectIds:string,person:string};
export function createStore(url:string,opts:{scope?:string,role?:string}={}){
 let scopeOf:()=>Scope|null=()=>null;
 // unscoped(fn): the coordinator's own reads and writes inside a person's request (placement, account and host choice) run
 // in '*' (§2: dispatch and helpers filter in code).
 const system=new AsyncLocalStorage<boolean>();const scope=()=>system.getStore()?null:scopeOf();
 const db=backend(url,opts.scope??'*');
 // Legacy kinds served from typed tables (executions.ts, tasks.ts); every other kind is a document.
 const kinds=new Map<string,Adapter>();
 // The current transaction follows the async call chain, so get/put/all inside tx(fn) join it without passing a client around.
 type Frame={sp:string|null,parent:Frame|null};
 // doomed: the first retryable failure (deadlock, or a serialization failure from a concurrent update) seen in this transaction.
 // Later queries rethrow it and the transaction never commits, even when handler code caught the error.
 // held: scope locks this transaction holds; release runs when it ends.
 type Tx={q:Query,done:boolean,mode:Mode,doomed?:unknown,held:Set<string>,release:(()=>void)[],after:(()=>void)[]};
 const current=new AsyncLocalStorage<{t:Tx,frame?:Frame}>();
 // Query counts by SQL text, for profiling and the poll query budget test.
 const stats=new Map<string,number>();const count=(k:string)=>stats.set(k,(stats.get(k)??0)+1);
 let savepoints=0;
 const q:Query=async(s,p)=>{count(s);const c=current.getStore(),t=c?.t;if(!t||t.done)return scope()?run(()=>q(s,p),/^\s*(SELECT|WITH)/i.test(s)?'read':'write',false):db.query(s,p);
  // A savepoint is opened lazily, just before the first write inside a nested tx: reads never pay for one, and the state
  // before the first write equals the state at the start of the nested call. Outer frames get theirs first.
  if(c!.frame&&!/^\s*SELECT/i.test(s)){const chain:Frame[]=[];for(let f:Frame|null=c!.frame;f&&!f.sp;f=f.parent)chain.unshift(f);for(const f of chain){f.sp='s'+(++savepoints);count('SAVEPOINT');await t.q('SAVEPOINT '+f.sp)}}
  return t.q(s,p)};
 // Scope locks. A writer holds the lock of its scope for its whole transaction: GLOBAL by default, or one execution's scope for
 // host ingest. Inside this process a queue per key orders waiters before they take a pool connection; the transaction then takes
 // pg_advisory_xact_lock on the same key, which also orders writers in other processes (the migration tool, a second coordinator).
 const queues=new Map<string,Promise<void>>();
 const enqueue=async(key:string)=>{const prev=queues.get(key)??Promise.resolve();let release!:()=>void;const mine=new Promise<void>(r=>release=r),tail=prev.then(()=>mine);queues.set(key,tail);
  await prev;return ()=>{release();if(queues.get(key)===tail)queues.delete(key)}};
 const lockIn=async(t:Tx,key:string)=>{if(t.held.has(key))return;t.held.add(key);t.release.push(await enqueue(key));count('LOCK '+key.split(':')[0]);await t.q('SELECT pg_advisory_xact_lock(hashtext($1))',[key])};
 // tx rolls back when fn throws. atomic commits what fn wrote before it threw: a handler on synchronous SQLite kept every write
 // it made before an error. A nested call joins the outer transaction (its mode and locks); a nested tx undoes its own writes on
 // error through a savepoint opened on its first write. Deadlocks (40P01) are retried with a short randomized backoff.
 const run=async<T>(fn:()=>Promise<T>|T,mode:Mode,commitOnError:boolean,lock?:string):Promise<T>=>{
  const c0=current.getStore(),t0=c0?.t;
  if(t0&&!t0.done){if(mode==='write'&&t0.mode==='read')throw Error('Write transaction requested inside a read-only transaction');
   if(lock)await lockIn(t0,lock);if(commitOnError)return fn();const frame:Frame={sp:null,parent:c0!.frame??null};
   try{const v=await current.run({t:t0,frame},fn);if(frame.sp)await t0.q('RELEASE SAVEPOINT '+frame.sp);return v}catch(e){if(t0.doomed)throw t0.doomed;if(frame.sp)await t0.q('ROLLBACK TO SAVEPOINT '+frame.sp);throw e}}
  for(let attempt=0;;attempt++){const t:Tx={q:null as any as Query,done:false,mode,held:new Set(),release:[],after:[]};
   try{const out:{v?:T,e?:unknown}=await db.tx(async tq=>{
     t.q=async(s,p)=>{if(t.doomed)throw t.doomed;try{return await tq(s,p)}catch(e){if(retryable(e))t.doomed=e;throw t.doomed??e}};
     try{const sc=scope();if(sc){if(opts.role)await t.q('SET LOCAL ROLE '+opts.role.replace(/\W/g,''));await t.q("SELECT set_config('fleet.project_ids',$1,true),set_config('fleet.person',$2,true)",[sc.projectIds,sc.person])}
     if(mode==='write')await lockIn(t,lock??GLOBAL);const v=await current.run({t},fn);if(t.doomed)throw t.doomed;return {v}}
     catch(e){if(t.doomed)throw t.doomed;if(commitOnError&&!retryable(e))return {e};throw e}finally{t.done=true}},mode);
    for(const f of t.after)try{f()}catch(e){console.error('afterCommit',e)}
    if('e' in out)throw out.e;return out.v as T}
   catch(e){if(!retryable(e))throw e;count('RETRY');await backoff(attempt)}
   finally{for(const r of t.release.reverse())r()}}
 };
 const put=async(kind:string,v:any):Promise<void>=>{if(!inWrite())return run(()=>put(kind,v),'write',false);const a=kinds.get(kind);if(a)return a.put(v);await q('INSERT INTO document(kind,id,data,project_id) VALUES($1,$2,$3,$4) ON CONFLICT(id) DO UPDATE SET data=excluded.data,project_id=excluded.project_id,updated_at=now() WHERE document.kind=excluded.kind',[kind,v.id,JSON.stringify(v),typeof v.projectId==='string'?v.projectId:null])};
 const inWrite=()=>{const t=current.getStore()?.t;return !!t&&!t.done};
 const api={
  async get(id:string,kind:string):Promise<any>{const a=kinds.get(kind);if(a)return a.get(id);const r=(await q('SELECT data FROM document WHERE id=$1 AND kind=$2',[id,kind])).rows[0];return r?r.data:notFound()},
  // Several documents of one kind in one query, keyed by id; missing ids are absent (callers fall back to get for its 404).
  async many(ids:string[],kind:string):Promise<Map<string,any>>{const a=kinds.get(kind);if(a)return a.many([...new Set(ids.filter(x=>typeof x==='string'))]);const rows=(await q('SELECT data FROM document WHERE kind=$1 AND id=ANY($2)',[kind,[...new Set(ids.filter(x=>typeof x==='string'))]])).rows;return new Map(rows.map(r=>[r.data.id,r.data]))},
  // An id that already exists under another kind is left unchanged, as with the SQLite records table.
  // Outside a transaction a put is a one-statement global write, so it never lands inside another writer's transaction.
  put,
  // Insertion order (seq) as SQLite rowid gave it; filter is a jsonb containment match ({} matches all).
  // omit drops top-level keys in SQL, for reads that never use them (artifact bodies).
  // A filter with id, executionId or sessionId reads through btree indexes only (primary key, migration 4); the other keys are
  // matched in JS. GIN predicate locks sit on shared entry pages, so under SERIALIZABLE they conflict with unrelated inserts.
  async all(kind:string,filter?:object,omit?:string[]):Promise<any[]>{const a=kinds.get(kind);if(a)return a.all(filter,omit);const params:any[]=[kind],where=['kind=$1'],rest:any={};
   for(const [k,v] of Object.entries(filter??{})){if(v===undefined)continue;if(['id','executionId','sessionId'].includes(k)&&typeof v==='string'){params.push(v);where.push(k==='id'?`id=$${params.length}`:`data->>'${k}'=$${params.length}`)}else rest[k]=v}
   const narrow=where.length>1,inJs=narrow&&Object.keys(rest).length>0;
   if(!narrow&&Object.keys(rest).length){params.push(JSON.stringify(rest));where.push(`data @> $${params.length}`)}
   if(omit)params.push(omit);
   const rows=(await q(`SELECT data${omit?` - $${params.length}::text[]`:''} AS data FROM document WHERE ${where.join(' AND ')} ORDER BY seq`,params)).rows.map(r=>r.data);
   return inJs?rows.filter(d=>contains(d,rest)):rows},
  // Write transactions: lock is a scope key (default GLOBAL); see the store model in docs/designs/fleet-data-foundation.md.
  tx:<T>(fn:()=>Promise<T>|T,lock?:string)=>run(fn,'write',false,lock),
  atomic:<T>(fn:()=>Promise<T>|T,lock?:string)=>run(fn,'write',true,lock),
  // Read-only snapshot for handlers that only read; a write inside it fails.
  read:<T>(fn:()=>Promise<T>|T)=>run(fn,'read',false),
  // Takes one more scope inside the current write transaction (for example GLOBAL writer touching one execution's ingest scope).
  // Runs fn once the current write transaction committed (now, outside one): side effects outside the database.
  afterCommit:(fn:()=>void)=>{const t=current.getStore()?.t;if(t&&!t.done)t.after.push(fn);else fn()},
  lock:async(key:string)=>{const t=current.getStore()?.t;if(!t||t.done||t.mode!=='write')throw Error('lock() needs a write transaction');await lockIn(t,key)},
  query:(sql:string,params:any[]=[])=>q(sql,params).then(r=>r.rows),
  setScope:(fn:()=>Scope|null)=>{scopeOf=fn},
  unscoped:async<T>(fn:()=>Promise<T>|T):Promise<T>=>{const t=current.getStore()?.t,sc=scope(),open=!!t&&!t.done&&!!sc;
   if(open)await t!.q("SELECT set_config('fleet.project_ids','*',true)");
   try{return await system.run(true,fn)}finally{if(open)await t!.q("SELECT set_config('fleet.project_ids',$1,true)",[sc!.projectIds])}},
  // upTo stops after that migration (tests start from an older schema).
  async migrate(upTo=Infinity){
   await db.exec('CREATE TABLE IF NOT EXISTS schema_migration(version int PRIMARY KEY,applied_at timestamptz NOT NULL DEFAULT now())');
   const done=new Set((await db.query('SELECT version FROM schema_migration')).rows.map(r=>r.version));
   const file=readFileSync(new URL('./db/schema.sql',import.meta.url),'utf8');
   for(const part of file.split(/^-- migration (?=\d+)/m).slice(1)){const version=Number(part.match(/^\d+/)![0]);if(done.has(version))continue;if(version>upTo)break;
    // Each migration is one GLOBAL write: no writer runs while the schema or its data move.
    await run(async()=>{for(const s of part.replace(/^\d+.*\n/,'').split(/;\s*$/m).map(x=>x.trim()).filter(Boolean))await q(s);
     if(version===6)await moveLegacyDocuments();
     await q('INSERT INTO schema_migration(version) VALUES($1)',[version])},'write',false)}
  },
  // Serves a legacy kind from a typed module (Phase 3: artifact from artifacts.ts).
  registerKind:(kind:string,adapter:Adapter)=>{kinds.set(kind,adapter)},
  // Runs fn outside the caller's transaction (for work scheduled from inside one, such as store reads after a poll).
  outside:<T>(fn:()=>T):T=>current.exit(fn),
  stats,
  close:()=>db.close(),
  // Change feed (docs/designs/fleet-operator-app.md §4). Rows are read in (xid, seq) order and only from transactions older
  // than every transaction still running, so the cursor never passes a change that may still commit. The cursor is the seq
  // of the last row delivered; a cursor whose row is gone (pruned) or unknown asks the client to resync.
  listen:(channel:string,fn:()=>void)=>db.listen(channel,fn),
  changes:{
   async since(cursor:number|null,limit=500):Promise<{changes:any[],cursor:number,resync?:true,held?:true}>{
    const visible='xid < pg_snapshot_xmin(pg_current_snapshot())';
    const head=async()=>Number((await q(`SELECT seq FROM change WHERE ${visible} ORDER BY xid DESC, seq DESC LIMIT 1`)).rows[0]?.seq??0);
    if(cursor===null)return {changes:[],cursor:await head()};
    let from:any;
    if(cursor>0){from=(await q('SELECT xid::text AS xid, seq FROM change WHERE seq=$1',[cursor])).rows[0];if(!from)return {changes:[],cursor:await head(),resync:true}}
    const rows=(await q(`SELECT seq, kind, id, task_id, session_id, host_id, op FROM change WHERE ${visible}${from?' AND (xid, seq) > ($1::xid8, $2::bigint)':''} ORDER BY xid, seq LIMIT ${Math.max(1,Math.min(1000,Math.floor(limit)))}`,from?[from.xid,from.seq]:[])).rows;
    // held: committed rows wait behind an older transaction still running anywhere on the server (xids and the snapshot xmin
    // span every database). They commit no second NOTIFY when that transaction ends, so the reader must look again.
    const held=rows.length<limit&&(await q(`SELECT 1 FROM change WHERE NOT (${visible}) LIMIT 1`)).rows.length>0;
    return {changes:rows.map(r=>({seq:Number(r.seq),kind:r.kind,id:r.id,taskId:r.task_id,sessionId:r.session_id,hostId:r.host_id,op:r.op})),cursor:rows.length?Number(rows.at(-1).seq):cursor,...(held?{held:true as const}:{})}},
   // Retention: batches in their own lock scope, never GLOBAL, so writers do not wait for a prune.
   async prune(retentionDays:number,batch=10000):Promise<number>{let total=0;for(;;){const n=await run(async()=>(await q(`DELETE FROM change WHERE seq IN (SELECT seq FROM change WHERE at < now() - make_interval(days => $1) ORDER BY xid, seq LIMIT ${batch}) RETURNING seq`,[retentionDays])).rows.length,'write',false,'change:prune');total+=n;if(n<batch)return total}},
  },
 };
 const executions=executionModel(api),tasks=taskModel(api,executions);
 for(const [k,a] of Object.entries({...executions.adapters,...tasks.adapters}))kinds.set(k,a);
 // An execution of a task settles the task (design §2: queued -> in_progress when its first execution starts).
 {const ex=kinds.get('execution')!;kinds.set('execution',{...ex,put:async(v:any)=>{await ex.put(v);// A step can be written before its task in the same transaction (the task_id foreign key is deferred): skip until it exists.
  const t=(await q('SELECT e.task_id FROM execution e JOIN task k ON k.id=e.task_id WHERE e.id=$1',[v.id])).rows[0]?.task_id;if(t)await tasks.settle(t)}})}
 // Migration 6: legacy documents of the typed kinds move to their tables in insertion order and keep their seq.
 // A delegation lives on its child execution's row, so it follows the executions.
 async function moveLegacyDocuments(){const rows=(await q("SELECT kind,id,data,seq FROM document WHERE kind = ANY($1) ORDER BY (kind='delegation'), seq",[TYPED_KINDS])).rows;
  for(const r of rows)await keep(r.kind,r.data,r.seq);
  await q('DELETE FROM document WHERE kind = ANY($1)',[TYPED_KINDS]);await settleDeliveries()}
 // A delivery moves before its step executions (insertion order); once all are in, its task state is settled again.
 async function settleDeliveries(){for(const d of await api.all('delivery'))await kinds.get('delivery')!.put(d)}
 // Writes a legacy record through its adapter and gives the typed row the given seq (SQLite rowid on forward migration).
 async function keep(kind:string,data:any,seq:number){await kinds.get(kind)!.put(data);const table=TABLE_OF[kind];if(table)await q(`UPDATE ${table} SET seq=$2 WHERE id=$1`,[data.id,seq])}
 return {...api,executions,tasks,keep,settleDeliveries,typedKinds:TYPED_KINDS};
}
// Legacy kinds that live in typed tables since migration 6, and the table holding each record's own row.
export const TYPED_KINDS=['execution','pipeline-run','delivery','project-question','question','delegation'];
const TABLE_OF:Record<string,string>={execution:'execution','pipeline-run':'execution',delivery:'task','project-question':'question',question:'question'};
export type Docs=Pick<Store,'get'|'put'|'all'|'tx'|'atomic'|'read'|'lock'|'afterCommit'>;
// Operations that wait on a pipeline run; they open short transactions around their own synchronous steps.
// Operations that only read: they run in a read-only snapshot (a write inside one fails loudly).
export const READ_OPERATIONS=new Set(['subscription_limits','budgets_list','route_decisions','connections_list','lab_experiments_list','lab_experiment_get','lab_ratings_list','lab_rating_get','lab_registry','lab_examples_list','view_link','task_derived_check','artifact_list','artifact_get','stores_list','artifact_rule_preview','task_page','changes_since','list_membership','sessions_list','hosts_list','me','task_get','task_list','questions_list','executions_list','question_history','project_context','catalogue','pipeline_get','pipeline_history','project_list','account_list','account_hosts','account_login_handoff','transport_inspect','session_handoffs','queue_list','questions','execution_status','session_get','thread_entries','thread_blocks','thread_search','thread_export','execution_differences','execution_events','profiles_list']);
// Lab operations open their own transactions: reconcile reads committed state and runs checks outside any lock but its own.
export const LONG_OPERATIONS=new Set(['pipeline_run','direct_search','delivery_generate','lab_experiment_create','lab_experiment_action','lab_cell_action','lab_promote','lab_rating_create','lab_score_correct']);
// Sequential async forms of the array methods, for callbacks that read the store; order of reads and writes stays as in the sync code.
export const every=async<T>(xs:T[],f:(x:T)=>unknown)=>{for(const x of xs)if(!await f(x))return false;return true};
export const some=async<T>(xs:T[],f:(x:T)=>unknown)=>{for(const x of xs)if(await f(x))return true;return false};
export const find=async<T>(xs:T[],f:(x:T)=>unknown)=>{for(const x of xs)if(await f(x))return x;return undefined};
export const filter=async<T>(xs:T[],f:(x:T)=>unknown)=>{const r:T[]=[];for(const x of xs)if(await f(x))r.push(x);return r};
export const map=async<T,U>(xs:T[],f:(x:T,i:number)=>U|Promise<U>)=>{const r:U[]=[];for(const [i,x] of xs.entries())r.push(await f(x,i));return r};
