import {executorBinary} from './executor-binary.ts';import {wakeAll} from './wake.ts';
// fleet-stack L4: protect artifact path confinement while replacing the MCP adapter.
// The agent's artifact tools in the executor's MCP adapter (go/executor/mcp_artifact.go): artifact_save copies only a
// regular file inside the working area into the transfer journal, never following a link.
import {test} from 'node:test';import assert from 'node:assert/strict';import {execFile,execFileSync} from 'node:child_process';import {once} from 'node:events';import {mkdtempSync,mkdirSync,writeFileSync,symlinkSync,linkSync,readFileSync,realpathSync} from 'node:fs';import {tmpdir} from 'node:os';import {DatabaseSync} from 'node:sqlite';
test('artifact_save refuses links and paths outside the working area, and queues a file inside it',()=>{
 const d=mkdtempSync(tmpdir()+'/fleet-tools-');mkdirSync(d+'/work/sub',{recursive:true});writeFileSync(d+'/work/sub/a.txt','hi');writeFileSync(d+'/outside.txt','secret');symlinkSync(d+'/outside.txt',d+'/work/link.txt');linkSync(d+'/outside.txt',d+'/work/hard.txt');
 new DatabaseSync(d+'/j.sqlite').exec("CREATE TABLE transfers(id TEXT PRIMARY KEY,kind TEXT NOT NULL,body TEXT NOT NULL,state TEXT NOT NULL DEFAULT 'queued',response TEXT,attempts INTEGER NOT NULL DEFAULT 0,next_at INTEGER NOT NULL DEFAULT 0,created_at INTEGER NOT NULL);CREATE TABLE delegation_requests(id TEXT PRIMARY KEY,body TEXT,response TEXT);CREATE TABLE outbox(route TEXT,body TEXT);");
 const ctx=JSON.stringify({hostId:'h',commandId:'c',executionId:'e',profiles:[],workdir:d+'/work',inputDir:d+'/in',spool:d+'/spool'});
 const call=(id:number,args:any)=>JSON.stringify({jsonrpc:'2.0',id,method:'tools/call',params:{name:'artifact_save',arguments:args}});
 const out=execFileSync(executorBinary(),['mcp','delegation',d+'/j.sqlite',ctx],{input:[call(1,{path:'link.txt'}),call(2,{path:'../outside.txt'}),call(3,{path:'sub/a.txt'}),call(4,{path:'hard.txt'})].join('\n')+'\n',timeout:60000}).toString().trim().split('\n').map(l=>JSON.parse(l));
 assert.equal(out[0].result.isError,true);assert.equal(out[1].result.isError,true);assert.equal(out[3].result.isError,true,'hard link refused');
 const saved=JSON.parse(out[2].result.content[0].text);assert.ok(saved.artifactId);
 const rows=new DatabaseSync(d+'/j.sqlite').prepare('select body from transfers').all().map((r:any)=>JSON.parse(r.body));
 assert.equal(rows.length,1);assert.equal(rows[0].name,'sub/a.txt');assert.equal(readFileSync(rows[0].path,'utf8'),'hi')});
// Lab trial 2026-11-17: an absolute path of a file in the working area was refused while its relative path was saved.
test('artifact_save takes an absolute path inside the working area, also through a linked spelling of its root, and refuses one outside',async()=>{
 const d=realpathSync(mkdtempSync(tmpdir()+'/fleet-tools-'));mkdirSync(d+'/real/work/output',{recursive:true});writeFileSync(d+'/real/work/output/report.md','report');writeFileSync(d+'/secret.txt','secret');symlinkSync(d+'/real',d+'/alias');symlinkSync(d,d+'/real/work/up');
 const db=new DatabaseSync(d+'/j.sqlite');db.exec("PRAGMA journal_mode=WAL;PRAGMA busy_timeout=5000;CREATE TABLE transfers(id TEXT PRIMARY KEY,kind TEXT NOT NULL,body TEXT NOT NULL,state TEXT NOT NULL DEFAULT 'queued',response TEXT,attempts INTEGER NOT NULL DEFAULT 0,next_at INTEGER NOT NULL DEFAULT 0,created_at INTEGER NOT NULL);CREATE TABLE delegation_requests(id TEXT PRIMARY KEY,body TEXT,response TEXT);CREATE TABLE outbox(route TEXT,body TEXT);");
 const ctx=JSON.stringify({hostId:'h',commandId:'c',executionId:'e',profiles:[],workdir:d+'/alias/work',inputDir:d+'/in',spool:d+'/spool'});
 const call=(id:number,args:any)=>JSON.stringify({jsonrpc:'2.0',id,method:'tools/call',params:{name:'artifact_save',arguments:args}});
 const paths=[d+'/alias/work/output/report.md',d+'/real/work/output/report.md',d+'/secret.txt',d+'/alias/work/up/secret.txt',d+'/alias/work/../../secret.txt'];
 const responder=setInterval(()=>{db.prepare("UPDATE transfers SET state='done',response=? WHERE response IS NULL").run(JSON.stringify({artifactId:'a',state:'done'}));wakeAll(d)},10); // plays the transfer lane
 const child=execFile(executorBinary(),['mcp','delegation',d+'/j.sqlite',ctx],{timeout:60000});child.stdin!.end(paths.map((path,i)=>call(i+1,{path})).join('\n')+'\n');
 let stdout='';child.stdout!.on('data',s=>stdout+=s);
 try{assert.equal((await once(child,'close'))[0],0)}finally{clearInterval(responder)}
 const out=stdout.trim().split('\n').map(l=>JSON.parse(l));
 assert.deepEqual(out.map(o=>o.result.isError===true),[false,false,true,true,true]);
 const rows=db.prepare('select body from transfers').all().map((r:any)=>JSON.parse(r.body));
 assert.deepEqual(rows.map(r=>r.name),['output/report.md','output/report.md']);assert.equal(readFileSync(rows[0].path,'utf8'),'report')});
