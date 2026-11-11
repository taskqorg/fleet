// Real executor delegation MCP discovery + journal transport + scoped coordinator. No agent/provider process is launched.
import {test} from 'node:test';
import assert from 'node:assert/strict';
import {randomUUID} from 'node:crypto';
import {spawn} from 'node:child_process';
import {once} from 'node:events';
import {createInterface} from 'node:readline';
import {DatabaseSync} from 'node:sqlite';
import {mkdtempSync,rmSync} from 'node:fs';
import {tmpdir} from 'node:os';
import {join} from 'node:path';
import {executorBinary} from '../executor/executor-binary.ts';
const BASE=process.env.FLEET_TRIAL_URL!,SECRET=process.env.FLEET_TRIAL_BROWSER_SECRET!;
const member='member@trial.test',owner='owner@trial.test';
async function call(path:string,body:object,person=owner){const r=await fetch(BASE+path,{method:'POST',headers:{authorization:'Bearer '+SECRET,'x-fleet-person':person,'content-type':'application/json'},body:JSON.stringify(body)});assert.equal(r.status,200,await r.clone().text());return r.json() as Promise<any>;}
// R2, R3; risk: unauthorized scope, stale authority or inconsistent retained project/session state.
test('Native projectless PM discovers project tools, creates/focuses project, edits and creates linked Held task', {timeout:60_000},async()=>{
 const hostId='pm-project-host';const host=await call('/api/ops/hosts_register',{id:hostId,holder:member});
 const poll=async()=>{const r=await fetch(BASE+'/api/hosts/poll',{method:'POST',headers:{authorization:'Bearer '+host.secret,'content-type':'application/json'},body:JSON.stringify({platform:'darwin',capabilities:[],stores:[],problems:[]})});assert.equal(r.status,200);return r.json() as Promise<any>;};
 await poll();const session=await call('/api/sessions',{},member);await call(`/api/sessions/${session.sessionId}/profile`,{profileId:'pm'},member);
 const execution=await call(`/api/sessions/${session.sessionId}/messages`,{requestId:randomUUID(),text:'Manage a trial project'},member);
 const commands=(await poll()).commands;const command=commands.find((c:any)=>c.executionId===execution.id&&c.kind==='start');assert.ok(command,'start command exists without launching vendor');
 assert.ok(command.payload.fleetOperations.some((o:any)=>o.name==='project_create'));
 const root=mkdtempSync(join(tmpdir(),'fleet-project-mcp-'));const journal=join(root,'journal.sqlite');const db=new DatabaseSync(journal);
 db.exec("PRAGMA journal_mode=WAL; PRAGMA busy_timeout=5000; CREATE TABLE delegation_requests(id TEXT PRIMARY KEY,body TEXT,response TEXT);CREATE TABLE outbox(route TEXT,body TEXT);");
 const context={hostId,commandId:command.id,executionId:execution.id,operations:command.payload.fleetOperations,profiles:[]};
 const child=spawn(executorBinary(),['mcp','delegation',journal,JSON.stringify(context)],{stdio:['pipe','pipe','pipe']});const closed=once(child,'close');
 const lines=createInterface({input:child.stdout})[Symbol.asyncIterator]();let n=0,bridgeError:unknown;const pending=new Set<string>();
 const bridge=setInterval(()=>{for(const row of db.prepare('SELECT id,body FROM delegation_requests WHERE response IS NULL').all() as any[]){if(pending.has(row.id))continue;pending.add(row.id);void fetch(BASE+'/api/hosts/agent-tools',{method:'POST',headers:{authorization:'Bearer '+host.secret,'content-type':'application/json'},body:row.body}).then(async r=>{const body=await r.json();db.prepare('UPDATE delegation_requests SET response=? WHERE id=?').run(JSON.stringify(body),row.id);}).catch(e=>bridgeError=e);}},20);
 const rpc=async(method:string,params?:object)=>{child.stdin.write(JSON.stringify({jsonrpc:'2.0',id:++n,method,params})+'\n');const next=await lines.next();assert.equal(next.done,false);const r=JSON.parse(next.value!);assert.equal(r.error,undefined);return r.result;};
 const tool=async(name:string,args:object)=>{const r=await rpc('tools/call',{name,arguments:args});assert.notEqual(r.isError,true,JSON.stringify(r));return JSON.parse(r.content[0].text);};
 try {
  const tools=(await rpc('tools/list')).tools.map((t:any)=>t.name);assert.ok(tools.includes('project_create'));assert.ok(tools.includes('project_update'));
  const id='pm-'+randomUUID();const content={settings:{id,name:'PM project',contextFiles:['rules.md'],roles:{pm:'pm'}},contextFiles:[{path:'rules.md',text:'Retain linked task evidence.'}]};
  const made=await tool('project_create',{id,requestId:randomUUID(),content});assert.equal(made.revision,1);
  const read=await tool('project_context',{projectId:id});assert.equal(read.context[0].text,'Retain linked task evidence.');
  content.settings.name='Edited by PM';assert.equal((await tool('project_update',{projectId:id,requestId:randomUUID(),baseRevision:1,content})).revision,2);
  const denied=await rpc('tools/call',{name:'project_context',arguments:{projectId:'hello-world'}});assert.equal(denied.isError,true);
  const task=await tool('task_create',{projectId:id,title:'Retained PM task',expectedOutcome:'Readable linked result'});assert.ok(task.id);
  const taskRead=await call('/api/ops/task_get',{taskId:task.id},member);assert.equal(taskRead.projectId,id);assert.equal(taskRead.state,'paused');assert.equal(taskRead.pauseReason,'held');assert.equal(taskRead.createdBy,'execution:'+execution.id);assert.equal(taskRead.directedBy,member);
  assert.equal(bridgeError,undefined);
 }finally{clearInterval(bridge);child.stdin.end();if(child.exitCode===null){child.kill('SIGTERM');}await closed;db.close();rmSync(root,{recursive:true,force:true});}
});
