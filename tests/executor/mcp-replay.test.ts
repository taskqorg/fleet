// fleet-stack L4: replay stdio exchanges (first captured from the former Python servers) and concurrent durable journal effects against the Go binary.
import {test} from 'node:test';
import assert from 'node:assert/strict';
import {spawn} from 'node:child_process';
import {once} from 'node:events';
import {mkdtempSync,mkdirSync,writeFileSync,symlinkSync,linkSync,readFileSync,rmSync} from 'node:fs';
import {tmpdir} from 'node:os';
import {join} from 'node:path';
import {createInterface} from 'node:readline';
import {DatabaseSync} from 'node:sqlite';
import {executorBinary} from './executor-binary.ts';

function normalize(value:any,root:string):any {
 if(typeof value==='string')return value.replaceAll(root,'<root>').replace(/[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}/g,'<uuid>');
 if(Array.isArray(value))return value.map(v=>normalize(v,root));
 if(value&&typeof value==='object'){
  const result=Object.fromEntries(Object.entries(value).map(([k,v])=>[k,normalize(v,root)]));
  if(value.type==='text'&&typeof value.text==='string'){try{result.text=normalize(JSON.parse(value.text),root)}catch{}}
  return result;
 }
 return value;
}

test('L4 captured MCP exchanges and journal effects match the Rust subcommands',async()=>{
 const binary=executorBinary();
 const cases=JSON.parse(readFileSync('tests/executor/fixtures/mcp.json','utf8'));
 for(const fixture of cases){
  const root=mkdtempSync(join(tmpdir(),'fleet-mcp-replay-'));
  mkdirSync(join(root,'work/sub'),{recursive:true});mkdirSync(join(root,'spool'));
  writeFileSync(join(root,'work/sub/a.txt'),'hello');writeFileSync(join(root,'outside'),'outside');
  symlinkSync(join(root,'outside'),join(root,'work/link'));linkSync(join(root,'outside'),join(root,'work/hard'));
  for(const name of ['.txt','note.MD','archive.tgz','blob.unknown'])writeFileSync(join(root,'work',name),'fixture');
  const context=JSON.parse(JSON.stringify(fixture.context).replaceAll('<root>',root));
  const db=new DatabaseSync(join(root,'journal.sqlite'));
  db.exec("PRAGMA journal_mode=WAL; PRAGMA busy_timeout=5000; CREATE TABLE delegation_requests(id TEXT PRIMARY KEY,body TEXT,response TEXT);CREATE TABLE outbox(route TEXT,body TEXT);CREATE TABLE transfers(id TEXT PRIMARY KEY,kind TEXT,body TEXT,state TEXT DEFAULT 'queued',response TEXT,created_at INTEGER);");
  const args=['mcp',fixture.server,...(fixture.server==='delegation'?[join(root,'journal.sqlite'),JSON.stringify(context)]:[JSON.stringify(context)])];
  const child=spawn(binary,args,{stdio:['pipe','pipe','pipe']});
  const closed=once(child,'close');
  let stderr='';child.stderr.setEncoding('utf8');child.stderr.on('data',s=>stderr+=s);
  const lines=createInterface({input:child.stdout})[Symbol.asyncIterator]();
  let responderError:unknown;
  // The Rust child owns a separate SQLite connection; this timer writes replies while its stdio call awaits them.
  const responder=setInterval(()=>{
   try{
    db.exec('BEGIN IMMEDIATE');
    db.prepare('UPDATE delegation_requests SET response=? WHERE response IS NULL').run(JSON.stringify(context._response??{childExecutionId:'child',result:{content:[{type:'text',text:'reply'}]}}));
    db.prepare('UPDATE transfers SET state=?,response=? WHERE response IS NULL').run(context._transferState??'done',JSON.stringify(context._transferResponse??{artifactId:'artifact',state:'done'}));
    db.exec('COMMIT');
   }catch(error){responderError=error;try{db.exec('ROLLBACK')}catch{}}
  },5);
  try{
   const replies=[];
   for(const request of fixture.requests){
    child.stdin.write((typeof request==='string'?request:JSON.stringify(request))+'\n');
    if(typeof request==='object'&&!('id' in request))continue;
    let timer:ReturnType<typeof setTimeout>|undefined;
    try{
     const line=await Promise.race([lines.next(),new Promise<never>((_,reject)=>{timer=setTimeout(()=>reject(Error('MCP reply did not arrive within 10 seconds')),10000)})]);
     assert.equal(line.done,false,`MCP exited without a reply: ${stderr}`);
     replies.push(normalize(JSON.parse(line.value!),root));
    }finally{clearTimeout(timer)}
   }
   child.stdin.end();
   let timer:ReturnType<typeof setTimeout>|undefined;
   try{
    const [code]=await Promise.race([closed,new Promise<never>((_,reject)=>{timer=setTimeout(()=>reject(Error('MCP did not exit within 5 seconds')),5000)})]);
    assert.equal(code,0,stderr);
   }finally{clearTimeout(timer)}
   assert.equal(responderError,undefined);
   const effects=Object.fromEntries(['outbox','delegation_requests','transfers'].map(table=>[table,db.prepare(`SELECT body FROM ${table} ORDER BY rowid`).all().map(row=>normalize(JSON.parse(row.body as string),root))]));
   assert.deepEqual({replies,effects},fixture.expected,fixture.server);
  }finally{
   clearInterval(responder);
   if(child.exitCode===null&&child.signalCode===null){child.kill('SIGKILL');await closed}
   db.close();rmSync(root,{recursive:true,force:true});
  }
 }
});
