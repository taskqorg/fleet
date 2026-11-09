// One-time move of the SQLite records table into the Postgres document table, with a check and a rollback path.
//   node migrate-sqlite.js forward <sqlite> <database-url>
//   node migrate-sqlite.js verify  <sqlite> <database-url>
//   node migrate-sqlite.js reverse <sqlite> <database-url> <output.sqlite> [since-ISO-time]
// forward keeps kind, id and data, and copies SQLite rowid into seq so all() keeps the old order. It skips ids already present.
// verify compares row counts and a data hash per kind (keys sorted, so jsonb key order does not count).
// reverse copies <sqlite> to <output.sqlite> and writes back every document changed after since (default: every document),
// including the typed tables (executions, tasks, questions), each record written back as its legacy document.
import Database from 'better-sqlite3';
import {copyFileSync,existsSync} from 'node:fs';
import {createHash} from 'node:crypto';
import {fileURLToPath} from 'node:url';
import {createStore,type Store} from './store.js';

const canonical=(v:any):any=>Array.isArray(v)?v.map(canonical):v&&typeof v==='object'?Object.fromEntries(Object.keys(v).sort().map(k=>[k,canonical(v[k])])):v;
const digest=(rows:{id:string,data:any}[])=>{const h=createHash('sha256');for(const r of [...rows].sort((a,b)=>a.id<b.id?-1:a.id>b.id?1:0))h.update(r.id+'\0'+JSON.stringify(canonical(r.data))+'\n');return h.digest('hex')};
const group=(rows:{kind:string,id:string,data:any}[])=>{const m=new Map<string,{id:string,data:any}[]>();for(const r of rows){if(!m.has(r.kind))m.set(r.kind,[]);m.get(r.kind)!.push(r)}return m};
const records=(path:string)=>{if(!existsSync(path))throw Error('SQLite file not found: '+path);const db=new Database(path,{readonly:true,fileMustExist:true});try{return (db.prepare('SELECT rowid AS seq,kind,id,data FROM records ORDER BY rowid').all() as any[]).map(r=>({...r,data:JSON.parse(r.data)}))}finally{db.close()}};

// Legacy kinds that live in typed tables (store.typedKinds) go through their adapters, so the typed columns are filled the same
// way as by the running coordinator; a delegation follows its child execution. Every other kind is a document row.
export async function forward(sqlite:string,store:Store){
 const rows=records(sqlite),typed=new Set(store.typedKinds);let inserted=0;
 const present=async(kind:string,id:string)=>{try{await store.get(id,kind);return true}catch(e:any){if(e?.statusCode===404)return false;throw e}};
 await store.tx(async()=>{
  for(const r of rows.filter(r=>!typed.has(r.kind)))inserted+=(await store.query('INSERT INTO document(kind,id,data,project_id,seq) VALUES($1,$2,$3,$4,$5) ON CONFLICT(id) DO NOTHING RETURNING id',[r.kind,r.id,JSON.stringify(r.data),typeof r.data?.projectId==='string'?r.data.projectId:null,r.seq])).length;
  for(const r of rows.filter(r=>typed.has(r.kind)).sort((a,b)=>Number(a.kind==='delegation')-Number(b.kind==='delegation')||a.seq-b.seq))if(!(await present(r.kind,r.id))){await store.keep(r.kind,r.data,r.seq);inserted++}
  await store.settleDeliveries();
  // New rows continue after the highest migrated rowid.
  await store.query("SELECT setval(pg_get_serial_sequence('document','seq'),GREATEST((SELECT max(seq) FROM document),(SELECT max(seq) FROM execution),(SELECT max(seq) FROM task),(SELECT max(seq) FROM question),1))")});
 return {read:rows.length,inserted,skipped:rows.length-inserted};
}

// Every legacy record the coordinator holds, as kind/id/data: document rows plus the typed tables read back as legacy records.
const legacyRecords=async(store:Store)=>{const out:{kind:string,id:string,data:any}[]=(await store.query('SELECT kind,id,data FROM document ORDER BY seq'));
 for(const kind of store.typedKinds)for(const data of await store.all(kind))out.push({kind,id:data.id,data});return out};

export async function verify(sqlite:string,store:Store){
 const left=group(records(sqlite)),right=group(await legacyRecords(store));
 const kinds=[...new Set([...left.keys(),...right.keys()])].sort().map(kind=>{const a=left.get(kind)??[],b=right.get(kind)??[];const sqliteHash=digest(a),postgresHash=digest(b);return {kind,sqlite:a.length,postgres:b.length,match:a.length===b.length&&sqliteHash===postgresHash}});
 return {ok:kinds.every(k=>k.match),kinds};
}

// Ids of typed records changed after since; a pipeline run counts as changed when one of its steps did, a delegation when its row did.
const CHANGED:Record<string,string>={
 execution:"SELECT id FROM execution WHERE legacy_kind='execution' AND updated_at>$1",
 'pipeline-run':"SELECT DISTINCT coalesce(CASE WHEN legacy_kind='pipeline-step' THEN parent_execution_id END,id) AS id FROM execution WHERE legacy_kind IN ('pipeline-run','pipeline-step') AND updated_at>$1",
 delegation:"SELECT delegation->>'id' AS id FROM execution WHERE delegation IS NOT NULL AND updated_at>$1",
 delivery:"SELECT id FROM task WHERE legacy_kind='delivery' AND updated_at>$1",
 question:"SELECT id FROM question WHERE legacy_kind='question' AND updated_at>$1",
 'project-question':"SELECT id FROM question WHERE legacy_kind='project-question' AND updated_at>$1"};
// Rollback export: every document and typed record changed after since (default: all), written back as legacy records.
export async function reverse(sqlite:string,store:Store,output:string,since?:string){
 if(existsSync(output))throw Error('Output exists; choose a new file: '+output);
 copyFileSync(sqlite,output);
 const rows=[...await store.query(`SELECT kind,id,data FROM document${since?' WHERE updated_at>$1':''} ORDER BY seq`,since?[since]:[])];
 for(const kind of store.typedKinds){const ids=since?(await store.query(CHANGED[kind],[since])).map((r:any)=>r.id):null;
  for(const data of ids?[...(await store.many(ids,kind)).values()]:await store.all(kind))rows.push({kind,id:data.id,data})}
 const db=new Database(output);
 try{const put=db.prepare('INSERT INTO records VALUES(?,?,?) ON CONFLICT(id) DO UPDATE SET data=excluded.data WHERE records.kind=excluded.kind');db.transaction(()=>{for(const r of rows)put.run(r.kind,r.id,JSON.stringify(r.data))})()}finally{db.close()}
 return {written:rows.length,output};
}

if(process.argv[1]===fileURLToPath(import.meta.url)){
 const [mode,sqlite,url,output,since]=process.argv.slice(2);
 if(!['forward','verify','reverse'].includes(mode)||!sqlite||!url||(mode==='reverse'&&!output)){console.error('usage: migrate-sqlite forward|verify <sqlite> <database-url> | reverse <sqlite> <database-url> <output.sqlite> [since]');process.exit(2)}
 const store=createStore(url);
 try{if(mode==='forward')await store.migrate();
  const result=mode==='forward'?await forward(sqlite,store):mode==='verify'?await verify(sqlite,store):await reverse(sqlite,store,output,since);
  console.log(JSON.stringify(result,null,1));if(mode==='verify'&&!(result as any).ok)process.exitCode=1}
 finally{await store.close()}
}
