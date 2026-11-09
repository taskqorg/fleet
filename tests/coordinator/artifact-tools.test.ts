// The agent's artifact tools in the executor's MCP adapter (crates/executor/src/delegation.py): artifact_save copies only a
// regular file inside the working area into the transfer journal, never following a link.
import {test} from 'node:test';import assert from 'node:assert/strict';import {execFileSync} from 'node:child_process';import {mkdtempSync,mkdirSync,writeFileSync,symlinkSync,linkSync,readFileSync} from 'node:fs';import {tmpdir} from 'node:os';
test('artifact_save refuses links and paths outside the working area, and queues a file inside it',()=>{
 const d=mkdtempSync(tmpdir()+'/fleet-tools-');mkdirSync(d+'/work/sub',{recursive:true});writeFileSync(d+'/work/sub/a.txt','hi');writeFileSync(d+'/outside.txt','secret');symlinkSync(d+'/outside.txt',d+'/work/link.txt');linkSync(d+'/outside.txt',d+'/work/hard.txt');
 execFileSync('python3',['-c',`import sqlite3;db=sqlite3.connect('${d}/j.sqlite');db.executescript("CREATE TABLE transfers(id TEXT PRIMARY KEY,kind TEXT NOT NULL,body TEXT NOT NULL,state TEXT NOT NULL DEFAULT 'queued',response TEXT,attempts INTEGER NOT NULL DEFAULT 0,next_at INTEGER NOT NULL DEFAULT 0,created_at INTEGER NOT NULL);CREATE TABLE delegation_requests(id TEXT PRIMARY KEY,body TEXT,response TEXT);CREATE TABLE outbox(route TEXT,body TEXT);")`]);
 const ctx=JSON.stringify({hostId:'h',commandId:'c',executionId:'e',profiles:[],workdir:d+'/work',inputDir:d+'/in',spool:d+'/spool'});
 const call=(id:number,args:any)=>JSON.stringify({jsonrpc:'2.0',id,method:'tools/call',params:{name:'artifact_save',arguments:args}});
 const out=execFileSync('python3',['-c',readFileSync('crates/executor/src/delegation.py','utf8'),d+'/j.sqlite',ctx],{input:[call(1,{path:'link.txt'}),call(2,{path:'../outside.txt'}),call(3,{path:'sub/a.txt'}),call(4,{path:'hard.txt'})].join('\n')+'\n',timeout:60000}).toString().trim().split('\n').map(l=>JSON.parse(l));
 assert.equal(out[0].result.isError,true);assert.equal(out[1].result.isError,true);assert.equal(out[3].result.isError,true,'hard link refused');
 const saved=JSON.parse(out[2].result.content[0].text);assert.ok(saved.artifactId);
 const rows=JSON.parse(execFileSync('python3',['-c',`import sqlite3,json;print(json.dumps([json.loads(b) for (b,) in sqlite3.connect('${d}/j.sqlite').execute('select body from transfers')]))`]).toString());
 assert.equal(rows.length,1);assert.equal(rows[0].name,'sub/a.txt');assert.equal(readFileSync(rows[0].path,'utf8'),'hi')});
