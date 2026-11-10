import {test} from 'node:test';import assert from 'node:assert/strict';import Database from 'better-sqlite3';import {mkdtempSync} from 'node:fs';import {tmpdir} from 'node:os';
import {createStore} from '../../packages/coordinator/store.ts';import {forward,verify,reverse} from '../../packages/coordinator/migrate-sqlite.ts';
// Fixture shaped like var/fleet.sqlite: the records table, interleaved kinds, nested JSON, base64 artifacts, unicode, project ids, upserted rows.
const fixture=(path:string)=>{const db=new Database(path);db.exec('CREATE TABLE records(kind TEXT,id TEXT PRIMARY KEY,data TEXT)');const put=db.prepare('INSERT INTO records VALUES(?,?,?) ON CONFLICT(id) DO UPDATE SET data=excluded.data WHERE records.kind=excluded.kind');const add=(kind:string,v:any)=>put.run(kind,v.id,JSON.stringify(v));
 add('session',{id:'s1',createdAt:1,projectId:'hello-world',agentConfig:{id:'baseline',model:'gpt-6.1-sol',effort:'low',tools:['a','b']}});
 add('execution',{id:'e1',executionId:'e1',sessionId:'s1',hostId:'mac',state:'queued',createdAt:2,effectiveAccount:{binding:'fleet-codex',legacy:true}});
 add('event',{id:'event:e1:0',executionId:'e1',sessionId:'s1',seq:0,type:'thread.started',data:{text:'Привет ✓ "quoted"\n'}});
 add('artifact',{id:'artifact:e1:output/index.html',executionId:'e1',path:'output/index.html',contentBase64:Buffer.from('<h1>x</h1>').toString('base64'),size:10,sha256:'00'});
 add('managed-account',{id:'m1',name:'fleet-codex',harness:'codex',enabled:true,bindings:[{hostId:'mac',binding:'fleet-codex',readiness:'ready',roots:null}]});
 add('execution',{id:'e2',executionId:'e2',sessionId:'s1',hostId:'mac',state:'completed',createdAt:3,error:null,n:1.5});
 add('execution',{id:'e1',executionId:'e1',sessionId:'s1',hostId:'mac',state:'completed',createdAt:2});
 db.close()};
test('forward, verify and reverse round-trip a SQLite records file',async()=>{const dir=mkdtempSync(tmpdir()+'/fleet-migrate-'),sqlite=dir+'/fleet.sqlite';fixture(sqlite);
 const store=createStore('memory://');await store.migrate();
 assert.deepEqual(await forward(sqlite,store),{read:7-1,inserted:6,skipped:0});
 assert.deepEqual(await forward(sqlite,store),{read:6,inserted:0,skipped:6});
 const v=await verify(sqlite,store);assert.equal(v.ok,true);assert.deepEqual(v.kinds.map(k=>[k.kind,k.sqlite,k.postgres]),[['artifact',1,1],['event',1,1],['execution',2,2],['managed-account',1,1],['session',1,1]]);
 assert.deepEqual((await store.all('execution')).map(e=>e.id),['e1','e2']);assert.equal((await store.get('e1','execution')).state,'completed');
 assert.equal((await store.query("SELECT project_id FROM document WHERE id='s1'"))[0].project_id,'hello-world');
 // New documents sort after migrated ones.
 const cut=(await store.query('SELECT now() AS t'))[0].t.toISOString();await new Promise(r=>setTimeout(r,5));
 await store.put('execution',{id:'e3',state:'queued'});await store.put('execution',{id:'e2',executionId:'e2',state:'failed'});
 assert.deepEqual((await store.all('execution')).map(e=>e.id),['e1','e2','e3']);
 assert.equal((await verify(sqlite,store)).ok,false);
 const back=await reverse(sqlite,store,dir+'/rollback.sqlite',cut);assert.equal(back.written,2);
 assert.equal((await verify(dir+'/rollback.sqlite',store)).ok,true);
 const db=new Database(dir+'/rollback.sqlite',{readonly:true});const rows=db.prepare("SELECT id,data FROM records WHERE kind='execution' ORDER BY rowid").all() as any[];db.close();
 assert.deepEqual(rows.map(r=>[r.id,JSON.parse(r.data).state]),[['e1','completed'],['e2','failed'],['e3','queued']]);
 await assert.rejects(reverse(sqlite,store,dir+'/rollback.sqlite'),/Output exists/);await store.close()});
